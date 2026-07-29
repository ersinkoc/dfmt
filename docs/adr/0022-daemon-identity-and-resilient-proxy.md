# ADR-0022: Daemon Build Identity and a Self-Healing MCP Proxy

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-07-29 |
| Deciders | Ersin Koç |
| Related | [ADR-0019](0019-global-daemon.md), [ADR-0020](0020-mcp-proxy-and-cleanup.md), [ADR-0021](0021-single-binary-self-promotion.md) |

## Context

ADR-0019 through ADR-0021 converged on one host-wide daemon that outlives every command, holds a singleton lock, and serves every project. That is the right architecture, and it works. But it created a category of failure none of those ADRs addressed: **the daemon's own identity is invisible, and nothing notices when it stops being the thing you think it is.**

Two failures of that kind shipped, and both presented as *silently wrong answers* rather than errors — the worst possible signature, because there is nothing to debug.

### 1. Liveness is not identity

`client.fastDialOK` decides "is a daemon running" with a socket dial. Any build satisfies a dial. Nothing anywhere read `version.Current` except `--version` output and the MCP `serverInfo` block.

So rebuilding and reinstalling DFMT did nothing: the old process kept the lock and kept answering. Its replies were well-formed, so no error surfaced anywhere. Observed on the maintainer's own machine: a **May-15 binary serving a July v0.6.9 checkout**, returning `{"exit":0}` with empty stdout for every `dfmt_exec`. The fix for the underlying exec bug had been written, committed, and installed — and had no effect whatsoever, because the code that ran was months old.

The developer case is worse than the release case. Working on DFMT means rebuilding many times at the same tag, so even a version comparison would have compared equal on every iteration while the daemon served replaced code.

### 2. A proxy that resolves its connection exactly once

`dfmt mcp` (ADR-0020) resolves its backend at startup via `acquireBackendForLongRunner` and never revalidates it. The daemon idle-exits after 30 minutes (`lifecycle.idle_timeout`, deliberately retained). Agent sessions routinely idle longer than that.

The result was not a degraded experience but a **guaranteed** one: idle past the timeout, and every subsequent tool call returned a raw platform dial error (`connectex: No connection could be made`) for the remainder of the session. Nothing recovered on its own. The user had to notice the tools were dead and reconnect the MCP server by hand — which is exactly what happened, repeatedly, before this was diagnosed.

## Decision

### Publish build identity, and treat a mismatch as a restart trigger

The global daemon writes `~/.dfmt/daemon.json` — `{version, pid, exe, exe_fingerprint, started_at}` — in `Daemon.Start`, next to the PID file, and removes it in `Stop`.

It is written **by the daemon itself, not by the HTTP server**, because a socket-only daemon with HTTP disabled writes no port file, and the check must hold on every transport.

`Identity.IsCurrent(clientVersion)` combines two independent signals:

| Signal | Catches |
| --- | --- |
| Version skew | One installed release replaced by another |
| Fingerprint skew (`exe` modtime + size, re-statted) | The daemon's binary overwritten while it kept running — a rebuild, or an installer writing over it |

The fingerprint is the load-bearing half for anyone developing DFMT, where the tag never moves. It is modtime+size rather than a hash deliberately: the question is "was this replaced", not "is this authentic", and hashing a 13 MB executable before every command would cost far more than it tells us. This is not a tamper-detection mechanism and must not be mistaken for one — the threat model (ADR-0019) is a single-user, single-trust-principal local daemon.

`inspectGlobalDaemon` gains a `globalDaemonStale` state alongside Dead/Running/Stuck/Orphan; `ensureGlobalDaemon` handles it by stopping the old daemon and spawning a fresh one.

Invariants, each chosen against a specific failure:

- **Missing or malformed `daemon.json` counts as stale.** This is what lets the first run of an upgraded binary replace a pre-identity daemon — without it the mechanism could never bootstrap.
- **An unstatable `exe` is NOT stale.** A moved or deleted binary is not evidence of stale code, and treating it as such would restart the daemon on every single command.
- **An unstamped client** (plain `go build`, no `-ldflags`) matches any *version*, so a contributor's dev build never restarts the daemon serving everyone else's projects. The fingerprint check still applies to it — "the file I am running from changed" is valid regardless of stamping.
- **One restart per process.** If the fresh daemon still mismatches, warn and continue. A broken identity write must degrade to a warning, never become a restart loop.
- **Never under `isTestBinary()` or `DFMT_DISABLE_AUTOSTART=1`.** Those environments manage the daemon deliberately; killing a developer's daemon mid-suite would be a worse surprise than a version skew.
- **`acquireBackend` calls `ensureGlobalDaemon` unconditionally.** The previous `if !client.DaemonRunning(...)` guard skipped the check on precisely the path that mattered: a stale daemon *is* running.

### Make the proxy self-healing

`cli.reconnectingBackend` wraps the client backend for the `dfmt mcp` process. On a failed call it re-runs `ensureGlobalDaemon` (spawning or replacing as needed), rebuilds the client, and retries once.

Detection deliberately **does not parse the error**. Dial failures surface as wrapped, platform- and locale-specific strings, and a string match would rot at the first Go release or Windows locale that reworded them. Instead the wrapper asks the question it can actually answer — *is a daemon reachable right now?* — using the same liveness probe every other path uses. If one is reachable, the error came from the call itself and is returned untouched.

That distinction is the safety property: **only a vanished daemon is retryable.** A policy denial, a bad path, or a failing command surfaces immediately and unchanged, because retrying those would double whatever side effects the first attempt already had.

`StreamEvents` is exempt. It returns a channel outliving the call, so transparently re-subscribing would silently restart the event position and duplicate or skip events; a mid-stream death is the consumer's to observe via channel close.

## Alternatives Considered

### A. Warn on version skew instead of restarting

Rejected. It preserves the actual defect — the user keeps getting answers from code they already replaced — and converts a silent failure into a warning most callers never see, since MCP stderr is not surfaced to the agent. The whole value of the singleton daemon is that it is invisible; an invisible component that needs manual intervention after every upgrade is worse than one that restarts itself in ~200 ms.

### B. Refuse to serve until the operator runs `dfmt stop`

Rejected. Correct but hostile: it breaks every command on the host until manual intervention, including commands for projects entirely unrelated to whatever was just rebuilt.

### C. Hash the binary instead of modtime+size

Rejected on cost. The check runs before every command; hashing 13 MB to answer "did this file change" is orders of magnitude more expensive than a `stat`, and buys authenticity guarantees the threat model does not ask for. Modtime+size fails only if a replacement preserves both exactly — not a scenario a local developer's rebuild produces.

### D. A shutdown RPC so the daemon always stops gracefully

Deferred, not rejected. On Windows a `DETACHED_PROCESS` daemon owns no console, so no external process can deliver a console control event; `stopGlobalDaemon` waits 5 s and force-kills. An authenticated shutdown endpoint would fix that, but it adds a remotely-triggerable state change to the HTTP surface and the current cost is bounded and understood: the journal is append-only and its reader already tolerates a truncated trailing line, so the only loss is the `index.gob` cache, rebuilt by replay, once per upgrade.

### E. Retry every failed call, not just connection failures

Rejected. `dfmt_exec` and `dfmt_write` are not idempotent. A retry that cannot distinguish "the daemon vanished before it ran" from "it ran and then the response was lost" will re-run side effects. Gating the retry on a liveness probe keeps the retryable set to the case where we know no work happened.

## Consequences

**Positive.** An upgrade takes effect, including a same-tag rebuild. A daemon that dies mid-session is invisible to the agent. `status`, `list`, and `doctor` report which build is serving and warn when it is stale, so the class of failure is now diagnosable from one command instead of inferred from wrong answers.

**Negative.** An upgrade costs one ~200 ms daemon restart, during which in-flight RPCs from other projects' agents fail once. On Windows that restart force-kills after a 5 s graceful attempt and discards the index cache. Both were accepted over serving stale results.

**Neutral.** `~/.dfmt/daemon.json` is new state to clean up; `dfmt stop` and `Daemon.Stop` both remove it, and a stale copy next to a dead listener reads as "stale" — the safe direction.
