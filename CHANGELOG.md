# Changelog

All notable changes to **DFMT** ("Don't Fuck My Tokens") are
documented here. Format follows [Keep a Changelog
1.1.0](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

The wire surfaces under SemVer guarantees today are:

- The MCP tool names (`dfmt_exec`, `dfmt_read`, `dfmt_fetch`,
  `dfmt_glob`, `dfmt_grep`, `dfmt_edit`, `dfmt_write`,
  `dfmt_remember`, `dfmt_search`, `dfmt_recall`, `dfmt_stats`).
- The shape of `tools/call` request and response payloads
  (`structuredContent` envelope, the existing field set on each
  tool's input arguments).
- The CLI subcommand surface (`dfmt init`, `dfmt setup`,
  `dfmt daemon`, `dfmt mcp`, `dfmt doctor`, `dfmt remember`,
  `dfmt search`, `dfmt recall`, `dfmt stats`, `dfmt exec`,
  `dfmt read`, `dfmt fetch`, `dfmt glob`, `dfmt grep`, `dfmt edit`,
  `dfmt write`, `dfmt list`, `dfmt status`, `dfmt stop`,
  `dfmt install-hooks`, `dfmt shell-init`, `dfmt capture …`).
- The on-disk shape of `.dfmt/journal.jsonl` event records (`id`,
  `ts`, `project`, `type`, `priority`, `source`, `actor`, `data`,
  `refs`, `tags`, `sig`).

Internal package shapes (`internal/...`) are NOT covered by SemVer.

## [Unreleased]

## [0.6.1] — 2026-05-06

The cleanup release that closes v0.6.0's "Notes / known limitations".
Every dfmt subcommand now self-promotes the daemon in-process when
none is running, and the legacy auto-spawn path inside
`client.NewClient` is gone for good.

### Changed

- **Tool-call wrappers self-promote.** `dfmt exec`, `dfmt read`,
  `dfmt fetch`, `dfmt glob`, `dfmt grep`, `dfmt edit`, `dfmt write`
  use `acquireBackend` instead of `client.NewClient` directly. Same
  pattern as the read/write commands shipped in v0.6.0: connect to
  existing daemon, or become it. `dfmt exec` and `dfmt read` keep
  their direct-sandbox fallback for the truly-degraded case.
- **`client.NewClient` no longer spawns.** `ensureDaemon`,
  `startDaemon`, and the test-binary guard `isTestBinary` retired
  from `internal/client`. The legacy per-project port/socket
  fallback remains so surviving v0.3.x daemons keep working; the
  pre-v0.6.0 `exec.Command(self, "daemon", "--global")` is gone.

### Removed

- `internal/client.ensureDaemon` (unexported).
- `internal/client.startDaemon` (unexported).
- `internal/client.isTestBinary` (unexported; CLI side has its own
  copy in `internal/cli/dispatch.go`).
- The `DFMT_DISABLE_AUTOSTART` env-var opt-out is now a no-op since
  there's nothing left to disable. Documentation references it for
  back-compat with operator runbooks but the code path is gone.

### Notes

- The `exec.Command(self, ...)` count in the dfmt source is now
  zero outside the explicit `dfmt daemon` background-mode path
  (`startGlobalDaemonBackground` in `dispatch.go`). That one is
  opt-in by user invocation, not implicit auto-spawn.
- Terminal-blocking for short-lived commands (`dfmt stats`,
  `dfmt search`, etc. when no daemon is running) is still queued
  for v0.6.x. The detach helper (`FreeConsole` / `setsid+fork`) is
  the next item on the v0.6.x roadmap.

## [0.6.0] — 2026-05-06

The single-binary release. `dfmt mcp` and the main user-facing CLI
commands no longer spawn a separate `dfmt daemon` child via
`exec.Command`. When no daemon is running, the calling process
self-promotes via `daemon.PromoteInProcess`, binds the host-wide
listener, serves its own command using the in-process Handlers as
its Backend, and (for long-lived foreground commands) keeps running
until SIGINT or the idle-exit timer fires.

Net result: one `dfmt.exe` in `tasklist` instead of the v0.5.x pair
(MCP subprocess + spawned daemon). The user-visible promise: any
`dfmt <command>` invocation either connects to the existing daemon
or *becomes* the daemon, no second binary launched.

### Added

- **`daemon.PromoteInProcess(ctx, cfg)`** — constructs and starts a
  global daemon in the current process. Returns `*LockError` when
  another process already holds the host-wide lock so the caller
  can `errors.As` it and fall back to a thin RPC client.
- **`Daemon.Done() <-chan struct{}`** — closed on Stop / idle-exit.
  Long-lived subcommands block on it after their primary work
  completes.
- **`Daemon.Handlers() *transport.Handlers`** — exposes the live
  handler set so promoted-in-process callers can use it as a
  `transport.Backend` directly without an IPC roundtrip.
- **`cli.acquireBackend(projectPath)`** (internal) — single seam
  every daemon-touching subcommand goes through. Returns either a
  `client.ClientBackend` (existing daemon) or the in-process
  Handlers (promoted), plus the owned `*Daemon` when this process
  must keep it alive.

### Changed

- **`dfmt mcp`** no longer calls `client.NewClient`'s auto-spawn
  path. On daemon-not-running, the MCP subprocess self-promotes;
  when stdin EOF closes the MCP transport (Claude Code closed),
  the daemon role keeps serving inbound RPCs from other dfmt
  invocations on the same host until SIGINT or idle-exit.
- **`dfmt stats`, `dfmt search`, `dfmt recall`, `dfmt remember`,
  `dfmt tail`** apply the same self-promotion pattern. Tests that
  previously asserted "no daemon → return 1" now accept either 0
  (promoted + served) or 1 (legitimately degraded).

### Notes / known limitations

- **Terminal blocking for short-lived commands.** When `dfmt stats`
  or `dfmt search` runs without an existing daemon, it self-promotes
  and the daemon role keeps the process alive after the command
  output prints. The user's terminal blocks until SIGINT or the
  idle-exit timer (30 min default). Workarounds: shell-background
  the invocation, or run `dfmt mcp` / `dfmt daemon` first as a
  long-lived foreground process. A `FreeConsole` (Windows) /
  `setsid+fork` (Unix) detach helper is queued for v0.6.x.
- **Tool-call wrappers** (`dfmt exec`, `dfmt read`, `dfmt fetch`,
  `dfmt glob`, `dfmt grep`, `dfmt edit`, `dfmt write`) still use
  `client.NewClient` directly. They typically run through MCP
  rather than the CLI; the auto-spawn path inside `NewClient`
  remains alive until they migrate too. v0.6.x cleanup.
- **`client.NewClient`'s `startDaemon` / `exec.Command`** is still
  alive for the tool-call wrappers above. v0.6.x removes it once
  every caller routes through `acquireBackend`.

## [0.5.0] — 2026-05-06

The architectural cleanup release. `dfmt mcp` is now a thin proxy over
the global daemon — no more duplicate journal handles, no more
in-memory index drift between MCP subprocess and daemon. Per-project
filesystem capture, `dfmt remove` cache eviction, live SSE event log
on the dashboard, and a bounded multi-project cache.

### Breaking

- **`dfmt daemon --legacy` is gone.** The per-project daemon mode
  (kept on life support during the v0.4.x deprecation window) is
  removed. `dfmt daemon` always brings up the host-wide global
  daemon. Operators upgrading from v0.3.x must run `dfmt setup
  --refresh` (which already migrates legacy daemons in v0.4.x) and
  update any hand-rolled systemd / launchctl units to drop `--legacy`.

- **`dfmt mcp` requires a daemon.** The subprocess no longer opens
  its own journal/index/sandbox state. When the daemon is unreachable
  (and auto-spawn fails within ~4 s), tool calls return JSON-RPC
  error `-32603 daemon unreachable` instead of falling back to local
  state. There is no silent local mode — this is the deliberate
  trade-off behind closing the duplicate-journal-handle borç.

### Added

- **MCP proxy mode.** `dfmt mcp` forwards every tool call through
  `client.NewClient` to the daemon. The MCPProtocol now reads from a
  `transport.Backend` interface; the local `*Handlers` and the new
  `client.ClientBackend` adapter both satisfy it. Effects: zero
  duplicated journal handles, zero per-startup journal replay in the
  MCP subprocess, all per-project state owned by one process.

- **Per-project filesystem watcher.** Extra projects served by the
  global daemon now get their own `FSWatcher` when their config opts
  in via `capture.fs.enabled: true`. `ProjectResources.startFSWatch`
  spawns the consume goroutine; teardown order in
  `closeExtraProjects` is fswatch → index-tail → journal close so a
  shutting-down project never appends past its own journal close.
  v0.4.x only ran fswatch for the default project; secondary projects
  fell back to MCP/CLI events alone.

- **Live SSE journal tail.** `/api/stream?follow=true` now keeps the
  connection open after historical replay drains, polls every 2 s for
  events strictly after the last cursor, and forwards them to the
  client. The dashboard's project switcher opens an `EventSource`
  with `follow=true` so the new "Live Events" panel surfaces appends
  without a page reload. Without `follow=true` the endpoint stays in
  its pre-v0.5.0 one-shot replay contract — `dfmt tail` and
  `Client.StreamEvents` both depend on the channel closing at HEAD.

- **`dfmt remove` daemon cache eviction.** When a daemon is reachable,
  `dfmt remove` calls `dfmt.drop_project` so the running daemon
  evicts its `ProjectResources` cache entry (Checkpoint + Persist +
  Close, then drop) before .dfmt/ is deleted on disk. Without this,
  the daemon kept open file handles to the removed journal/index, the
  dashboard switcher continued to list the dead project, and the next
  `Resources(P)` call would race against the missing directory. New
  RPC method on the JSON-RPC surface (also accessible via the
  `drop_project` alias).

- **LRU eviction in extra-project cache.** The `extraProjects` map
  caps at 8 entries (`extraProjectsMaxEntries`). On a cache miss
  when full, the project with the oldest `LastActivityNs` is evicted
  before the new bundle loads. Eviction teardown runs in a goroutine
  after the cache entry is removed under the write lock, so concurrent
  `Resources()` lookups never block on the slow flush. A long-running
  global daemon serving 50+ projects no longer accumulates unbounded
  memory.

### Changed

- **`compressionStats` reads the journal via Backend.StreamEvents.**
  The MCP-side stats summary used to reach into `m.handlers.journal`
  directly; it now goes through the same Backend interface as every
  other tool call. Local mode reads the daemon's journal as before;
  proxy mode reads the daemon's journal via the network call.

### Notes

- `Client.NewClient`'s legacy port-file fallback (Step 2/2b/2c) is
  intentionally still present. Removing it requires updating ~20
  `TestClient_*` tests that seed legacy port files to assert behavior;
  that follow-up is queued for v0.5.x. User-visible behavior is
  unchanged: a v0.3.x port file still gets reaped on first auto-spawn
  and the global daemon takes over.

- Default project's filesystem watcher continues to use the existing
  `Daemon.fswatcher` / `Daemon.consumeFSWatch` path. Migrating it to
  `defaultRes` is a cleanup for a follow-up.

## [0.4.7] — 2026-05-06

The biggest pre-v0.5.0 correctness fix: the daemon's in-memory search
index no longer drifts away from on-disk journal state when another
process (most importantly `dfmt mcp` subprocesses) appends events.

### Fixed

- **MCP-subprocess index drift.** The `dfmt mcp` subprocess opens its
  own journal handle to the same `.dfmt/journal.jsonl` file as the
  daemon and appends events directly. The daemon's in-memory index
  was never updated for those events — `dfmt_search` results stayed
  stale until the next daemon restart (which on a global daemon
  serving long sessions could be hours away). A new per-project tail
  goroutine in `ProjectResources` polls the journal every 3 s and
  `Add()`s new events to the index. `Index.Add` is idempotent so
  events the daemon itself appended are safely re-processed as
  no-ops. Wired for both the default-project (legacy daemons) and
  extra-project (global daemon) paths. Closes drift within ~3 s.

### Note

This is the first piece of the v0.5.0 architectural cleanup landed
as a v0.4.x patch — additive only, no API or wire changes. The
larger MCP-proxy refactor (eliminating the duplicate journal handle
entirely) remains queued for v0.5.0.

## [0.4.6] — 2026-05-06

Patch release with two more global-daemon UX fixes after v0.4.5.

### Fixed

- **`dfmt status` works from any directory.** Previously errored out
  with `error: no project found` when invoked outside a DFMT-
  initialized directory, hiding the host-wide global daemon from any
  fresh terminal. The missing-project case is now a degraded report:
  the project line is annotated with the resolution error, but daemon
  liveness, dashboard URL, transport endpoint and last-crash all
  still render. JSON adds a `project_error` field so scripts can
  distinguish the no-project case from a real failure.
- **`dfmt setup --uninstall` stops the global daemon up front.** Was
  stripping agent MCP configs and removing manifest files but
  leaving the host-wide daemon running for up-to-the-idle-timeout,
  with stale handles open for every cached project. Also closed the
  awkward window where agent configs no longer pointed to dfmt but
  the daemon was still serving requests routed through them.
  Falls through with a warning if the stop fails so uninstall still
  completes.

## [0.4.5] — 2026-05-06

Patch release closing two more global-daemon UX gaps caught after
v0.4.4 shipped.

### Fixed

- **Dashboard initial load + refresh.** The page fired `loadStats()`
  with empty params before any project was selected, so the first
  `/api/stats` POST returned `-32603` (errProjectIDRequired) and the
  page showed an error before the user had done anything. Refresh
  button had the same problem — it always called `loadStats()`,
  silently navigating back to the empty-params view even after the
  user picked a project. `loadDaemons` now returns the first project
  path so init can preselect it; refresh routes through a wrapper
  that reads the selector value and falls back to bare `loadStats()`
  only for legacy single-project daemons.
- **`dfmt status --json` socket field.** Was always emitting the
  per-project legacy socket path under `socket`, regardless of which
  daemon was actually answering RPCs. In global daemon mode the
  per-project path is a stale v0.3.x artifact — the real listener is
  `~/.dfmt/daemon.sock` (Unix) or `~/.dfmt/port` (Windows). Operators
  debugging "why won't my client connect" got pointed at the wrong
  file. Now resolves the global path first when a host-wide daemon
  is up.

## [0.4.4] — 2026-05-06

Patch release closing four cross-project correctness gaps in the
host-wide daemon's resource cache. Each is a real bug that hits
users running one DFMT daemon for multiple projects (the v0.4.0
default).

### Fixed

- **Per-project redactor routing.** Every redactor-touching helper
  (`redactString`, `redactData`, `redactEventForRender`,
  `redactMatches`) read the default project's redactor on every call.
  In global daemon mode that meant project A's `redact.yaml` patterns
  scrubbed project B's tool output, and project B's own patterns
  never fired against its own bytes — a two-direction privacy leak.
  New `redactorFor(ctx)` helper resolves the per-project redactor
  via the bundle; legacy single-project daemons keep working through
  the fallback path.
- **Per-project content store + dedup key.** `stashContent` read
  `h.getStore()` (default project's store, nil in global mode) so
  every Exec / Read / Fetch returned `content_id=""` and the
  dashboard's "show content" links broke for every project. The
  dedup cache was also project-blind: identical bytes from project
  A and B collapsed to one chunk-set ID pointing into A's store,
  so B's lookup 404'd. Both fixed by routing through
  `bundle.ContentStore` + `bundle.ProjectPath` and prepending
  project ID to the dedup key.
- **Per-project config.** `loadProjectResources` reused the daemon's
  startup cfg for every cached extra project. A global daemon
  serving project A and B silently used A's retention / budget /
  path_prepend on every B call regardless of B's `.dfmt/config.yaml`.
  Each project now loads its own config; daemon-level cfg is the
  fallback when project YAML is missing or malformed.
- **Index persistence on shutdown.** `closeExtraProjects` (the Stop
  path for the per-project resource cache) closed each cached
  journal but never persisted its index. Every restart of a global
  daemon serving N projects forced a full journal replay per project
  on the next run — user-visible as a cold-recall pause that scaled
  with journal size. Persist now happens before Close, gated on
  `NeedsRebuild=false` to avoid writing a cursor=HEAD that would
  silently mark older events as indexed.

### Internal

- **Daemon registry honors `DFMT_GLOBAL_DIR`.** The `~/.dfmt/daemons.json`
  path now resolves on every save/load instead of caching at first
  `GetRegistry()` call, so test sandboxes and migration tooling get
  the right path. Test pollution of the developer's real registry
  from `internal/daemon/{TestRegister,TestUnregister}` closed via
  `t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())` + a deferred
  `unregister()` cleanup.

## [0.4.3] — 2026-05-06

Patch release closing three more global-daemon visibility gaps
caught in the v0.4.2 audit pass.

### Fixed

- **`dfmt mcp` registers project with the host-wide daemon at
  startup.** MCP-only projects (no other CLI command had touched
  them yet) were invisible to the dashboard's project switcher
  even though events were being recorded. `runMCP` now fires an
  async best-effort Stats RPC at startup so the daemon's
  Resources(projectID) lazy-loads the project into extraProjects.
  Never spawns a daemon, never blocks startup.
- **`dfmt list` shows the global daemon.** Was reporting "No
  running daemons" against a live host-wide daemon because the
  on-disk registry stays empty in global mode. Now synthesizes
  rows from `/api/all-daemons` so every project the daemon's
  resource cache holds appears in the listing.
- **`dfmt doctor` port + PID checks see the global paths.** Both
  rows flagged red under the global daemon because they only
  checked `<proj>/.dfmt/{port,daemon.pid}`. They now check the
  global paths first and fall back to per-project for v0.3.x
  straddle setups.

### Note

The deeper architectural issue — `dfmt mcp` writing to its own
journal/index handles instead of forwarding through the daemon —
is preserved for v0.5.0. The MCP startup ping is a v0.4.x
visibility fix, not the proxy refactor.

## [0.4.2] — 2026-05-06

Patch release closing two more audit findings against the global
daemon. v0.4.0 shipped the host-wide daemon; v0.4.1 wired the
dashboard cross-project switcher; v0.4.2 closes the gaps in the
SSE stream endpoint and the `dfmt stop` command.

### Fixed

- **`/api/stream` accepts `project_id` query param.** The SSE
  endpoint used by `Client.StreamEvents` and any future dashboard
  live-event view broke against the global daemon: Stream(ctx,...)
  was called with no project_id pushed in, so resolveBundle
  returned `project_id required: daemon has no default project`
  and every connection failed. `Client.StreamEvents` now appends
  the client's project_id to the URL on both Unix-socket and TCP
  paths.
- **`dfmt stop` actually stops the global daemon.** v0.4.0/v0.4.1
  read only `<proj>/.dfmt/daemon.pid`, saw no PID against a host-
  wide daemon, deleted per-project lock/socket files (which were
  never there), and printed "Daemon stopped" while the process
  kept running. `runStop` now short-circuits to a global-aware
  shutdown when `globalDashboardURL` is non-empty: SIGINT (or
  taskkill /T on Windows), 5 s graceful window, escalate to
  SIGKILL / taskkill /F if needed, then remove ~/.dfmt/{port,
  daemon.pid, lock, daemon.sock} once the process is gone.

## [0.4.1] — 2026-05-06

Patch release for the dashboard's cross-project switcher. Phase 2's
project_id routing already worked in v0.4.0 (the daemon serves every
project from one process, every RPC carries `project_id`), but the
dashboard's project-selector dropdown was reading from the on-disk
daemon registry — which the host-wide daemon doesn't populate. The
result was an empty dropdown that broke the user-visible promise of
"every project monitored from one dashboard."

### Fixed

- `/api/all-daemons` now enumerates the daemon's in-process project
  cache when the host daemon installs a `ProjectsLister` (global
  mode). Falls back to the on-disk registry only when no lister is
  installed (back-compat for v0.3.x straddle setups).
- Dashboard JS `loadStatsForProject` no longer routes through
  `/api/proxy` (which was designed for cross-daemon forwarding in the
  per-project model). It POSTs `/api/stats` with `project_id`
  stamped in params and lets the daemon's resolveBundle route to the
  right cache. `/api/proxy` is preserved on the daemon side for
  operators still running per-project daemons.

### Added

- `Daemon.LoadedProjects()` — returns the union of
  `defaultProjectPath` and the keys of `extraProjects` for the
  cross-project view.
- `Handlers.SetProjectsLister` / `LoadedProjects()` — parallel seam
  to `SetResourceFetcher` / `resolveBundle`.

## [0.4.0] — 2026-05-06

Phase 2: host-wide global daemon. The "tek daemon" rework — one DFMT
process per host serving every project, one stable dashboard URL, no
multi-process clutter in `tasklist` / `ps`. ADR-0019 (supersedes
ADR-0001) documents the architectural reversal.

### Breaking

- **One daemon, not N.** A single `dfmt daemon --global` process binds
  at `~/.dfmt/{port|daemon.sock,daemon.pid,lock}` and serves every
  project via lazy per-project resource caching. The legacy
  per-project daemon mode (`dfmt --project <p> daemon`) is preserved
  through the v0.4.x series for back-compat but operators should
  migrate via `dfmt setup --refresh`. v0.5.0 will remove it.
- **`flock` singleton.** Two `dfmt daemon --global` invocations on the
  same host cannot both bind. The second exits with a `LockError`
  carrying the global directory path.
- **Wire field.** Every JSON-RPC params struct now carries an
  `omitempty` `project_id` field. v0.3.x clients that omit it fall
  back to the daemon's `defaultProjectPath` (the legacy single-
  project pin) — backward compatible. New clients stamp it from the
  CLI's `--project` resolver or the MCP subprocess's session-bound
  cwd.

### Added

- `dfmt daemon --global` flag — host-wide daemon mode.
- `dfmt setup --refresh` migration step that stops legacy
  per-project daemons and removes their per-project transport
  scaffolding (port, daemon.sock, daemon.pid, lock). Project
  state (config, journal, index, content/) is preserved.
- `~/.dfmt/last-crash.log` — the global daemon writes a structured
  panic record (timestamp, version, panic value, full stack) on any
  unhandled panic via `safefs.WriteFileAtomic`. `dfmt doctor` and
  `dfmt --json status` surface the file's age and path.
- `internal/project/global.go` — `GlobalDir`, `GlobalSocketPath`,
  `GlobalPortPath`, `GlobalPIDPath`, `GlobalLockPath`,
  `GlobalCrashPath` helpers with `DFMT_GLOBAL_DIR` env override for
  tests / sandboxed environments.
- `daemon.NewGlobal(cfg)` constructor — daemon comes up with no
  per-project journal/index pre-loaded; resources resolve on each
  RPC via `Daemon.Resources(projectID)`.

### Changed

- `client.NewClient` connection order is now: probe global at
  `~/.dfmt/{port|daemon.sock}` → fall back to legacy per-project
  endpoint → auto-spawn global. The auto-spawn path uses
  `dfmt daemon --global`, so a fresh project's first call lands
  on the host-wide daemon.
- ADR-0001 (Per-Project Daemon Model) marked Superseded by ADR-0019.

### Migration

```sh
# v0.3.x → v0.4.0 upgrade path
dfmt setup --refresh   # stops legacy daemons, prints summary
dfmt status            # next call spawns the global daemon; URL stable
```

The migration is idempotent. Project journals, indexes, and
configuration files are never touched — only daemon transport
scaffolding (`<project>/.dfmt/{port,daemon.sock,daemon.pid,lock}`)
is removed.

## [0.3.2] — 2026-05-06

Security audit remediation cycle. The 4-phase pipeline (Recon → Hunt
→ Verify → Report) closed 26 findings: 0 critical, 4 high, 12 medium,
5 low, 5 informational. No exploitable issue reached production.

### Security

- **Recall snapshots are re-redacted before render** (V-01) — the
  recall path streamed events through markdown / JSON / XML
  formatters without re-applying the redactor, so a value redacted
  at journal-append time but updated by a later patch could leak
  through `dfmt_recall`. Redact now runs in both the inline-markdown
  and structured render paths.
- **Log file sink is `0o600`, not `0o644`** (V-02) — pre-fix the
  log file was world-readable; mkdir mode was `0o755` instead of
  `0o700`. Both tightened.
- **Deny rules normalized; reserved-name check hoisted into Write/
  Edit; default-policy doc aligned with default-permissive design**
  (V-03, V-05, V-15) — `extractBaseCommand` strips leading directory
  and treats tab/newline as IFS separators so `/usr/bin/sudo\twhoami`
  no longer slips past `deny:exec:sudo *`. `globToRegexShell` maps a
  literal space to `[ \t]+`. `safefs.CheckNoReservedNames` is now
  invoked on the Write/Edit hot path so `dfmt_write path="NUL"` is
  refused on Windows.
- **Setup writers preserve foreign MCP entries** (V-04) — pre-fix
  `dfmt setup` clobbered each agent's `mcp.json` with a single-key
  `{"mcpServers":{"dfmt":{...}}}`, silently destroying any other
  MCP servers (playwright, context7, github, …) the user had
  configured. New `MergeMCPServerEntry` splices our entry in and
  preserves every other key. A one-shot `<path>.dfmt.bak` pristine
  backup is captured on first patch.
- **Markdown injection in render pipeline closed** (V-06) — table
  cell pipes are escaped, code fence lengths grow with the body's
  longest backtick run, and recall ref-token forgery (`[r12]` from
  agent-controlled text) is escaped before render.
- **HTML tokenizer hardened** (V-07, V-08) — drop-set widened to
  cover `object`/`embed`/`applet`/`link`/`template`/`frame`/`frameset`/
  `math`/`portal`/`meta`. Raw-text scan switched to a windowed case-
  fold compare (was `strings.ToLower(t.src[t.pos:])` — O(N²) on
  pathological input). Token cap (200_000) and tag-depth cap (1024)
  added.
- **Sandbox Read closes TOCTOU via `O_NOFOLLOW`** (V-09) — Unix
  uses the syscall flag directly; Windows Lstat-then-Open with a
  reparse-point check on the leaf.
- **JSON decoders depth-capped on every agent-controlled path**
  (V-10) — new `internal/safejson` package; HTTP body decode (3
  call-sites), JSON-RPC envelope, journal lines, persisted index,
  and cursor file all run through `safejson.Unmarshal` with a
  64-deep nesting limit.
- **HTTP body and connection caps** (V-11, V-12) — `/api/proxy`
  bodies capped at 1 MiB (`MaxBytesReader`); HTTP and Unix-socket
  listeners wrapped with `LimitListener` (max 128 concurrent
  connections each).
- **In-memory index bounded** (V-13) — `MaxIndexDocs` (100_000)
  with FIFO eviction by oldest event ID. ULID time-sortable IDs
  give the eviction stable lexicographic ordering.
- **Setup integrity follow-ups** (V-14) — manifest now persisted
  BEFORE the agent file write so a save failure can never leave an
  injected file with no uninstall row. Claude trust flags
  (`hasTrustDialogAccepted`, `hasClaudeMdExternalIncludesApproved`,
  `hasClaudeMdExternalIncludesWarningShown`) are now captured to
  `<state>/claude-trust-prior.json` on first patch and restored on
  uninstall. Capture is idempotent so a re-patch doesn't lose the
  original state.
- **Redactor unicode-aware** (V-16) — bearer / generic-secret /
  basic / password patterns switched from ASCII `\s` to
  `[\s\p{Z}]` so NBSP / ZWSP / YAML whitespace-only emits don't
  bypass redaction. Truncated-PEM pattern added.
- **Fetch / exec timeouts clamped** (V-17) — pre-fix
  `time.Duration(params.Timeout) * time.Second` could overflow on
  large user-supplied values; the post-multiply `<= 0` floor reset
  to default but pinned a fetch semaphore slot for arbitrarily
  long. Now clamped to ceiling before multiplication.
- **MCP `tools/call` decoder is strict** (V-18) — was bare
  `json.Unmarshal`; now uses `decodeParams` (DisallowUnknownFields,
  trailing-token reject) plus `decodeRequiredParams` for tools whose
  schema needs at least one field. Agent-side typos (`limt: 10`)
  surface as -32602 instead of silently running with limit=0.
- **`safefs.WriteFile` clamps mode to user-only bits** (V-19) — the
  helper had been trusting caller-passed mode; one accidental
  `0o644` would have shipped world-readable secrets.
- **Setup writers all routed through safefs** (V-20) — `.dfmt/
  config.yaml` seed and the setup manifest itself were still using
  `os.WriteFile` (no symlink protection). Both now use
  `safefs.WriteFileAtomic`.
- **`/api/proxy` Unix-branch tightened** (V-21) — switched to
  `io.ReadAll(io.LimitReader(conn, 16<<20))` so multi-packet
  responses are read fully and oversized bodies surface explicitly
  rather than silently truncating; the post-write `err` shadowing
  is fixed.
- **Default response headers strict everywhere** (V-I1) —
  `wrapSecurity` now sets `X-Frame-Options: DENY` and a strict CSP
  (`default-src 'none'; frame-ancestors 'none'; base-uri 'none'`)
  on every non-health endpoint, not just `/dashboard`.
- **RFC 6598 CGNAT range blocked in SSRF defense** (V-I2) —
  `100.64.0.0/10` was uncovered by Go's `IsPrivate()` but routes to
  internal infrastructure on AWS NAT-gateway-fronted hosts.

## [0.3.1] — 2026-05-05

### Changed

- **DFMT no longer injects `deny` entries into Claude Code's
  `.claude/settings.json`** — pre-v0.3.1 init/setup added
  `permissions.deny: ["Bash", "WebFetch", "WebSearch"]` so the host
  agent could not call those tools natively. That call belongs to the
  user, not DFMT. Init/setup now register the MCP server and the
  PreToolUse routing hook only; the deny list is left untouched.
- **Stale legacy deny entries are pruned** — on the first post-upgrade
  init/setup, if `permissions.deny` contains the exact triple
  `{Bash, WebFetch, WebSearch}` that older DFMT versions injected,
  those three are removed. Any user-added entries (anything else, or
  any partial subset) are left in place. The heuristic deliberately
  errs on the side of preserving the user's policy.
- **HTTP transport: bearer-token auth removed** — the auth gate added
  in v0.2.7 is dropped. All HTTP endpoints (dashboard + JSON-RPC) are
  publicly accessible on the bound loopback port. Re-enable per-deployment
  with a reverse proxy if needed; the daemon binds 127.0.0.1 by default.

### Fixed

- **dev.sh / dev.ps1 build the right version string** — both scripts
  now stamp `v0.3.1` into `internal/version.Current`. dev.sh was
  previously frozen at `v0.2.7-dev`.

## [0.3.0] — 2026-05-05

### Changed

- **Default-permissive exec policy** — sandbox exec is now fully allowed by
  default. All commands (`gh`, `curl`, `sudo`, `rm`, etc.) pass without
  configuration. Operators can restrict specific commands via
  `.dfmt/permissions.yaml` if needed.
- **Default-permissive read/write/edit** — all read, write, and edit
  operations are allowed by default. Operators can restrict paths via
  `.dfmt/permissions.yaml`.
- **Hard-deny list cleared** — `hardDenyExecBaseCommands` is now empty.
  SSRF protections (cloud metadata IPs, file:// scheme) remain enforced via
  fetch deny rules.
- **`dfmt remove` command** — new `dfmt remove` (alias: `dfmt teardown`)
  undoes `dfmt init`: removes `.dfmt/`, strips DFMT block from
  `.claude/settings.json`, CLAUDE.md, and AGENTS.md. Does NOT touch
  agent MCP configs — use `dfmt setup --uninstall` for that.
- **Fallback mapping documented** — AGENTS.md and CLAUDE.md now explicitly
  list the fallback tool for each dfmt_* MCP tool.

## [0.2.7] — 2026-05-05

Security release: fixes and verifies 8 security findings.

### Security

- **Bearer token auth on HTTP endpoints** (AUTH-01/02) — all HTTP endpoints
  now require `Authorization: Bearer <token>` header. Token is generated
  on daemon startup and stored in `.dfmt/port` alongside the port number.
  Unauthenticated requests receive 401.
- **PATH prepend world-writable rejection** (CMDI-002) — `ValidatePathPrepend`
  now returns an error (not warning) when path_prepend entries are
  world-writable or non-existent. Daemon startup fails fast on invalid config.
- **Azure IMDS IP blocked** (SSRF-001) — `168.63.129.16` added to `isBlockedIP()`.
- **GCP metadata hostname blocklist expanded** (SSRF-002) — `metadata.goog.internal`
  and `metadata.goog.com` added alongside existing `metadata.google.internal`.
- **IPv6 AWS IMDS blocked** (SSRF-003) — `fd00:ec2::254` added to `isBlockedIP()`.
- **SSRF block logging** (SSRF-006) — blocked fetch attempts are now logged
  via `logging.Warnf` for operator visibility.
- **Dashboard CSP hardened** (XSS-01) — removed `unsafe-inline` from
  Content-Security-Policy header; replaced inline `style="display:none"`
  with class-based visibility toggling.

### Verified Protected (no action needed)

- CMDI-001/010 (shell chaining) — `hasShellChainOperators` + `splitByShellOperators`
- CMDI-003/004/009/01/02 — env var injection, LookPath, heredoc, here-string
- SSRF-005/007 — redirect metadata check, URL scheme enforcement
- RACE-01/02/03 — logger mutex, registry snapshot, FSWatcher recover

## [0.2.5] — 2026-05-03

Feature release: dashboard multi-project switching.

### Features

- **Dashboard multi-project dropdown** — the dashboard now shows all
  running daemon projects in a dropdown and lets you switch between them
  to view per-project stats. Two new HTTP endpoints support this:
  `/api/all-daemons` (returns unfiltered daemon registry) and
  `/api/proxy` (forwards requests to other daemons via HTTP, enabling
  the browser to talk to any daemon through the local relay without
  hitting same-origin restrictions).

### Internal

- `internal/transport/http.go` — added `handleAPIAllDaemons` and
  `handleAPIProxy` handlers
- `internal/transport/dashboard.go` — `loadStatsForProject()` and
  `projectSelect` change listener wired; `loadDaemons` now fetches
  `/api/all-daemons`

## [0.2.3] — 2026-05-02

Patch release: BatchExec stub now returns `ErrBatchExecNotImplemented`
instead of silently succeeding, preventing silent failures when batch
operations are called before the feature is implemented.

### Security

- **BatchExec stub returns error** — `BatchExec` in
  `internal/sandbox/permissions.go` now returns
  `ErrBatchExecNotImplemented` instead of `nil, nil`. Tests updated.
- **Write TOCTOU closed** — `safefs.WriteFile` now uses `O_NOFOLLOW`
  (Unix) / `FILE_FLAG_OPEN_REPARSE_POINT` (Windows) so a symlink at
  the leaf position is refused at open time, closing the residual
  TOCTOU window.
- **Panic recovery in long-running goroutines** — `consumeFSWatch`
  (all platforms), `journal.Append` scanner, and `daemon.idleMonitor`
  are now wrapped with `defer recover()` to prevent a single panic
  from terminating the daemon.
- **Doctor log-close errors now reported** — `runDoctor` surfaces
  file-close errors instead of silently suppressing them, fixing
  diagnosis on stale daemon paths.
- **stdlib CVEs patched** (GO-2026-4866/4870/4946/4947) — HTTP
  body lifecycle corrected in `Fetch`/`Exec`, `errors.Unwrap`
  chains added to JSON-RPC error responses, `RWMutex` write-skew
  fixed in handler stats cache, logging wrapper re-aligned.
- **Redaction dedup bypass closed** — `SetRedactor` now clears
  `dedupCache` / `sentCache` / `sentOrder` so a cached `content_id`
  never returns pre-redaction content under a changed redaction config.
- **LookPath cache staleness closed** — `Runtimes.Reload()` clears the
  binary-path cache and re-probes after a permitted exec may have
  mutated `PATH`.
- **Windows backslash normalization fixed** — `permissions.go` now
  replaces `\` with `/` for non-exec rules before regex matching.
- **Context leak in `tail --follow` closed** — cancel function properly
  captured and deferred in stream follow mode.
- **Redaction coverage expanded** — Azure storage account key pattern
  (`AccountKey=<86-char-base64>`) and GCP `client_email` JSON field
  matcher added to redaction patterns.

## [0.2.2] — 2026-05-01

Patch release. Config knob consolidation (ADR-0015 v0.4), metrics
instrumentation (ADR-0016/0017/0018), and operator override file
wiring (ADR-0014) land in this build. No wire-format changes.

### Added

- **`/metrics` Prometheus endpoint** — `GET /metrics` on the
  transport HTTP server emits in-tree Prometheus text format with
  gauges for index size, dedup-cache size, journal bytes, and
  tracked tool counts (ADR-0016).
- **Per-tool latency histograms** — `tool_call_duration_ms` per
  tool name, bucketed. `dfmt_stats` surfaces running totals
  (ADR-0018).
- **Per-tool call counters + dedup-hit counter** — `tool_calls_total`
  labelled by tool and outcome (success/allow/deny/error),
  `dedup_hits_total` for the content-stash dedup layer
  (ADR-0016 follow-up).
- **Journal `Size()` interface + `dfmt_journal_bytes` gauge** —
  `core.Journal` now exposes `Size() int64`; the daemon reports
  rotated-journal bytes to the `/metrics` endpoint (ADR-0017).

### Changed

- **Config knob wiring (ADR-0015 v0.4)** — the following
  previously-Reserved fields are now functional runtime gates:
  `transport.socket.enabled`, `logging.level`, `logging.format`,
  `retrieval.default_budget`, `retrieval.default_format`,
  `lifecycle.shutdown_timeout`, `index.bm25_k1`, `index.bm25_b`.
- **`.dfmt/redact.yaml` override wired** — operator-defined
  redact patterns are loaded and applied at daemon start
  (ADR-0014).
- **`.dfmt/permissions.yaml` override wired** — operator-defined
  exec allow rules are loaded and merged at daemon start,
  superseding defaults (ADR-0014).

### Fixed

- **Linux reserved-device rejection on Windows** —
  `safefs` now checks for Windows reserved device names
  (`CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`, case-insensitive)
  before `os.MkdirAll`, closing a path-confusion vector on
  cross-platform write paths.
- **glob regex precompilation** — `Rule` now compiles its glob
  pattern once at construction instead of on every match call,
  eliminating repeated regex compilation in hot sandbox paths.

### Internal

- **Linux race detector in CI** — `scripts/coverage-gate.sh`
  runs `go test -race` on Linux as a non-blocking report; any
  race reports are surfaced for developer follow-up.
- **Fuzz regression suite expanded** — BM25 search, HTML
  conversion, and glob matching now carry fuzz test coverage
  (Faz 4).
- **golangci-lint v2.4.0 → v2.11.4** — toolchain bump; closes
  130 lint findings across the tree.

## [0.2.1] — 2026-04-29

Patch release. The v0.2.0 binaries shipped before a Linux-only
security regression and a CI-toolchain mismatch were diagnosed
under WSL; v0.2.1 republishes the same feature set with both
closed. No wire-format or behaviour changes for end users on
Windows or macOS — Linux operators should upgrade.

### Security

- **F-03 closure on Linux** —
  `internal/sandbox/permissions.go::globMatch` previously called
  `filepath.ToSlash` to normalise path separators before matching
  deny rules. `ToSlash` is a no-op on Unix because `\` is a valid
  filename byte there, so a Windows-shaped path like
  `C:\proj\.env` would slip past a `**/.env*` deny rule when the
  daemon ran on Linux/WSL — re-opening the same gap F-03 had
  closed for Windows hosts. Switched to a cross-platform
  `strings.ReplaceAll(text, "\\", "/")` so both axes are
  normalised regardless of host OS. Regression test:
  `internal/sandbox/sandbox_test.go::TestGlobMatch_NormalizesPathSeparatorsForAllPathOps`.

### Fixed

- **`internal/content/store_ttl_test.go::TestStore_PruneExpiredCountsDropped`** —
  live sets used `Created = now-1h` with `TTL = 1h`, putting their
  expiration at exactly `now`. On a fast Linux runner
  `PruneExpired` would observe `now+ε > expiry` and reap them too
  (dropped=5 instead of 3). Bumped the live-set TTL to 24 h.
- **`internal/sandbox/sandbox_test.go::TestSandboxEditReadOnly`** —
  on POSIX, `rename(2)` checks the parent directory's mode, not
  the target file's, so a `0o444` file inside a `0o755` parent is
  still atomically replaceable by its owner. The test now also
  locks the parent directory to `0o555` (and restores it in a
  `defer` so `t.TempDir` cleanup can remove it). Windows behaviour
  is unchanged.
- **CI: golangci-lint v2.4.0 → v2.11.4** — v2.4.0 was built with
  go1.25 and panicked inside `go/types.(*Checker).initFiles` with
  `"file requires newer Go version go1.26 (application built with
  go1.25)"` once `setup-go@v5` started installing the go1.26
  toolchain. v2.11.4 is the first v2.x release built with go1.26.1
  and runs clean against this tree. The bump also turned on
  staticcheck QF1012; the 12 `WriteString(fmt.Sprintf(…))` sites in
  `cmd/dfmt-bench/tokensaving.go` were converted to
  `fmt.Fprintf(&builder, …)` (byte-identical bench output).

### Internal

- `internal/transport/http.go` — hoisted `runtime.GOOS == "windows"`
  literal into a `goosWindows` const (goconst threshold once Linux
  platform-only files compile in).
- `.golangci.yml` — added `internal/capture/fswatch*` exclusion
  for goconst (event-type literals `"create"` / `"modify"` /
  `"delete"` mirror inotify / `ReadDirectoryChangesW` operations
  and reading them inline is what one expects).

## [0.2.0] — 2026-04-29

First public release after the original v0.1.x prototype. Headline
work was a documentation/code consistency pass + a token-savings
hardening of the sandbox return path.

### Added

- **8-stage NormalizeOutput pipeline** for sandbox tool output —
  binary refusal, ANSI strip, CR-overwrite collapse, RLE,
  stack-trace path collapsing, git-diff `index` line drop,
  JSON/NDJSON/YAML noise compaction, Markdown frontmatter strip,
  HTML→markdown via the in-tree tokenizer (ADR-0008, ADR-0010).
- **Token-aware tier gating** (`ApproxTokens(s) =
  ascii_bytes/4 + non_ascii_runes`) replaces the byte-cinsinden
  tier check so CJK and Turkish bodies hit the same agent-cost
  thresholds as English (ADR-0012).
- **Cross-call wire dedup** — content stash keys on
  `sha256(kind, source, body)` for 30 s, and the MCP layer now
  tracks `content_id`s already emitted in this session and
  substitutes `(unchanged; same content_id)` on repeats
  (ADR-0009, ADR-0011).
- **`dfmt_search` excerpts** — each hit carries an opt-in
  `excerpt` field (≤ 80 bytes, rune-aligned) so agents get enough
  signal in a single round-trip to decide whether to follow up
  with `dfmt_recall`.
- **Journal event signing + verify-on-read** —
  `Event.ComputeSig()` runs on every `Append`, and
  `journal.Stream` / `scanLastID` re-verify with `Validate()` on
  every read. Tampered or mismatched lines are warn-and-skipped;
  legacy events (Sig == "") replay cleanly.
- **Expanded default exec allow-list** — `yarn`, `bun`, `npx`,
  `pnpx`, `bunx`, `deno`, `tsc`, `tsx`, `ts-node`, `vitest`,
  `jest`, `eslint`, `prettier`, `vite`, `next`, `webpack`,
  `make` (each pair: bare + `*` form). Removes the zero-config
  friction of having to drop a `permissions.yaml` override on
  every modern JS/TS project.
- **`.claude/settings.json` merge-safe auto-init** — the
  project-local Claude settings file is now structurally merged
  on every auto-init: pre-existing user keys
  (`mcp.callTimeoutMs`, `defaultMode`, `outputStyle`, custom
  permission entries) are preserved verbatim. Refuses to write
  under `$HOME/.claude/`.
- **Allow-rule trailing-space contract (V-20)** — exec allow
  rules ship as pairs (`<cmd>` + `<cmd> *`); the trailing
  space + `*` is the explicit end-of-token boundary so an entry
  like `git` no longer accidentally matches `git-shell` or
  `git-receive-pack`.
- **`docs/ROADMAP.md`**, **`SECURITY.md`**, **`CHANGELOG.md`**
  added; ARCHITECTURE.md synchronised with the post-V-20
  codebase.

### Changed

- **Three independent version strings consolidated** to a single
  source. `cmd/dfmt/version.go`'s `var version`,
  `internal/cli/Version`, and the literal `"0.1.0"` previously
  hard-coded into `transport/mcp.go::handleInitialize` are now
  all driven by `internal/version.Current`. Build sets the
  string via
  `-ldflags "-X github.com/ersinkoc/dfmt/internal/version.Current=$(VERSION)"`.
  `dfmt --version` and the MCP `serverInfo.version` returned by
  `initialize` now always agree.
- **CI: `golangci-lint` version pinned** in
  `.github/workflows/ci.yml` (was tracking `latest`). Lint rules
  no longer drift between PRs that don't change Go code.

### Fixed

- **V-9** — `journal.Stream` no longer silently drops malformed
  JSON lines; surfaces them as `journalWarnf` warnings with a
  snippet preview before skipping.
- **V-16 / V-17 / V-19** — transport API hygiene plus a Windows
  filesystem path case fix that affected project ID resolution
  on case-insensitive volumes.
- **V-18** — `index.gob`'s on-disk format documented as JSON
  payload (the `.gob` filename is retained for backwards
  compatibility with older daemons).
- **V-20** — exec allow-rule trailing-space contract above.

### Security

- **F-A-LOW-1** closure — operator-facing guidance for
  non-standard secret stores added to
  `internal/sandbox/permissions.go::DefaultPolicy()` doc
  comment.
- **gzip / file close on all paths** (commit 7fab730) — journal
  rotation now always closes the active file handle even on
  error paths; closes a handle leak that surfaced as
  `EBUSY`/`EIO` on Windows after long-running daemons.
- **Read-path event signature verification** (above under
  Added) — closes the in-place tampering blind spot called out
  in earlier audits.

### Known issues

These are documented stubs and unwired knobs that did not block
the v0.2.0 cut; they are tracked in `docs/ROADMAP.md` and slated
for v0.3.x:

- `storage.compress_rotated` config flag is wired through the
  option struct but the journal rotation path never invokes
  gzip; rotated `.jsonl.<ULID>.jsonl` segments stay plain JSONL.
- `index.heading_boost` config field is accepted and validated
  but not wired to any scoring path.
- `privacy.telemetry`, `privacy.remote_sync`,
  `privacy.allow_nonlocal_http` not wired — DFMT never
  phones home regardless.

[Unreleased]: https://github.com/ersinkoc/dfmt/compare/v0.3.0...HEAD
[0.2.8]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.8
[0.2.2]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.2
[0.2.1]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.1
[0.2.0]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.0
