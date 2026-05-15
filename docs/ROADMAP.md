# DFMT roadmap

This file tracks the load-bearing work between the current
release and the next two milestones. It is meant to be **short
and current** — items that ship are deleted from here and
recorded in `CHANGELOG.md`; items that miss a milestone slip to
the next one with a one-line explanation. If you want the full
"why each thing exists" reasoning, the corresponding ADR in
`docs/adr/` is the place.

The release identities:

- **v0.6.7** — *Current shipped cut* (single-binary,
  self-promoting daemon; see ADR-0019, ADR-0020, ADR-0021).
- **v0.7.0** — *Navigability & test-floor* (the next
  milestone).
- **v1.0.0** — *Stability commitment* (SemVer guarantees on
  the wire surfaces enumerated in `CHANGELOG.md`).

For everything that already shipped on the v0.2 → v0.6 line,
see `CHANGELOG.md` — this file no longer maintains a release
ledger.

## v0.6.x — Single-binary self-promoting daemon (shipped)

The v0.6 line landed the architectural cleanup tracked through
ADR-0019/0020/0021: one host-wide global daemon, lazy-loaded
per-project resources, self-promotion of short-lived `dfmt`
invocations into a detached daemon child. See `CLAUDE.md` and
`AGENTS.md` for the steady-state behavior contract. The line
closed with **v0.6.7** (2026-05-08) fixing `--help` mutating
state on `task` / `install-hooks` (CHANGELOG `[Unreleased]`
moved into v0.6.7 at tag time).

The v0.3 punch-list ("stop shipping placeholders" — wire
`.dfmt/permissions.yaml`, `.dfmt/redact.yaml`, `dfmt tail`,
`dfmt config get/set`, `SnapshotBuilder` recall) is **all
shipped**. Detailed entries live in `CHANGELOG.md`.

## v0.7.0 — Navigability & test-floor

The theme: **reduce the surface area for future bugs**. The
codebase is now ~32 KLOC across 96 source files; three files
hold 30 % of the total (dispatch / permissions / handlers).
v0.7 is a refactor-and-coverage cut — no new user-facing
features, but the package layout, test floor, and CI gates
land in a state we can stand behind at v1.0.

See `refactor.md` (repo root) for the detailed audit and
file-split proposals. The check-boxes below summarise the
deliverables.

### Refactor (split god-files, no behavior change)

- [ ] **`internal/cli/dispatch.go` (5 087 LOC) → 10 files**.
  Group by responsibility — `daemon.go`, `project.go`,
  `recall.go`, `tools.go`, `setup.go`, `doctor.go`, `mcp.go`,
  `dashboard.go`, `config.go` — with `dispatch.go` shrinking
  to ~150 LOC (Dispatch + global flag handling). One PR per
  file to keep diffs reviewable. Carries over from the v0.3
  "stretch" item, now load-bearing.
- [ ] **`internal/sandbox/permissions.go` (2 542 LOC) → 8
  files**. Separate policy types, glob compiler, shell
  parser, and the seven tool primitives (`Exec`, `Read`,
  `Fetch`, `Glob`, `Grep`, `Edit`, `Write`).
- [ ] **`internal/transport/handlers.go` (2 208 LOC) → 7
  files**. One file per tool handler; shared plumbing
  (dedup, redact, store) stays in the root handlers.go.
- [ ] **`internal/daemon/daemon.go::New` (261 LOC)** —
  extract `loadIndexJournal`, `startFSWatcher`,
  `bindListener` private helpers so the constructor reads
  as a 4-step recipe.
- [ ] **`internal/transport/mcp.go::handleToolsList` (302
  LOC)** — move per-tool schemas to `mcp_schemas.go` as
  `var toolSchemaXxx = json.RawMessage(...)`.

### Test-floor

The CLAUDE.md / AGENTS.md targets drifted out of compliance:

| Package | Current | Target | Gap |
|---|---|---|---|
| `internal/core` | 91.5 % | ≥ 90 % | ✅ |
| `internal/transport` | 82.4 % | ≥ 85 % | −2.6 pp |
| `internal/daemon` | 75.2 % | ≥ 80 % | −4.8 pp |
| `internal/cli` | 63.5 % | ≥ 75 % | **−11.5 pp** |

- [ ] **`internal/cli` ≥ 75 %**. The `dispatch.go` split
  enables targeted unit tests per subcommand. Existing
  integration tests stay; new tests go after the split.
- [ ] **`internal/daemon` ≥ 80 %**. `daemon.New` decomposition
  makes the constructor branches reachable.
- [ ] **`internal/transport` ≥ 85 %**. Mostly a matter of
  HTTP-handler coverage (`handleAPIProxy`, `handleAPIStream`).

### CI hardening

- [ ] **Re-enable PR-time CI**. Today the workflow is
  release-only (commit 3cab1f9). A 90-second
  `go vet + golangci-lint + go test -short` gate on PRs
  would catch the kind of `gofmt` drift the v0.7.0 cleanup
  commit erased (13 files at audit time).
- [ ] **Race detector on Linux + macOS** in the CI matrix.
  Carry-over from the v1.0 list — promoted here because
  the v0.7 refactors will touch concurrency-sensitive code.
- [ ] **Coverage gate** enforcing the targets above.

### Config-schema hygiene (carry-over from v0.3)

- [ ] **`storage.compress_rotated`**: accepted in schema;
  `journal.Rotate()` renames files but never invokes gzip.
  Either wire it or drop with an ADR.
- [ ] **`index.heading_boost`**: stored on the `Index`
  struct; no scoring path reads it. ADR-or-remove.
- [ ] **`privacy.telemetry` / `privacy.remote_sync` /
  `privacy.allow_nonlocal_http`**: all Reserved. Defaults
  are safe; no consumer reads them. ADR-or-remove.
- [ ] **`logging.format`**: validated to only "text"; the
  JSON output path doesn't exist. ADR-or-implement.

## v1.0.0 — Stability commitment

The promise: **SemVer guarantees apply** to the wire surfaces
listed in `CHANGELOG.md`. Breaking changes in those surfaces
require a major bump. The pre-1.0 work is to make sure we are
not promising something built on sand.

### Reserved-code resolution

ARCHITECTURE.md § 18 lists code that lives in the binary with
full test coverage but no production caller. Each row is a
v1.0 decision point — either wire it (with an ADR) or remove
it (with a removal ADR):

- [x] `internal/retrieve/` — **wired in v0.3** via the
  `SnapshotBuilder` integration into `handlers.Recall`. Keep.
- [ ] `content.Summarizer` (`internal/content/summarize.go`)
  — replace `sandbox.GenerateSummary` for kind-aware
  summaries, or remove.
- [ ] `core.EnglishStopwords` (the 70-entry exported map) —
  wire `Index.Add` to use it (with an ADR — changes BM25
  postings on existing journals), or downgrade to
  unexported.
- [x] `core.Levenshtein` + `core.FuzzyMatch` — **removed**
  in ADR-0013; the `fuzzy` Search layer remains accepted
  for forward compatibility but returns no results.
- [ ] `capture.GitCapture` / `capture.ShellCapture` builders
  — collapse `internal/cli/dispatch.go::buildCaptureParams`
  onto these helpers (deduplicates the inline construction)
  or remove the helpers.

### Wire compat freeze

- [ ] **MCP wire-shape regression suite** — golden-file
  fixtures for every `tools/call` shape (request +
  response). Any change after v1.0 ought to fail this suite
  before it can land, forcing the ADR + major-bump
  conversation.
- [ ] **JSONL event regression suite** — same idea for
  `journal.jsonl` lines. Ensures we cannot accidentally
  break backwards-compatible replay of older project
  journals on a v1.x daemon.
- [ ] **CLI flag inventory** — `dfmt --help` golden-file
  test for every subcommand so a removed or renamed flag
  shows up in CI.

### Operational ergonomics

- [ ] **`dfmt doctor` exits 0 on the unhappy paths today**
  for stale port / pid / lock files (the hard-failure test
  table flips to 1 only on a small subset). Decide whether
  these should be hard fails for v1.0; retiring the warning
  rows would tighten the operator contract.
- [ ] **`dfmt list` cross-host display** — when
  `~/.dfmt/daemons.json` includes daemons from a previous
  hostname (laptop reimaged, container rebuilt), show them
  as "stale, run `dfmt list --prune`" rather than as live
  rows.

### Documentation

- [ ] **`docs/MIGRATING.md`** — once we cut v1.0, document
  any user-visible behavior shifts from the v0.x line so
  external dashboards / scripts can adapt.
- [ ] **Public API reference** — godoc'd
  `internal/version.Current`, the MCP wire shapes, and the
  CLI surface. Today the godoc is uneven; v1.0 should ship
  a coherent reference the operators can lean on.
- [ ] **`docs/ARCHITECTURE.md` is 3 394 lines** — consider
  splitting by subsystem under `docs/arch/` and keeping
  `ARCHITECTURE.md` as a 2-page index. Same pattern as the
  ADR layout.

---

*Owner: Ersin Koç (`ersinkoc@gmail.com`).
Open a GitHub issue with the `roadmap` label to discuss
priorities, or send a PR that updates a check-box plus the
relevant `CHANGELOG.md` entry.*
