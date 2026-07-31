# ADR-0023: Real Deadlines, a Concurrent MCP Proxy, and an RPC That Checks Its Own Token

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-07-30 |
| Deciders | Ersin Koç |
| Related | [ADR-0019](0019-global-daemon.md), [ADR-0020](0020-mcp-proxy-and-cleanup.md), [ADR-0022](0022-daemon-identity-and-resilient-proxy.md) |

## Context

A full read-through of the code base turned up three defects that share one shape: a control existed, looked implemented, and did not hold. Each was invisible from the outside — no error, no log line, no failing test — and each degraded the tool DFMT exists to make better than `Bash`.

### 1. `req.Timeout` did not stop anything

`execImpl` derived a `context.WithTimeout` and handed it to `exec.CommandContext`, which kills the process it started. `bash -c "a; b"` forks, so the command the agent actually cares about is a *grandchild*, and it inherits the stdout pipe. Killing the shell left it running and holding that pipe, so `io.ReadAll` blocked until the command finished on its own.

Measured in-process, against the current source, with `Timeout: 3s`:

```
code="sleep 20"                       elapsed=20.7s exit=1 timed_out=false stdout=""
code="echo START; sleep 20; echo END" elapsed=20.1s exit=1 timed_out=false stdout="START\n"
```

Three separate failures in one line: the deadline did not bound anything, `ExecResp.TimedOut` was never assigned by `execImpl` so it read `false` on every call ever made, and the output printed before the kill was discarded by the `readErr` path. Every layer above then reported the symptom in its own vocabulary — a client RPC deadline, an HTTP write deadline — so the agent saw a transport failure where the truth was "your command ran too long."

### 2. A 30-second ceiling under a 300-second contract

`http.Server.WriteTimeout` was 30s. Go's write deadline is absolute from the moment request headers are read, so it is a hard ceiling on handler duration — including `dfmt_exec`, whose schema advertises `"Timeout in seconds. Default: 60"` and whose sandbox clamps at `MaxExecTimeout` (300s).

Verified against a running daemon: `sleep 25` returned normally; `sleep 45` died with a bare `EOF` while the command kept running server-side. Nothing in that response distinguished a long build from a dead daemon. The same deadline tore down `/api/stream`, which is why the dashboard's live view reconnected every 30 seconds.

The practical consequence: `go test ./...` on this repository takes ~9 minutes, so **DFMT could not run its own test suite** through the tool meant to replace `Bash`.

### 3. Head-of-line blocking in the proxy

The `dfmt mcp` stdio loop was `read → handle → write`, strictly one at a time. JSON-RPC has always permitted concurrent in-flight requests correlated by `id`, and agents issue parallel tool calls routinely. One slow `dfmt_exec` therefore froze every other dfmt tool in the session. Observed during the review that produced this ADR: a `dfmt_read` waited more than two minutes behind a running `go test`, and the agent fell back to native tools — the exact outcome this project exists to prevent.

### 4. An auth token nobody checked

`HTTPServer.Start` generated a 32-byte token, stored it in `s.authToken`, and wrote **an empty string** into the port file. `wrapSecurity` carried the comment `// Auth disabled — all HTTP endpoints are publicly accessible`. Meanwhile `client.go` faithfully set `Authorization: Bearer` on every request. The server never read the field.

On Windows the RPC transport is loopback TCP, which carries no ACL: every process on the host could POST to `/` and reach `exec`, `write`, `edit`, and `fetch` — arbitrary code execution as the user running the daemon, against a policy that is permissive by design.

## Decision

**Deadlines are enforced against the process tree, and their verdict is reported.**

`configureChildProc` now puts the child in its own process group on Unix (`Setpgid`); `killProcessTree` signals the group there and shells out to `taskkill /T /F` on Windows, which walks the parent-pid tree. `cmd.Cancel` is wired to it, and `cmd.WaitDelay` (2s) bounds how long `Wait` may block afterwards, so a descendant that survives the kill cannot hold the call open. On the timeout path the read error is tolerated rather than returned, because the bytes printed before the deadline are the most useful thing a hung command leaves behind. `ExecResp.TimedOut` is set, and stderr gains an unmissable `(timed out after 3s; process tree killed)` line.

A Job Object would be the tighter Windows mechanism, but `os/exec` offers no hook between `CreateProcess` and the child's first instruction, so assignment would race the child's own spawns. `taskkill` runs after the fact and has no such window.

**`WriteTimeout` is 0, deliberately.** What bounds a handler is the handler: exec clamps to `MaxExecTimeout`, fetch to `MaxFetchTimeout`, and every RPC runs under a request context that is canceled when the client hangs up. `ReadHeaderTimeout` still covers Slowloris and `IdleTimeout` still reaps idle keep-alives, so no load-bearing protection was removed. SSE needs an unbounded write deadline by construction.

**The stdio loop reads serially and handles concurrently.** One reader, a mutex-guarded writer, and a bounded worker count (16). `initialize` stays inline because `handleInitialize` mutates the session ID and is documented as running before any tool call. On EOF the loop waits for in-flight calls so no response is dropped and no half-written line reaches stdout. The loop moved into `serveMCPStdio(ctx, in, out, handler)` so the property — a slow call must not delay a fast one — is testable with pipes instead of a live daemon.

**The token is published and required, on `/` only.** `writePortFile` receives the real token (the file is 0600, which is how a client learns it), and `wrapSecurity` checks it with a constant-time compare on the JSON-RPC endpoint. Scope is deliberately narrow:

- `/` is where every mutating capability lives, and it is the only endpoint whose clients can read the port file.
- The dashboard and the read-only `/api` endpoints it calls run in a browser with no token. Embedding one in an unauthenticated page would hand it to exactly the callers being excluded, so their posture is unchanged: same-origin plus Host validation.
- When `authToken` is empty the check is skipped, which keeps a server built without a port file (tests, embedded use) and the Unix socket transport (guarded by file mode) working unchanged.

## Consequences

Existing clients need no change: they already sent the header, and the token they read now has a value. An old client against a new daemon works for the same reason.

`dfmt_exec` can now run a build or a test suite to completion, which makes the 300s `MaxExecTimeout` the real ceiling rather than a documented fiction. Commands that exceed it get a clean `timed_out` verdict with partial output instead of a transport error.

Concurrent dispatch means the daemon can now see several tool calls from one session at once. That is already the HTTP server's normal condition, and the per-tool semaphores in `Handlers` (exec 4, read/fetch 8, write 4) remain the real concurrency limit; the proxy's cap of 16 only keeps goroutines bounded.

The kill path shells out to `taskkill.exe` on Windows, resolved under `SystemRoot` rather than `PATH`. That is one process spawn per timed-out exec — paid only on the timeout path.

Two behaviors are worth watching: `WaitDelay` returns `exec.ErrWaitDelay` when a descendant outlives the grace period, which is reported as `exit=-1` with `timed_out=true` and leaves that descendant to be reaped by the OS; and a `taskkill` that cannot run falls back to killing the leader alone, which restores the old orphan behavior for that one call rather than failing it.
