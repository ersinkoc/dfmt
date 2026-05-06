# ADR-0019: Host-Wide Global Daemon

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-05-06 |
| Deciders | Ersin Koç |
| Supersedes | [ADR-0001](0001-per-project-daemon.md) |
| Related | [ADR-0014](0014-operator-override-files.md), [ADR-0011](0011-per-session-wire-dedup.md) |

## Context

ADR-0001 (Per-Project Daemon Model) chose one daemon per project as the operational unit. After eight months of dogfooding the model and watching real users adopt it, three problems became unignorable:

1. **Multiple `dfmt` processes in `tasklist` / `ps`.** Every project the user touched in a session left a daemon idling for 30 minutes. Operators reported four-to-six concurrent daemons after a normal workday — six independent inverted indexes, six file-watchers, six Prometheus exporters on different ports. A user could not answer "is dfmt running?" without a follow-up "for which project?" and `dfmt list` was the only tool that bridged the gap.

2. **Dashboard URL fragmentation.** Each daemon served `/dashboard` on its own port. Switching projects meant re-discovering the URL, bookmarking it, then watching the bookmark go stale when the daemon idle-exited. The "I want to glance at recent dfmt activity" use-case devolved into a port-hunting exercise. ADR-0001's "no global cache" property was preserved at the cost of a usable observability surface.

3. **Cold-start latency stacked.** Each new project's first call paid the ~100 ms startup tax in ADR-0001 — invisible per-project, but a user touching three new projects in a session felt the staircase. Worse, a user with already-running daemons for projects A and B who then opened project C saw the daemon for C come up alongside them, not in place of them.

The Phase 2 goal (Turkish, from operator brief): _"tek bir DFMT daemon — bir kez bağlanılır, spawn yok, dashboard dahil ayakta, CLI sadece URL'i alır, tüm projeler aynı dashboard'tan izlenir"_ — one DFMT daemon, single connection, no spawning, dashboard always up, the CLI just gets a URL, every project monitored from one place. This requires a fundamental rework of ADR-0001's scoping rule.

## Decision

**One daemon per host.** The daemon binds at host-scoped paths (`~/.dfmt/daemon.sock` on Unix, `~/.dfmt/port` on Windows TCP loopback), holds a singleton lock at `~/.dfmt/lock`, and writes its PID to `~/.dfmt/daemon.pid`. Every CLI invocation, every MCP subprocess, and every dashboard page-load talks to the same process.

The daemon owns a **lazy per-project resource cache** keyed by project root. The first RPC for a project loads `config.yaml`, `journal.jsonl`, `index.gob`, `permissions.yaml`, and `redact.yaml` into a `ProjectResources` struct kept in `Daemon.extraProjects`. Subsequent RPCs reuse the cached handles. Per-project data lives where it always lived (in `<project>/.dfmt/`); only the transport endpoint moves up to `~/.dfmt/`.

**Wire-level routing.** Every RPC params struct gains a `ProjectID string` field stamped by the client (CLI flow) or the MCP subprocess (agent flow). Empty falls back to the daemon's `defaultProjectPath` (legacy single-project pin) so v0.3.x clients continue to work during the v0.4.x deprecation window. The `Handlers.resolveBundle(ctx)` path uses a `ResourceFetcher` closure so handler code does not import `daemon` — the dependency stays one-way.

**Idle / shutdown.** The 30-minute idle timer becomes daemon-process-global rather than per-project: any RPC for any project resets it. Per-project last-activity timestamps live on `ProjectResources` for future per-project metrics but are not yet surfaced. On `Stop` the daemon flushes every cached project's journal and persists every index — the same per-project guarantees, fanned out over the cache.

**Singleton enforcement.** `flock` on `~/.dfmt/lock` ensures two `dfmt daemon --global` invocations on the same host cannot both bind. The second exits with a `LockError` carrying the global directory path so operators can tell global-vs-legacy contention apart from a stuck file.

**Crash containment.** ADR-0001 had per-process blast radius — one project's panic killed only that project's daemon. The global model widens this: a panic in any handler takes down RPCs for every project. To compensate, `cmd/dfmt/main.go` installs a `recoverAndLogCrash` defer that captures the panic value, the dfmt version, an RFC3339 timestamp, and the full stack trace to `~/.dfmt/last-crash.log` via `safefs.WriteFileAtomic`. `dfmt doctor` and `dfmt --json status` surface the file's age and path without flipping their exit codes red — a stale crash log is informational, not an error.

**Migration.** `dfmt setup --refresh` now enumerates the daemon registry, sends SIGINT (taskkill /T on Windows) to every legacy per-project daemon, waits up to 3 s for graceful shutdown, escalates to SIGKILL/taskkill /F on holdouts, and removes per-project transport scaffolding (`port`, `daemon.sock`, `daemon.pid`, `lock`). Project state — `config.yaml`, `journal.jsonl`, `index.gob`, `content/` — is intentionally preserved.

## Alternatives Considered

### A. Keep per-project model; add a "dashboard aggregator"

A separate small daemon at `~/.dfmt/dashboard.sock` would scrape the per-project daemons and serve a unified dashboard, leaving ADR-0001 unchanged.

Rejected because:

- It ships a third process type. We already have CLI and daemon; aggregator would be the third consumer of the wire format, the third place to break on protocol changes, the third thing to teach operators about.
- The "tek daemon" goal (one process in `tasklist`) is unmet — the aggregator just adds another.
- Cold-start latency stacking is unsolved — every new project still spawns its own daemon.

### B. Per-project daemon with shared singleton bind

One daemon serves the project that started it; subsequent RPCs from other projects fail with "wrong daemon, please switch directories." User-hostile and requires teaching every agent integration about project pinning.

Rejected outright. The whole Phase 2 motivation is that users do not think in terms of which daemon is bound to which project.

### C. Cluster mode (one process, multiple project worker goroutines)

Project A's RPCs handled by worker pool A, project B's by worker pool B, sharded inside the same process. Effectively the chosen design but with per-project resource isolation pushed harder — separate goroutines, separate channels, optional separate panic-recovery domains.

Rejected as premature. The lazy cache + shared dispatcher is simpler and meets the goal; if real workloads show panic-blast-radius problems we can introduce per-project workers later under a follow-up ADR. ADR process favors accepting the simplest workable model.

### D. Status quo, fix only the dashboard URL

Add a `~/.dfmt/dashboard.json` file written by every daemon listing its `(project, port)` so clients could discover them. Smallest possible change.

Rejected because it solves visibility but not the underlying multi-process problem. Operators still see N daemons in `tasklist`, still pay N startup costs, still face the synchronization tax of N parallel inverted indexes.

## Consequences

### Positive

- Exactly one `dfmt` process in `tasklist` / `ps` regardless of how many projects the operator touches in a session. Resource accounting becomes trivial.
- Dashboard URL is stable for the lifetime of the daemon; a single bookmark works across project switches. The dashboard can render every cached project's recent activity in one place (future work; the data model now allows it).
- Cold-start cost paid once per host per session, not per project. A user with five projects sees a 5× speedup on the second-through-fifth project's first RPC.
- Migration is shipped — `dfmt setup --refresh` makes the upgrade a one-command operation and prints a clear summary so operators know the fleet has converged.

### Negative

- **Wider crash blast radius.** A panic in any handler now affects every project's in-flight RPCs. Mitigated by the `recoverAndLogCrash` handler and the `dfmt doctor` last-crash row, but never fully eliminated. If real-world panics show this is too much to ask, a per-project worker model (Alternative C) is the planned escalation path.
- **Cross-project memory pressure.** Every cached project keeps its inverted index in memory. A user with 50 projects and large journals could OOM. Today's `Daemon.Resources()` keeps everything cached for the daemon's lifetime; an LRU policy is a follow-up if real users hit this.
- **Migration risk.** Operators with hand-rolled per-project daemon setups (e.g. systemd units pinning `dfmt --project /srv/foo daemon`) will see them killed by `setup --refresh`. The legacy `dfmt daemon --project <p>` mode is preserved through v0.4.x for back-compat; v0.5.0 will remove it under a separate ADR.

### Neutral

- The on-disk schema for `~/.dfmt/daemons.json` (registry) gains a future `Mode` field. v0.4.x writers do not set it; readers tolerate empty as "legacy". Schema bump deferred to v0.5.0 alongside the legacy-mode removal.

## Implementation Notes

### Touched packages

- `internal/project/global.go` (new) — Path helpers (`GlobalDir`, `GlobalSocketPath`, `GlobalPortPath`, `GlobalPIDPath`, `GlobalLockPath`, `GlobalCrashPath`) with `DFMT_GLOBAL_DIR` env override for tests / sandboxed environments.
- `internal/transport/handlers.go` — `ProjectID` on every params struct; `Bundle` + `ResourceFetcher` seam; `resolveBundle(ctx)` helper.
- `internal/transport/session.go` — `WithProjectID` / `ProjectIDFrom` mirroring the existing session-ID plumbing.
- `internal/transport/mcp.go` — `MCPProtocol.projectID` session-bound, stamped on every `tools/call` dispatch.
- `internal/daemon/projectres.go` (new) — `ProjectResources` struct, lazy `Daemon.Resources(projectID)` loader.
- `internal/daemon/daemon.go` — `NewGlobal` constructor, `globalMode` flag, `extraProjects` cache, register/unregister no-op in global mode.
- `internal/daemon/lock.go` — `AcquireGlobalLock` + shared `acquireLockAt`.
- `internal/client/client.go` — three-step connection order: probe global → legacy fallback → auto-spawn global. `globalMode` field; `globalDaemonTarget` + `fastDialOK` helpers.
- `internal/cli/dispatch.go` — `dfmt daemon --global` flag, `runGlobalDaemonForeground`, `startGlobalDaemonBackground`, `runSetupRefresh` legacy migration, doctor last-crash row, status `last_crash` JSON field.
- `cmd/dfmt/main.go` — `dispatchWithRecover` panic→crash-log handler.

### Test coverage

- `internal/project/global_test.go` — path resolution under `DFMT_GLOBAL_DIR`.
- `internal/transport/session_test.go`, `mcp_project_test.go` — context plumbing, MCP stamping, resolveBundle paths.
- `internal/daemon/projectres_test.go`, `global_test.go` — cache hits/misses, `NewGlobal` lifecycle, singleton lock under contention.
- `internal/client/client_global_test.go` — global-first selection, fastDialOK rejection.
- `internal/cli/dispatch_migration_test.go` — scaffolding cleanup, dead-PID early return.
- `internal/cli/dispatch_crash_test.go` — doctor / status renderers.
- `cmd/dfmt/main_test.go` — crash log format and overwrite semantics.

## v0.5.0 Follow-Ups (Out of Scope Here)

- Remove legacy `dfmt daemon --project <p>` mode and the legacy fallback path in `client.NewClient`.
- Drop the `Mode` field forward-compat in `~/.dfmt/daemons.json` and bump the schema version.
- Decide on cluster mode (Alternative C) based on observed panic blast-radius incidents.
- Optional: LRU eviction for `Daemon.extraProjects` if memory pressure shows up under heavy multi-project workloads.
