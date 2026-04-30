# DFMT roadmap

This file tracks the load-bearing work between the current
release and the next two milestones. It is meant to be **short
and current** — items that ship are deleted from here and
recorded in `CHANGELOG.md`; items that miss a milestone slip to
the next one with a one-line explanation. If you want the full
"why each thing exists" reasoning, the corresponding ADR in
`docs/adr/` is the place.

The release identities:

- **v0.2.0** — *Hardening release* (current cut).
- **v0.3.0** — *Operator UX*.
- **v1.0.0** — *Stability commitment* (SemVer guarantees on
  the wire surfaces enumerated in `CHANGELOG.md`).

## v0.2.0 — Hardening release (shipped 2026-04-29)

The cut that brought the code and the docs back into agreement
and added the missing release artifacts. Everything in this
section is **done**; the entries below are the punch-list
references that link `CHANGELOG.md` back to the work.

- [x] `docs/ARCHITECTURE.md` synchronised with the post-V-20
  codebase: 10-stage NormalizeOutput pipeline, ApproxTokens
  tier gating, cross-call wire dedup, search excerpts,
  expanded default exec allow-list, journal event signing +
  verify-on-read, V-20 trailing-space contract, ADR
  0009–0012 added to the index.
- [x] `CHANGELOG.md` started, `SECURITY.md` published,
  `docs/ROADMAP.md` (this file) written.
- [x] Three version strings consolidated into
  `internal/version.Current` (driven by ldflags). MCP
  `serverInfo.version` and `dfmt --version` now agree.
- [x] CI: `golangci-lint` version pinned in
  `.github/workflows/ci.yml` (was `latest`).

## v0.3.0 — Operator UX

The theme: **stop shipping placeholders**. Every documented stub
or unwired knob in v0.2.0 either lands a real implementation, or
gets a removal ADR if we conclude the feature was a mistake.
Users should be able to act on the hints DFMT prints today.

### Sandbox & permissions

- [ ] **Wire `.dfmt/permissions.yaml`** into
  `sandbox.NewSandbox(projectPath)`. The parser
  (`sandbox.LoadPolicy`) exists and is tested; the daemon
  currently always installs `DefaultPolicy()`. Goal: the
  hint message DFMT prints on every denial ("add
  `allow:exec:<cmd> *` to .dfmt/permissions.yaml") becomes
  a true escape hatch instead of a forward-looking promise.
- [ ] **Wire `.dfmt/redact.yaml`** into the redactor at
  daemon start. `Redactor.AddPattern` exists; the loader is
  the missing half. Operators with site-specific credential
  shapes (custom vault keys, tenant-scoped tokens, internal
  OAuth flows) currently have to fork the binary.

### CLI command stubs → real implementations

- [ ] **`dfmt tail`** — real journal tail with `--follow`.
  Today: `runTail` (`internal/cli/dispatch.go`) prints a
  not-implemented stub.
- [ ] **`dfmt config get/set`** — mutate `.dfmt/config.yaml`
  through the CLI rather than requiring a hand-edit. Today:
  `runConfig` is read-only and only prints three fields.
  Schema-aware `set` should validate against
  `Config.Validate()` before writing.
- [ ] **`dfmt task done <id>`** — actually journal a
  `task.done` event keyed on the referenced task. Today:
  prints "Task `<id>` marked done" and returns without
  writing anything.

### Recall / retrieval

- [ ] **Wire `internal/retrieve/SnapshotBuilder` into
  `handlers.Recall`**, replacing the inline tier-bucket fill.
  This is what lets path interning (Refs table at the top of
  the snapshot + `[rN]` references in event lines) actually
  surface to agents — the renderer is implemented in
  `internal/retrieve/render_md.go` but unwired today.
- [ ] **Honor `RecallParams.Format`** — the `format` field
  is accepted (`md` / `json` / `xml`) but the production
  handler ignores it and always emits markdown. JSON is
  occasionally useful for tooling; XML is a leftover and
  should either be implemented or dropped.

### Config-schema hygiene

- [ ] **Decision on `storage.compress_rotated`**: either wire
  gzip into `journal.Rotate()` (real space saving on
  long-running projects) or drop the flag with an ADR.
  Today the value is accepted but the rotation path never
  invokes gzip.
- [ ] **Decision on `index.bm25_k1` / `index.bm25_b` /
  `index.heading_boost`**: wire into the BM25 scorer or drop
  with an ADR. Today `NewBM25Okapi` hardcodes 1.2 / 0.75
  regardless of config.
- [ ] **Decision on `retrieval.default_budget` /
  `retrieval.default_format`**: wire into `Recall`'s default
  path or drop. Today `Recall` hardcodes 4096 / `"md"`.
- [ ] **Decision on `lifecycle.shutdown_timeout`**: wire into
  `daemon.Stop()` or drop. Today `Stop()` uses a hardcoded
  10 s.
- [ ] **Decision on `index.rebuild_interval`,
  `index.stopwords_path`, `retrieval.throttle.*`,
  `privacy.telemetry`, `privacy.remote_sync`,
  `privacy.allow_nonlocal_http`, `logging.level`,
  `logging.format`** — each is in the schema but no consumer
  reads it. Pick one of: wire, drop, or document with a
  pending-ADR pointer.

### Logging

- [ ] **Drive logger threshold from `logging.level`** in
  `.dfmt/config.yaml` instead of (only) `DFMT_LOG`. Both
  fields stay supported; config wins, env overrides at the
  process level.

### Stretch (only if v0.3 has runway)

- [ ] **Refactor `internal/cli/dispatch.go`** — the file is
  ~3 300 lines today. Group subcommands into per-command
  files (`dispatch_setup.go`, `dispatch_exec.go`, …) and
  retire the giant switch. No behavior change; pure
  readability work. Skip if the v0.3 surface is already
  stuffed.

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

- [ ] `internal/retrieve/` — wired in v0.3? If yes, keep; if
  no, removal ADR.
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

### CI hardening

- [ ] **Race detector** in CI on Linux + macOS
  (`go test -race`). Windows opt-in via `CGO_ENABLED=1`.
  Currently developer-side only; flaky concurrency bugs
  would land before being caught.
- [ ] **Coverage gate** in CI matching the AGENTS.md /
  CLAUDE.md targets — `internal/core` ≥ 90 %,
  `internal/transport` ≥ 85 %, `internal/daemon` ≥ 80 %,
  `internal/cli` ≥ 75 %. Either enforce or write an ADR
  documenting why we chose to keep them aspirational.

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

---

*Owner: Ersin Koç (`ersinkoc@gmail.com`).
Open a GitHub issue with the `roadmap` label to discuss
priorities, or send a PR that updates a check-box plus the
relevant `CHANGELOG.md` entry.*
