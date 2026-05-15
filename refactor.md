# DFMT — Refactor & Improvement Report

> Snapshot date: **2026-05-15** · Commit: `cefd0de` · Release: **v0.6.7**
> Scope: full system audit (96 source files / 32,108 non-test LOC + 120 test files).
> Methodology: build + full test suite + static-analysis (`go vet`, `golangci-lint`),
> per-file size & per-function length histograms, coverage profile review,
> duplication & idiom inspection, repo hygiene scan.

This document is **descriptive, not prescriptive** — it lists candidate
improvements with rough effort and risk estimates. Each item is independent;
the team can pick any subset. None of the issues below break correctness — the
project is healthy and shipping. The goal is to keep it that way as the codebase
grows past 30 k LOC.

---

## 0. Health snapshot

| Signal                                                     | Value                       | Verdict |
|------------------------------------------------------------|-----------------------------|---------|
| `go test ./...`                                            | **all pass** (18 pkgs)      | ✅ |
| `go vet ./...`                                             | clean                       | ✅ |
| `golangci-lint run`                                        | 16 issues                   | ⚠ minor |
| Binary size (`cmd/dfmt`, Windows)                          | 13.1 MiB                    | ✅ |
| Cold `go build` time                                       | ~1.0 s                      | ✅ |
| Third-party deps                                           | 2 (`x/sys`, `yaml.v3`)      | ✅ ADR-0004 |
| `panic(...)` in non-test code                              | 0                           | ✅ |
| ADR count                                                  | 22 (`docs/adr/`)            | ✅ |
| TODO/FIXME/XXX/HACK in non-test                            | 0 real items (7 false hits) | ✅ |

### Coverage vs CLAUDE.md thresholds

| Package                | Coverage | Threshold | Status |
|------------------------|----------|-----------|--------|
| `internal/core`        | **91.5 %** | ≥ 90 % | ✅ |
| `internal/transport`   | **82.4 %** | ≥ 85 % | ⚠ −2.6 pp |
| `internal/daemon`      | **75.2 %** | ≥ 80 % | ⚠ −4.8 pp |
| `internal/cli`         | **63.5 %** | ≥ 75 % | ⚠ **−11.5 pp** |
| `internal/sandbox`     | 82.4 % | — | — |
| `internal/setup`       | 77.1 % | — | — |
| `internal/safefs`      | 78.3 % | — | — |
| `internal/client`      | 40.8 % | — | low |
| `internal/content`     | 94.8 % | — | — |
| `internal/retrieve`    | 94.8 % | — | — |
| `internal/redact`      | 88.9 % | — | — |
| `internal/safejson`    | **100 %** | — | ✅ |

Three of four thresholds slipped since they were set. The biggest gap is
`internal/cli` (the dispatch layer), which is at **63.5 %** against a 75 %
target. The single 5 087-line file accounts for almost all of the uncovered
code — see §1.

---

## 1. Top issue: the `internal/cli/dispatch.go` god file

```
5 087 LOC · 100 functions · 34 runXxx subcommand handlers · 14 imports
```

This file is the single largest source of accidental complexity in the repo.
Every CLI subcommand (`init`, `setup`, `daemon`, `doctor`, `stats`, `recall`,
`exec`, `read`, `fetch`, `glob`, `grep`, `edit`, `write`, `mcp`, `task`,
`config`, `tail`, `dashboard`, …) is bolted into the same file alongside
daemon-spawn plumbing, global-daemon detection, and 12 different helper
functions for path equality, port-file reading, dashboard URL lookup, etc.

### Longest functions (top 10, by line count)

| Lines | Function                                | Concern |
|-------|-----------------------------------------|---------|
| **344** | `runDoctor`                             | health check renderer mixed with probe logic |
| 212 | `runMCP`                                  | stdio loop + bundle wiring + signal handling |
| 158 | `runStats`                                | tier accounting + table formatting |
| 152 | `runQuickstart`                           | init + setup + verify orchestration |
| 144 | `runDaemon`                               | flag parse + foreground/background routing |
| 142 | `runDashboard`                            | URL lookup + browser launch + JSON path |
| 135 | `runStatus`                               | global-daemon inspect + per-project summary |
| 130 | `runSetupRefresh`                         | re-write MCP configs for all detected agents |
| 122 | `buildCaptureParams`                      | CLI flag → `RememberParams` mapping |
| 111 | `runSetup`                                | argument parsing + per-agent dispatch |

### Why this hurts

- Compilation unit is large enough that **every CLI change retriggers
  compilation of 5 KLOC** — measurable on a slower box.
- Test coverage is **63.5 %**, the lowest of the threshold-tracked packages.
  Subcommands without dedicated test files (`runRead`, `runFetch`,
  `runGlob`, `runGrep`, `runEdit`, `runWrite`, `runShellInit`,
  `runInstallHooks`, …) are exercised only indirectly.
- The function-length distribution makes **diffs noisy** for unrelated
  changes — touching one subcommand's option set re-flows the whole file.
- `go to definition` and `grep -n "^func "` produce 100-line scrollbacks.

### Suggested decomposition

Split by responsibility, *not* alphabetically. Concrete proposal:

```
internal/cli/
  dispatch.go              # ~150 LOC — keeps Dispatch(), printUsage(),
                           # stripGlobalFlags(), helpRequested(), setters
  daemon.go                # runDaemon, runStop, runList, runStatus,
                           # ensureGlobalDaemon, acquireBackend,
                           # startGlobalDaemonBackground, etc.
  project.go               # runInit, runRemove, writeProjectInstructionFiles,
                           # runQuickstart, registerProjectWithGlobalDaemon
  recall.go                # runSearch, runRecall, runRemember, runStats,
                           # runTail, buildCaptureParams
  tools.go                 # runExec, runRead, runFetch, runGlob, runGrep,
                           # runEdit, runWrite (+ shared option parsing)
  setup.go                 # runSetup{,Refresh,Uninstall,Verify}, runShellInit,
                           # runInstallHooks, runHook, runCapture
  doctor.go                # runDoctor + its 4 helpers (≥ 60 % of doctor's
                           # 344 lines is presentation — split renderer)
  mcp.go                   # runMCP (stdio loop) + its private helpers
  dashboard.go             # runDashboard + lookupDashboardURL + opener
  config.go                # runConfig + setConfigField
```

Each split file stays under 700 LOC; cross-file dependencies inside the
package are unchanged (everything is still `package cli`). **No public API
change.** A separate PR per file keeps reviewability sane.

**Effort:** 1–2 dev-days · **Risk:** low (mechanical move, full test suite
covers behavior).

---

## 2. Other large files worth splitting

### `internal/sandbox/permissions.go` — 2 542 LOC

Today this file holds five distinct concerns:

1. `Rule` / `Policy` types + compile + match + merge + load (lines 1–~370).
2. Glob → regex compilation, regex LRU cache, `globMatch*` (lines ~370–580).
3. `SandboxImpl` constructor + working-dir + policy-check (lines ~580–910).
4. Tool primitives — **`Exec`, `Read`, `Fetch`, `Glob`, `Grep`, `Edit`,
   `Write`** — each 80–170 lines (lines ~910–2 500).
5. Shell parsing helpers (`splitByShellOperators` 192 lines,
   `hasShellChainOperators`, `isRedirectionOperand`).

The name says "permissions" but the file is really the entire sandbox runtime.
Suggested split:

```
internal/sandbox/policy.go        # types, compile, match, merge, load
internal/sandbox/glob.go          # globMatch + globToRegex* + regex cache
internal/sandbox/shell.go         # splitByShellOperators, isRedirectionOperand,
                                  # isEnvAssignment, hasShellChainOperators,
                                  # extractBaseCommand
internal/sandbox/exec.go          # SandboxImpl.Exec + execImpl + buildEnv
internal/sandbox/read.go          # SandboxImpl.Read
internal/sandbox/fetch.go         # SandboxImpl.Fetch + assertFetchURLAllowed +
                                  # isBlockedIP + isIPv6InPrefix
internal/sandbox/glob_grep.go     # SandboxImpl.Glob + SandboxImpl.Grep
internal/sandbox/edit_write.go    # SandboxImpl.Edit + SandboxImpl.Write
permissions.go                    # keep only SandboxImpl{} struct + ctor
```

The names already in use (`DefaultPolicy`, `LoadPolicyMerged`, …) stay public
and identical. **Effort:** 1 day · **Risk:** low.

### `internal/transport/handlers.go` — 2 208 LOC

The 7 tool primitives (`Exec`, `Read`, `Fetch`, `Glob`, `Grep`, `Edit`,
`Write`) each have an 80–140-line handler; `Recall` is **244 lines**;
`Stats` is 141; `Remember` is 129. Suggested split mirrors `internal/sandbox`:

```
handlers.go              # NewHandlers + Handlers struct + Set* +
                         # acquireLimiter + stashContent + dedup helpers
handlers_recall.go       # Recall + cloneStatsResponse + cloneIntMap
handlers_stats.go        # Stats
handlers_search.go       # Search + Remember
handlers_exec.go         # Exec
handlers_read_fetch.go   # Read + Fetch
handlers_grep_glob.go    # Glob + Grep
handlers_edit_write.go   # Edit + Write
```

`Handlers.Recall` at 244 lines is also a good extract-method candidate
on its own — the markdown-snapshot tier-streaming loop deserves a
`renderSnapshot()` helper.

### `internal/transport/http.go` — 1 572 LOC

`HTTPServer.handleAPIProxy` is **254 lines** — by far the largest. The
dashboard JSON API (`handleAPIStats`, `handleAPIDaemons`, `handleAPIStream`,
`handleAPIAllDaemons`, `handleAPIProxy`) deserves its own
`http_api.go` (split from `http.go`'s "Start / Stop / handle / wrapSecurity"
plumbing). Same package, same struct.

### `internal/daemon/daemon.go` — 1 044 LOC

`Daemon.New` is **261 lines** with a Christmas-tree of nested conditionals
for index reload, journal verification, fsWatcher startup, listener wiring,
and resource-cache init. Extract:

- `newIndexAndJournal(projectPath, cfg)` → `(*Index, Journal, error)`
- `startFSWatcher(d, projectPath, cfg)` → no return; mutates `d`
- `newGlobalListener(cfg)` → `(net.Listener, string, error)`

These are pure mechanical splits that already exist as anonymous code
blocks inside `New` — promoting each to a private function shrinks the
constructor to ≤ 80 lines and lets each block be unit-tested in isolation.

### `internal/transport/mcp.go` — 963 LOC

`MCPProtocol.handleToolsList` is **302 lines** — most of it is literal tool
schema JSON. The schemas should live in a sibling file
(`mcp_schemas.go`) as `var toolSchemaXxx = json.RawMessage(`{…}`)` blocks so
diffs to one tool's schema do not require scrolling past six others. The
function itself becomes ~30 lines.

`handleToolsCall` is **229 lines** — a `switch req.Params.Name` over 12 tool
names with per-tool argument unmarshalling and result wrapping. A `toolMap
map[string]func(ctx, params) (any, error)` keyed by name shrinks this to
~40 lines + one entry per tool registered at init.

---

## 3. Lint debt (small, but blocking the green-CI dream)

```
$ golangci-lint run ./...
16 issues:
  * gofmt: 13     ← 80 % of the noise
  * goconst: 1
  * misspell: 1
  * staticcheck: 1
```

### gofmt drift (13 files)

A handful of files have `gofmt` discrepancies that `make fmt` would erase
instantly. Likely from manual edits or CRLF-toggle interactions. Worth
running:

```
gofmt -l . | xargs gofmt -w
git diff --stat
```

…and committing the result in a single "chore: gofmt" cleanup commit. Files
affected include `cmd/dfmt-bench/tokensaving.go`, `internal/daemon/daemon.go`,
`internal/sandbox/runtime.go`, `internal/setup/claude.go`, etc.

### Concrete one-off fixes

| Issue | File | Suggested fix |
|---|---|---|
| `goconst` | `internal/project/global.go:71` | hoist `"windows"` to `const goosWindows = "windows"` (already used as `goosWindows` in `internal/sandbox/runtime.go` — share it) |
| `misspell` | `internal/core/journal_test.go:1861` | `cancelled` → `canceled` |
| `staticcheck ST1005` | `internal/cli/dispatch.go:1325` | error string ends with punctuation/newline — strip the trailing `.` |
| `//nolint` (3 occurrences) | mixed | review each — two look load-bearing, one (in `journal.go`) could be replaced with a more specific `nolint:directive` |

### Make `golangci-lint` the gate

Right now CI is documented as release-only (commit `3cab1f9`: "disable CI
workflow"). A small `pre-commit` or revived workflow that runs:

```
go vet ./...
golangci-lint run ./...
go test -race -short ./...
```

…on PRs would catch the lint drift before it lands. Worth the 90-second
wall-clock cost on PRs.

**Effort:** 1 h for the fixes + 30 min for the CI re-enable · **Risk:** zero.

---

## 4. Code duplication (low-volume, easy wins)

### 4.1 `pathHint` vs `canonicalizePath` — identical functions

`internal/sandbox/permissions.go:31–41` and
`internal/sandbox/runtime.go:18–30` are byte-equivalent implementations of
"convert backslashes to forward slashes and lowercase the Windows drive
letter." Pick one (`canonicalizePath` is more descriptive) and delete the
other; same-package, no API churn.

### 4.2 `runtime.GOOS == "windows"` everywhere

67 occurrences across `cmd/` and `internal/`. The `goosWindows` constant
*does* exist in `internal/sandbox/runtime.go:15` — but it's never imported
elsewhere because it's lowercased. Two paths:

- **Export it** as `sandbox.GOOSWindows` and import everywhere (modest
  refactor, ~30 sites).
- Or **introduce `internal/osutil`** with `osutil.IsWindows()` (1-line
  helper). Reduces both string literal repetition and lets future
  platform expansion happen in one file.

This is purely cosmetic — neither option changes behaviour. **Effort:** 1 h.

### 4.3 `interface{}` vs `any` — finish the migration

```
$ grep -rn "interface{}" cmd internal | grep -v _test.go | wc -l
23
```

Go 1.18+ lets us write `any` everywhere `interface{}` appears in non-test
code. The codebase is already overwhelmingly on `any` (271 uses); the
remaining 23 are stragglers — a 5-minute sed pass:

```
gofmt -r 'interface{} -> any' -w $(grep -rl 'interface{}' cmd internal)
```

Apply, run `go test ./...`, commit. **Risk:** zero. (`gofmt -r` only
rewrites where semantically equivalent — embedded `interface{ ... methods }`
declarations are untouched.)

---

## 5. Repository hygiene

### 5.1 Stray coverage artefacts at repo root

```
cov.out  cov_proj.out  cov_sf.out  cov_trans.out  coverage.out  coverage_cli.out
```

Six coverage profile files at the project root. Either commit them (no —
they bloat the repo) or add to `.gitignore` and `make clean`. Likely
already-untracked but present in working tree.

```
# add to .gitignore:
/*.out
coverage*.out
cov*.out
```

…then `git rm --cached cov*.out coverage*.out` if any are tracked.

### 5.2 `dist/` artefacts

`dist/dfmt-test.exe` looks like a development scratch binary alongside
`dist/dfmt.exe`. Either `make clean` should remove both, or the test
binary should live in `dist/test/` (or `/tmp/`). 13 MiB × 2 in the repo
working tree is wasteful.

### 5.3 `docs/ROADMAP.md` is stale

The roadmap headline points at **v0.2.3 → v0.3.0 → v1.0.0** with shipped
dates in *April–May 2026* — but the actual project is at **v0.6.7**. The
v0.3 punch list ("wire `.dfmt/permissions.yaml`", "real `dfmt tail`", "config
get/set") is all done. Either:

- Roll the file forward — v0.7 / v0.8 / v1.0 with current open items, or
- Delete it and point readers to `CHANGELOG.md` + GitHub issues.

A drifted roadmap is worse than no roadmap because it sets wrong
expectations for new contributors.

### 5.4 `docs/ARCHITECTURE.md` is 3 394 lines

For a 32 KLOC codebase, a 3.4 KLOC architecture doc is heavy. Suggested:
split by subsystem (`docs/arch/`*`.md`) and keep `ARCHITECTURE.md` as a
2-page index. Already mirrored by the ADR layout — same pattern.

---

## 6. API & idiom polish

### 6.1 Error wrapping ratio

```
fmt.Errorf with %w :  234 occurrences
errors.Is / As     :   27 occurrences
errors.New         :   52 occurrences
```

234 wrap sites with only 27 unwrap-and-inspect sites is a 9:1 ratio. Some of
that is appropriate (error chains for diagnostics where callers never
introspect), but it does suggest one of:

- The wrapped errors are *informational only* and could be `fmt.Errorf("%s: %v")`
  with `%v` to avoid pretending they're inspectable.
- Or there are call sites that *should* `errors.Is` and don't (e.g., handling
  `os.ErrNotExist` vs other path errors).

Worth a one-time grep through `errors.Is` callsites and spot-checking that
the matching wrap sites use `%w`. Not a bug — a consistency check.

### 6.2 Sentinel errors

`grep "^var Err" internal/` is sparse — most errors are constructed via
`fmt.Errorf` at the throw site. For errors that callers *do* check, prefer:

```go
var ErrPolicyDenied = errors.New("policy denied")
// ...
return fmt.Errorf("exec %q: %w", cmd, ErrPolicyDenied)
```

This lets `errors.Is(err, sandbox.ErrPolicyDenied)` work cleanly. Audit the
sandbox package first — its "deny" errors are the most likely consumer
inspection targets.

### 6.3 Context discipline

42 `context.Background()` call sites in non-test code, almost all in
`internal/cli/dispatch.go`. They are wrapped in `context.WithTimeout` (~5s,
~10s) at the call site, which is correct, but consider centralising the
timeouts as named constants:

```go
const (
    dispatchShortTimeout = 5 * time.Second
    dispatchLongTimeout  = 10 * time.Second
    dispatchShutdownTimeout = 30 * time.Second
)
```

Right now `5*time.Second` is repeated 5×, `2*time.Second` once, `10*time.Second`
once, and `3*time.Second` once — small but easy to drift.

### 6.4 Long parameter lists

`Sandbox.Fetch` (142 lines) and `Sandbox.Exec` (94 lines) take `*Req` structs
with 7–10 fields each. Mostly fine, but a few helpers
(`assertFetchURLAllowed(rawURL)` then `isBlockedIP(ip)` then `isIPv6InPrefix`)
could be a `urlPolicy{}` struct with methods. Marginal.

---

## 7. Concurrency review

19 files use `sync.Mutex` / `sync.RWMutex`; 15 explicit `go func()` spawns in
non-test code. No `panic()` in production paths (good).

Items worth a second look:

- **`internal/daemon/daemon.go` `Daemon.New` (261 lines)** — fsWatcher
  goroutine, idleMonitor goroutine, journal-tail goroutine all started in the
  constructor. Hard to reason about lifecycle. Consider returning a
  fully-constructed `*Daemon` and starting the goroutines from a separate
  `Start()`. ADR-0021 (single-binary) is silent on this.

- **`internal/transport/handlers.go`** — `acquireLimiter` + the bounded
  semaphore (`sem chan struct{}`) is correct but unobvious. A short comment
  on the channel's capacity-as-rate-limit semantics would help reviewers.

- **`internal/content/store.go`** — `Store.PutChunk` and `Store.evict` lock
  separately; eviction is best-effort under `s.mu.RLock()`. Worth checking
  for races during high churn (`go test -race ./internal/content -count=10`).

- **Goroutine leak hunt** — `runMCP` 212 lines includes stdio loops that
  may not propagate cancellation cleanly on shutdown. A `go test -race` with
  artificial shutdown would surface any leaks; the project's
  `daemon_lock_integration_test.go` is the right place for that probe.

---

## 8. CLI UX & flags

The `Dispatch` function uses **manual flag parsing** (substring matching for
`--help`, `--json`, `--project`) instead of `flag.NewFlagSet` for the global
switches. This is intentional (CLAUDE.md / ADR-0004 forbid CLI frameworks),
and the recent fix `cefd0de` shows the cost — `--help` was mutating state
across subcommands until that commit landed. The defensive coverage in
`helpRequested(args)` and `stripGlobalFlags` is good, but the pattern is
**fragile** as new flags land.

Mitigation worth considering:

- **A single `parseGlobals(args []string) (globals, []string)`** that walks
  args once, separates `--json`, `--project`, `--help`, and returns a clean
  rest-list. Tested in isolation.
- Each `runX` then receives the cleaned `args` and is free to use
  `flag.NewFlagSet(name, flag.ContinueOnError)` internally — stdlib `flag`
  is allowed (it's stdlib).

This already partially exists (`stripGlobalFlags`, `helpRequested`,
`SetGlobalJSON`, `SetGlobalProject`) but the contracts are spread over four
functions. Consolidating reduces future bugs of the
`cefd0de:fix(cli): --help no longer mutates state` shape.

---

## 9. Specific function-level refactors

Concrete extract-method opportunities, ordered by impact-to-effort ratio:

| Function | Current | Suggested decomposition | Win |
|---|---|---|---|
| `cli.runDoctor` | 344 LOC | `probeDaemon()`, `probeAgents()`, `probePermissions()`, `renderReport()` | 4 testable helpers, ≤ 80 LOC each |
| `transport.MCPProtocol.handleToolsList` | 302 LOC | move schemas to `mcp_schemas.go` var blocks | function drops to ~30 LOC |
| `daemon.New` | 261 LOC | extract `loadIndexJournal`, `startFSWatcher`, `bindListener` | constructor → ~80 LOC, each helper testable |
| `transport.HTTPServer.handleAPIProxy` | 254 LOC | split request validation, target selection, response stream | enables proxy-policy unit tests |
| `transport.Handlers.Recall` | 244 LOC | extract `renderTier()`, `evictForBudget()`, `internPaths()` | unit-testable rendering |
| `transport.MCPProtocol.handleToolsCall` | 229 LOC | replace switch with `map[string]toolDispatch{}` table | adding a tool = adding a map entry |
| `sandbox.splitByShellOperators` | 192 LOC | extract `tokenizeShell()` + `splitOnOperators()` | already complex, deserves a 2-pass design |
| `sandbox.Sandbox.Grep` | 171 LOC | extract `walkRoot()`, `applyGlobFilter()`, `formatHits()` | test grep filtering in isolation |

---

## 10. Documentation & onboarding

`CLAUDE.md`, `AGENTS.md`, and `docs/ARCHITECTURE.md` are **well-aligned**
and detailed. Three small wins:

1. **`AGENTS.md` is 49 lines** but `CLAUDE.md` is far longer — the CLAUDE
   doc says "trust AGENTS.md if they diverge", but AGENTS.md is sparse
   enough that you have to read CLAUDE.md anyway. Either expand AGENTS.md
   to be the real canonical source, or invert the "trust" arrow.

2. **`README.md` is 247 lines** — fine. Consider adding a 5-line
   "performance promise" with the token-savings numbers from
   `dfmt-bench tokensaving` (the binary exists; the numbers are not in
   the README).

3. **`docs/adr/ADR-INDEX.md` exists** — verify it includes all 22 ADRs
   (0000–0021). Last spot-check on ADR-INDEX.md was around v0.2.0.

---

## 11. Suggested execution order

If picked up in sequence, this is a roughly 4–6 dev-day refactor that
leaves the code substantially easier to navigate without changing behaviour.

| Wave | Item | Effort | Risk | Value |
|------|------|--------|------|-------|
| 1 | gofmt + 3 lint fixes + nolint audit (§3) | 1 h | 0 | ✅ green lint |
| 1 | `interface{} → any` sweep (§4.3) | 30 min | 0 | consistency |
| 1 | Delete duplicate `pathHint` / `canonicalizePath` (§4.1) | 15 min | 0 | -10 LOC |
| 1 | `.gitignore` + `dist/` hygiene (§5.1, §5.2) | 15 min | 0 | clean tree |
| 1 | `docs/ROADMAP.md` refresh or remove (§5.3) | 30 min | 0 | accurate signal |
| 2 | Split `dispatch.go` into 10 files (§1) | 1 day | low | reviewability, build-time |
| 2 | Split `permissions.go` into 8 files (§2) | 1 day | low | navigability |
| 2 | Split `handlers.go` into 7 files (§2) | 1 day | low | navigability |
| 3 | Extract `runDoctor` helpers (§9) | 0.5 day | low | testability |
| 3 | `handleToolsList` schemas → `mcp_schemas.go` (§9) | 0.5 day | low | maintenance |
| 3 | `handleToolsCall` switch → dispatch map (§9) | 0.5 day | medium | extensibility |
| 4 | Push `internal/cli` coverage to ≥ 75 % (§0) | 1 day | low | meets threshold |
| 4 | Re-enable PR-time CI (lint + vet + race tests) (§3) | 1 h | 0 | regression guard |

Waves are independent — Wave 1 alone is a worthwhile afternoon. Waves 2–3
should each land as **N small PRs**, not one mega-PR, so reviewers can
verify per-file the move was mechanical.

---

## 12. What is explicitly **not** suggested

To prevent scope creep, this report deliberately does **not** propose:

- Adding any third-party Go dependency (ADR-0004 forbids it; the existing
  stdlib-first stance is correct).
- Changing the on-disk journal format, the index serialisation, or any of
  the MCP wire-protocol fields. These are stable surfaces with version
  guarantees in `CHANGELOG.md`.
- Introducing a CLI framework (`cobra`, `urfave/cli`, …). The manual
  `Dispatch` is a feature, not a bug.
- Removing the global daemon or returning to per-project daemons
  (ADR-0019 / ADR-0021 settled this).
- Adding telemetry, tracing libraries, or external metric exporters
  beyond the existing Prometheus-shaped `/metrics` endpoint
  (ADR-0016).

---

## Appendix A — Per-file LOC histogram (top 25 non-test)

```
 5087  internal/cli/dispatch.go
 2542  internal/sandbox/permissions.go
 2208  internal/transport/handlers.go
 1572  internal/transport/http.go
 1044  internal/daemon/daemon.go
  963  internal/transport/mcp.go
  866  internal/client/client.go
  796  internal/daemon/projectres.go
  702  internal/sandbox/intent.go
  668  internal/core/index.go
  660  internal/sandbox/htmlmd.go
  648  internal/setup/legacy.go
  625  internal/core/journal.go
  513  internal/transport/metrics.go
  507  cmd/dfmt-bench/tokensaving.go
  505  internal/sandbox/structured.go
  500  internal/content/store.go
  498  internal/setup/setup.go
  493  internal/redact/redact.go
  478  internal/sandbox/htmltok.go
  469  internal/setup/projectdocs.go
  459  internal/transport/dashboard.go
  434  internal/config/config.go
  425  internal/setup/claude.go
```

## Appendix B — Reproducing the audit

```
# Static analysis
go vet ./...
golangci-lint run ./...

# Size histograms
find . -name "*.go" -not -name "*_test.go" -not -path "./.git/*" \
  | xargs wc -l | sort -rn | head -30

# Function-length histogram for any file
awk 'BEGIN{last=0;name=""} /^func /{
  if(name!="")print NR-last,name; name=$0;last=NR
} END{print NR-last,name}' <file> | sort -rn | head

# Coverage
go test -cover ./...
```

---

*Audit complete. The project is in good shape — these are growth-pain items, not
correctness defects. Pick the wave that fits your bandwidth.*
