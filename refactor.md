# DFMT — System Audit & Refactoring Report

> **Snapshot:** 2026-07-31 · branch `fix/exec-timeout-and-tool-correctness` · `855c0b1` · `git describe` = `v0.7.3-7-g855c0b1`
> **Supersedes:** the 2026-05-15 edition of this file (v0.6.7 / `cefd0de`), preserved in git history.
> **Corpus:** 291 Go files — ~35,951 non-test LOC + ~57,969 test LOC across 19 packages, 27 ADRs.
> **Method:** six parallel package-scoped read-throughs of every non-test source file, cross-checked against
> call sites; `go vet ./...`; `golangci-lint run ./...`; per-package LOC and git-churn histograms; targeted
> re-verification of every finding rated P0/P1 by direct grep or file read.

## How to read this document

Findings carry a stable ID (`CORE-3`, `TRN-7`, `SBX-1`, …), a **severity** and an **effort**.

| Severity | Meaning |
|---|---|
| **P0** | Silent wrong behaviour, data loss, or a security control that does not hold. Fix before the next release. |
| **P1** | User-visible defect, a documented guarantee that is not met, or a real resource leak. |
| **P2** | Structural debt with a concrete cost — duplication that has already produced a bug, performance cliffs, oversized units. |
| **P3** | Hygiene: dead code, naming, doc comments, magic numbers. |

Effort: **S** = under an hour · **M** = a few hours · **L** = a day or more.

Every finding names the file and line, states the failure in one sentence, then the fix. Where a finding was
verified beyond a code read (grep for call sites, running the linter, comparing docs to constants), it is
marked **[verified]**.

---

# Executive summary

DFMT is a healthy, unusually well-documented codebase with a real architectural spine: two dependencies, a
clean package DAG, 27 ADRs with honest supersession tracking, and comments that record the measured failure
behind nearly every non-obvious branch. Four separate de-duplication passes have already landed successfully.
All four documented coverage thresholds pass. Nothing here suggests a rewrite.

It also has a specific, recognisable failure pattern, and almost every serious finding in this report is an
instance of it:

> **A capability was built, tested in isolation, and never connected to the path that needs it — and because
> the disconnected version returns a plausible-looking result, nothing failed loudly enough to notice.**

The clearest cases:

* The **output-normalization pipeline** — roughly 1,900 lines across four files, two ADRs, its own fuzz
  targets — is only ever called from `exec`. The HTML, JSON, YAML and binary compactors it consists of are
  reachable only from shell output, while the bodies they were written for arrive through `fetch` and `read`.
  The published token-savings benchmark measures a path production does not take.
* **Journal rotation** is fully implemented and has **no production caller**, so a project that reaches the
  size cap silently stops recording forever.
* The **content store** is written on every tool call and never read, never pruned, and never has its chunk
  bodies persisted at all — while the `content_id` handed to agents cannot be dereferenced by any RPC.
* The **markdown recall renderer** with path interning and ref-token-forgery escaping is dead; the default
  format uses a second, inline implementation that has neither.
* **CI is disabled.** The workflow file is well-built — 3 OS × 2 Go versions, SHA-pinned actions, race
  detector, coverage gate — and it does not run. Every other finding in this report is something CI would have
  surfaced continuously.
* **184 of 299 tests in `internal/cli` cannot fail** — they are `if x != want { t.Logf(...) }`, an assertion
  with the failure removed. The package's 72.3 % coverage measures lines executed, not lines verified.

The second pattern is **knowledge duplicated across files that cannot see each other**: three `**` glob
implementations with three different semantics (two of them fuzz-tested for self-consistency, neither for
agreement); four ignore-lists; 31 hand-joined `.dfmt` paths in a repo whose own guidance forbids hand-joining
them; two `Source` constant sets with conflicting values; two logging systems in one package, the dead one
defaulting to **stdout** in a process that speaks JSON-RPC over stdout.

## The ten findings to fix first

| # | ID | Finding | Where |
|---|---|---|---|
| 1 | BLD-1 | CI is disabled; releases publish binaries that were never tested | `.github/workflows/` | ✅ FIXED `a728dbc` |
| 2 | SBX-1 | Deny rules are bypassable on chained commands — `true && sudo whoami` is allowed under the documented operator configuration | `sandbox/exec.go:79` | ✅ FIXED `a353f8c` |
| 3 | TRN-1 | The stats cache is not keyed by project, so one project's numbers are served for another | `transport/handlers_stats.go:95` | ✅ FIXED `0782051` |
| 4 | CAP-5 / XC-18 | `.dfmt/**` is missing from the Go-side default ignore list, re-opening the journal self-amplification loop | `config/config.go:172` | ✅ FIXED `9e23477` |
| 5 | XC-28 | ADR-0014's hard-deny security invariant was deleted in code and is still asserted in the ADR, the index, and CLAUDE.md | `sandbox/policy.go:238` |
| 6 | XC-13 | Dead slog subsystem defaults to **stdout** inside the MCP process | `logging/logging.go:51` | ✅ FIXED `53bb962` |
| 7 | CORE-1 | Journal rotation has no caller — a full journal stops recording forever | `core/journal.go:483` |
| 8 | TRN-4 | `/api/*` bypasses bearer auth and streams any project's journal by query parameter | `transport/http.go:387` |
| 9 | LIF-1/2/3 | The daemon singleton protocol: the lock file is deleted while held, the listener binds before the lock, and nothing interlocks classify→spawn | `cli/daemon.go`, `daemon/daemon.go` |
| 10 | SBX-4 | The normalization pipeline never runs for `fetch` or `read` | `sandbox/fetch.go:305`, `read.go:132` |

Findings 1–6 are each under an hour of work. Finding 1 makes the rest verifiable and should land first.

## Scoreboard

| Signal | Value | Verdict |
|---|---|---|
| `go vet ./...` | clean | ✅ |
| `gofmt -l` | clean | ✅ |
| `golangci-lint run ./...` | **67 issues** (45 staticcheck, 12 lll, 9 misspell, 1 goconst) | ❌ `make lint` is red |
| CI | **disabled**; release publishes without tests | ❌ |
| Coverage vs CLAUDE.md | core 90.1 / transport 87.9 / daemon 76.4 / cli 72.3 | ✅ all four pass |
| Coverage vs `scripts/coverage-gate.go` | same numbers vs 90 / 85 / **80** / **75** | ❌ daemon −3.6, cli −2.7 |
| Tests that cannot fail | 189 `t.Logf`-instead-of-assert sites; 184/299 functions in `cli_test.go` | ❌ |
| Skips | 306 total; **47 convert setup failures into passes** | ❌ |
| `t.Parallel()` | 0 uses in 160 files | ⚠️ |
| `panic(` in library code | 0 | ✅ |
| Error wrapping | 241 `%w` vs 80 bare `fmt.Errorf` | ✅ |
| Error-string matching | 0 | ✅ |
| TODO/FIXME/HACK | 0 real (2 false positives) | ✅ |
| Third-party dependencies | 2 (`x/sys`, `yaml.v3`), no indirect | ✅ ADR-0004 holds |
| ADRs | 27; index 3 stale; 1 contradicted by code | ⚠️ |
| Largest files | `transport/http.go` 1,598 · `cli/daemon.go` 1,294 · `transport/handlers.go` 1,096 · `daemon/daemon.go` 1,085 | ⚠️ |
| Highest churn | `cli/dispatch.go` 104 commits · `transport/http.go` 60 · `sandbox/permissions.go` 55 | — |

Churn and size intersect at `transport/http.go` and `cli/daemon.go` — both are large, both are edited
constantly, and both host P0 findings. They are the two highest-value split candidates.

---

# Part I — `internal/core`, `internal/retrieve`, `internal/content`

The persistence and retrieval heart of the system: append-only journal, BM25 + trigram index with tiered
retention, snapshot rendering, and the ephemeral content store.

## I.1 — P0 findings

### CORE-1 · Journal rotation is dead code; a full journal stops recording forever **[verified]**
`internal/core/journal.go:483-553` (impl), `:109` (interface). **P0 · M**

A repo-wide grep for `Rotate(` outside tests returns only the declaration, the implementation, and one
comment — there is **no production caller**. Meanwhile `Append` returns `ErrJournalFull` permanently once the
file reaches `storage.journal_max_bytes` (`journal.go:240-244`), and the daemon's consumer merely logs and
continues (`internal/daemon/daemon.go:847`).

So a project that reaches the cap silently stops journaling: recall degrades to whatever was captured before
the ceiling, `dfmt_remember` writes vanish, and nothing in `dfmt doctor` reports it. All the rotation
machinery — tombstones, the `journalSegments` glob, multi-segment `Stream` — is unreachable weight that still
costs a directory glob on every `Stream` call.

**Fix:** rotate-then-write inside `Append` when `size >= maxBytes`, or drive `Rotate` from the daemon's
maintenance tick. Add an integration test that appends past the cap and asserts both a rotated segment and a
successful subsequent write. Surface `ErrJournalFull` as a doctor row for the case where rotation itself fails.

### CORE-2 · The content store is write-only: unbounded disk growth, unresolvable `content_id` **[verified]**
`internal/content/store.go` — `GetChunk:236`, `GetChunkSet:259`, `GetChunks:298`, `PruneExpired:281`,
`LoadChunkSet:433`, `Close:468`. **P0 · M**

None of these have a non-test caller. Writes arrive from `transport/handlers.go:681 stashContent` on every
Exec/Read/Fetch. The consequences compound:

* Every tool call gzip-writes a file into `.dfmt/content/` that nothing ever reads back and nothing ever
  deletes — `PruneExpired` is uncalled, `dropSetLocked`/`evict` never unlink files, there is no startup sweep.
* The `content_id` returned to the agent cannot be dereferenced by any RPC, so the wire-dedup reply
  `(unchanged; same content_id)` points at bytes the agent has no way to retrieve.
* `Store.Close()` — the only code path that would write a set's chunk list to disk — is never called
  (see CORE-3).

**Fix:** decide the product intent explicitly. Either (a) add a `dfmt_content(id)` handler plus a startup
loader and a bounded sweep, making the persisted store real; or (b) drop persistence, run the store purely
in-memory with a default TTL, and stop advertising `content_id` as a retrieval handle.

### CORE-3 · Persisted chunk sets never contain their chunks
`internal/content/store.go:180-205`, `:414-430`. **P0 · S**

`PutChunkSet` snapshots and persists the set **before** any chunk exists; `stashContent` then calls
`PutChunk`, which appends to the in-memory `set.Chunks` and never re-persists. Only `Close()` rewrites the
full set, and `Close()` has no production caller. Every `.json.gz` on disk is therefore a set with an empty
chunk list, and chunk *bodies* are never persisted at all.

The same write path is non-atomic: `O_TRUNC` directly onto the live path (a crash mid-write leaves a truncated
archive), no `fsync`, and `defer gz.Close()` discards the gzip flush error while `return enc.Encode(set)`
reports success.

**Fix:** persist on chunk completion, mirror `index_persist.go:108-140` (temp + checked `gz.Close()` + `Sync` +
rename), and call `Store.Close()` from daemon shutdown regardless.

### CORE-4 · `PersistIndex` reads index fields without the lock — a genuine data race ✅ FIXED `a5a2af5`
`internal/core/index_persist.go:89-90`. **P0 · S**

`TotalDocs: index.totalDocs, AvgDocLen: index.avgDocLen` are plain field reads. Callers
(`daemon.go:729,806`, `projectres.go:285,747,796`) run concurrently with `index.Add` from the fs-watch
consumer (`daemon.go:851`), the async rebuild (`daemon.go:795`), and Remember handlers. The race detector will
flag it; functionally it can persist a cursor whose `TotalDocs` disagrees with the marshalled index.

**Fix:** use the existing `index.TotalDocs()` accessor and add `AvgDocLen()`, or a single
`Stats() (int, float64)` under one `RLock`.

### CORE-5 · Restored documents lose their tier — restart inverts the retention policy
`internal/core/index.go:289-294`. **P0 · S**

Every deserialized document is pushed into `evictHeaps[3]` (the P4 heap) because `MarshalJSON`
(`index.go:221-227`) does not persist `meta`. After a restart the index evicts the *highest-priority*
historical decisions first — precisely the failure the tiered heap was built to prevent, and the one
ADR-0024 claims to have fixed.

It compounds with **CORE-6**: `docStems`/`docTrigrams` are not persisted either, so every document loaded from
disk lacks its reverse map, and `removeFromPostingList`'s fast path never applies.

**Fix:** persist `meta` (it already carries `Priority`) and rehydrate the tier heaps on load.

### CORE-6 · Eviction on a loaded index takes the full-sweep path on every insert
`internal/core/index.go:764-766` → `purgeFromAllPostings:778-789`. **P0 · M**

Because the reverse maps are not persisted (CORE-5), a restarted daemon at the document cap sweeps **every**
stem and trigram posting list on **every** `Add`. This is exactly the 739 µs → 8.7 µs regression the comment
at `index.go:704-734` documents as fixed — the fix is real in a freshly-built index and absent in a loaded one.

**Fix:** persist the reverse maps, or rebuild them once at load from the posting lists. Add
`BenchmarkAddAtCapAfterLoad` so the loaded path is measured, not just the built path.

### CORE-7 · Index persistence has no format version
`internal/core/index.go:208-214` (`indexJSON`), `index_persist.go:152`. **P0 · S**

`indexJSON` carries no schema version; `LoadIndexWithCursor` validates only `TokenizerVersion` in the
*sibling* cursor file. If the cursor is current but `indexJSON`'s shape changed between releases, the index
deserializes into a silently wrong state — absent fields become zero values, and `AvgDocLen: 0` is then
papered over by `bm25.go:50`'s IDF-only fallback, so scoring degrades without an error.

**Fix:** add `"v": N`, validate on load, treat a mismatch as `needsRebuild`.

### CORE-8 · `Priority`/`Source` constants exist twice with conflicting values
`internal/core/event.go:50-65` vs `internal/core/core.go:32-47`. **P0 · S**

`SrcFSWatch = "fswatch"` vs `SourceFS = "fs"`; `SrcGitHook = "githook"` vs `SourceHook = "hook"` /
`SourceGit = "git"`. A producer using one set and a consumer filtering on the other will never match, and the
type system cannot catch it because both are `Source`.

**Fix:** keep one set (`Pri*`/`Src*`), alias the other with `// Deprecated:` for one release, then delete. Add
an exhaustiveness test over the `Source` domain.

## I.2 — P1 findings

### CORE-9 · `JSONRenderer` interns paths but emits no legend
`internal/retrieve/render_md.go:229-257`. **P1 · S**

The renderer replaces `Data["path"]` with `r3`-style tokens, but `Snapshot` (`snapshot.go:26-30`) has no
`Refs` field — so the JSON a caller receives contains reference tokens nothing can resolve. This is live:
`dfmt_recall` with `format=json` uses this renderer. XML gets a `<refs>` block; JSON does not.

**Fix:** add `Refs map[string]string` to `Snapshot` and populate it.

### CORE-10 · XML ref ids are re-derived from a loop index
`internal/retrieve/render_md.go:304-306` vs `:324`. **P1 · S**

`<ref id="r%d">` uses the position in a locally re-sorted slice while `<path ref="…"/>` uses the assigned
token. They agree only because `buildPathRefs` happens to assign in the same order — an invariant nothing
enforces.

**Fix:** emit `refs[p]` directly; add a test asserting the two agree.

### CORE-11 · Markdown renderer escapes messages and tags but not paths
`internal/retrieve/render_md.go:152,166` (escaped) vs `:158` (not). **P1 · S**

`escapeRefTokenForgery` guards message and tag text; the path is written into a backtick span unescaped. A
path containing a backtick or newline breaks the span and can inject arbitrary markdown into the snapshot an
agent reads — the same forgery class the escape exists to prevent.

**Fix:** route the path through the same sanitizer.

### CORE-12 · `ComputeSig` ignores its marshal error, producing one shared signature ✅ FIXED `a5a2af5`
`internal/core/event.go:107`. **P1 · S**

`data, _ := CanonicalJSON(e2)` — on error, `data` is nil and every failing event gets `sha256("")[:8]`, which
then *validates* on read. A signature that is identical across unrelated events is worse than no signature.

**Fix:** return `(string, error)`, or fall back to a sentinel that fails `Validate`.

### CORE-13 · `LoadIndexWithCursor` swallows every error and always returns nil
`internal/core/index_persist.go:143-163`. **P1 · S**

A permission error, a corrupt index, and a missing file are indistinguishable; the daemon's `if err != nil`
at `daemon.go:971` is dead, and operators get a silent full rebuild on every start with no way to learn why.

**Fix:** return the cause alongside `needsRebuild=true` and log it.

### CORE-14 · `Event.Validate` accepts an empty signature
`internal/core/event.go:172-177`. **P1 · S**

Tampering with a journal line requires only deleting its `sig` field. The back-compat rationale is sound; the
absence of a sunset is not.

**Fix:** gate on a `strict_sig` config flag, default on for journals created after this release, and warn once
per file when unsigned lines are seen.

### CORE-15 · `StreamN` leaks the upstream goroutine and its file descriptor
`internal/core/journal.go:428-439`. **P1 · S**

When the limit is reached the drain goroutine returns without consuming `src`; the producer in `Stream`
blocks forever on `ch <- e` unless the caller's context is cancelled. With `context.Background()` that is a
permanent leak. (`StreamN` also has no production caller — see CORE-27.)

**Fix:** derive a cancellable context, `defer cancel()`, or drain `src`.

### CORE-16 · Content store: lock-upgrade TOCTOU, pointers into locked state, and size drift
`internal/content/store.go:244-253`, `:266-272`, `:255,274,313`, `:157-158`. **P1 · S**

Three distinct defects in one type: the lazy-expiry path releases the read lock before taking the write lock,
so a freshly re-inserted set with the same id is deleted; `GetChunk`/`GetChunkSet` hand out pointers into
state that `PutChunk` mutates under the lock (a slice-header race); and re-putting an existing chunk id adds
its size again without subtracting the old body, so `curSize` drifts upward permanently until the cap becomes
meaningless.

**Fix:** re-check expiry after upgrading; return value copies with a cloned `Chunks` slice; subtract the old
body on overwrite and assert `curSize == Σ len(body)` in a test.

### CORE-17 · `Journal.Close` is not idempotent and leaks the fd on a Sync error
`internal/core/journal.go:556-592`. **P1 · S**

No `if j.closed` guard at entry, so a second `Close` calls `Sync`/`Close` on a closed file; and at `:585-587`
a Sync error returns before `j.file.Close()`.

**Fix:** early-return when closed; always close the file, joining errors with `errors.Join`.

### CORE-18 · Rotation tombstone uses an undeclared type and a backdated ULID
`internal/core/journal.go:495-504`. **P1 · S**

`Type: "journal.rotate"` is not in the `EventType` block (while `EvtTombstone` sits unused), the record is
unsigned and carries no `Project`/`Source`/`Priority`, and `NewULID(time.Now().Add(-time.Millisecond))`
rewinds the generator's monotonic state. The tombstone is streamed to consumers as an ordinary event, so it
gets indexed and can surface in recall.

**Fix:** use `EvtTombstone`, populate and sign it, mint at the current time, and filter it in `streamFile`.

### CORE-19 · `eventText` indexes only string values, and never the actor
`internal/core/index.go:497-510`. **P1 · S**

Numbers, booleans and nested maps in `Data` are dropped, and `Actor` is never indexed even though
`buildExcerpt` advertises it. Searching for an actor name, an exit code, or a token count silently returns
nothing.

**Fix:** stringify scalars, recurse one level, append `Actor`; bump `TokenizerVersion` so existing indexes
rebuild.

## I.3 — P2: performance

| ID | Location | Problem | Fix |
|---|---|---|---|
| CORE-20 | `index.go:573-581`, `:628-637` | Search pushes **every** scoring doc into a heap, then pops `limit` — O(N log N) plus N allocations for a limit of ≤50 | Fixed-size min-heap of `limit` entries; pre-size the score map |
| CORE-21 | `index.go:574,629`, `:679` | Map iteration plus a tiebreak-free `Less` makes results **nondeterministic on score ties** | Tiebreak on id descending (ULIDs sort by time) |
| CORE-22 | `trigram.go:93-114` | Intersection clones and sorts both posting lists per trigram — 8 clone+sort passes for a 10-char token | Maintain a `sorted` invariant and linear-merge into a reusable buffer |
| CORE-23 | `index.go:390-403`, `trigram.go:34` | `Add` allocates a throwaway `TrigramIndex` per event and a `tg + "|" + id` string **per trigram occurrence** | Emit directly into `ix.trigramPL`; dedup on trigram only; pool the scratch map |
| CORE-24 | `journal.go:241` | `os.Stat` syscall on **every** append | Track bytes written in a field, reconcile occasionally |
| CORE-25 | `journal.go:602-651` | `scanLastID` reads the entire journal at open, per project, per daemon start | Tail-read the last N KB; full scan only as fallback |
| CORE-26 | `index.go:217-229` | `MarshalJSON` holds `RLock` across the whole encode; at 100k docs writers block for the duration and Go's RWMutex then blocks subsequent readers behind them | Shallow-copy under the lock, encode outside |
| CORE-26b | `journal.go:246-253` | `Append` fsyncs **under** the mutex; in durable mode every writer serializes behind a full fsync, and `Size()`/`Checkpoint()` block on the same mutex | Group-commit, or a dedicated writer goroutine |
| CORE-26c | `content/store.go:317-373` | `Search` holds `RLock` while re-tokenizing **every chunk body** — up to 64 MB per query | Precompute token counts at insert |

## I.4 — P3: dead code, duplication, hygiene

* **Dead in production:** `StreamN` (CORE-27), the whole of `content/summarize.go`, `core.ULIDLen`
  (and wrong — IDs are 32 hex chars, not 26), `core.MaxEventSize`, `core.EnglishStopwords`, `BM25Result`,
  `EvtTombstone`, `ErrJournalNotFound`, `TrigramIndex.Search`, `NGrams`/`Unique`/`Merge`,
  `IndexCursor.TotalDocs`/`AvgDocLen` (written, never read).
* **Dead struct fields:** `journalImpl.compress` is threaded all the way from `storage.compress_rotated` in
  config into a field nothing reads — the operator believes rotated journals are compressed. Either implement
  gzip-on-rotate or remove the config key. Same for `batchMS`, `syncInterval`, `lastSync`.
* **Two tokenizers:** `event.go:180-200 Tokenize` and `tokenize.go:20-50 TokenizeFull` are the same loop;
  content search uses one, the index the other, so they tokenize differently. Collapse to one.
* **Priority↔tier mapping duplicated four times:** `index.go:885-900`, `:903-914`,
  `transport/handlers_recall.go:107-119`, `retrieve/snapshot.go:94-99`. Add `Priority.Tier()` /
  `TierPriority(int)` in `core`.
* **`SnapshotBuilder.Build` writes the same loop four times** (`snapshot.go:41-79`) — one range over a tier
  slice replaces 40 lines with 10.
* **`Index.Add` is a 95-line god function** (`index.go:320-414`) mixing dedupe, eviction, excerpting,
  classification, tokenization, and two posting-list merges under one write lock. Split into four
  `…Locked` helpers.
* **`Index` itself carries seven responsibilities** (`index.go:25-105`): inverted index, trigram index,
  excerpt store, doc metadata, retention policy, BM25 parameters, persistence codec. Extract `retention`,
  `docstore`, and a `codec`.
* **Repo artifacts:** `internal/core/NUL.../test.journal` is **git-tracked** — the exact Windows
  reserved-name artifact that `safefs.CheckNoReservedNames` exists to prevent. A 6.4 MB `core.test.exe` sits
  untracked. `git rm -r` the former; add `*.test.exe` to `.gitignore`.
* **Doc/comment drift:** `TokenizeFull`'s comment names `Tokenize`; package docs in `retrieve/retrieve.go` and
  `content/content.go` sit *after* the package clause and are invisible to godoc; `bm25.go:37`'s stated
  formula differs from the code; `porter.go:7-8` claims the full 1980 algorithm but implements steps 1a–1c;
  `store.go:375` says "least recently used" for a FIFO; `index.gob` contains JSON.

## I.5 — Strengths

1. **Comments encode measured causes, not intentions.** `index.go:704-734` (reverse-map cost with the
   µs numbers), `:816-827` (memmove 3.0 ms → 0.22 ms), `:840-853` (why age-only eviction was wrong on a
   200:1 journal), `journal.go:524-533` (Windows AV rename failure). Preserve these verbatim through any move.
2. **Untrusted input is handled at every boundary**: `safejson.Unmarshal` at all six decode sites,
   `safefs.CheckNoReservedNames` before `MkdirAll`, 0600/0700 modes throughout, a path-traversal gate on chunk
   ids, an `io.LimitReader` zip-bomb cap, XML escaping and ref-token forgery escaping.
3. **Atomic index persistence is done correctly** — temp in the same directory, chmod, write, fsync, close,
   rename, with a `success` flag driving cleanup on every early return.
4. **The concurrency handshakes that exist are carefully reasoned** — `Close` captures sync channels under the
   lock and waits outside it; `Append` re-checks context after acquiring the mutex; the `atomic.Pointer` test
   seam for `journalWarnf` is race-free where the usual mutable-package-var trick is not.
5. **Test depth is above average**: cap-parameterized benchmarks, retention tests that pin the *policy* rather
   than the mechanism, corruption-path tests, and TTL tests that avoid flake by backdating instead of sleeping.

---

# Part II — `internal/transport`

Three faces on one daemon: MCP over stdio, HTTP JSON-RPC plus dashboard, and the Unix-socket/loopback-TCP RPC
used by the CLI. ~5,900 non-test LOC over 25 files, plus the stdio loop that lives in `internal/cli/mcp.go`.

## II.1 — P0 findings

### TRN-1 · `statsCache` is not keyed by project — one project's numbers served for another **[verified]** ✅ FIXED `0782051`
`internal/transport/handlers.go:110-112`, `handlers_stats.go:94-102`, `:202-205`. **P0 · S**

The cache holds a single `*StatsResponse` for the whole daemon. In global-daemon mode — the default topology,
one process serving every project — a stats call for project B within the 5 s TTL of a call for project A
returns **A's** numbers. Worse, the store at `:202-205` is unconditional, so a `no_cache: true` call for A
*poisons* the cache for B.

The sibling `recallCache` keys on `bundle.ProjectPath`; this one does not. The dashboard's project switcher
therefore shows another project's event counts, token totals and byte savings, silently and with no error.

**Fix:** key on `bundle.ProjectPath` with a small cap, mirroring `recallCache`; skip the store when `NoCache`
is set.

### TRN-2 · `Validate()` runs on the MCP path only — the silent-success bug survives on two transports ✅ FIXED `70a4e7f`
`internal/transport/mcp.go:464-468` (only call site) vs `socket.go:251-326`, `http.go:610-622`. **P0 · S**

`params_validate.go:5-37` documents the failure precisely: `dfmt_exec` with no `code` hands an empty string to
the shell, which exits 0 in ~25 ms having printed nothing, and the caller gets a successful-looking result. The
fix was wired to one of three transports. The socket path is the primary control plane — the CLI and the
`dfmt mcp` proxy both use it — so any direct socket or HTTP caller still gets the silent empty success.

**Fix:** call the validator in `SocketServer.dispatch` and in the HTTP decode path. The socket already maps
`ParamsError → -32602`, and `missingParam` already returns that type, so the mapping slots straight in.

### TRN-3 · Data race on MCP stdout: `writeParseError` does not take the write mutex
`internal/cli/mcp.go:128-138`, called from the read loop at `:242`/`:258` while workers write through
`writeResp:194-209` (which does lock). **P0 · S**

Two goroutines encode into the same `bufio.Writer` concurrently — interleaved JSON frames and a genuine
`-race` failure. The file's own doc comment at `:181-182` claims "one writer, guarded by writeMu"; one of the
two writers is not. A malformed line arriving while any tool call is in flight can corrupt an unrelated
response, which takes down the session for the agent.

**Fix:** make `writeParseError` call `writeResp` so there is literally one write path; add a `-race` test that
fires a slow handler and a malformed line concurrently.

### TRN-4 · `/api/*` endpoints are exempt from the bearer token and accept an arbitrary `project_id`
`internal/transport/http.go:387`, `handleAPIStats:915`, `handleAPIStream:1371`. **P0 · M**

The exemption is justified in-code on the grounds that these are read-only endpoints the browser calls. But
`/api/stream` streams **the full journal of any project named in the query string** — including `tool.exec`
`code` fields, file paths, and remembered notes — and `/api/stats` accepts a caller-chosen `project_id`. On
Windows the RPC transport is loopback TCP with no ACL, which is the exact threat model that motivated the
token on `/`.

Any local process can therefore read every project's journal without the token while being correctly blocked
from `/`. Host and Origin checks stop browsers, not processes.

**Fix:** require the token on `/api/*` and hand it to the dashboard at page load — the dashboard is served
from the same authenticated origin, so injecting it into the served JS (or a `__Host-` `SameSite=Strict`
cookie) gives it to nobody who could not already read the 0600 port file. Keep `/healthz`/`/readyz` open;
decide `/metrics` explicitly.

### TRN-5 · The `/api/proxy` Unix branch never terminates its frame — every call hangs ~60 s and returns nothing
`internal/transport/http.go:1296-1316`, `forwardProxyOverUnix:1190-1199`, codec at `jsonrpc.go:66-86`. **P0 · S**

`proxyBody` is built with `json.Marshal` (no trailing newline) and written to the socket; the codec only
dispatches on `'\n'`. The target daemon waits for a newline that never arrives, the proxy blocks in
`io.ReadAll`, and the deadlock breaks only when the target's 60 s read-idle timeout closes the connection — at
which point the proxy writes an empty body. The entire macOS/Linux proxy path is non-functional and ties up an
HTTP handler for a minute per call.

No test covers the success path; the existing tests exercise error branches only, which is why this survived.

**Fix:** append `'\n'`, set a connection deadline, add an end-to-end test against a real `SocketServer`.
Consider deleting the endpoint — the dashboard stopped calling it, which is why nobody noticed.

### TRN-6 · Recall's markdown path is a second implementation, and it is the default format
`internal/transport/handlers_recall.go:86-194` vs `internal/retrieve/snapshot.go:34-124` +
`render_md.go`. **P0 · M**

`retrieve.NewMarkdownRenderer` has **no production reference**. The handler's inline renderer therefore loses
everything the real renderer provides:

* **path interning** (`[rN]` refs) — the token-saving feature this project exists for, active on json/xml,
  absent on the default format;
* **`escapeRefTokenForgery`** — hostile event text containing `[r3]` is rendered verbatim in the format most
  callers use, which is the injection the escape exists to stop;
* tier and size header metadata.

Two budget algorithms coexist with different cost models (rendered-line bytes vs `json.Marshal` bytes) and two
different tier orderings. A caller asking for 4096 bytes of JSON receives a fraction of the events the same
budget yields in markdown, with no indication why (**TRN-6b**), and when the budget cannot hold even one
event, `selectedCount = len(lines) - 1` computes 1, so json/xml render an event markdown declared
unaffordable (**TRN-6c**).

**Fix:** delete the inline renderer. Extract the streaming tier-bucket collector — which *is* better than
`groupByTier`, being bounded-memory — into `retrieve.CollectTiers`, then run one `SnapshotBuilder` pass and a
`map[string]Renderer` lookup for output. One budget algorithm, one tier order, interning and forgery escaping
everywhere, ~90 lines deleted.

## II.2 — P1 findings

### TRN-7 · MCP tool failures are returned as JSON-RPC errors; `isError` is dead
`internal/transport/mcp.go:471-473`, field at `:151` never set. **P1 · M**

The MCP spec distinguishes protocol errors (unknown tool, bad arguments → JSON-RPC error) from tool execution
errors (command failed, host blocked, no project → `CallToolResult{isError:true}`, so the model sees the text
and can recover). Every handler error here becomes `-32603`. Many hosts surface that as "tool call failed"
without giving the model the message — so the agent loses carefully-written recovery hints such as
`errNoProject`'s instruction at `handlers.go:202`.

**Fix:** map execution errors to a tool result with `IsError: true`; reserve `-32602` for validation,
`-32601` for unknown tool, `-32603` for "daemon not connected".

### TRN-8 · `Response.ID` is `omitempty` — id `0` vanishes and parse errors omit `"id":null`
`internal/transport/jsonrpc.go:27`, used at `http.go:554-559`, `socket.go:232-237`. **P1 · S**

JSON-RPC 2.0 §5 requires `id` in every response and §5.1 requires `id: null` on parse errors. The comment at
`http.go:553` says exactly that; the struct tag three files away contradicts it. A client that pipelines with
integer ids starting at 0 — common — cannot correlate its first response. (`MCPResponse.ID` got this right.)

**Fix:** drop `omitempty`; test that `{"id":0}` round-trips and that the parse-error envelope contains
`"id":null`.

### TRN-9 · Notifications receive responses on HTTP and socket
`internal/transport/http.go:563-599`, `socket.go:207-238`. **P1 · S**

The MCP path correctly returns nothing for an id-less request; the other two run the method and write an
envelope. §4.1 forbids it, and a pipelined socket client that sends one notification then a request mis-pairs
every subsequent response.

**Fix:** hoist the rule into one shared helper — HTTP replies `204`, socket writes nothing.

### TRN-10 · Partial-write and flush errors on MCP stdout are swallowed
`internal/cli/mcp.go:194-209`, `:128-138`. **P1 · S**

`json.NewEncoder(writer).Encode(resp)` writes into a 4 KiB buffered writer; a response larger than the buffer
flushes mid-object, so an error partway through leaves a truncated JSON line and the next response is appended
to it. The final `_ = writer.Flush()` discards its error, so a failed flush loses a whole response and the
client waits forever on that id.

**Fix:** marshal first, then one locked `Write` + `Flush`; on error, log to stderr and cancel the process
context so the host restarts the transport rather than reading a corrupt stream.

### TRN-11 · HTTP shutdown is unbounded and SSE never unblocks it
`internal/transport/http.go:296-307`, SSE at `:1328-1454`. **P1 · M**

`http.Server.Shutdown` waits for active handlers and does not cancel their request contexts. A connected
dashboard SSE stream keeps `handleAPIStream` alive indefinitely, so the watcher's
`Shutdown(context.Background())` never returns, and `Stop`'s own 5 s shutdown returns an error while leaving
the connection and both goroutines alive.

**Fix:** set `Server.BaseContext` to a server-lifetime context, select on it in the SSE loop, and follow the
timed shutdown with `server.Close()`. The pattern to copy is already in this package —
`SocketServer`'s cancel → close → bounded `connWG` drain.

### TRN-12 · `StopJobs` cancels but does not wait — workers can append to a closing journal
`internal/transport/exec_jobs.go:296-306`, called from `daemon.go:679` immediately before the journal
closes. **P1 · S**

`runJob → runExec → logEvent → Journal.Append` can execute after close; the sandbox alone allows ~2 s of
unwinding via `execWaitDelay`. Best case is error spam, worst case a torn final line.

**Fix:** add a `jobsWG`; cancel then drain with a bounded wait.

### TRN-13 · Async jobs are unscoped — any caller can poll or cancel any `job_id`
`internal/transport/handlers_exec.go:76-81`, `exec_jobs.go:232-271`. **P1 · M**

The job table is daemon-global with no session or project key. A caller holding a ULID can read another
project's command output or kill its job, and the 32-job cap is shared, so one project can starve every other
project on the host.

**Fix:** store project/session on the job and compare on poll/cancel, returning `errJobUnknown` on mismatch so
existence is not leaked; make the cap per-project.

### TRN-14 · SSE re-scans and re-verifies the entire journal every 2 s, per client
`internal/transport/http.go:1441`, `core/journal.go:285-345`, `:388-391`. **P1 · M**

Each tick calls `Stream(ctx, {From: lastID})`, which opens every segment and unmarshals **and
signature-verifies** every line before discarding those up to the cursor. The comment at `:1456-1460` claims
the cursor short-circuits the scan; it does not. At the 10 MiB cap that is continuous SHA-256 burn per open
dashboard tab.

**Related (TRN-14b):** if the cursor's segment rotates away, `foundFrom` never becomes true and the stream goes
silent **forever** — no error, no reconnect trigger, and `EventSource` still sees a healthy connection.

**Fix:** add a `StreamFrom(cursor)` fast path that seeks by byte offset; on an unknown cursor, fall back to
head and emit an SSE comment noting the gap. Add a heartbeat comment every ~15 s (there is none today, and with
`WriteTimeout` deliberately 0 the server never notices a half-open connection).

### TRN-15 · Error→code mapping and decode strictness differ per transport
`socket.go:222-225` vs `http.go:636` (×12) vs `mcp.go:464-473`. **P1 · S**

Socket maps `ParamsError → -32602`; HTTP maps everything to `-32603` and decodes with bare `json.Unmarshal`
instead of `decodeParams`, so it gets no trailing-token rejection, no strict mode, and no typed params error.

**Fix:** one `rpcErrorFor(err) int` used by all three; route the HTTP decode through `decodeParams`.

### TRN-16 · `WireHandlerMetrics` reads direct fields, so index and journal gauges are dead in global mode
`internal/transport/metrics_handlers.go:157-188`. **P1 · M**

`h.index` and `h.journal` are nil whenever a `ResourceFetcher` is installed — the default topology. So
`dfmt_index_docs` reports 0 and `dfmt_journal_bytes` is never registered.

**Related (TRN-16b):** `Remember` calls `recordToolCall("remember", …)` but `trackedTools` omits `remember`,
and `recordToolCall` returns early on a map miss — the counter and histogram are silently dropped. `Stats`
records nothing at all.

**Fix:** aggregate over `LoadedProjects()`, or label gauges by project; add the missing tool names and a test
asserting every `recordToolCall` literal appears in `trackedTools`.

### TRN-17 · Schema/validator drift on two tools
`internal/transport/mcp_schemas.go:78`, `:164-197`, `:249-252`. **P1 · S**

* `dfmt_exec` declares `required: ["code"]`, but polling or cancelling a job requires omitting `code`. Hosts
  that validate arguments client-side will refuse to send the poll call at all.
* `dfmt_remember` declares `type` both required **and** defaulted — an agent trusting the default gets
  `-32602`.
* `format` has no `enum` and is never validated: `format:"yaml"` falls through and returns markdown labelled
  `"format":"yaml"`. Same for `return`, whose enum exists in the schema and is never checked by a handler.

**Fix:** express exec's rule as `anyOf`, drop `type` from `required`, validate `format`/`return` against their
enums.

### TRN-18 · `MaxAsyncExecTimeout == HardMaxExecTimeout` — the backstop can never fire
`internal/transport/exec_jobs.go:80` vs `internal/sandbox/sandbox.go:239`. **P1 · S**

Both are 2 h. The layering is documented as "policy ceiling under an absolute backstop", but at equal values
the job context deadline and the sandbox deadline fire at the same instant, making the terminal status a
coin-flip between `done`+`timed_out` and `canceled` — which is exactly why the `DeadlineExceeded` branch at
`:205-213` exists.

**Fix:** give the backstop headroom (`2h + 5m`) so the ladder is strictly ordered, and document the full ladder
(`60s default → 900s sync → 2h async → backstop`) in one place. The same clamp is currently written four
times (`exec_jobs.go:311`, `handlers_exec.go:115`, `sandbox/exec.go:124`, `handlers_io.go:201`) — extract one
helper.

## II.3 — P2: structure

* **`http.go` is 1,598 lines** mixing JSON-RPC dispatch, port-file and token management, dashboard assets,
  daemon-registry reads, cross-daemon proxying and SSE. Split into five files along those seams — pure moves,
  reviewable in one pass.
* **Twelve near-identical HTTP handlers and ten near-identical socket cases** (~190 lines of
  `decode → WithProjectID → call → wrap`) exist because only the MCP path adopted the generic dispatcher.
  `dispatchTool` (`mcp.go:446-479`) is the right abstraction and should become a shared
  `map[string]handlerFunc` method table for all three transports. Adding a tool is currently a four-file
  edit — a cost the code itself documents at `backend.go:10-14`. **This is where TRN-2, TRN-9, TRN-15 and
  TRN-8 all get fixed once instead of three times.**
* **`Handlers` is a 110-line god object** with eleven responsibilities. Extract the four caches (dedup, wire
  dedup, stats, recall) and the job table into their own types with their own tests — each is ~40 lines of
  mutex + map + eviction currently sharing a struct with transport wiring.
* **The MCP stdio loop lives in `internal/cli`**, out of reach of this package's tests and invariants — which
  is how TRN-3 and TRN-10 survived. Move it to `internal/transport/mcp_stdio.go`.
* **Job-table hygiene:** the cap check is TOCTOU (two submitters at 31 both pass), `cancelJob` busy-waits with
  a 20 ms poll for up to 6 s instead of selecting on a `done` channel, and finished jobs are reaped only on the
  next submission, so a quiet daemon holds full `ExecResponse` payloads past the retention window.
* **Dead and misleading:** `RPCError.Unwrap` on a type with no `Error()` method (unreachable by
  `errors.Is/As`), `Codec.rw` stored and never read, `MCPResponse.IsError` never set, a stale doc comment for
  `handleAPIProxy` sitting above `writeProxyError`, `mcpMaxLineBytes` duplicating
  `transport.MaxJSONRPCLineBytes`, and `readCapped` duplicating `Codec.readCappedLine` with different EOF
  semantics.
* **Seven files end with an orphaned doc comment** left by a file split, so godoc for `ExecParams`,
  `ReadParams`, `RememberParams`, `RecallParams`, `StatsParams`, `EditParams` and `WriteParams` is empty or
  attached to the wrong symbol. `handleToolsList` also mutates tools by positional index (`tools[0]`,
  `[1]`, `[2]`), an invariant documented twice and enforced nowhere.

## II.4 — Strengths

1. **The comments encode measurements.** The `WriteTimeout: 0` rationale carries the observed `sleep 25` vs
   `sleep 45` evidence; the framing regression note names the transport that hit it; the recall tier-bucket
   comment explains what it replaced and why. These are the package's real documentation.
2. **`dispatchTool` is the right abstraction** — generic over params and response, decoder injected per tool,
   validation as an optional interface. It collapsed 11×12 lines into 11×2 and should be the template for the
   rest.
3. **Layered defence on the HTTP surface**: `safejson` depth checks at every entry point, a correct
   `sync.Once`-guarded `limitListener`, 0600 atomic port-file writes, constant-time bearer compare,
   fail-closed `/api/daemons` filtering, and a fail-closed non-TCP origin check with an explicit
   "don't fix this" note.
4. **`SocketServer` shutdown is the model** — cancel, close the listener, drain `connWG` with a bounded
   timeout and a warning on overrun. Applying that shape to exec jobs and SSE closes two leaks found here.
5. **The recall cursor-keyed cache and its empty-cursor guard** show real care: a TTL would have been simpler
   and wrong in both directions, and the reasoning is written down.

---

# Part III — `internal/sandbox`

The seven tool primitives, the policy engine, and the output-normalization pipeline. 24 non-test files,
~5,100 production LOC against ~10,800 test LOC.

## III.1 — P0 findings

### SBX-1 · Deny rules with arguments are unenforced on chained commands **[verified]** ✅ FIXED `a353f8c`
`internal/sandbox/exec.go:62-82`, specifically `:79`. **P0 · S**

In the chain-aware path each split part is checked as `s.policy.Evaluate("exec", partBase)` — **only the first
word**. A rule `deny:exec:sudo *` compiles to `(?s)^sudo[ \t]+.*$`, which cannot match the bare text `sudo`.
The other two checks do not cover it either: the full-chain check matches the whole string
`git status && sudo whoami` against the same start-anchored pattern and fails, and the base-command check only
sees `git`.

Under the documented remediation path (default-permissive `allow:exec:**` plus operator `deny:` rules):

| Command | Outcome |
|---|---|
| `sudo whoami` | denied (single-command path checks the full text) |
| `true && sudo whoami` | **allowed** |
| `(sudo whoami)` | **allowed** |

The existing tests miss it because the subshell and here-string cases use a *restrictive allow list*, where
the denial actually comes from the allow-miss branch rather than the deny rule — so the one configuration
operators are told to use is the untested one. Every operator-authored `deny:exec:<cmd> <args>` rule, the sole
mechanism this project offers for restricting exec, is bypassable with a one-character prefix.

**Fix:** in the per-part loop, additionally evaluate the full trimmed part text (`EvaluateReason`) for the deny
side. Add a matrix over `{sudo x, a && sudo x, a; sudo x, a | sudo x, (sudo x), $(sudo x)}` × default-permissive
+ one deny rule.

### SBX-2 · Path deny rules are case-sensitive on case-insensitive filesystems
`internal/sandbox/policy.go:71-79`, `glob.go:23-38`. **P0 · S**

`Rule.Match` lowercases text only when `op == "exec"`. For read/write/edit the raw path is compared
byte-for-byte, so on Windows and default macOS `deny:read:**/.env` fails to block `.ENV`, `.Env`, `.eNv` — all
of which open the same file. The mirror-image bug: exec *text* is lowercased but the *pattern* is not, so
`deny:exec:SUDO *` never fires.

**Fix:** case-fold both sides for path ops on case-insensitive platforms (or unconditionally — over-denying is
the safe direction), and lowercase exec patterns at compile time.

### SBX-3 · `.dfmt/permissions.yaml` is not YAML, and malformed rules fail open silently
`internal/sandbox/policy.go:336-375`. **P0 · M**

`LoadPolicy` parses a bespoke `action:op:text` line format from a file named `.yaml`. An operator who writes
actual YAML — the near-certain first attempt — produces **zero rules**, because `SplitN(line, ":", 3)` yields
fewer than three parts or an `op` of `- read`. Unknown actions and ops are dropped with `continue`.
`PolicyLoadResult.Warnings` exists but `LoadPolicy` never populates it.

The failure mode is "operator believes secrets are walled off; nothing is". A typo (`deny:raed:creds/**`)
produces an inert rule with no diagnostic.

**Fix:** validate action and op against their domains, append a warning per rejected line, and surface warnings
in `dfmt doctor` and at daemon startup. Rename the file to `.dfmt/permissions.rules` or add a real YAML
front-end. Cap file size and rule count.

### SBX-4 · The normalization pipeline never runs for `Fetch` or `Read` **[verified]**
`internal/sandbox/fetch.go:305-309`, `read.go:132-139`. **P0 · M**

The only production callers of `NormalizeOutput` are `exec.go:266` and `:286`. Everything gated on
HTML/JSON/YAML/binary bodies — `ConvertHTML`, `CompactHTML` (ADR-0008), `CompactBinary`, `CompactStructured`
(ADR-0010), `CompactYAML`, `CompactMarkdownFrontmatter` — is therefore reachable only from shell output, while
the bodies it was written for arrive through `dfmt_fetch` and `dfmt_read`.

Today a fetched web page reaches `ApplyReturnPolicy` as raw HTML: the walker never runs, boilerplate is never
dropped, and the agent pays full token price for div soup. Roughly 1,900 LOC across `htmltok.go`, `htmlmd.go`,
`structured.go` and `binary.go` — two ADRs' worth of implementation — is unreachable from the tools it
targets. The benchmark harness models "NormalizeOutput on raw bytes first" for fetch, so the published savings
describe a path production does not take.

**Fix:** call `NormalizeOutput` in `Fetch` (before `ApplyReturnPolicy`) and in `Read`, with a file-oriented
mode for the latter. **Land SBX-5 through SBX-7 first** — the destructive stages below would otherwise start
corrupting file reads.

### SBX-5 · RLE runs before the structural compactors and invalidates them
`internal/sandbox/intent.go:474` (RLE) vs `:476` (diff), `:488` (JSON), `:492` (YAML). **P0 · M**

`runLengthEncode` rewrites any run of ≥4 identical adjacent lines into one line plus a marker. Downstream:

* **Pretty-printed JSON** with repeated identical lines (`"value": 0,` ×6 — routine in `aws`/`kubectl` output)
  becomes syntactically invalid. `CompactStructured` then declines because `json.Valid` fails, so the agent
  receives corrupted, unparseable JSON **and** loses the compaction.
* **Unified diffs** with 4+ identical adjacent lines are mangled into something that cannot be applied — and
  diffs are exactly what an agent copies verbatim.
* **YAML** lists with repeated entries: same failure, and `CompactYAML` may still parse the mangled text and
  re-emit it.

**Fix:** move RLE to the end of the pipeline, and skip it entirely when the body was recognized as
JSON/YAML/diff. Cleanest: have each compactor return `(out string, matched bool)` and let the driver choose
the tail stages.

### SBX-6 · `CompactYAML` fires on ordinary text and silently rewrites it
`internal/sandbox/structured.go:93`, `:106-168`. **P0 · M**

Detection is `^---\s*$` **or** a line starting `apiVersion: `/`kind: ` anywhere in the body. A
`grep -rn "kind: "` result set, a log line `kind: warning`, or a markdown file with a horizontal rule all
trigger it. If `yaml.Unmarshal` then succeeds — and YAML parses an enormous range of plain text — the body is
re-marshalled, which **drops every comment** (usually the thing that makes it shrink, so the
"never inflate" guard passes), **re-sorts map keys**, normalizes quoting and indentation, expands anchors, and
drops keys named `url`, `cursor`, `generation`, `total_count` from a body that is not an API response at all.

An agent that reads a config, edits it and writes it back loses all comments and reorders all keys.

**Fix:** require the marker at the *start* of the body, require the decoded root to be a map with
`apiVersion` + `kind` (or explicit document separators), and never apply noise-field dropping to bodies that do
not look like API output.

### SBX-7 · `normalizeLineEndings` defeats `collapseCarriageReturns` on exactly the platform that needs it
`internal/sandbox/intent.go:428-437`, `:527-542`. **P0 · S**

`normalizeLineEndings` early-returns when there is no CRLF — but if there is even one, it also maps **every
standalone `\r` to `\n`**. Progress-bar output therefore explodes into dozens of separate lines *before* the
stage whose entire purpose is collapsing progress bars gets to run. The collapse is disabled precisely on
Windows/PowerShell output.

**Fix:** swap the order — `collapseCarriageReturns` first (it already trims trailing `\r` per line, so CRLF is
safe), then `normalizeLineEndings`; or drop the standalone mapping and let the collapse own all `\r` semantics.

### SBX-8 · Short UTF-16LE output is misdetected as binary and discarded (Windows)
`internal/sandbox/exec.go:550-559` + `binary.go:97`. **P0 · S**

The BOM-less UTF-16 heuristic requires more than 15 NULs over the even positions of the first 100 bytes — at
least 16 characters of output. A PowerShell or Git-Bash command emitting fewer (`Get-Date`, `echo hi`, an exit
probe) fails the heuristic, keeps its NUL bytes, and then hits `CompactBinary`, whose threshold is **one** NUL,
at the very first pipeline stage. The agent receives `(binary; type=application/octet-stream; 18 bytes; …)`
instead of `hi`.

**Fix:** lower the threshold to "≥2 NULs at even positions and none at odd positions over the available
prefix", or special-case short even-length inputs. Add a `NormalizeOutput` regression test with a 6-byte
UTF-16LE body.

### SBX-9 · Windows children get no `SystemRoot`, `COMSPEC`, `PATHEXT` or `WINDIR`
`internal/sandbox/exec.go:396-410`. **P0 · S**

`buildEnv` constructs the Windows environment from scratch with five variables. `SystemRoot` is required by
Winsock initialization and the CryptoAPI; `PATHEXT` is how `.bat`/`.cmd` resolution works; `COMSPEC` is
required by anything that shells out. Python's `socket`/`ssl`, `ping`, and much of .NET fail without them.
Compounding it, `SYSTEMROOT` and `COMSPEC` are in `sandboxBlockedEnvNames`, so the agent cannot supply them
either — the block list assumes a base environment `buildEnv` never provides.

**Fix:** add `SystemRoot`, `windir`, `COMSPEC`, `PATHEXT`, `APPDATA`, `ProgramData`, `ProgramFiles`,
`NUMBER_OF_PROCESSORS`, `PROCESSOR_ARCHITECTURE`.

### SBX-10 · `DetectShell` can return a shell that `Runtimes.Probe` never probes
`internal/sandbox/runtime.go:29-59` vs `:104-127`. **P0 · M**

`detectShellWindows` may return `pwsh`, `powershell` or `cmd`; `Probe` enumerates bash, sh, node, python,
python3, go, ruby, perl, php, R, elixir — none of the three. On a Windows host without Git Bash, **every**
`dfmt_exec` with an unspecified language fails with `runtime not available: powershell`.

Even with the runtime present, `writeTempFile`'s extension map has no PowerShell entry, so the script is
written as `.txt` and invoked as `powershell.exe C:\Temp\dfmt-sandbox-x.txt`, which PowerShell reads as a
command name. `cmd` would need `/c`.

**Fix:** add the three shells to `Probe`, add `.ps1`/`.cmd` to the extension map, and replace the two-branch
invocation with a table (`bash|sh → -c`, `pwsh|powershell → -NoProfile -NonInteractive -File`, `cmd → /c`).
`-NoProfile` matters — otherwise the operator's profile runs inside every sandboxed exec.

## III.2 — P1 findings

### SBX-11 · `Read` silently converts CRLF to LF, breaking `Edit` anchors
`internal/sandbox/read.go:172-222`, doc claim at `:169-171`. **P1 · M**

`bufio.ScanLines` strips the trailing `\r` and `readLineWindow` re-joins with bare `\n`. The docstring
promises a byte-exact round-trip *because* anything else breaks `dfmt_edit` anchors — and that promise is false
on the platform this project is developed on. `Edit` does byte-exact counting, so any anchor copied out of a
CRLF file fails with `old string not found`. The round-trip test uses LF fixtures only.

**Fix:** preserve the terminator (custom `SplitFunc`, or detect the dominant terminator once). Add CRLF and
mixed fixtures plus an end-to-end Read→Edit test.

### SBX-12 · Fetch redirects bypass the policy layer
`internal/sandbox/fetch.go:279-285`. **P1 · S**

`CheckRedirect` re-runs the SSRF IP/host checks but never re-runs `PolicyCheck("fetch", …)`. An operator rule
denying an internal host is enforced only on the initial URL, so a public URL that redirects to a denied
public host is fetched.

**Fix:** re-run the policy check in `CheckRedirect` before the SSRF assertion.

### SBX-13 · SSRF blocklist gaps: NAT64, 6to4, reserved IPv4
`internal/sandbox/fetch.go:100-144`. **P1 · S**

The baseline is strong — loopback, link-local, multicast, RFC1918/ULA, CGNAT, documentation prefixes, metadata
literals, IPv4-mapped v6, zone-id rejection, DNS-failure-is-deny, and a rebinding-proof custom dialer. Missing:
`64:ff9b::/96` (NAT64 — `64:ff9b::7f00:1` reaches 127.0.0.1), `2002::/16` (6to4), `192.0.0.0/24`,
`198.18.0.0/15`, `240.0.0.0/4`, `255.255.255.255`; and the hostname list omits `instance-data`, bare
`metadata`, and `192.0.0.192`.

**Fix:** replace the hand-rolled predicate chain with a `[]net.IPNet` table built at init. This also deletes
`isIPv6InPrefix`, a hand-rolled comparator that `IPNet.Contains` does correctly.

### SBX-14 · Non-shell code runs from the shared temp directory
`internal/sandbox/exec.go:141-148`, `:318-356`. **P1 · S**

`os.CreateTemp("", …)` places the script in the *shared* system temp dir. CPython prepends the script's
directory to `sys.path`; Node resolves `./node_modules` relative to it. On a multi-user host, an attacker who
plants `/tmp/os.py` or `/tmp/json.py` hijacks imports in every `dfmt_exec` with `lang: python`. The file is
0600 and unpredictable; its *directory* is the attack surface.

**Fix:** `os.MkdirTemp("", "dfmt-exec-")` at 0700, write inside, `defer os.RemoveAll`.

### SBX-15 · Agent-supplied fetch timeout is unclamped at the sandbox boundary
`internal/sandbox/fetch.go:229-232`. **P1 · S**

`MaxFetchTimeout` is applied only in the transport handler; the sandbox API accepts an arbitrary duration and
pins a `fetchSem` slot. Exec clamps in-package; fetch is asymmetric.

**Fix:** clamp in `Fetch`. (Also: `Timeout: -1` on exec produces an already-expired context rather than the
default.)

### SBX-16 · The HTML tokenizer cap flushes raw markup into agent context
`internal/sandbox/htmltok.go:110-132`. **P1 · S**

At `maxHTMLTokens` the remainder of the document is emitted as a single text token, so `<script>` bodies,
hidden divs and comment text past the cap are whitespace-collapsed into the "markdown" output — a
prompt-injection channel through the very machinery built to close it.

**Fix:** emit a truncation marker and drop the remainder.

### SBX-17 · stdout truncation is silent while stderr's is announced
`internal/sandbox/exec.go:205`, `:246-256` vs `:287-289`. **P1 · S**

Stderr gets a truncation marker; stdout is cut at 256 KB with no marker in `Stdout`, `RawStdout`, `Summary` or
any flag. An agent reading a truncated test tail cannot distinguish "the run ended here" from "we cut here" —
and the tail-bias logic presents the truncated tail as the verdict.

**Fix:** detect truncation via a `LimitReader` of `MaxRawBytes+1`, append the same marker, add
`Truncated bool` to `ExecResp`.

### SBX-18 · `RawStdout` is not raw
`internal/sandbox/exec.go:266`, `:303`. **P1 · S**

`output` is reassigned to `NormalizeOutput(output)` before `RawStdout: output`, so the content store never
holds the real bytes. An agent that stashes a `git diff` and retrieves it later gets a version with index
lines removed and identical runs RLE-collapsed — a diff that will not apply.

**Fix:** capture the pre-normalization bytes for the stash, or rename the field and document it honestly.

### SBX-19 · `GrepReq.Context` is accepted end-to-end and ignored
`internal/sandbox/sandbox.go:153`, `glob_grep.go:289-395`. **P1 · M**

The MCP schema exposes `context`, the handler copies it into the request, and the sandbox never reads it.
Users asking for context lines get none, with no error.

**Fix:** implement it in `grepFileLines` — it is the single most-requested grep affordance and is trivial once
the file is line-split.

### SBX-20 · Intent filtering happens after the 100-match cap
`internal/sandbox/glob_grep.go:317`, `:386-388`. **P1 · M**

The walk stops at 100 raw regex hits and only then filters by intent. A search whose relevant hits are the
101st onward returns "0 matches after filtering" from a tree that contains them — and which hits those are
depends on lexical directory order.

**Fix:** score during the walk with a bounded top-N heap instead of truncate-then-filter.

### SBX-21 · A single long line fails the whole read; `Edit` has no size ceiling
`internal/sandbox/read.go:19`, `:201-203`; `edit_write.go:75`. **P1 · S**

A file with one line over 1 MiB (minified bundle, single-line JSON) makes the scanner return `ErrTooLong` and
`Read` returns an error with no partial content and no explanation — pushing the agent to the shell fallback
this tool exists to prevent. Separately, `Edit` does an unbounded `os.ReadFile` while `Read` caps at 4 MiB and
`Grep` at 8 MiB, so editing a 3 GB log allocates it twice in a long-lived daemon.

**Fix:** degrade gracefully on `ErrTooLong` (return what was read plus a summary naming the line); stat before
`Edit` and refuse above a `MaxEditFileBytes`.

### SBX-22 · Every filesystem primitive ignores its `context.Context`
`glob_grep.go:17`, `:289`; `read.go:26`; `edit_write.go:14`, `:150`. **P1 · S**

Five methods take a context and never consult it, so a `**/*` walk over a monorepo cannot be cancelled when
the caller's deadline fires — the daemon keeps walking after the client is gone.

**Fix:** check `ctx.Err()` in both walk callbacks and before each file read.

### SBX-23 · Path traversal guard is missing when `wd == ""`, and NUL checks are inconsistent
`internal/sandbox/read.go:70-74`; `glob_grep.go:30-34`, `:199-206`. **P1 · S**

`Read` refuses *absolute* paths when the working directory is empty but accepts `../../etc/passwd`, which then
resolves against the process CWD with no containment check — Glob/Grep/Edit/Write all substitute `"."` and are
anchored. Separately, Read/Edit/Write reject embedded NUL bytes; Glob's pattern and Grep's path do not, so the
policy can be evaluated on a path the kernel truncates differently.

**Fix:** treat empty `wd` as `"."` everywhere; hoist the NUL check into the shared sanitizer below.

### SBX-24 · Windows tree-kill has PID-reuse and re-parenting holes; Unix has no SIGTERM grace
`spawn_windows.go:73-94`; `spawn_unix.go:33-46`. **P1 · L / S**

On Windows, if the child exits between deadline and `taskkill /PID`, a recycled PID means an unrelated tree is
force-killed; and `/T` walks the parent-pid field, so a grandchild whose parent already exited is not in the
tree — precisely the orphaned-`sleep` case the design targets (`execWaitDelay` bounds the damage, so the
deadline still holds; the descendant survives). On Unix, `SIGKILL` goes straight to the group with no grace, so
a killed `git` leaves `.git/index.lock` behind for every later exec.

**Fix:** Windows — assign a Job Object with kill-on-close (an ADR-sized change; the stated objection is
solvable by assigning the job at daemon start so children inherit it). Unix — `SIGTERM`, ~250 ms, then
`SIGKILL`, both comfortably inside `execWaitDelay`. Add a Windows-tagged descendant-kill test; today only the
Unix semantics are covered.

### SBX-25 · Binary detection: one NUL condemns a body, and two-byte magic numbers over-match
`internal/sandbox/binary.go:35`, `:87-121`. **P1 · S**

`binaryNullByteThreshold = 1` means a single NUL in the first 512 bytes replaces the entire body with a
summary. `matchBinaryMagic` compares raw prefixes with no corroboration, and `MZ` is two bytes — any output
beginning with `MZ` (a CSV column, a German log line) is discarded as an executable. Detection samples only
the head, so text-then-binary output passes through and gets mangled by JSON encoding.

**Fix:** require corroboration (invalid UTF-8 or a NUL) for prefixes shorter than four bytes; make the NUL rule
density-based; optionally sample the tail.

### SBX-26 · Noise-field dropping is silent, including meaningful nulls and pagination state
`internal/sandbox/structured.go:23-61`, `:443-506`. **P1 · S**

`url`, `html_url` and every `*_url` key is removed from **any** JSON, so `gh api` for a download URL returns an
object without it. `{"status":"ok","error":null}` becomes `{"status":"ok"}`, so the agent cannot distinguish
"no such key in this schema" from "explicitly null". `total_count`/`has_more`/`next_page` are dropped, removing
the only signal that a paginated result is incomplete. The docstring points at `return: "raw"` as the escape —
but nothing tells the agent compaction happened, so it cannot know to retry.

**Fix:** append a one-line footer when anything was dropped; keep pagination state; make the empty-value drop
opt-in. Same for `CompactMarkdownFrontmatter`, which strips frontmatter from arbitrary exec output.

## III.3 — P2: structure and performance

* **The path-sanitization prologue is written five times** (`read.go:28-74`, `edit_write.go:20-57` and
  `:156-194`, `glob_grep.go:24-40` and `:195-211`) with **inconsistent membership** — Read has the NUL check
  and symlink re-check but no reserved-name check; Edit/Write have reserved-name and `CheckNoSymlinks`;
  Glob/Grep have neither. That inconsistency *is* SBX-23. Extract one
  `resolveUnderWD(path string, tier accessTier)` and delete ~120 lines.
* **Four glob dialects live in this package and a fifth outside it**, three of which implement `**`
  differently:

  | Implementation | File | `**` semantics | Used by |
  |---|---|---|---|
  | `globToRegex` | `glob.go:121-171` | `.*`, with `/**` special-cased to require a directory | policy read/write/edit/fetch |
  | `globToRegexShell` | `glob.go:48-95` | `.*` crossing `/`, space → `[ \t]+` | policy exec |
  | `globPatternRegex` | `globexpand.go:148-187` | doublestar (`**/` = zero or more segments), **no `(?s)`** | `dfmt_glob` |
  | `filepath.Glob` / `Match` | `glob_grep.go:61`, `:353` | no `**` | glob fallback, grep `files:` |
  | `matchIgnorePattern` | `capture/fswatch.go:273` | a fifth dialect | fs watcher |

  The result is user-visible: `**/*.go` in `dfmt_glob` matches a root-level file, the same pattern in a
  `deny:read:` rule does not. **Consolidate into one `globcompile` package** with explicit option flags and
  three named presets. Note the `(?s)` audit came out clean on the policy side — `anchorPattern` is applied by
  both policy compilers, so every policy-derived pattern is covered; only the file walker's regex lacks it.
* **Oversized units:** `execImpl` (194 lines), `Glob` (149), `splitByShellOperators` (117),
  `walker.popFrame` (118) mirroring `handleStart` (87) with an invariant nothing checks, `Grep` (106),
  `Exec`'s policy tree (90 — where SBX-1 hides), `ApplyReturnPolicy` (84). Highest leverage:
  extract `authorizeExec` with a table-driven test; split `execImpl` into build/run/shape; replace the two
  HTML switches with one `elementSpec` table.
* **Performance:** `findRecentAnchorHref` rescans the token stream per `</a>` → O(N²) on link-heavy pages
  (fix: keep attributes on the walker's frame stack, O(1)); the builder pool is used in a way that returns a
  *different* pointer than it borrowed, so nothing is reused **and** each pooled entry retains its whole
  `walker` including up to 200k tokens; `Glob` with an intent reads every matched file uncapped (up to 5,000
  full `os.ReadFile`s, no size limit); `grepFileLines` slurps + `strings.Split` an 8 MiB file into ~200k
  string headers per file; `finalize` collapses blank lines with a repeated full-buffer `ReplaceAll` loop;
  `walkDropNoise` calls `os.Getenv` per NDJSON line despite a `WithFlags` split existing to avoid exactly that.
* **`ApproxTokens` under-counts code by 25–40 %** (`tokens.go:37-51`): `ascii/4 + nonASCII` is accurate for
  English prose and CJK — the migration's stated goal — but source, JSON and diffs run ~2.8–3.2 bytes/token.
  Since exec output is overwhelmingly code, the dominant input is the one the heuristic handles worst, and it
  errs toward inlining. Cheap fix: `punct/2 + alnum/4 + nonASCII`.
* **Dead code:** `hardDenyExecBaseCommands` is an **empty map**, so `isHardDenyExec` is constant-false, the
  `MergePolicies` filtering branch and its warnings are unreachable, and the docstring describes a guard that
  cannot fire — ADR-0014 is documented as implemented and is inert. Also `maxHTMLTagDepth` (declared,
  never referenced, and the walker stack is genuinely unbounded), `TailBytes`, `MediumThreshold`,
  `Runtimes.Reload`, `globMatchDefault`, `BatchExec`, and seven files ending in an orphaned doc comment so
  godoc for `Exec`, `Glob`, `Edit` and `SandboxImpl` is missing or misattributed.

## III.4 — Strengths

1. **The best in-code postmortems in the repository.** `spawn_windows.go:20-47` (detached-daemon console
   allocation: "every `dfmt_exec` hung forever while read/glob/grep kept working"), `exec.go:174-180`
   ("Timeout=3s against `sleep 20` returned after 20.7s"), `walkskip.go:9-44` ("58 of 100 match slots inside
   `dist/dfmt.exe`"), `globexpand.go:11-27` ("`**/*.go` returned 2 of 278 Go files"). This made the audit
   dramatically faster and must survive any refactor.
2. **The exec deadline implementation is complete and correct** — `cmd.Cancel` + process-tree kill +
   `WaitDelay` + draining the pipe + distinguishing deadline from caller cancellation. Most implementations
   get two of the five.
3. **SSRF defence is properly layered** — pre-check, a dialer that resolves-validates-then-dials the literal
   IP (closing the rebinding window a second lookup would open), per-redirect revalidation, DNS failure as
   deny, metadata hostnames blocked before DNS.
4. **Failure modes are surfaced rather than hidden** — `walkSkipStats` exists because "a skip the agent cannot
   see is indistinguishable from an absence of matches"; `Read` reports `TotalLines: 0` rather than a partial
   count presented as a total; `DenyReason` splits allow-miss from explicit-deny because the remedies are
   opposites.
5. **`Edit` refuses to guess.** Turning "silently rewrite the first of five matches and report success" into
   an actionable error, with both remedies named, is the right call and rarer than it should be.
6. **Security fixes are traceable** — V- and F-numbered findings are cited at the lines that close them, with
   the bypass spelled out, which materially lowers the regression risk of a refactor.

---

# Part IV — `internal/capture`, `internal/setup`, `internal/safefs`, `cmd/`, build & hygiene

## IV.1 — Capture: the filesystem watcher is the weakest subsystem in the repo

### CAP-1 · No macOS watcher exists, and enabling capture there is a silent no-op **[verified]**
`internal/capture/` has `fswatch_linux.go` and `fswatch_windows.go`; there is no `fswatch_darwin.go`. **P0 · S**

`initWatcher` stays nil, so `Start()` walks the whole tree doing nothing and returns nil. `capture.fs.enabled:
true` on macOS produces no events, no error, no warning and no doctor signal — while the release workflow
ships `darwin/amd64` and `darwin/arm64` binaries.

**Fix now:** warn at construction when `initWatcher == nil` and surface it in `dfmt doctor`.
**Fix properly:** kqueue (`EVFILT_VNODE`), or reuse the polling path behind `//go:build darwin || freebsd`.

### CAP-2 · The inotify buffer is ~4.2 MB **per watched directory** **[verified]**
`internal/capture/fswatch_linux.go:50-51`. **P0 · S**

```go
const eventSize = 16 + unix.PathMax   // 4112
buf := make([]byte, eventSize*1024)   // 4,210,688 bytes — per goroutine, per directory
```

One goroutine and one buffer per directory. A 500-directory repo — ordinary for a Go or Node monorepo —
allocates ~2.1 GB of live heap the moment capture is enabled. The kernel never returns more than the read
length; 64 KiB is the conventional size.

### CAP-3 · One inotify instance per directory — capture silently caps out at ~128 directories
`internal/capture/fswatch_linux.go:20-30`. **P0 · M**

`InotifyInit1` is called **per directory** instead of once with N watches. The default
`fs.inotify.max_user_instances` is 128, so the 129th directory fails with EMFILE and the function returns with
**no log, no error, no counter**. On any real repository, capture watches the first ~128 directories in walk
order and the rest of the tree is invisible forever. `InotifyAddWatch` failures (`max_user_watches`, default
8192) are dropped the same way.

**Fix:** one fd, a `map[int]string` from watch descriptor to path, one reader goroutine; log every add failure
and expose a `watchFailures` counter beside `DroppedEvents`.

### CAP-4 · The Windows watcher polls and re-walks the same subtree from every nesting level
`internal/capture/fswatch_windows.go:41-149`. **P0 · M**

Each directory gets a goroutine with a 2 s ticker whose `scanDir` performs a **recursive** `filepath.Walk`, and
every newly-seen subdirectory spawns another watcher that re-walks that same subtree. For a tree of depth *d*,
every file is stat-ed ~*d* times every two seconds and every create is emitted once per ancestor watcher
(deduplicated only by the per-path debounce, and only when `debounce_ms > 0`). Deletions always report
`isDir=false`; goroutines for deleted directories poll a nonexistent path until `Stop()`.

This also contradicts the docs (see IV.5): `capture.go:4` and three places in `ARCHITECTURE.md` describe a
`ReadDirectoryChangesW` implementation, including an optimization note for code that does not exist.

**Fix:** short term, make the per-directory scan non-recursive so nesting stops multiplying work. Medium term,
implement `ReadDirectoryChangesW` with IOCP on the project root with `bWatchSubtree=TRUE` — one handle, no
polling, no per-directory goroutines.

### CAP-5 · The Go default ignore list omits `.dfmt/**`, re-opening the journal feedback loop **[verified]** ✅ FIXED `9e23477`
`internal/config/config.go:172` vs the comment at `internal/config/defaults.go:24-26`. **P0 · S**

```go
c.Capture.FS.Ignore = []string{".git/**", "node_modules/**", "__pycache__/**"}
```

The YAML template's own comment states that the list **must** include `.dfmt/**`, because otherwise every event
the daemon journals triggers a filesystem event the watcher feeds back in — a self-amplifying loop the project
has already paid for once. Two paths reach the unsafe list: a `config.yaml` that enables capture without an
`ignore:` key (yaml.v3 leaves absent fields at their pre-populated default), and `merge`'s auto-creation of
`~/.dfmt/config.yaml` from `Default()`, which persists the three-pattern list to disk permanently.

**Fix:** derive `Default().Capture.FS.Ignore` from the same source as the YAML template; add a test asserting
both contain `.dfmt/**` and are set-equal.

### CAP-6 · Ignore knowledge lives in three places with three shapes
`config/defaults.go:23-51` (22 glob patterns), `config/config.go:172` (3), `sandbox/walkskip.go:44-72`
(~35 basenames). **P1 · M**

CLAUDE.md admits two; there are three. The divergence is live: `walkskip` prunes `.gradle`, `.terraform`,
`.mypy_cache`, `vendor`, `target`, `.tox` that the watcher happily indexes, while the watcher ignores
`*.log`/`*.swp` that the walker greps.

**Assessment:** unify the *data*, not the matchers — the matchers are legitimately different (O(1) basename
pruning inside `WalkDir` vs user-configurable globs). One `internal/ignoreset` package exposing
`DefaultDirNames()` and a derived `DefaultGlobs()`, consumed by all three, with a parity test. CAP-5 then
becomes structurally impossible.

### CAP-7 · `dfmt install-hooks` overwrites existing git hooks with no backup and no manifest row **[verified]**
`internal/cli/setup.go:96-111`. **P0 · M**

`os.WriteFile(dst, content, 0755)` unconditionally. A user with husky, lefthook or a hand-rolled `post-commit`
loses it silently and permanently. Nothing is recorded in the setup manifest, so uninstall leaves three dfmt
hooks firing after the binary is gone. Three further defects in the same 25 lines: `.git` may be a *file*
(worktrees, submodules), so `MkdirAll` fails with a raw ENOTDIR; `core.hooksPath` is never consulted, so on a
repo that redirects hooks dfmt writes where git will never look and reports success; and there is no
idempotency marker distinguishing dfmt's hook from the user's.

**Fix:** resolve via `git rev-parse --git-path hooks`; back up or refuse when the target lacks the dfmt marker;
record the manifest row **before** writing, as `writeMCPConfig` already does.

### CAP-8 · Git-hook and shell-integration defects
**P1 · S each**

* **`post-checkout` records the wrong ref and the wrong flag** (`hooks/git-post-checkout.sh:5-16`): git passes
  `$1` = previous HEAD, `$2` = new HEAD, `$3` = branch flag; the script reads `$1` and asks
  `show-ref --verify refs/heads/<sha>`, which is essentially never true — so every checkout journals the
  *origin* commit with `is_branch=false`. `$3` gives the right answer for free. The encoding is also
  inconsistent across producers: the hook emits `"true"`/`"false"`, `capture/git.go:47-52` emits `"0"`/`"1"`,
  and both feed one consumer field.
* **`pre-push` uses `symbolic-ref HEAD`** instead of the refs git supplies on stdin, so
  `git push origin feature:feature` from another branch records the wrong branch.
* **The bash prompt hook clobbers `PROMPT_COMMAND`** (`hooks/bash.sh:7`), destroying starship/direnv/oh-my-bash
  wiring; and both bash and zsh fire on **every prompt** rather than on directory change, so 200 commands
  produce 200 `env.cwd` events and 200 round-trips. The fish hook (`--on-variable PWD`) gets this right.
* **`dfmt shell-init` output is not evallable** (`cli/setup.go:46-59`): it prints comments plus
  `source /dev/stdin <<'EOF'`, and under the documented `eval $(...)` usage `source /dev/stdin` reads the
  *terminal* — so the shell hangs or swallows the next typed line. There is also no PowerShell target.

### CAP-9 · Flood protection is drop-only and the drop counter is never read
`internal/capture/fswatch.go:75`, `:401-407`. **P1 · S**

`DroppedEvents()` has zero non-test callers: not in `dfmt stats`, not in `doctor`, not logged. The one
designed-in flood signal is invisible. There is no coalescing either — a `go build ./...` into a non-ignored
directory produces one journal append plus one index add per distinct path, with redaction, and no rate limit.
Compounding it, `IN_Q_OVERFLOW` is masked out of the Linux event handler, so the kernel's own "you lost
events" signal is dropped (the docs claim it is reported).

**Fix:** surface `DroppedEvents()` in stats and doctor; add a token bucket that emits one synthetic
`fs.flood` event with a suppressed count instead of N events; handle `IN_Q_OVERFLOW`.

### CAP-10 · `Stop()` writes files into the user's source tree, and can block forever
`internal/capture/fswatch.go:209-216`, `:221`. **P1 · M**

To wake parked reads, `Stop` writes `.dfmt_stop_<pid>` into **every** watched directory and removes it. It is
genuinely necessary only on Linux but runs everywhere, so on Windows it churns mtimes and trips the polling
watchers. A crash between write and remove leaves litter in arbitrary project directories that only
`emitEvent` filters — `git status` does not. On a read-only directory the write fails silently and
`watchWG.Wait()` then blocks **forever**; `Stop` takes a context and never uses it (as does `Start`).

**Fix:** use the standard self-pipe/`eventfd` wake with `unix.Poll`; bound the wait on the context.

## IV.2 — Setup: config writers

### SET-1 · Nine agents, one filename, one schema — several targets are almost certainly wrong
`internal/cli/setup.go:970-1040`. **P1 · M** *(verify each against upstream before implementing)*

Eight agents are configured by writing `mcp.json` into a per-agent directory. Cursor matches; the others do not
match their documented config locations or key names (Codex uses `config.toml` with `[mcp_servers.*]`; Gemini
uses `settings.json`; VS Code uses a profile-scoped `mcp.json` with a `servers` key; Zed uses
`context_servers`; Continue uses `config.yaml`; Windsurf uses `~/.codeium/windsurf/mcp_config.json`; OpenCode
uses `opencode.json`).

The failure is silent **and self-confirming**: `dfmt setup --verify` only stats the paths dfmt itself wrote, so
it prints a check mark for files no agent ever reads.

**Fix:** one `agentTarget` table (path, format kind, key), one writer per format kind, and a `--verify` that
re-reads the file and asserts the dfmt entry is present under the expected key. Also collapses ~70 lines of
duplicated boilerplate.

### SET-2 · `~/.claude.json` is fully parsed and rewritten with no lock and no size bound
`internal/setup/claude.go:97-271`. **P1 · M**

The file is Claude Code's live session state — routinely multi-MB, containing project history. dfmt does
read → `map[string]any` → mutate → `MarshalIndent` → temp+rename. Three consequences: any write Claude Code
makes between our read and our rename is **destroyed** (atomic rename prevents torn files, not lost updates,
and the `.dfmt.bak` is taken only on the first patch); `map[string]any` re-formats every number through
`float64` and re-sorts keys, so a surgical four-key patch produces a whole-file diff; and the read is
unbounded while the far smaller *manifest* is carefully capped at 256 KiB.

**Fix:** decode untouched top-level keys as `json.RawMessage` so they are copied byte-for-byte; take an
advisory lock around read→rename using the existing flock helpers; re-stat mtime and size immediately before
rename and retry on change; cap the read.

### SET-3 · Four hand-rolled atomic writes bypass `safefs` — including the ones that decide what binary an agent launches
`claude.go:246-268`, `:403-422`, `projectinit.go:204-226`, `:355-375`. **P1 · M**

CLAUDE.md requires `safefs.WriteFile`/`WriteFileAtomic` for writes under project-managed paths.
`mcpmerge.go:74` and `legacy.go:248` comply; these four do not, so the F-07 symlink-plant that `safefs` exists
to stop is open on `~/.claude/settings.json` and `<proj>/.claude/settings.json`.

**Fix:** replace all four bodies with `safefs.WriteFileAtomic` — a net deletion of ~70 lines.

### SET-4 · Uninstall leaves live hooks behind
`AddFile` is called from three sites; four categories of written state are untracked. **P1 · M**

| Written by | Path | On uninstall |
|---|---|---|
| `WriteClaudeCodeSettingsHook` | `~/.claude/settings.json` PreToolUse hook | **left behind** — Claude Code keeps invoking `dfmt hook claude-code pretooluse` after dfmt is deleted |
| `writeProjectClaudeSettings` | `<proj>/.claude/settings.json` (3 hooks + 11 permissions) | left behind |
| `runInstallHooks` | `.git/hooks/*` | left behind |
| `EnsureProjectInitialized` | `<proj>/.dfmt/` | left behind |

**Fix:** add a `FileKindJSONStrip` whose teardown calls a registered per-file stripper, and record all four at
write time using the existing manifest-first ordering.

### SET-5 · Smaller setup defects
**P1–P3 · S each**

* **`WriteClaudeCodeSettingsHook` returns a nonsense error on an empty `settings.json`**
  (`projectinit.go:335-341`): the `else if !os.IsNotExist(err)` branch evaluates with `err == nil`, producing
  `read …: %!w(<nil>)`. The hook is never installed and setup still reports success. The sibling reader 60
  lines up handles the same case correctly — make them one helper.
* **Manifest read-modify-write has no cross-process lock**, so two concurrent `dfmt setup` runs silently drop
  each other's rows — and a dropped row is a file uninstall will never clean.
* **JSONC configs fail hard**: VS Code and Zed settings are JSONC by convention and Cursor users comment their
  `mcp.json`; `encoding/json` rejects both with `invalid character '/'` and the agent is skipped.
* **Hook idempotency is keyed on exact string equality**, so any historical spelling appends a second hook
  group and Claude Code runs both — which is precisely what `legacy.go` exists to clean up afterwards. Key on
  the existing `dfmtHookCmdPattern` instead.
* **WSL path conversion hardcodes `C:\Users\<user>`** from `/home/<user>`, though the WSL and Windows
  usernames frequently differ and the profile drive is not always `C:`. Prefer `wslpath -w`.
* **Mode inconsistency**: `MkdirAll` uses `0o700` in five places and `0o755` in three.

## IV.3 — `internal/safefs`

### SFS-1 · The Windows write path does not provide the guarantee the package doc claims
`safefs.go:10-12` vs `safefs_windows.go:59-69`, `:87-100`. **P1 · S**

The package doc says `WriteFile` uses `FILE_FLAG_OPEN_REPARSE_POINT` on Windows to refuse a racing symlink at
open time. The Windows file itself states that the flag only changes *what the open returns* and cannot make
the open fail. `OpenReadNoFollow` compensates with an explicit `Lstat`-and-reject; `WriteFile` does not. The
read tier is hardened and the write tier is not, on the platform this project is primarily developed on.

**Fix:** mirror the read tier — `Lstat` the leaf immediately before opening and reject symlinks — and correct
the package doc to describe check-then-open rather than kernel enforcement.

### SFS-2 · Junctions and hardlinks are outside the threat model, silently
`safefs.go:150`. **P1 · M**

`CheckNoSymlinks` tests `ModeSymlink` only. On Windows, NTFS **junctions** (creatable without admin) are the
redirect an attacker actually has, and Go's `Lstat` reporting for mount points has shifted across releases
(`ModeIrregular` in recent Go), so a junction may pass. On Unix, **hardlinks** are not symlinks: an attacker
who can create the target as a hardlink defeats `WriteFile`'s in-place `O_TRUNC` write entirely —
`WriteFileAtomic`'s rename is immune, which is another argument for SET-3.

**Fix:** reject `ModeIrregular` and check `FILE_ATTRIBUTE_REPARSE_POINT` on Windows; `Fstat` the fd and reject
`st_nlink > 1` on Unix; document both in the threat model.

### SFS-3 · Atomic writes never fsync the parent directory, and the two writers disagree about permissions
`safefs.go:239-251`, `safefs_unix.go:44`. **P1 · S**

`os.Rename` is atomic but not durable without a directory fsync — after a crash the file can be absent despite
a nil return, which for `setup-manifest.json` means an uninstall that cannot find what to remove. Separately,
`WriteFile` masks `mode &= 0o700` (documented as V-19) while `WriteFileAtomic` applies the caller's mode
verbatim, and two live call sites pass `0o644` — so the same nominal API yields 0600 through one door and
0644 through the other.

**Fix:** fsync the parent directory on non-Windows; pick one permission policy and enforce it in both writers.

### SFS-4 · Residual TOCTOU windows are real; the doc implies none remain
`safefs.go:142-146`, `:222`; `setup.go:483-510`. **P1 · M**

Three concrete windows: `CheckNoSymlinks` returns nil at the first *missing* component and the caller then
`MkdirAll`s the rest, so a planted symlinked intermediate directory redirects `os.CreateTemp` and the rename
follows it; intermediate components can be swapped between check and open, since `O_NOFOLLOW` protects only
the leaf; and `BackupFile` resolves the same path three separate times.

**Fix:** on Linux, `openat2` with `RESOLVE_NO_SYMLINKS|RESOLVE_BENEATH` gives one atomic resolution;
portably, walk with `Openat`+`O_NOFOLLOW|O_DIRECTORY` keeping fds. **Minimum:** state in the package doc which
windows are accepted and why.

### SFS-5 · No rename retry on Windows; orphaned temp files after a crash
`safefs.go:251`, `:222`. **P2 · S**

Rename over an existing file fails with `ERROR_ACCESS_DENIED`/`ERROR_SHARING_VIOLATION` when any process
(antivirus, an editor, a running agent) holds the target open — and this is the code path that writes agent
configs *while agents are running*. Add 5 retries with 20 ms backoff. Separately, `.safefs-*` temp files are
cleaned only on error paths, so a kill leaves them in `~/.claude/`, `~/.cursor/` and `.dfmt/` forever; sweep
them at daemon start.

## IV.4 — `cmd/`, build, CI

### BLD-1 · CI is disabled; releases publish untested binaries **[verified]** ✅ FIXED `a728dbc`
`.github/workflows/ci.yml.disabled`; the only live workflow is `release.yml`. **P0 · S**

Nothing runs on push or pull request: no build, no test, no vet, no lint, no race, no coverage gate. `release.yml`
cross-compiles and publishes on tag **without running a single test first**. The disabled file is
well-constructed (3 OS × 2 Go versions, SHA-pinned actions, pinned linter, informational coverage gate), which
suggests it was parked because lint and tests were red — a self-reinforcing loop.

**Fix:** rename it back today with `lint: continue-on-error: true` so the build/test signal returns
immediately; fix the lint backlog (BLD-3); then make lint blocking. Add a test job dependency to `release.yml`.

### BLD-2 · An unstamped build reports a specific, wrong version — and defeats the stale-daemon check ✅ FIXED `b38d8fe`
`internal/version/version.go`. **P1 · S**

`var Current = "v0.7.3"` while `git describe` says `v0.7.3-7-g855c0b1`. A `go install …@main` today produces a
binary that confidently claims to be the v0.7.3 release. This is not cosmetic: `daemon.SameVersion` is what
decides whether a stale daemon is restarted after a rebuild, so two different unstamped binaries compare equal
and the safety net never fires **during local development, which is exactly when it matters**. (The fingerprint
check still catches the same-path rebuild case — see the daemon section.)

**Fix:** default to `"dev"` and fill from `debug.ReadBuildInfo()` (`vcs.revision` + `vcs.modified`) when the
ldflag is absent.

### BLD-3 · `make lint` is red: 67 issues **[verified]** ✅ FIXED `a728dbc`
**P1 · M**

45 staticcheck, 12 `lll`, 9 misspell, 1 goconst. Non-test violations: `config/config.go:209` (goconst),
`cli/reconnect.go:154-214` (12 long lines), and three British spellings against the configured US locale
(`behaviour` in `reconnect.go:39`, `honours` in `client.go:851`, `favours` in `handlers_search.go:153`). The 45
staticcheck hits are `SA5011` nil-deref-after-nil-check patterns in tests — each is an `if x == nil` that calls
`Errorf` or omits a `return`, i.e. a genuine test bug that panics on failure instead of reporting.

The config also disables the defaults (`default: none`), so `unused`, `unparam` (which would have caught the
dead `dfmtBin` parameter) and `gosec` never run — there is a `//nolint:gosec` in `transport/metrics.go:491`
referencing a linter that is not enabled. `gocyclo` is set to 65, more than twice the conventional threshold,
with a comment naming the four functions it accommodates; four targeted `//nolint` directives at a threshold of
30 would keep the exception visible. `dupl` settings are configured but `dupl` is not enabled.

### BLD-4 · Makefile produces an unexecutable artifact on Windows and drifts from the release workflow
`Makefile:13-16`. **P1 · S**

`go build -o dist/dfmt` on Windows writes a file with no `.exe`, which Windows will not execute by name;
`install:` copies it with the same problem. `release:` omits the `-trimpath` and `-s -w` that `release.yml`
uses and produces no checksums, so a locally-cut release is not comparable to a CI one. `test:` is a bare
`go test ./...` while the documented expectation includes `-race`.

**Fix:** derive an `EXT` from `go env GOOS`; align flags with the workflow (or have both call one script); add
`vet`, `race` and `cover` targets.

### BLD-5 · `cmd/dfmt-bench` is Unix-only and un-versioned
`cmd/dfmt-bench/main.go:236`, `:249`. **P2 · S**

It benchmarks `ls -la /tmp`, which on Windows exits non-zero — so the "benchmark" measures failure latency;
`_ = bytes.Buffer{}` exists only to keep an import alive; every `sb.Exec` error is discarded; there is no
warmup or iteration scaling. It is not built by `release.yml`.

**Fix:** portable commands per GOOS, fail loudly on non-zero exit, or delete it in favour of `go test -bench`
(`sandbox/bench_test.go` and `core/index_bench_test.go` already exist).

**No action needed on `cmd/dfmt/main.go`** — 104 lines, one responsibility, crash log through
`safefs.WriteFileAtomic`, defer scope deliberately narrowed.

### BLD-6 · Dependency policy holds **[verified]**
`go.mod` declares exactly `golang.org/x/sys v0.43.0` and `gopkg.in/yaml.v3 v3.0.1`, with no indirect entries.
`x/sys` appears in five non-test files, `yaml.v3` in three, and nothing in `cmd/` imports either directly. The
`gopkg.in/check.v1` line in `go.sum` is yaml.v3's test-only dependency and is not in any build. **No action.**

### BLD-7 · Repo hygiene
**P3 · S**

Two different copies of the same dev script (`dev.sh` at the root and `scripts/dev.sh`), only `dev.ps1` being
referenced from CONTRIBUTING; unexplained `hook.sh` and `hw.sh` at the root; `.gitignore` contains a `.dmft/`
typo; `internal/core/NUL…/test.journal` is git-tracked; an untracked 6.4 MB `core.test.exe`. Test file naming
has drifted into three conventions (`*_units_test.go`, `*_extra_test.go`, `uncovered_*_test.go` are
coverage-driven names that tell a reader nothing), and `internal/cli/cli_test.go` is **4,730 lines** — larger
than any production file in the repo.

**Notable positive:** a repo-wide grep finds **zero** real TODO/FIXME/HACK/XXX markers (two hits, both the
literal string inside an MCP tool description). `go vet` and `gofmt -l` are clean. The debt in this codebase is
not in comment markers; it is in doc-to-behaviour drift and the disabled CI.

## IV.5 — Documentation drift

Spot-checks of documented claims against the code:

| Claim | Reality | Verdict |
|---|---|---|
| CLAUDE.md: MCP proxy worker pool is 16 | `cli/mcp.go:124` `mcpMaxConcurrentCalls = 16` | ✅ |
| CLAUDE.md: async semaphore 2, `execSem` 4 | `handlers.go:215-219`, `exec_jobs.go:64` | ✅ |
| CLAUDE.md: coverage thresholds daemon ≥75 %, cli ≥70 % | `scripts/coverage-gate.go:44-49` gates daemon at **80**, cli at **75** | ❌ **drift** — a contributor hitting the documented number fails the gate |
| `capture/capture.go:4` + ARCHITECTURE.md ×3: Windows uses `ReadDirectoryChangesW`, "heavily optimized because it fires for every metadata blip" | `fswatch_windows.go` is a 2-second `filepath.Walk` poller; no Windows API is called anywhere | ❌ **drift, load-bearing** — the doc explains an optimization for code that does not exist |
| ARCHITECTURE.md: `IN_Q_OVERFLOW` is reported as a separate event | The Linux mask omits it; overflow falls through and is skipped | ❌ drift — the one "you lost events" signal is dropped |
| CLAUDE.md: setup "tracks every change for clean uninstall" | Four categories untracked (SET-4) | ❌ drift |
| CLAUDE.md: use `safefs` for any new write site | 4 of 6 setup write sites hand-roll it (SET-3) | ❌ drift |
| `safefs.go:10-12`: the Windows flag "closes the residual TOCTOU window" | The Windows file says the flag cannot refuse an open; no compensating check on the write path (SFS-1) | ❌ **drift within one package, on the security claim** |
| ARCHITECTURE.md: shell integration usage is `eval $(...)` | The emitted heredoc hangs under `eval` (CAP-8) | ❌ drift |
| ARCHITECTURE.md: post-checkout `$1` quirk, dead `dfmtBin`, `PROMPT_COMMAND` clobber | All three accurately describe the code | ✅ accurate, and unusually candid — but documenting a defect is not fixing it |

`ARCHITECTURE.md` is 167 KB and describes internals at line-number granularity. That is itself a liability:
the four drifted items above are all implementation details no test enforces. **Extract the checkable
tables — semaphore sizes, thresholds, agent config paths — into markdown generated from the Go constants, so
drift becomes a build failure rather than an audit finding.**

## IV.6 — Strengths

1. **Dependency discipline is real, not aspirational** — two modules, zero indirect build deps, HTML parsing
   and BM25 and Porter stemming and JSON-RPC all in-tree, with the policy stated and ADR-gated. Verified; it
   holds exactly.
2. **Comment-as-postmortem culture extends here too** — the nanosecond unit mismatch in the cleanup ticker, the
   off-by-one that produced `C:c\Users\foo`, why `path.Match` and not `filepath.Match` on Windows, Claude
   Code's case-variant `projects` keys.
3. **Manifest-first ordering in setup** — the uninstall row is recorded *before* the file is written, with the
   reasoning spelled out: a stale row pointing at nothing is harmless, an untracked file is not.
4. **Trust-flag restoration is genuinely thoughtful** — `claude_trust.go` distinguishes "flag absent" from
   "flag false", makes capture first-write-wins so a re-patch cannot record the flipped state as the prior, and
   deletes the sidecar when it empties. Most uninstallers would blindly clear the flags.
5. **Defence in depth where the language does not help** — the inotify parser bounds-checks header and name
   slice before the `unsafe.Pointer` cast even though the kernel guarantees whole events; the manifest reader
   uses a `LimitReader` with a deliberate over-read to detect truncation; stdin is capped at 1 MiB; the
   manifest decoder both disallows unknown fields and rejects trailing values.

---

# Part V — `internal/cli`, `internal/daemon`, `internal/client`, `internal/project`

Process lifecycle, the singleton protocol, the RPC client, and the CLI surface. ~5,600 production LOC.
`internal/cli/dispatch.go` is the most-changed file in the repository (104 commits), and `cli/daemon.go` is the
second-largest source file (1,294 lines) — churn and size intersect here more than anywhere else.

The problems in this area are overwhelmingly **structural rather than local**: the same logic exists in two or
three copies with divergent semantics (PID readers, port-file readers, process-liveness checks, spawn paths),
and the daemon-singleton protocol is enforced by *file-existence checks* rather than by the lock itself.

## V.1 — The singleton protocol: four defects that are one design fix

These four should be fixed together. Individually each looks like an edge case; together they are the reason a
spawn race can leave the host with two daemons, or with none.

### LIF-1 · `cleanupStaleGlobalDaemon` deletes `~/.dfmt/lock`, which breaks the flock invariant
`internal/cli/daemon.go:625-635`. **P0 · M**

On Unix, `flock` binds to the **inode**, not the path. Deleting the lock file while a process holds it does not
release the lock — it makes the lock *invisible*: the next daemon `open(O_CREAT)`s a fresh inode at the same
path and acquires an exclusive lock on it successfully. Two daemons then both believe they are the singleton.

The window is reachable. `inspectGlobalDaemon` classifies `Dead` whenever there is no listener and no live PID
— which is exactly the state of a daemon between `AcquireGlobalLock` and the listener bind plus PID write. A
CLI running in that window wipes the lock and the socket, then spawns a sibling.

On Windows this accidentally does not bite: deleting the lock while `LockFileEx` holds it fails with a sharing
violation, and the error is discarded. Same code, opposite safety.

**Fix:** never delete the lock file. Remove only `port`, `daemon.pid`, `daemon.json` and (on Unix) the socket,
and only after proving the lock is free by acquiring it.

### LIF-2 · Nothing interlocks the classify→spawn sequence
`internal/cli/daemon.go:657-712`. **P0 · M**

Two CLI processes — very common, since an agent fires three MCP tools while the user types `dfmt status` — can
both read `Dead`, both clean up, and both spawn. The losing child is *supposed* to fail on the lock, but given
LIF-1 and LIF-3 it may instead win the socket and lose the lock, or the reverse.

**Fix:** a separate non-blocking spawn lock (`~/.dfmt/spawn.lock`). If it cannot be acquired, another CLI is
already spawning — poll `DaemonRunning` for the readiness timeout and return. Roughly 15 lines, and it closes
LIF-1 and LIF-2 together.

### LIF-3 · The listener is bound before the singleton lock is acquired
`internal/daemon/daemon.go:312` (in `NewGlobal`) vs `:499` (in `Start`). **P0 · M**

`transport.ListenUnixSocket` runs in the constructor; `AcquireGlobalLock` runs in `Start`. So the loser of the
lock race has **already bound the socket**, and the winner then fails at `NewGlobal` with "address already in
use" — leaving **no daemon at all**, reported to the user as the generic "did not become ready within 4s". The
TCP path has the same inversion.

**Fix:** the lock must strictly precede any bind. Acquire it as the first act of `NewGlobal`, or move listener
construction into `Start` after the lock.

### LIF-4 · A hung daemon with a missing PID file is classified `Dead`
`internal/cli/daemon.go:532-551`. **P1 · S**

If `daemon.pid` was deleted — by a racing CLI's cleanup, or by a user following the manual-recovery
instructions half-way — a hung daemon classifies as `Dead` rather than `Stuck`. Instead of the pointed
"PID N is hung, kill it" message, the CLI wipes state and spawns a sibling that immediately dies on the lock.

**Fix:** when there is no listener, probe the lock. If acquiring it fails, someone alive holds it — that is
`Stuck`, regardless of the PID file.

## V.2 — Other P0/P1 lifecycle findings

### LIF-5 · Every `Client` RPC accepts a context and drops it
`internal/client/client.go:861-911` — `http.NewRequest`, not `NewRequestWithContext`. **P0 · S**

Every `Client.Exec/Read/…/Stats` takes a `ctx` and silently ignores it. The consequences are all user-visible:

* `cli/mcp.go:80-95` threads a cancellable context into `Handle` specifically so Ctrl-C aborts a long tool
  call — **inert**. The proxy then waits on in-flight calls that cannot be cancelled, so SIGINT during a
  ten-minute exec hangs the process.
* MCP `notifications/cancelled` can never abort daemon-side work.
* The `context.WithTimeout` in every CLI tool command is decorative; the real deadline is the HTTP client
  timeout.

**Fix:** `http.NewRequestWithContext(ctx, …)` and derive the client timeout as `min(ctx deadline, timeout)`.
A one-line change with a large behavioural win.

### LIF-6 · `dfmt stop` force-kills an unverified PID
`internal/cli/daemon.go:1068-1104`. **P1 · S**

The PID is read from the file and passed straight to `taskkill /PID N /T` — no liveness check, no cross-check
against the identity file's PID or exe path. After a SIGKILLed daemon leaves a stale `daemon.pid`, PID reuse
(routine on Windows, and on Linux with a low `pid_max`) means `dfmt stop` **force-kills an unrelated process
and its entire tree**.

**Fix:** verify liveness first; prefer `ReadIdentity().PID`, written by the live process, and check that `Exe`
looks like dfmt before signalling.

### LIF-7 · On a socket-only Unix daemon, `dfmt stop` reports success without stopping anything
`internal/cli/daemon.go:1017`. **P1 (Unix, non-default config) · S**

`runStop` gates on `globalDashboardURL() != ""`, which reads `~/.dfmt/port` — a file a socket-only global
daemon never writes. So with `transport.http.enabled: false`: the global stop is skipped entirely; the legacy
per-project path finds `DaemonRunning` true via the global socket probe; there is no per-project PID so nothing
is signalled; the CLI deletes `.dfmt/{daemon.pid,lock,daemon.sock}` and prints **"Daemon stopped"**. The daemon
is untouched.

The same port-file gate degrades `dfmt dashboard`, `dfmt status`, and two `doctor` checks. It is masked in the
default config, which enables TCP — which makes the socket branch a rarely-exercised path, i.e. exactly where
bugs live.

**Fix:** gate on `inspectGlobalDaemon()`, which is transport-agnostic, at all five sites.

### LIF-8 · Windows "graceful" stop is a no-op, so the index is never persisted
`internal/cli/daemon.go:1158-1181`. **P0 · M**

The daemon is spawned `DETACHED_PROCESS | CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP` — it has no console and
no window, so `taskkill` without `/F` delivers events it cannot receive. The graceful attempt always times out
(5 s of pure latency on every `dfmt stop`), then `/F` hard-kills. **`Daemon.Stop` never runs on Windows**: the
index is never persisted (full journal replay on next start), the port/socket/identity files are left behind,
and the registry row is never removed.

CLAUDE.md describes this as expected and survivable, and the journal genuinely tolerates it — but "once per
upgrade" understates it: it is **every stop, including every idle-driven restart cycle**.

**Fix:** an authenticated `POST /admin/shutdown` (the token already exists) as the graceful path on every
platform, with signals as the fallback. Alternatively `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, pid)`, which
works precisely because of `CREATE_NEW_PROCESS_GROUP`.

### LIF-9 · The HTTP status code is never checked
`internal/client/client.go:896-910`. **P1 · S**

A 401 from the token guard — reachable whenever the daemon restarted and the caller holds an old token — a
404, or a 500 HTML body is handed straight to `json.Unmarshal`, producing
`unmarshal response: invalid character 'u' …`. The operator has no path from that message to "your port-file
token is stale".

**Fix:** branch on the status: 401/403 → a typed `ErrUnauthorized` naming the remedy, 404 → old daemon, 5xx →
include the first 200 bytes of the body.

### LIF-10 · `DaemonRunning("")` builds *relative* legacy paths
`internal/client/client.go:437-488`, `:173-186`. **P1 · S**

With an empty project path the legacy fallback uses `.dfmt/port` and `.dfmt/daemon.sock` — **relative to the
CWD**. So inside a project with a legacy per-project socket, the "is the *global* daemon up" probe can answer
yes about a per-project daemon, and `dfmt status` then prints global PID/version fields describing a different
process. Worse, `cleanupStaleDaemon("")` can delete the **current project's** PID file during a global probe.

**Fix:** return false for an empty project path before the legacy leg.

### LIF-11 · The reconnect wrapper retries non-idempotent calls
`internal/cli/reconnect.go:126-136`. **P1 · M**

The comment states that retrying "would double every side effect the first attempt already had" — and the code
then retries `Exec`, `Edit`, `Write` and `Remember` whenever the daemon happens to be unreachable. The
dangerous case is concrete: the daemon runs `go build && deploy`, writes the journal event, and is killed
(idle-exit, `dfmt stop`, OOM) before the response flushes. The client sees an error, probes, finds no daemon,
reconnects, and **runs the command again**.

The liveness probe used to make that decision is also a poor proxy: `writePortFile` removes the file before
renaming (`transport/http.go:756`), so a probe landing in that window concludes "dead" and triggers a spurious
retry — and the `os.Remove` is unnecessary, since Go's `os.Rename` already replaces the target on both
platforms.

**Fix:** delete the `os.Remove` (one line). Introduce typed client errors and retry **only** on a proven
pre-delivery dial failure. If broader retry is wanted for write methods, give them an idempotency key the
daemon deduplicates on.

### LIF-12 · `renew()` holds the backend mutex across a 4-second spawn attempt
`internal/cli/reconnect.go:78-91`. **P1 · M**

`ensureGlobalDaemon` may spawn and poll for up to the readiness timeout while `current()` takes the same mutex.
During a reconnect **every** concurrent MCP tool call (up to 16) blocks; and if the daemon cannot start, each
of the 16 queued calls performs its own 4-second renew **serially** — 64 seconds of stall before the agent
receives 16 identical errors.

**Fix:** singleflight the renew and add a short negative-cache window.

### LIF-13 · A long-lived proxy never re-checks the daemon version
`internal/cli/reconnect.go`, with `restartAttempted` at `cli/daemon.go:575`. **P1 · M**

The wrapper reaches the staleness check only when a call *fails*. An MCP session that started before an upgrade
keeps using the outdated daemon indefinitely, because the outdated daemon answers everything fine. Combined
with the per-process `restartAttempted` latch — which never resets in a days-long proxy — a session can be
pinned to stale code for its entire lifetime. The mirror-image failure also exists: short-lived CLI processes
each start with the latch clear, so if the fingerprint never matches (see LIF-14), **every** `dfmt` command
kills and respawns the daemon, at 4 seconds plus a full index reload each time.

**Fix:** re-check on a low-frequency timer inside the wrapper; persist restart bookkeeping in
`~/.dfmt/restart.json` and rate-limit to one restart per minute per version pair, escalating to a loud message
on the second failure.

### LIF-14 · The `mtime:size` fingerprint has both false-negative and false-positive modes
`internal/daemon/identity.go:71-107`. **P1 · M**

*False negative:* any replacement that preserves mtime and size — `cp -p`, archive extraction with `-p`,
`rsync --times`, some installers — compares equal, so the daemon is judged current while serving replaced code.

*False positive:* the daemon fingerprints its own `os.Executable()`; the CLI re-stats the recorded path. If
that path is reachable through a different mount with different timestamp semantics — a network drive, a WSL
`/mnt/c` view versus the Windows path, FAT/exFAT with 2-second granularity on a portable install — the two
values differ and the daemon is judged stale on **every** command. With LIF-13's missing rate limit that is a
restart storm.

**Fix:** fingerprint as `size + hash of the first and last 64 KiB` (~0.2 ms, immune to timestamp semantics);
require two consecutive mismatches before restarting; fall back to version-only when the exe resolves onto a
non-local volume.

**Related (LIF-14b):** `SameVersion`'s dev-build exemption is unreachable, because `version.Current` defaults
to a concrete `"v0.7.3"` rather than empty (see BLD-2). The documented protection — "a contributor's dev build
will not restart everyone's daemon" — does not apply; what actually saves contributors is the fingerprint
check. Fix BLD-2 and this becomes real.

## V.3 — Resource cache

### LIF-15 · The cold load runs under the global write lock
`internal/daemon/projectres.go:232-317`. **P1 · M**

`projectsMu.Lock()` covers `loadProjectResources`, which does `MkdirAll`, a YAML parse, `OpenJournal`,
**a full index decode** (hundreds of milliseconds to seconds on a large repo), policy load, redactor load, and
content-store construction. For that entire window every RPC for **every other project** blocks — including
the hot read path.

**Fix:** store a `cacheEntry{once sync.Once; res; err}`; insert under the lock, release, then `once.Do(load)`.
The map lock is then held for microseconds and concurrent first-time loads of the same project still coalesce.

### LIF-16 · LRU eviction has no lease, so a bundle in active use can be torn down
`internal/daemon/projectres.go:252-296`. **P1 · M**

The victim is chosen by an activity timestamp stamped at *resolve* time, not held for the request's duration. A
long `dfmt_exec` (up to 900 s) resolves its bundle, nine other projects get touched, and the evictor picks the
exec's bundle — closing the journal handle the in-flight handler is about to append its completion event to.
Symptom: "file already closed" on the *result* write, after the command already ran.

**Fix:** refcount the bundle around each handler and skip refcounted entries when choosing a victim; exceed the
cap temporarily rather than evict live state.

### LIF-17 · `dfmt config set` has no effect on the running daemon, and says nothing
`internal/daemon/projectres.go:394-402`. **P1 · M**

Configuration is snapshotted once at load and nothing watches `config.yaml`. So `dfmt config set
storage.durability durable` prints `ok:` and changes nothing until the daemon restarts. The same applies to
`exec.path_prepend` — the very knob `dfmt doctor` tells users to add; doctor's own hint does say "then restart
the daemon", but `dfmt config set` does not.

**Fix:** stat `config.yaml` in the existing 3-second tail tick and hot-swap the derived knobs; at minimum,
print the restart requirement and have doctor compare on-disk config against the daemon's loaded snapshot.

### LIF-18 · `Resources()` creates `.dfmt/` for any `project_id` it is handed
`internal/daemon/projectres.go:378-381`. **P1 · S**

The daemon `MkdirAll`s `<whatever>/.dfmt` with no check that the path is an initialized project or even a
plausible project root. A mis-rigged agent hook or a typo'd `--project` scatters `.dfmt/` directories across
the filesystem and caches a bundle for each, evicting real ones.

**Fix:** require an existing `.dfmt` or `.git` and return a clear "not initialized: run dfmt init" error.

### LIF-19 · `NeedsRebuild` is never cleared, permanently disabling index persistence for that project
`internal/daemon/projectres.go:420-434`, guards at `:282`, `:744`, `:792`. **P1 · S**

A cached project that loads with `needsRebuild=true` is self-healing within 3 seconds — the first full tail
pass effectively rebuilds it. But the flag is never cleared, so the persist guards skip `PersistIndex`
**forever** for that bundle, and every daemon restart re-replays the entire journal for that project for the
life of the install.

**Fix:** clear the flag once the first full tail pass completes.

### LIF-20 · Idle-exit ignores in-flight work
`internal/daemon/daemon.go:861-918`; `/api/stream` has no activity touch at all. **P1 · S**

Activity is stamped at handler *entry*. A 45-minute async exec or a live SSE tail produces no further signal,
so the daemon idle-exits after 30 minutes and kills the stream mid-flight.

**Related (LIF-20b):** a `time.ParseDuration` failure on `idle_timeout` sets the timeout to 0, which means
"run forever" — contradicting the `defaultIdleTimeout` constant, which is itself dead. A typo'd `30min` turns
a self-limiting daemon into an immortal one, silently.

**Fix:** an in-flight counter held for the life of a stream; fall back to the default on a parse error and warn,
reserving 0/"off" for explicit disabling.

**Worth preserving:** the rest of the idle monitor is well built — ticker plus `lastActivityNs`, self-clamping
the tick to [1s, 1m], deliberately kept out of the wait group to avoid a `Stop` deadlock.

## V.4 — P2: duplication and structure in this area

* **The same primitive is implemented two or three times, with divergent semantics:**

  | Primitive | Copies | Divergence |
  |---|---|---|
  | Port-file reader | `cli/daemon.go:346`, `client/client.go:78` | byte-identical; a format change must land in both |
  | PID reader | `cli/daemon.go:256`, `:1110`, `client/client.go:491` | different trimming; one works only by luck of `Sscanf` |
  | **Process liveness** | `client/process_windows.go:28`, `daemon/process_windows.go:12` | **`ACCESS_DENIED` ⇒ alive in one, dead in the other** |
  | Spawn | `startGlobalDaemonBackground` vs the dead `startDaemonBackground` | the dead one omits the detach flags |

  The liveness split is not cosmetic: `dfmt stop` uses the "dead" variant, so against a daemon running under a
  different token it concludes "gone" and deletes the state files of a process that is still serving. **Extract
  one `osutil.ProcessAlive(pid)` with the client's safer semantics and delete the others.**

* **`dispatch.go`'s switch is not a smell** — 37 single-delegation cases with no logic is the correct shape for
  a router; a map would only add indirection. **Do not refactor it.** The problems are adjacent: `printUsage`
  documents roughly 60 % of the command surface (`daemon`, `remove`, `read`, `fetch`, `glob`, `grep`, `edit`,
  `write`, `hook` are all missing), and `ParseGlobals` mutates the process environment via `os.Setenv` — a
  hidden side effect in a function whose name promises parsing, which leaks into every child and cannot be
  undone in tests. Deriving both dispatch and usage from one table makes the drift impossible.

* **~500 lines of copy-pasted command bodies** in `cli/tools.go` (seven) and `cli/recall.go` (five), all
  following build-FlagSet → parse → arity → getProject → acquireBackend → timeout → call → render. One generic
  `runTool[P,R](spec, args)` driver plus a per-command spec removes ~400 lines and makes the divergences below
  impossible. Two of those divergences are live bugs: `runExec` hard-codes a 60 s client deadline, ignoring both
  the shared tool timeout **and the user's `-timeout` flag**; and the SSE client's 60 s cap kills
  `dfmt tail --follow` after exactly one minute, silently, with exit 0.

* **Dead code, ~150 lines:** `daemon.PromoteInProcess` (no production caller; referenced only by three stale
  comments that describe it as the fallback path), `runDaemonForeground`, `startDaemonBackground`,
  `waitForDaemonShutdown` (**its argument is always nil**, because both backend acquirers return a nil daemon
  by construction — along with 12 dead `defer` blocks plumbing that nil), `defaultIdleTimeout` (declared,
  documented as the fallback, never referenced), and `waitForExit`'s unused `pid` parameter with a comment
  apologising for it.

* **`acquireBackend` and `acquireBackendForLongRunner` are now identical except for an error prefix.** The
  former's "test-binary fallback" calls the latter, which re-runs the same `ensureGlobalDaemon` that just
  failed and prints a **second** error line — so in a test binary every command emits two errors for one
  failure. Collapse to one function with a single return value.

* **Oversized functions that mix gathering with rendering:** `runStatus` (159), `runList` (112 — with
  hand-rolled JSON via `fmt.Printf` that emits a **different schema per platform**), `runDaemon` (105 — a
  second copy of the `ensureGlobalDaemon` state machine with different messages and exit codes), `runStats`
  (125, of which 90 are `Printf`), and `doctor`'s 275 lines where five checks bypass the otherwise-good
  `coreChecks` table and print directly. Splitting each into `gatherX() → renderX(format)` also gives `doctor`
  a `--json` mode for free.

* **`cli/config.go` maintains two parallel 22-case switches** (`get` and `set`) by hand; they have already
  diverged. Adding a config field currently means editing four places.

* **Client RPC bodies repeat the same 25 lines eleven times**, and each round-trips the result through an extra
  marshal/unmarshal hop. One generic `rpc[P,R]` with `json.RawMessage` removes ~300 lines and one
  allocation-heavy hop per call.

## V.5 — P2: UX consistency

* **Exit codes have no convention.** Usage errors return 2 in five commands and 1 in eleven; "no daemon"
  returns 1 from `dashboard` and 0 from `list` and `status`. Publish and enforce
  `0 ok / 1 runtime failure / 2 usage error / 3 degraded`.
* **Four stderr prefix conventions coexist** — `error:`, `warn:`, `[!]`/`[i]`, and `acquireBackend: %v`, which
  leaks an internal function name to the user. One error also prints to **stdout**, breaking `dfmt stats | jq`.
* **`dfmt mcp --help` starts a server and hangs** — `runMCP` discards its arguments entirely. Help handling
  differs three ways across the CLI, and `dfmt task help` prints usage instead of creating a task named "help".
* **`dfmt doctor` has no `--json`** — the one command an agent or CI would parse is the only diagnostic without
  machine-readable output, while `status`, `list` and `stats` all have it. Its five marker vocabularies (`✓`,
  `✗`, `⚠`, `[i]`, `[!]`) also mojibake on a legacy Windows console.
* **One condition, four hand-written recovery paragraphs.** "Daemon unreachable" suggests `dfmt doctor`;
  "stuck" suggests `dfmt stop` or `taskkill`, in two different wordings; "spawn timed out" suggests doctor plus
  `tasklist`; "pid file missing" suggests locating the process manually. One `recoveryHint(status, pid)` helper
  replaces all four.
* **`--json` coverage is arbitrary** — honoured by ten commands, ignored by `recall`, `tail`, `doctor`,
  `dashboard` and `config`. An agent cannot rely on the flag meaning anything.
* **`dfmt list` always prints `uptime 0s`** for the global daemon, because the synthetic row stamps
  `time.Now()` while the identity file's real start time is right there.
* **`dfmt daemon`, `ensureGlobalDaemon` and `doctor` disagree about the `Orphan` state** — success line,
  warning, and failed check respectively, for the same condition.

## V.6 — P2: cross-platform asymmetry

* **The TCP-plus-token versus Unix-socket split is unmanaged.** Everything downstream of "is there a port file"
  degrades on a socket-only daemon: the dashboard URL, `dfmt status`'s dashboard line, **`dfmt stop`**
  (LIF-7), two doctor checks, and `loadedProjectsViaAPI` — which is HTTP-over-TCP only, so `dfmt list` on Unix
  never shows the per-project rows its own comment promises. Introduce one `daemonEndpoint` abstraction
  resolved once, and route `loadedProjectsViaAPI` through `Client`, which already speaks HTTP over the socket.
* **The default config binds a fixed port** (`127.0.0.1:3490`) on both platforms, with **no fallback to an
  ephemeral port** on bind failure. A second OS user, or any unrelated process holding 3490, blocks startup —
  reported as the generic "did not become ready within 4s" with no mention of the port. Retry on `0` and log.
* **The hashed-socket fallback for long home paths is honoured by three call sites and not by the fourth** —
  `cleanupStaleGlobalDaemon` joins the plain name, so on such a host a stale socket is never removed and every
  subsequent start fails permanently with no diagnostic.
* **`signalStopProcess` shells out to `taskkill`** with the exit code discarded — PATH-dependent, and the code
  that distinguishes "no such PID" from "access denied" is thrown away. Use `OpenProcess` + `TerminateProcess`.
* **The V-15 root warning silently no-ops on Windows** (`geteuid()` returns −1), with no equivalent
  elevated-Administrator or SYSTEM check, though the sandbox exposure there is identical.
* **A dependency-policy comment is contradicted by a sibling file** — `detach_windows.go` says it avoids
  importing `x/sys/windows` to keep the policy intact; `flock_windows.go` imports exactly that.

## V.7 — Strengths

1. **`reconnect.go`'s generic `callRetrying` with a `pick` closure** — retry logic in exactly one place across
   twelve methods, re-binding to the *new* connection rather than a captured method value, with a compile-time
   `var _ transport.Backend` guard making interface drift a build error. The `StreamEvents` exemption is
   correctly reasoned.
2. **`Daemon.Stop`'s ordered, documented teardown** — seven phases, wait-group-tracked goroutines, a
   `stopOnce` plus compare-and-swap double guard, and especially the "skip index persist when the async rebuild
   was cancelled" reasoning, which prevents a silent permanent search gap that would be near-impossible to
   diagnose in the field.
3. **`identity.go` as a concept.** Turning "is this daemon running my code?" from an unanswerable question into
   a cheap file read, with a fingerprint that catches same-version rebuilds, is the highest-value design in
   this area. The implementation needs hardening (LIF-14); the idea and its documentation are excellent.
4. **`serveMCPStdio`'s concurrency model** — serial reads, concurrent handling, a single guarded writer,
   bounded worker slots, per-request panic recovery, `initialize` deliberately inline, and a byte-capped reader
   that recovers the message boundary instead of killing the loop. Every one of those is a bug someone hit.
5. **Security defaults are consistently right** — 0700/0600 throughout, `safefs` symlink-refusing writes for
   PID, identity and registry files, umask around the socket bind, an atomic port-file write with the token
   inside it, and a 16 MiB response cap on the client.
6. **`globalDaemonStatus`'s five-way classification** is the right abstraction, shared by both consumers. The
   remaining work is making those consumers agree and making the cleanup it triggers safe.

---

# Part VI — Cross-cutting: tests, coverage, conventions, duplication, ADRs

## VI.1 — Coverage: all four thresholds pass, and the number that matters is not trustworthy

Measured in a single run (`go test ./internal/{core,transport,daemon,cli}/... -cover -count=1`):

| Package | Actual | CLAUDE.md | Verdict | `scripts/coverage-gate.go` | Verdict | Test time |
|---|---|---|---|---|---|---|
| `internal/core` | **90.1 %** | ≥ 90 | ✅ +0.1 | 90 | ✅ +0.1 | 0.9 s |
| `internal/transport` | **87.9 %** | ≥ 85 | ✅ +2.9 | 85 | ✅ +2.9 | 41.9 s |
| `internal/daemon` | **76.4 %** | ≥ 75 | ✅ +1.4 | **80** | ❌ **−3.6** | 3.8 s |
| `internal/cli` | **72.3 %** | ≥ 70 | ✅ +2.3 | **75** | ❌ **−2.7** | 38.6 s |

Three findings follow.

**XC-1 · Two sources of truth disagree (P2 · S).** CLAUDE.md documents 75/70 for daemon and cli; the
executable gate demands 80/75. A contributor who hits the documented number fails the gate. Pick one — the
gate's numbers as the target and CLAUDE.md's as the current floor, documented as such — and have the gate print
both.

**XC-2 · Nothing enforces either (P0 · S).** `scripts/coverage-gate.go` is `//go:build ignore` and is invoked
only from the disabled CI workflow. See XC-3.

**XC-3 · The `cli` number is not evidence of correctness (P0 · L).** `internal/cli/cli_test.go` is 4,730
lines and 299 test functions, and **184 of those 299 cannot fail** — they execute code (so it counts toward
coverage) with no statement capable of failing the test:

```go
// internal/cli/cli_test.go:281
code := Dispatch([]string{"capture", "git", "commit", "abc123"})
if code != 1 {
    t.Logf("capture git commit returned %d (expected 1 with no daemon)", code)
}

// internal/cli/cli_test.go:495
code := Dispatch([]string{"doctor", "-dir", tmpDir})
_ = code // May pass or fail depending on daemon state
```

Repo-wide there are **189 `if … { t.Logf }` sites** — an assertion shape with the failure removed — and **45
`_ = code` discards**. A rewrite of `Dispatch` returning garbage exit codes for every command would leave the
`cli` suite green. Realistic *verified* coverage for `internal/cli` is likely 35–45 %, and that package is
4,700+ lines carrying daemon lifecycle, MCP proxying and installer logic — the least-verified code in the repo
by a wide margin. Same pattern, smaller doses, in `dispatch_ops_test.go` (44 of 95), `dispatch_tools_test.go`
(9 of 34), `client_test.go` (18 of 132) and `socket_test.go` (16 of 53).

**Fix, mechanically, in three passes:** convert every `if x != want { t.Logf }` into `t.Errorf` and fix what
breaks — most were real assertions downgraded during a green-the-build push; for genuinely
environment-dependent outcomes assert the *invariant* instead of the value; delete tests that assert nothing
and cover nothing new. Expect the `cli` number to fall below 70 %, which is the honest one.

## VI.2 — Test suite: the other structural problems

**XC-4 · 47 setup failures are reported as skips (P1 · M).** `internal/client/client_test.go` has **116 skips
against 132 test functions**. Forty-one are legitimate Windows platform guards; **47 are of the form
`t.Skipf("skipping: could not create socket: %v", err)`** — converting a genuine failure into a pass. On the
Windows development host most of that file is inert. A further six are permanently-disabled tests
(`"Auto-start makes this test meaningless"`) that were never deleted, plus three unconditional
`t.Skip("requires running daemon")` in `cli_test.go`.
*Fix:* `t.Fatalf` for setup failures — if a temp dir plus socket cannot be created, that **is** a failure.
Consolidate the 41 platform guards into one `requireUnixSocket(t)` helper. Delete the disabled tests; git
history preserves them.

**XC-5 · `t.Parallel()` appears zero times in 160 test files (P2 · M).** The suite is fully serialized; of the
~85 s total, ~80 s is `transport` and `cli` running one after the other. The blocker is state: **150
`os.Setenv` calls in tests** (against 125 `t.Setenv`), only 54 of which restore, plus direct mutation of the
`flagProject`/`flagJSON` package globals.
*Fix:* convert `os.Setenv` → `t.Setenv` mechanically, then enable parallelism in the leaf packages first —
`core`, `redact`, `sandbox`, `safefs`, `safejson` — where it is nearly free.

**XC-6 · 42 `time.Sleep` synchronization points (P2 · M).** Concentrated in `capture/fswatch_linux_test.go`
(11), `daemon/projectres_test.go` (6), `transport/socket_test.go` (4). The fswatch ones are partly defensible —
inotify has genuine OS latency — but the resource-cache and connection-drain cases test in-process state
transitions where a bounded poll would be faster and deterministic. These are the tests most likely to flake on
a loaded runner, and with CI disabled the flake rate is unmeasured.
*Fix:* promote the existing `waitForJob` helper (`transport/exec_jobs_test.go:226`) into a shared
`waitFor(t, timeout, cond)`.

**XC-7 · Two empty-bodied stub tests remain from a deleted invariant (P3 · S).**
`sandbox/permissions_merge_fuzz_test.go` and `permissions_default_dev_tools_test.go:45` are stubs whose own
comments state the invariant they tested no longer exists — one is kept "so existing callsites don't break",
though no callsite of a test function can exist. These are fossils of XC-14 below. Delete both.

**XC-8 · Benchmarks cover two packages; `-race` runs nowhere (P2 · S).** Nineteen benchmarks, all in `sandbox`
and `core`. Nothing for `redact` (which runs on **every event**) or the `transport` JSON-RPC codec (which runs
on every call) — the two hot paths with no perf regression guard. `-race` is referenced only in the disabled CI
file; `make test` is a bare `go test ./...`. Given the daemon's concurrency, running the suite under `-race` at
least once per release is not optional.
*Fix:* add a `test-race` target and benchmarks for those two paths.

**Weakest test files:** `cli/cli_test.go` (4,730 lines, 184 non-failing functions), `client/client_test.go`
(116 skips / 132 functions), `cli/dispatch_ops_test.go` (44 of 95 assertion-less), the two empty stubs,
`cli/dispatch_tools_test.go` (9 of 34).

**Strongest test files, worth using as templates:** `core/journal_test.go` (1,877 lines, **210 assertions**,
one skip, zero sleeps, named subtests, real files, covers corruption/rotation/cursor recovery);
`transport/handlers_test.go` (336 assertions, **zero skips** on a platform-sensitive package, with a
concurrency-correct mock that documents its parity contract with production); `setup/projectdocs_test.go` (~4
assertions per test, table-driven, exercises idempotent-marker rewriting); `redact/redact_test.go` plus its
fuzz target; `safefs/safefs_test.go` plus two fuzz targets that encode **contracts**
(`AbsPathContract`, `NoFalseNegative`) rather than crash-freedom — and whose `requireSymlinks(t, base)` is the
only skip helper in the repo that probes the actual capability instead of guessing from `GOOS`.

**Not over-mocked — a genuine strength.** Only eight mock types exist across 160 test files; the suite
overwhelmingly uses real temp dirs, real sockets, real journals. Fuzzing is well-placed: ten targets across
glob matching, redaction, HTML conversion, JSON-RPC line reading, reserved names and token approximation.

## VI.3 — Error-handling conventions

This is one of the codebase's stronger areas, and the headline numbers deserve stating: **241 `%w` wraps
against 80 non-wrapping `fmt.Errorf`** (a 3:1 ratio in the right direction), **zero `panic` in library code**,
and **zero error-string matching** anywhere — ADR-0022 explicitly chose a liveness probe over parsing error
strings, and the code honours it. The findings below are localized inconsistencies, not a missing convention.

**XC-9 · A sentinel exists but the equivalent condition does not use it (P1 · S).**
`safefs.go:123` and `:126` return `fmt.Errorf("safefs: path outside baseDir: %s", path)` while `:163` defines
`ErrPathOutsideRoot` and `:200` correctly wraps it for the **same semantic condition** in
`EnsureResolvedUnder`. So `errors.Is(err, safefs.ErrPathOutsideRoot)` is true on the read path and false on the
write path for the same class of containment violation — in a security-boundary package.
*Fix:* wrap the sentinel at all three sites and add a test asserting `errors.Is` holds for both entry points.

**XC-10 · RPC error codes are discarded at 13 identical sites (P1 · S).**
`internal/client/client.go` (lines 278, 309, 340, 543, 581, 610, 641, 672, 703, 734, 765, 796) all do
`fmt.Errorf("rpc error: %s", resp.Error.Message)`, throwing away the numeric code. Callers cannot distinguish
`-32601 method not found` — client/daemon version skew, remedy "restart the daemon" — from `-32603 internal
error`. This is precisely what LIF-11's retry logic and ADR-0022's stale-daemon story need.
*Fix:* one `RPCCallError{Code, Message}` with `Error()` and `Unwrap()`, returned from a shared helper.

**XC-11 · 133 ignored errors, with one concerning cluster (P2 · M).** Most are legitimately best-effort and
the lint config encodes exclusions for them. The concerning subset is the **23 in `internal/cli/daemon.go`** —
the file that handles spawn, stop and PID-file lifecycle, where a dropped error means a stale PID file or an
orphaned process. Audit those individually; each should be handled, logged, or carry a one-line justification.

**XC-12 · Four error styles coexist (P3 · M).** Exported sentinels (`core`, `safefs`, `safejson`, `sandbox` —
used consistently and well), typed errors (`daemon.LockError`, `project.NoProjectError`, `transport.RPCError`,
`ParamsError`), unexported sentinels, and 20+ anonymous inline `errors.New` at the call site — seven of them in
one function (`cli/setup.go`'s `buildCaptureParams`). Write the convention down in CLAUDE.md ("sentinel when
callers branch on it; typed when it carries data; inline only for terminal user-facing messages") before a
fifth style appears.

## VI.4 — Logging and the MCP stdout boundary

**XC-13 · `internal/logging` contains two complete, mutually-unaware logging systems, and the dead one defaults
to stdout (P0 · S).** ✅ FIXED `53bb962`

| File | System | Sink | Consumers |
|---|---|---|---|
| `log.go` (192 L) | custom level-filtered `Debugf/Infof/Warnf/Errorf` | **stderr** | 86 call sites across 19 files |
| `logging.go` (161 L) | `log/slog` — `Init`, `Logger`, `With`, `FromContext`, `MultiWriter` | **stdout** | **zero** |

Verified: no symbol from `logging.go` is referenced outside `internal/logging`. The whole slog subsystem is
dead — plus a 389-line test file for it, plus two dedicated lint exclusions maintaining suppressions for it.

Three reasons this matters more than ordinary dead code. **It defaults to stdout** (`logging.go:51,53`, and
`InitDefault` hardcodes `"stdout"`), while `dfmt mcp` speaks JSON-RPC over stdout — so the day someone reaches
for `logging.With(...)`, which is the natural choice because it looks like the modern Go logger, the MCP
transport corrupts. **It contradicts its own package**: `log.go`'s doc comment explicitly rejects slog
("it adds `time=` and structured attrs that break parsers"), and ADR-0004 and CLAUDE.md list logging frameworks
as prohibited. And it is ~550 lines of code, tests and lint configuration maintained for nothing.

*Fix:* delete `logging.go`, its test file, and the two lint exclusions. Nothing imports it. If structured
logging is wanted later it needs an ADR and a stderr sink.

**XC-14 · Ten daemon-side writes bypass the level filter (P2 · S).** Raw `fmt.Fprintf(os.Stderr, …)` in
`transport/handlers.go` (3), `transport/http.go` (5), `daemon/daemon.go` (1) and `core/journal.go` (the default
`journalWarnf`). `DFMT_LOG=off` — documented as the way to silence daemon chatter in CI dashboards — silences
none of them, and they lack the `warning:`/`error:` prefix the format contract promises parsers. The journal
one is notable: the swappable-function indirection exists so `core` need not import `logging`, but `core`
already imports it, so the indirection buys nothing.

**XC-15 · The MCP stdout boundary is currently clean — verified.** Tracing the `dfmt mcp` path: `runMCP`
writes to stdout only through `serveMCPStdio`; its two diagnostics go to stderr; `acquireBackendForLongRunner`
and `ensureGlobalDaemon` write only to stderr and returned errors; `setup.EnsureProjectInitialized`, called on
that path, has **zero** print calls. All 331 stdout writes live in interactive CLI commands, `cmd/`, and
`scripts/`. This is easy to get wrong and it was gotten right — the only landmine in a daemon-reachable package
is XC-13.
*Recommended guard (P3 · S):* a regression test that runs `dfmt mcp` with a canned initialize request and
asserts stdout contains **only** valid JSON-RPC frames.

**Observability is a strength:** an in-tree Prometheus emitter per ADR-0016, per-tool counters, duration
histograms per ADR-0018 with a documented append-only bucket-migration contract, a `trackedTools` parity guard,
and 790 lines of metrics tests.

## VI.5 — The duplication inventory

Three prior de-duplication passes in this codebase **succeeded and are documented as such** — `internal/osutil`
(whose package doc names the four functions it absorbed), `internal/timeouts`, `sandbox.ApproxTokens` (one
implementation, fuzz-tested), and `detectBinary` (one definition, two call sites, explicitly noted). What
follows is the unfinished part of a programme that has already worked four times.

| # | Concept | Locations | Assessment | Sev · Eff |
|---|---|---|---|---|
| XC-16 | **`**` glob matching — three implementations** | `sandbox/glob.go` (regex + LRU cache), `sandbox/globexpand.go` (doublestar walker), `capture/fswatch.go:273` (hand-rolled over `path.Match`) | Three different `**` semantics in one binary. `fswatch.go:275` documents a Windows bug it fixed by switching `filepath.Match`→`path.Match`; `glob.go:17` documents the **same class** of bug fixed a *different* way. Neither fix is visible from the other file. **Two have fuzz targets — testing that two different implementations are each self-consistent, not that they agree.** | **P1 · L** |
| XC-17 | Plain `filepath.Match` where `**` is expected | `core/classifier.go:149` (matched against `filepath.Base`, so a user rule `src/**/*.go` **silently matches nothing** — and this accepts user-authored config), `sandbox/glob_grep.go:353` | The exact latent bug class `fswatch.go` documents having already been bitten by | **P1 · S** |
| XC-18 | **Ignore/skip lists — four sources** | `sandbox/walkskip.go` (35 basenames), `config/defaults.go` (22 globs), `config/config.go:172` (**3 globs**), `ARCHITECTURE.md` §13.2 (7, matching neither) | This is CAP-5, the live self-loop bug, seen from the duplication side. `walkskip.go:40` even says the watcher list is "the same knowledge, which existed but had never been wired to the walkers" | **P0 · M** |
| XC-19 | **`.dfmt` path construction — 31 hand-joined sites across 12 files** | `cli` (7), `client` (6), `config` (4), `daemon` (5), `transport` (3), `setup` (2), `redact`, `sandbox` | **CLAUDE.md explicitly forbids this** ("never hand-join these paths"), and `project.DaemonDir()` plus five filename constants exist with **zero cross-package consumers**. See XC-20 — one of the 31 is a live bug | **P1 · M** |
| XC-20 | **`$DFMT_GLOBAL_DIR` ignored in three transport handlers** | `transport/http.go:993`, `:1089`, `:1135` — each joins `home/.dfmt/daemons.json` directly, under the comment *"Read registry file directly (avoid circular import)"* | **The cited cycle does not exist**: `internal/project` imports only `internal/osutil`; `transport` can import `project` today. So `/api/daemons`, `/api/all-daemons` and `/api/proxy` read a **different registry** than the client writes whenever the override is set, breaking sandboxed test runs and non-standard installs — and the false comment is *why* three call sites diverged from the canonical helper | **P1 · S** |
| XC-21 | Path→slash normalization, three strategies | `safefs.go:76` (`ToSlash` then `ReplaceAll`), `sandbox/glob.go:17` (`ReplaceAll` only, explicitly rejecting `ToSlash`), `capture/fswatch.go:238` (`ToSlash` only) | Three defensible-in-isolation choices, no shared helper, no cross-reference. Belongs in `osutil` beside `SamePath` | **P2 · S** |
| XC-22 | Test helpers re-derived per package | `setHome` ×2, `tmpDir`, `withIsolatedGlobalDir`, `writeJSON`, plus the HOME/`USERPROFILE`/`DFMT_GLOBAL_DIR` isolation dance in `cli` (41×), `daemon` (17×), `project` (16×), `transport` (14×) | No `internal/testutil`. Also the direct blocker to XC-5 | **P2 · M** |
| XC-23 | `func itoa` | `sandbox/walkskip.go:217` plus three test copies | Four re-implementations of `strconv.Itoa`; the three in tests avoid nothing | **P3 · S** |

## VI.6 — Package structure

**XC-24 · `internal/daemon` imports `internal/client` (P2 · M).** The **server** imports the **client**
package to reach the daemon registry (`daemon.go:931-939`), which is shared server/client state living in
`client` for historical reasons. As a result `internal/daemon` transitively pulls in `internal/setup`, which a
daemon has no business depending on.
*Fix:* move `Registry`/`DaemonEntry`/`GetRegistry` into `internal/project`, which already owns global-state
paths and is a leaf. Both `client` and `daemon` then depend downward, and XC-20 resolves naturally because
`transport` can use the same package.

**XC-25 · `Handlers` requires eight post-construction setters (P2 · L).** A `Handlers` is only fully
constructed after eight imperative calls, so a forgotten one is a nil dereference at request time rather than a
compile error. Three of the setters take interfaces that exist purely so `transport` need not import `daemon`
— a legitimate inversion. `handlers_wiredup_test.go` exists specifically to guard this, which is a good
mitigation and confirms it is a known soft spot. Convert to a `HandlersConfig` when convenient; not urgent.

**XC-26 · Naming and file-layout nits (P3 · S).** `dashboard.go:317` declares
`var EVENT_LOG_MAX_ROWS = 100;` — correct JavaScript inside an embedded string, but it trips the eye and any
grep for Go constants; mark the JS boundary. `daemon.LOCKFILE_EXCLUSIVE_LOCK` mirrors a Win32 name (fine) but
is **exported with zero consumers** (not fine). Source-file naming and platform suffixes are consistent
throughout; test-file naming has drifted — `dispatch_extra_test.go` (576 lines), `dispatch_ops_test.go`
(1,441), `uncovered_units_test.go`, `projectinit_units_test.go`, `fswatch_units_test.go` describe *when they
were written*, not what they test.

**XC-27 · ~30 exported symbols have no cross-package consumer (P3 · M).** Highest confidence: everything in
`logging/logging.go` (delete with XC-13), the three `retrieve` renderer types, four `setup` types, five
`transport` symbols, nine `sandbox` symbols (note that the threshold constants are *documented* in CLAUDE.md as
the public contract — keep those exported deliberately, but the gap between "documented" and "referenced" is
worth knowing), five `core` types, `redact.LoadResult`, `content.Summary`. Since this is an `internal/` module
with no external API surface, the value is comprehension rather than compatibility — do the dead ones and skip
the rest. **The opposite case matters more:** `project.DaemonDir`, `EnsureDir`, `ID`, `SocketName` and the four
filename constants have zero consumers *precisely because* 31 sites hand-roll them (XC-19). Do not unexport
those — route the hand-joins through them.

## VI.7 — ADRs

Twenty-seven records, MADR-style, with explicit alternatives-considered sections, honest "Implementation
Status" amendments (0008, 0015, 0016), real supersession tracking (0001→0019), and several recorded
*reversals* with the reasoning intact (0006 reverses an earlier decision; 0021 documents a v0.6.2 decision
reversed in v0.6.3). **This is better ADR hygiene than most codebases.** The two problems below are maintenance
lapses in a good system, not the absence of one.

### XC-28 · ADR-0014 asserts a security invariant the code deleted **[verified]**
**P0 · S**

ADR-0014, the ADR index row, **and CLAUDE.md** all state that permissions merging has a hard-deny invariant —
that an operator override such as `allow:exec:rm *` is silently masked. The code:

```go
// internal/sandbox/policy.go:228-238
var hardDenyExecBaseCommands = map[string]struct{}{}
```

An empty map. The lookup at `:250` always misses. Four test files were edited to record the removal — one
states "hardDenyExecBaseCommands is empty (default-permissive exec)", another "no-op test since the hard-deny
list is now empty" — while neither the ADR, the index, nor CLAUDE.md was updated.

So a documented **security** invariant was removed deliberately, the tests were updated to match, and the
decision was never recorded. An operator reading ADR-0014 or CLAUDE.md today believes `.dfmt/permissions.yaml`
cannot be used to grant `rm *`. It can. This also renders the `MergePolicies` filtering branch and its warning
output unreachable (see the sandbox section).

**Fix:** write ADR-0026 superseding the exec-hard-deny half of ADR-0014, stating the default-permissive-exec
decision and its rationale, and update CLAUDE.md and the index — the four deliberate test edits indicate this
*is* the intent. Then delete the two stub tests (XC-7), which exist only as fossils of the removed invariant.
The alternative — restoring the list — is a product decision, not a cleanup.

### XC-29 · The ADR index is three records stale, and claims an automation that does not exist
**P2 · S**

The "Active Decisions" table ends at 0022. ADRs **0023, 0024 and 0025** — real deadlines and RPC auth,
retention ceilings and line-oriented reads, async exec jobs — are absent, all `Accepted`, all substantial, all
dated within the last three days of the snapshot. ADR-0022 is also listed before 0021.

Compounding it, the index's closing line claims *"CI lint checks run on every ADR change: required fields,
numbering contiguity, supersession reference validity."* No such check exists — the workflows directory
contains only `release.yml` and the disabled CI file, and neither mentions ADRs. **The index's staleness is
itself the evidence that the claimed automation is fictional.**

Minor drift in the same area: ADRs 0000–0012 and 0019–0025 use a table header for status while 0013–0018 use a
bullet list — two formats for the field the non-existent linter is supposed to check. ADR-0004 grants
`x/crypto`, which `go.mod` does not include (a permission need not be exercised, but say so).

**Fix:** add the three rows, fix the ordering, normalize the header format, and either implement the lint (a
~60-line script in `scripts/`, the same shape as `coverage-gate.go`) or delete the sentence.

### XC-30 · ADR-0005 describes capture sources that are not autonomously wired (P3 · S)
ADR-0005 describes five independent capture sources. Per ADR-0015's amendment log the `capture.{mcp,git,shell}`
knobs were removed, and `config.go:167-170` confirms only FS is wired; `capture/git.go` and `shell.go` are
reached only through explicit `dfmt capture git|shell` invocations. Add an "Implementation Status" amendment in
the style 0008 and 0015 already use.

### XC-31 · Three packages are absent from ARCHITECTURE.md (P3 · S)
`internal/osutil`, `internal/safejson` and `internal/timeouts` are never mentioned in a 167 KB document that
covers every other package. Two of the three are the results of *successful* de-duplication passes and are the
pattern the remaining clusters (XC-16, XC-21) should follow — they deserve a paragraph each so the next
contributor finds them before writing a fourth `samePath`. The `internal/logging` section documents `log.go`'s
API only, giving no hint the package also contains the dead slog half.

### ADR drift table

| ADR | Subject | Code state |
|---|---|---|
| 0003, 0006–0013, 0016–0022 | journal/index, sandbox scope, HTML parser, dedup, structured output, token budgets, metrics, global daemon, self-promotion, identity | ✅ implemented as described (0013's Levenshtein removal verified: zero occurrences repo-wide) |
| 0004 | stdlib-first dependencies | ✅ holds exactly; `x/crypto` granted but unused |
| **0014** | operator overrides, hard-deny invariant | ❌ **invariant deleted in code, still asserted in ADR + index + CLAUDE.md** (XC-28) |
| 0015 | config knob consolidation | ⚠️ excellent amendment log, but `config.Default()` contradicts its own YAML template (CAP-5) |
| 0005 | five capture sources | ⚠️ only FS autonomously wired; undocumented (XC-30) |
| **0023, 0024, 0025** | deadlines/auth, retention, async jobs | ⚠️ implemented, **missing from the index** (XC-29) |

## VI.8 — Overall strengths

1. **The commentary is exceptional and it is the reason this codebase can be refactored safely.** Comments
   explain *why*, cite the bug that motivated the code, name the alternative rejected, and frequently carry the
   measurement: the `**/*.go` glob that returned 2 of 278 files, the `sleep 20` that returned at 20.7 s, the 58
   of 100 grep slots consumed by `dist/dfmt.exe`, the 739 µs → 8.7 µs insert. One even records that "pre-extraction
   this exact eight-line block appeared 15 times." **Every refactor must carry these forward verbatim.**
2. **ADR discipline is real** — 27 records, honest supersessions, amendment logs, documented reversals.
3. **Security-sensitive code is the best-tested code** — `safefs` (79 assertions + two contract-encoding fuzz
   targets), `redact` (53 assertions + fuzz + 10 tables), `sandbox/permissions` (106 assertions + 10 tables).
4. **Not over-mocked** — eight mocks in 160 test files; real sockets, real temp dirs, real journals.
5. **Dependency discipline held exactly** — two modules, in-tree BM25, Porter stemmer, HTML parser, JSON-RPC
   codec, MCP wire format and Prometheus emitter, all tested.
6. **Error handling is fundamentally sound** — 3:1 wrap ratio, no panics in library code, no error-string
   matching, and a well-built crash handler with an inner recover to prevent double-panic.
7. **The MCP stdio boundary is respected** — verified end to end.
8. **Four prior de-duplication passes succeeded**, which is why the remaining clusters are a finishable list
   rather than a rewrite.

---

# Part VII — Prioritized execution plan

Sequenced so that each wave makes the next one verifiable. Effort totals assume one engineer familiar with the
codebase.

## Wave 0 — Make the project observable again (~half a day)

Nothing else can be trusted until this lands, because every finding in this report is something continuous
integration would have caught.

| ID | Action | Effort |
|---|---|---|
| BLD-1 | Rename `ci.yml.disabled` → `ci.yml` with `lint: continue-on-error: true` so build/test/vet signal returns immediately | S |
| BLD-3 | Fix the 67 lint issues — 45 are one-line `return`s after `t.Fatalf`; then make lint blocking | M |
| XC-2 | Make the coverage gate run in CI; reconcile its thresholds with CLAUDE.md (XC-1) | S |
| BLD-1b | Add a test-job dependency to `release.yml` so a tag can never publish an untested binary | S |
| XC-8 | Add `make test-race` and run it at least once per release | S |

## Wave 1 — Correctness and security fixes that are small and self-contained (~1–2 days)

Every item is P0 or P1 with effort **S**, no structural change, and each is independently shippable.

| ID | Action | Package |
|---|---|---|
| SBX-1 | Evaluate the full part text for deny rules in the chained-command path, plus the six-case test matrix | sandbox |
| SBX-2 | Case-fold path rules on case-insensitive filesystems; lowercase exec *patterns* | sandbox |
| SBX-8 | Fix the short-UTF-16LE misdetection that turns `echo hi` into a binary summary on Windows | sandbox |
| SBX-9 | Give Windows children `SystemRoot`, `COMSPEC`, `PATHEXT`, `WINDIR`, `APPDATA` | sandbox |
| TRN-1 | Key the stats cache on project path; skip the store when `no_cache` is set | transport |
| TRN-2 | Call `Validate()` on the socket and HTTP transports | transport |
| TRN-3 | Route `writeParseError` through the mutex-guarded writer; add a `-race` test | cli/mcp |
| TRN-5 | Append the newline to the proxy frame; add an end-to-end test (or delete the endpoint) | transport |
| TRN-8 | Drop `omitempty` from `Response.ID` | transport |
| CORE-4 | Use accessors in `PersistIndex` instead of unlocked field reads | core |
| CORE-12 | Stop discarding the marshal error in `ComputeSig` | core |
| CAP-5 | Put `.dfmt/**` in the Go-side default ignore list, derived from the YAML template | config |
| CAP-1 | Warn when no filesystem watcher exists for the platform; surface it in `doctor` | capture |
| CAP-2 | 64 KiB inotify buffer instead of 4.2 MB per directory | capture |
| LIF-5 | `http.NewRequestWithContext` — one line, restores all cancellation | client |
| LIF-6 | Verify liveness and identity before `dfmt stop` force-kills a PID | cli |
| LIF-7 | Gate `stop`/`dashboard`/`status`/`doctor` on `inspectGlobalDaemon`, not the port file | cli |
| LIF-10 | Return false for an empty project path before the legacy probe leg | client |
| LIF-11a | Delete the `os.Remove` before the port-file rename | transport |
| XC-13 | Delete `logging/logging.go`, its test, and its two lint exclusions | logging |
| XC-9 | Wrap `ErrPathOutsideRoot` in `CheckNoSymlinks` | safefs |
| XC-20 | Add `project.GlobalRegistryPath()`; fix the three transport sites; delete the false cycle comment | transport |
| XC-28 | Write ADR-0026; update CLAUDE.md and the ADR index; delete the two stub tests | docs |
| XC-29 | Add ADRs 0023–0025 to the index; fix ordering; implement or retract the lint claim | docs |
| BLD-2 | Default `version.Current` to `"dev"` and fill from `debug.ReadBuildInfo()` | version |
| BLD-4 | `.exe` suffix on Windows; align Makefile flags with the release workflow | build |

## Wave 2 — Connect what was built but never wired (~3–5 days)

This is where the report's central pattern gets closed. **Order matters**: the pipeline's destructive stages
must be fixed before the pipeline is pointed at file reads.

1. **SBX-5, SBX-6, SBX-7** — move RLE to the end of the pipeline and skip it for recognized JSON/YAML/diff;
   require a real API-shaped root before `CompactYAML` rewrites anything; swap the two line-ending stages.
2. **SBX-4** — call `NormalizeOutput` from `Fetch` and `Read`, with a file-oriented mode for the latter.
   Re-run `dfmt-bench tokensaving` afterwards; the published numbers should finally describe the shipped path.
3. **CORE-1 / CORE-2 / CORE-3** — rotate on cap; decide the content store's fate (retrieval handler and sweep,
   or in-memory with a TTL) and implement one of them.
4. **TRN-6** — delete the inline recall renderer; route markdown through `SnapshotBuilder` plus
   `MarkdownRenderer`, restoring interning and forgery escaping on the default format and collapsing TRN-6b
   and TRN-6c with it.
5. **CORE-5 / CORE-6 / CORE-7** — persist `meta` and the reverse maps, add a format version; add
   `BenchmarkAddAtCapAfterLoad` so the loaded path is measured.

## Wave 3 — The daemon singleton protocol (~2–3 days, one coherent change)

**LIF-1, LIF-2, LIF-3, LIF-4 are one design fix, not four patches.** Land them together with tests that
actually race two CLI processes:

1. Acquire the singleton lock **before** any listener bind.
2. Never delete the lock file; verify it is free by acquiring it before any destructive cleanup.
3. Add a separate non-blocking spawn lock so classify→spawn is interlocked.
4. Probe the lock to distinguish `Stuck` from `Dead` when the PID file is missing.

Then **LIF-8** (a real graceful stop via an authenticated shutdown endpoint, so the index is persisted on
Windows), **LIF-14** (content-hash fingerprint plus two-strike rule), and **LIF-13** (persisted, rate-limited
restart bookkeeping).

## Wave 4 — Test integrity (~1 week, mechanical but large)

| ID | Action |
|---|---|
| XC-3 | Convert 189 `t.Logf` pseudo-assertions to `t.Errorf`; fix what breaks; delete what asserts nothing. Expect `cli` coverage to fall below 70 % — that is the honest number, and it is the starting point, not a regression |
| XC-4 | Convert 47 setup-failure skips to `t.Fatalf`; delete the six permanently-disabled tests |
| XC-22 | Extract `internal/testutil` (env isolation, `waitFor`, `setHome`) |
| XC-5 | `os.Setenv` → `t.Setenv` (150 sites), then enable `t.Parallel` in the leaf packages |
| XC-6 | Replace the 42 `time.Sleep` synchronizations with the shared bounded-poll helper |
| XC-8 | Add benchmarks for `redact.Redact` and the JSON-RPC codec — the two hot paths with no perf guard |
| XC-15 | Add the MCP stdout-purity regression test |

## Wave 5 — Consolidation (~1–2 weeks, each item needs an ADR)

| ID | Action | Why an ADR |
|---|---|---|
| XC-16 | One `globcompile` package with explicit option flags and three presets, replacing four in-package dialects and the watcher's fifth; add a **differential** fuzz target across presets | Changes matching semantics users depend on |
| XC-18 / CAP-6 | One `internal/ignoreset` supplying both basenames and globs, consumed by the walkers, the watcher and the config template | Changes what gets indexed |
| XC-19 | Route the 31 hand-joined `.dfmt` paths through the existing `project` helpers | Mechanical, but touches 12 packages |
| TRN-S2 | One method table plus one error mapper shared by all three transports — this is where TRN-2, TRN-9, TRN-15 and TRN-8 stop being three fixes each | Changes the tool-addition workflow |
| SBX-Q1 | One `resolveUnderWD` path sanitizer replacing five inconsistent prologues (which is what produced SBX-23) | Security-boundary consolidation |
| XC-24 | Move the registry to `internal/project`; break `daemon → client` | Dependency-graph change |
| TRN-S1 / CORE-A7 | Split `http.go` into five files; extract `retention`, `docstore` and `codec` from `Index`; move the MCP stdio loop into `transport` | Large pure moves; do last so earlier diffs stay reviewable |

## Wave 6 — Documentation truth (~2 days)

The nine drift items in §IV.5 share one root cause: `ARCHITECTURE.md` asserts implementation details at
line-number granularity that no test enforces. Fix the individual claims, then **generate the checkable tables
— semaphore sizes, thresholds, caps, agent config paths — from the Go constants**, so the next drift is a build
failure rather than an audit finding.

Also: correct the `safefs` package doc's Windows security claim (SFS-1), amend ADR-0005 (XC-30), document
`osutil`/`safejson`/`timeouts` (XC-31), move `security-report/` under `docs/` while preserving the V-numbered
IDs the code comments reference (BLD-7), and delete the committed `coverage.out`.

---

## Appendix A — What this report deliberately does not recommend

* **Do not refactor `dispatch.go`'s 37-case switch.** Each case is a single delegation with no logic; a
  map-of-handlers adds indirection and buys nothing. Its adjacent problems (incomplete usage text, the
  environment-mutating `ParseGlobals`) are the real ones.
* **Do not reintroduce an HTTP `WriteTimeout`.** The absence is deliberate, the reasoning is measured, and a
  test pins it.
* **Do not add a dependency** to solve the glob, HTML, or BM25 consolidation items. ADR-0004 holds and the
  in-tree implementations are tested.
* **Do not chase the last exported-symbol unexports.** This is an `internal/` module with no external API; the
  value is comprehension, and it is concentrated in the dead symbols.
* **Do not delete `client/backend_test.go`** despite its zero runtime assertions — it is a deliberate
  compile-time interface check and says so.
* **Preserve every measured comment.** They are the single highest-value asset in the repository and the reason
  this audit could be specific. When code moves, the comment moves with it, verbatim.

## Appendix B — Finding index by severity

**P0 (18):** CORE-1, CORE-2, CORE-3, CORE-4, CORE-5, CORE-6, CORE-7, CORE-8 · TRN-1, TRN-2, TRN-3, TRN-4,
TRN-5, TRN-6 · SBX-1, SBX-2, SBX-3, SBX-4, SBX-5, SBX-6, SBX-7, SBX-8, SBX-9, SBX-10 · CAP-1, CAP-2, CAP-3,
CAP-4, CAP-5, CAP-7 · LIF-1, LIF-2, LIF-3, LIF-5, LIF-8 · BLD-1 · XC-2, XC-3, XC-13, XC-18, XC-28

**P1 (60+):** CORE-9…CORE-19 · TRN-7…TRN-18 · SBX-11…SBX-26 · CAP-6, CAP-8, CAP-9, CAP-10 · SET-1…SET-5 ·
SFS-1…SFS-4 · LIF-4, LIF-6, LIF-7, LIF-9…LIF-20 · BLD-2, BLD-3, BLD-4 · XC-1, XC-4, XC-9, XC-10, XC-16,
XC-17, XC-19, XC-20, XC-29

**P2/P3:** the structural, duplication, dead-code, naming and documentation items catalogued in each part's
closing sections.

---

*Report generated 2026-07-31 against `855c0b1`. Findings marked **[verified]** were re-confirmed by direct
grep or file read beyond the originating package read-through. Line numbers refer to that commit and will
drift; the file plus symbol name is the durable reference.*
