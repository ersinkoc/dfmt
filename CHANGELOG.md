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
