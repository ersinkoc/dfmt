# ADR-0020: MCP Subprocess as Daemon Proxy + v0.5.0 Architectural Cleanup

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-05-06 |
| Deciders | Ersin Koç |
| Related | [ADR-0001](0001-per-project-daemon.md), [ADR-0019](0019-global-daemon.md), [ADR-0004](0004-dependency-policy.md), [ADR-0014](0014-operator-override-files.md) |

## Context

ADR-0019 moved every project under one host-wide daemon. v0.4.x stabilised that move (registry hygiene, dashboard cross-project switcher, status-from-anywhere, per-call project routing, project-scoped redactor + content store), but two structural debts remained from the migration:

1. **`dfmt mcp` opens its own journal handle.** Every MCP subprocess loaded `core.OpenJournal`, `core.LoadIndexWithCursor`, `sandbox.NewSandboxWithPolicy`, and `redact.LoadProjectRedactor` for its project at startup, even when a global daemon was already serving that exact project from another file handle. Effects:
   - Two file descriptors on the same `journal.jsonl`, racing to append (mitigated by the journal's batch writer but still wasteful).
   - Two in-memory indexes for the same project, each running BM25 against its own snapshot. The daemon's index drifted from the on-disk journal whenever the MCP subprocess appended an event the daemon's tail had not yet polled (closed in v0.4.7 with a 3 s tail goroutine, but the duplication remained).
   - Full journal replay on every MCP subprocess startup. On large journals this was multi-second cold-start cost paid by every agent session.
   - Two sandbox + redactor instantiations holding policy state.

2. **Legacy `--legacy` daemon mode.** ADR-0019's migration window kept per-project daemons accessible via `dfmt daemon --legacy` and via `client.NewClient`'s Step 2/2b/2c port-file fallback. Every code path now had to handle three modes: legacy direct, legacy fallback, global. Operators who upgraded via `dfmt setup --refresh` already lived in global-only mode; the legacy code paths were dead weight on their flow but live for v0.3.x holdouts.

In parallel, three smaller v0.5.0 milestone items were queued:

- `dfmt remove` deleted `.dfmt/` on disk but left a stale `ProjectResources` in the running daemon's cache, with open file handles to the just-deleted directory and an entry on the dashboard switcher.
- `/api/stream` was a one-shot historical replay: dashboards saw new events only after a manual reload.
- Filesystem capture only ran for the daemon's default project. Secondary projects' fswatch events were dropped.
- The `extraProjects` cache had no eviction. A long-running daemon serving 50 projects held 50 journals + 50 indexes + 50 sandboxes resident.

## Decision

**`dfmt mcp` becomes a thin proxy.** The subprocess no longer opens local journal/index/sandbox state. It instantiates a `client.Client` against the running daemon, wraps it in a `client.ClientBackend` adapter that satisfies a new `transport.Backend` interface, and constructs `transport.NewMCPProtocol(backend)`. Every tool call forwards to the daemon over the existing JSON-RPC transport.

The Backend interface extracts the surface MCPProtocol needs: 12 tool methods + `StreamEvents` for `compressionStats`. The local `*Handlers` and the new `*ClientBackend` both implement it. MCPProtocol's dispatch code is unchanged; only the field type narrowed from `*Handlers` to `Backend`.

**Daemon-unreachable is a hard error, not a fallback.** When `client.NewClient` fails to dial and auto-spawn within its ~4 s budget, `dfmt mcp` returns JSON-RPC `-32603 daemon unreachable` to the agent. There is no silent local mode. This is the explicit trade-off behind closing the duplicate-journal-handle borç: any local fallback re-introduces the duplicate handle and re-creates the drift.

**Per-project filesystem watcher.** `ProjectResources` gains a `FSWatcher` field, `startFSWatch` / `stopFSWatch` lifecycle, and a `consumeFSWatch` goroutine that mirrors `Daemon.consumeFSWatch` but routes to the project's own journal/index/redactor. `loadProjectResources` constructs the watcher when the project's `cfg.Capture.FS.Enabled` is true. `Resources()` calls `startFSWatch` on every cache miss, the same way it already calls `startIndexTail`. Default project still uses `Daemon.fswatcher` unchanged — migrating it to `defaultRes` is a follow-up cleanup.

**`Daemon.DropProject(projectID)` + `dfmt.drop_project` RPC.** The daemon evicts a single project from its cache: stop fswatch, stop index tail, Checkpoint + PersistIndex when safe, close journal, delete the cache entry. `dfmt remove` calls this via the new `Client.DropProject` method *before* deleting `.dfmt/` on disk; failure is logged but never blocks the on-disk removal. Default project is a no-op (its handles are owned by `Daemon.Stop`'s lifecycle).

**LRU eviction.** `extraProjects` caps at `extraProjectsMaxEntries = 8`. On a cache miss when the cap is hit, the project with the oldest `LastActivityNs` is evicted before the new bundle loads. Eviction teardown runs in a goroutine (after the cache entry is removed under the write lock) so concurrent `Resources()` lookups never block on the slow flush. Cap is `var` so tests can compress it.

**Live SSE journal tail.** `/api/stream?follow=true` keeps the connection open after the historical replay drains, polls every 2 s for events strictly after the last cursor, and forwards them. Without `follow=true` the endpoint stays in its pre-v0.5.0 one-shot replay contract — `dfmt tail` and `Client.StreamEvents` both depend on the channel closing at HEAD. Dashboard JS opens `EventSource` with `follow=true` and renders new appends in a "Live Events" panel.

**`dfmt daemon --legacy` is removed.** The flag is no longer accepted; passing it is a flag parse error. `dfmt daemon` always brings up the host-wide global daemon. `--global` remains accepted as a v0.4.0-rc compatibility alias. `runDaemonForeground` and `startDaemonBackground` stay compiled because internal/cli tests reference them; their CLI dispatch entrypoint is gone.

**`Client.NewClient`'s legacy port-file fallback stays.** Step 2/2b/2c (read `<project>/.dfmt/port`, dial it, reap on miss) are still present. Removing them requires updating ~20 `TestClient_*` tests that seed legacy port files to assert behavior. The follow-up is queued for v0.5.x. User-visible behavior is unchanged: a v0.3.x port file still gets reaped on first auto-spawn and the global daemon takes over.

## Alternatives Considered

### A. Keep the duplicate handle; treat the index-tail goroutine (v0.4.7) as the permanent fix

The 3 s tail goroutine closes the drift symptom. Why not stop there?

Rejected because:

- The duplication is not just an index correctness problem. Every MCP subprocess startup pays the journal-replay tax (multi-second on real journals), allocates duplicate sandbox + redactor + content-store state, and holds a second file descriptor on the journal. On a host with three concurrent agent sessions for the same project, that is three full BM25 indexes resident for one project's events.
- The "two handles to the same file" pattern is fragile under platform-specific I/O behavior. Windows file locking semantics differ enough that we'd have to keep maintaining defensive code in the journal writer for every edge case the duplication exposes.
- v0.4.7 was always framed as a workaround, not a fix. Carrying it forward means carrying both the workaround and the borç it patches.

### B. Full rewrite around a single index per project

A more aggressive variant: rip out the local `Handlers` instantiation in `dfmt mcp` *and* in every CLI subcommand, route everything through `client`. The CLI commands (`dfmt remember`, `dfmt search`, etc.) currently still construct local handlers when no daemon is running.

Rejected for v0.5.0 because:

- The CLI fallback is operationally important — `dfmt search` should work without a running daemon for one-off scripted use. Removing the local handler path would force operators to keep a daemon up for any CLI use.
- Scope. The MCP proxy alone touches transport/cli/client/daemon; pulling CLI through the same lens is a separate, larger commit.

### C. fsnotify-based SSE tail instead of polling

Replace the 2 s ticker in `handleAPIStream` with a fsnotify watch on `journal.jsonl`. Lower latency, no idle ticks.

Rejected because:

- ADR-0004 stdlib-only. Adding fsnotify (or a platform-specific watcher) is a new third-party dependency and the cross-platform behavior diverges enough that the maintenance cost exceeds the latency win for our use case.
- Polling cost is one stat + one short read per tick per connected SSE client. With one dashboard tab open (the realistic case) the cost is negligible. We revisit if `>10` sustained SSE clients ever shows up in metrics.

### D. Refuse to start `dfmt mcp` without a daemon (no auto-spawn)

Stricter than the chosen "auto-spawn, then hard-fail." MCP subprocess startup would print "no daemon, run `dfmt daemon`" and exit non-zero.

Rejected because:

- Claude Code (and similar agents) start the MCP subprocess automatically when the user opens a project. Demanding a manual `dfmt daemon` step before the agent can use any tool is a worse UX than the ~3 s spawn pause on first call.
- The spawn already happens lazily inside `client.NewClient`; the MCP proxy reuses that same retry budget for free.

## Migration Contract

### v0.4.x → v0.5.0

- Run `dfmt setup --refresh` if not already on v0.4.x global mode. The migration code path stays in v0.5.0; it stops any per-project legacy daemons and removes their transport scaffolding while preserving project state.
- Update any hand-rolled systemd / launchctl unit files that pass `--legacy` to `dfmt daemon`. Drop the flag. `dfmt daemon --foreground` is the new shape.
- MCP wire and CLI surface are unchanged. Existing scripts, agent configurations, and `.dfmt/config.yaml` files keep working.

### v0.3.x → v0.5.0

- Same as above. `dfmt setup --refresh` handles the legacy-daemon teardown. After refresh, the global daemon auto-spawns on first CLI/MCP call.

### Operators who relied on `Capture.FS.Enabled` for secondary projects

- v0.5.0 is the first version where this works. Prior versions silently dropped fswatch events for any project other than the daemon's default. No config migration needed; the on-disk YAML is read on next daemon start.

## Verification

- All 9 v0.5.0 commits pass `go test ./...` + `golangci-lint run ./...` independently.
- `TestFSWatcherPerExtraProject` proves per-project fswatch events land in the right project's journal and not the default's.
- `TestDropProjectClearsCache` proves `dfmt remove` cache eviction returns a fresh `ProjectResources` on next access.
- `TestExtraProjectsLRUEviction` proves the cap-and-evict logic picks the least-recently-used project.
- `TestHTTPHandleAPIStream_LiveTail` proves the polling tail forwards events appended after the historical replay.
- Manual end-to-end: build, install, two MCP sessions for two projects, append on each, verify dashboard live tail and zero MCP-side journal handles via `lsof` on Linux / Process Explorer on Windows.

## Consequences

### Positive

- One file handle, one index, one sandbox, one redactor per project, full stop. The duplicate-handle class of bugs is closed.
- MCP subprocess cold start drops from "open journal + replay + build index" to "dial socket". On a 100 MB journal this is the difference between seconds and milliseconds.
- Dashboard observability moves from "reload to see new events" to "events appear as they happen." Operators watching agent activity in real time no longer poll.
- Long-running global daemons stay bounded. A daemon serving 50 projects across uptime no longer accumulates 50 journals + 50 indexes resident.
- One less mode to test. Removing `--legacy` removes a code path from every CLI command's mental model.

### Negative

- `dfmt mcp` now requires the daemon. A zero-daemon environment cannot run agent tools at all. Mitigated by `client.NewClient`'s auto-spawn path: the first call brings up a daemon, every subsequent call is fast.
- Daemon panics now affect every agent session for every project simultaneously. ADR-0019's `recoverAndLogCrash` defer + `~/.dfmt/last-crash.log` is the answer; `dfmt doctor` surfaces the file's age.
- The `Backend` interface ossifies the contract between MCPProtocol and its backing implementation. Adding a tool means adding a method to `Backend`, the local `*Handlers`, and the `ClientBackend` adapter. mockgen + dependency injection are queued for a follow-up if this becomes burdensome.

## Out of Scope

- Cluster mode (cross-host daemon coordination).
- Per-project Prometheus metric labels.
- mockgen + dependency injection on top of `Backend`.
- WebSocket dashboard transport (SSE alternative).
- Removing `Client.NewClient`'s legacy port-file fallback (queued for v0.5.x).
- Migrating `Daemon.fswatcher` onto `defaultRes` (cosmetic; keeps the pattern parallel to extra projects).

## Status of v0.5.0 follow-ups from ADR-0019

- ~~`dfmt remove` cache eviction~~ → done.
- ~~Per-project fswatch~~ → done.
- ~~`extraProjects` LRU eviction~~ → done.
- ~~Live SSE tail~~ → done.
- ~~MCP proxy~~ → done.
- ~~Drop `--legacy` flag~~ → done.
- Per-project metrics labels → deferred to v0.5.x.
- Cluster mode → deferred indefinitely; awaits real demand.
