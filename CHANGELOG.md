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

_None yet._

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

- `dfmt tail --follow` prints `"(tail --follow not yet
  implemented)"`; use `dfmt search` / `dfmt recall` for journal
  inspection.
- `dfmt config` is read-only — operators edit
  `.dfmt/config.yaml` directly to change values.
- `dfmt task done <id>` prints `"Task <id> marked done"` but does
  not journal a `task.done` event.
- `.dfmt/permissions.yaml` and `.dfmt/redact.yaml` overlay
  loaders exist (`sandbox.LoadPolicy`, `redact.AddPattern`) but
  are not yet called at daemon start.
- `storage.compress_rotated` config flag is wired through the
  option struct but the journal rotation path never invokes
  gzip; rotated `.jsonl.<ULID>.jsonl` segments stay plain JSONL.
- Several BM25 / index / retrieval / lifecycle config fields
  are accepted and validated but unwired to the corresponding
  consumer (see ARCHITECTURE.md §13.0 wired/unwired table).

[Unreleased]: https://github.com/ersinkoc/dfmt/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.1
[0.2.0]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.0
