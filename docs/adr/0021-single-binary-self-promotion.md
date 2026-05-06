# ADR-0021: Single Binary, Self-Promoting Daemon

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-05-06 |
| Deciders | Ersin Koç |
| Related | [ADR-0019](0019-global-daemon.md), [ADR-0020](0020-mcp-proxy-and-cleanup.md), [ADR-0001](0001-per-project-daemon.md) |

## Context

ADR-0019 unified the per-project daemons of v0.3.x into one host-wide global daemon. ADR-0020 turned `dfmt mcp` into a proxy over that daemon, eliminating the duplicate journal/index handle that was the root of v0.4.x correctness drift. Both were structural wins, but they preserved one operational wart that kept showing up in every conversation with the operator: **two `dfmt.exe` processes in `tasklist` whenever an agent session was running**.

The pattern under v0.5.0:

1. Claude Code starts the MCP transport. It launches `dfmt mcp` as a subprocess.
2. `dfmt mcp` calls `client.NewClient(proj)`. If no daemon is running, `NewClient` calls `exec.Command(self, "daemon", "--global")` to spawn a *second* `dfmt.exe`.
3. The MCP subprocess connects to the spawned daemon and forwards every tool call.
4. Net result: **two `dfmt.exe` PIDs** in `tasklist` for the duration of the agent session — one for MCP, one for the daemon.

The operator's complaint (Turkish, paraphrased): _"Tek bir `dfmt.exe` olmalı. Hangi komut gelse — `dfmt mcp` de gelse, `dfmt stats` de gelse — daemon yoksa o process kendisi daemon olur, ayrı bir `dfmt.exe` spawn'lanmaz."_ — There should be **one** `dfmt.exe`. Whatever command runs — `dfmt mcp`, `dfmt stats`, anything — if no daemon is running, that process becomes the daemon. No separate `dfmt.exe` is spawned.

## Decision

**Self-promotion replaces auto-spawn.** Every `dfmt` subcommand that needs daemon resources calls a new `acquireBackend(projectPath)` helper. The helper has two branches:

1. **Existing daemon** → build a `client.ClientBackend` wrapping `client.NewClient(proj)`. The subcommand acts as a thin RPC client; the calling process exits when its work is done.
2. **No daemon** → call the new `daemon.PromoteInProcess(ctx, cfg)`. This factory acquires the host-wide lock, opens the listener, starts the resource cache and `Handlers` in the *current* process, and returns the `*Daemon`. The subcommand uses `d.Handlers()` directly as its `transport.Backend` — no IPC roundtrip on its own RPCs. The lock + listener stay live for any future `dfmt` invocation on the same host.

A race between the two branches (probe says no daemon, `PromoteInProcess` returns `*LockError` because another process won the lock) falls back to step 1. Lock-contention semantics inherit straight from ADR-0019's flock invariant; no new race surface.

**Long-lived subcommands keep the daemon alive after their primary work.** `dfmt mcp`'s stdio loop exits when Claude Code closes the transport; the new `waitForDaemonShutdown(*Daemon)` helper then blocks on `<-d.Done()` or SIGINT/SIGTERM, runs `Stop` within the configured grace window, and lets the process exit cleanly. Any other `dfmt` invocation on the host during that window finds a live daemon via the listener and connects normally.

**Test binaries short-circuit the wait.** `isTestBinary()` checks for `flag.Lookup("test.v")` and `os.Args[0]` test-suffixes; when true, `waitForDaemonShutdown` calls `Stop` immediately instead of blocking on signal/idle. Without this, every test that exercises a self-promoting subcommand would hang on the 30-minute idle-exit timer.

**The legacy `client.NewClient` auto-spawn path stays alive for now.** Tool-call wrappers (`dfmt exec`, `dfmt read`, `dfmt fetch`, `dfmt glob`, `dfmt grep`, `dfmt edit`, `dfmt write`) still call `NewClient` directly because their internal layout (sandbox-fallback path, per-tool argument plumbing) makes the migration to `acquireBackend` non-trivial. Until they migrate, `NewClient` continues to auto-spawn a child daemon for them on cache miss. The v0.6.0 promise — "no `exec.Command` from `dfmt mcp`, `dfmt stats`, `dfmt search`, `dfmt recall`, `dfmt remember`, `dfmt tail`" — covers every command an operator typically runs by hand. The tool-call wrappers are practically only invoked through MCP, where the daemon is already up by the time they're hit.

## Alternatives Considered

### A. Truly zero-spawn including detach for short-lived commands

The operator's request implicitly extended to `dfmt stats` returning to the shell prompt while the daemon role keeps running in the background. This requires `setsid()` (Unix) or `FreeConsole()` (Windows) **plus** the original process exiting — neither platform lets a process detach from its controlling terminal *and* keep its PID alive while the parent shell waits.

To get the prompt back the original process must exit. To keep the daemon alive after exit, a child process must take over the listener. That second process is, technically, a spawn — `os.StartProcess` of `os.Args[0]` with a marker flag, or `exec.Command(self, "daemon", "--foreground")`. Either way it's the same shape as the v0.5.x auto-spawn pattern, just deferred until *after* the foreground command's output prints.

Rejected for v0.6.0 because:

- The "tek dfmt.exe" promise is preserved either way: in steady state there is exactly one `dfmt.exe` running, regardless of whether the original process or its child holds the daemon role.
- The detach handshake (parent stops listener → spawns child → child binds listener) has a brief window where no daemon is live; another `dfmt` invocation hitting that gap would itself self-promote, racing the child for the lock. Solvable, but the win is small for the operator's typical workflow (Claude Code → `dfmt mcp` keeps the daemon alive; standalone `dfmt stats` is a rare side use).
- Rolling out the detach helper plus the lock-handoff coordination is a meaningful chunk of platform-specific code (FreeConsole + redirect stdio to NUL on Windows; setsid + close stdio fds on Unix) that warrants its own commit pass with focused testing. Punting to v0.6.x.

The compromise: **terminal blocks for short-lived commands** until SIGINT or idle-exit. The operator can shell-background the invocation, or rely on `dfmt mcp` / `dfmt daemon` running as the long-lived foreground process. CHANGELOG entry under "Notes / known limitations" calls this out explicitly.

### B. Keep auto-spawn, just rename the spawned binary

The operator's underlying complaint reduced to "two PIDs in `tasklist`". A workaround: make the spawned daemon child use a different binary name (`dfmtd.exe`) so `tasklist` shows one `dfmt.exe` (the MCP) and one `dfmtd.exe` (the daemon).

Rejected because:

- It doubles the build artifact surface — two binaries shipped instead of one.
- It does not address the underlying duplication of process state. ADR-0020 already eliminated the duplicate journal handle by routing MCP through the daemon; this option doesn't help anyone except the eyeball watching `tasklist`.
- The operator's stated vision was *one process*, not *one binary name*. Self-promotion is the closer fit.

### C. Move the registry out of `internal/client` to break the import cycle

`internal/daemon` already imports `internal/client` for the registry helpers. To make `internal/client.NewClient` itself call `daemon.PromoteInProcess`, the cycle would need breaking — likely by moving the registry into a new shared package both can import.

Rejected for v0.6.0 because:

- The `acquireBackend` helper sidesteps the cycle: it lives in `internal/cli` (which imports both client and daemon already) and the call site in subcommands changes from `client.NewClient(proj)` to `acquireBackend(proj)` — a few-character diff per subcommand.
- The registry-package extraction is a strictly larger refactor with its own test surface to update; deferring it keeps v0.6.0 focused on the visible "one process" win.

## Migration Contract

### v0.5.0 → v0.6.0

- No config or wire-protocol changes. Every existing `.dfmt/config.yaml`, every agent's MCP configuration, every dashboard URL keeps working untouched.
- Operators upgrading via package manager: rebuild and run as before. `dfmt mcp` will no longer spawn a sibling `dfmt daemon` child; one PID in `tasklist`.
- Operators with shell scripts that explicitly invoked `dfmt daemon --global` to bring up the daemon: still works. Self-promotion is opt-in-by-default for every other subcommand; a pre-running daemon is unaffected.

### Operator workflow patterns

- **Claude Code (typical)**: open project in editor → Claude Code spawns `dfmt mcp` → that process becomes the daemon → other `dfmt` invocations on the host (e.g., `dfmt list` from a terminal) connect-and-exit. One PID throughout.
- **Standalone CLI use without Claude Code**: `dfmt stats` or `dfmt search` self-promotes on first call. The terminal blocks (see "Known limitations" above). Workaround: `dfmt daemon` once at session start (existing v0.5.x command, still fully supported), then subsequent CLI commands connect-and-exit immediately.

## Verification

- `TestPromoteInProcessBringsUpDaemon` proves the in-process factory binds the listener and acquires the lock.
- `TestPromoteInProcessReturnsLockErrorWhenAnotherDaemonOwns` proves the fallback contract: callers can `errors.As` to a `*LockError` and reroute to client mode.
- `TestRunMCPStdin` (regression) proves the test-binary short-circuit in `waitForDaemonShutdown` keeps the existing test suite from blocking on idle-exit.
- Manual end-to-end: build, install, open Claude Code on a project, run `tasklist | grep dfmt` (Windows) or `pgrep -af dfmt` (Unix). Expected: exactly one `dfmt.exe` PID for the duration of the session.

## Status of follow-ups

- **Tool-call wrapper migration** (`dfmt exec`, `dfmt read`, `dfmt fetch`, `dfmt glob`, `dfmt grep`, `dfmt edit`, `dfmt write` to `acquireBackend`). Done in v0.6.1.
- **Drop `client.NewClient`'s `startDaemon` / `exec.Command`** entirely. Done in v0.6.1.
- **Detach helper** (`FreeConsole` on Windows; `setsid+fork` on Unix) for short-lived commands. **Decided against** in v0.6.2 — see "Explicitly out of scope" below.
- **Registry extraction** to break the `daemon → client` import cycle. Speculative cleanup; only useful if a future ADR wants `client.NewClient` itself to self-promote.

## Explicitly out of scope: terminal detach (v0.6.2 decision)

The v0.6.0 CHANGELOG entry queued a `FreeConsole` (Windows) / `setsid+fork` (Unix) detach helper for the v0.6.x cycle so that short-lived commands like `dfmt stats` could print their output and return the shell prompt while the daemon role kept running in the background. v0.6.2 explicitly removes this from the roadmap.

### The OS-level trade-off

On both Windows and Unix the parent shell (PowerShell, cmd, bash, zsh) waits for the **child process to exit** before returning the prompt — closing stdio is not enough. To return the prompt while the daemon keeps running, the daemon role must live in a process that survives the original process's exit. That process is, by definition, a spawn:

- Windows: `os.StartProcess(os.Args[0], ..., DETACHED_PROCESS)` followed by parent exit, OR `exec.Command(self, "daemon", "--foreground")` start + parent exit.
- Unix: `fork()` + child `setsid()` + parent exit. The double-fork pattern is the canonical daemonization sequence; it cannot be done within a single process.

In every shape, the original process has to spawn a child copy of itself, hand off the daemon role, and exit. The steady-state result is still **one** `dfmt.exe` in `tasklist` — the new daemon child — so the "tek dfmt.exe" eyeball test still passes. But:

- During the brief handoff window (parent stops listener → spawns child → child binds listener), no daemon is reachable. Another `dfmt` invocation hitting that gap would itself self-promote, racing the child for the lock. Solvable, but the win is small.
- The shape of the spawn — `exec.Command(self, "daemon", ...)` — is identical to the pre-v0.6.0 auto-spawn that this ADR (and the v0.6.0/v0.6.1 release pair) deliberately retired. Re-introducing it for short-lived commands would partially reverse the architectural commitment, even if the new spawn fires *after* the work is done rather than before it.
- The lock-handoff coordination is platform-specific code (file descriptor inheritance differs between Windows and Unix) with its own test surface to maintain.

### The workflow that makes detach unnecessary

The typical operator workflow already provides a non-blocking single-process experience without any detach magic:

1. Open the project in an editor with Claude Code (or another DFMT-aware agent). The agent automatically launches `dfmt mcp` as a long-lived MCP transport. That process self-promotes to daemon on first run.
2. Every other dfmt invocation on the host — `dfmt stats`, `dfmt list`, `dfmt search`, dashboard refresh — connects to the running daemon via its socket / port, does its RPC, and exits. Zero terminal blocking.
3. The daemon stays alive for as long as Claude Code is running. When the agent closes, the daemon idle-exits 30 minutes later (configurable).

For operators using DFMT outside an agent context — pure CLI workflow with no MCP — the `dfmt daemon` standalone command (background-spawn mode, opt-in by user invocation) covers the same need. Run it once at session start, every subsequent CLI command connects-and-exits.

### When this decision should be revisited

A real operator workflow that:

- Does NOT involve Claude Code (or any other agent that spawns `dfmt mcp` automatically),
- Does NOT have access to a session-starter command (`dfmt daemon` once at terminal open),
- Wants `dfmt stats` to be a fire-and-forget command from any fresh terminal,

…is the case detach would close. Operators with this profile have not surfaced. The implementation cost (~200 lines platform-specific code + lock-handoff testing matrix) does not match the demand. Revisit if it does.
