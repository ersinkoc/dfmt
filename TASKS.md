# DFMT — Task Breakdown

| Field | Value |
| --- | --- |
| Document | `TASKS.md` |
| Status | Draft — v0.1 |
| Target | v1.0 |
| Related | `SPECIFICATION.md`, `IMPLEMENTATION.md` |

This document sequences the DFMT implementation into phases, each a set of discrete tasks with acceptance criteria and dependencies. Tasks are sized for delegation to Claude Code sessions (S = 1-2h, M = half-day, L = full day, XL = multi-day).

Milestones:

- **M0 — Scaffold.** Repo, CI, module, core types. No functionality.
- **M1 — POC.** Single binary that can `remember` and `search` over a journal. Demonstrates that the index works.
- **M2 — MVP.** All capture sources live. Snapshots work. MCP transport works. Usable end-to-end by a real agent.
- **M3 — v1.** HTTP transport, shell integration, doctor, list, cross-platform CI, benchmarks hitting targets.
- **M4 — Release.** Signed binaries, install scripts, Homebrew, docs polish.

Tasks within a phase can often run in parallel. Task IDs are stable; cross-references use them.

---

## Phase 0 — Repository Scaffold (M0)

Goal: produce a buildable, testable, CI-wired repository with nothing in it but types and dispatchers.

### P0-T01 — Initialize repository [S]

- Create GitHub repo `ersinkoc/dfmt`.
- `go mod init github.com/ersinkoc/dfmt`.
- Add `LICENSE` (MIT, 2026 ECOSTACK TECHNOLOGY OÜ).
- Add `.gitignore` for Go (covers `dist/`, `*.gob`, `.dfmt/`, etc.).
- Commit all four docs: `SPECIFICATION.md`, `IMPLEMENTATION.md`, `TASKS.md`, and the 5 ADRs under `docs/adr/`.

**Acceptance:** `git clone && go mod tidy` produces empty, valid module.

### P0-T02 — Directory skeleton [S]

- Create the full tree from `IMPLEMENTATION.md §1`.
- Each `.go` file has the minimal `package X` header and a single `TODO` comment.
- `cmd/dfmt/main.go` prints version and exits; enough to `go build`.

**Depends on:** P0-T01.
**Acceptance:** `go build ./...` succeeds with no errors.

### P0-T03 — Makefile and build targets [S]

- Implement `make build`, `make test`, `make lint`, `make fmt`, `make clean`, `make install`, `make release`.
- `build` uses `IMPLEMENTATION.md §2.2` ldflags with `main.version` injected.
- `release` cross-compiles to the 7 target triples.
- Version read from `VERSION` file or `git describe --tags --always`.

**Depends on:** P0-T02.
**Acceptance:** `make build` produces `./dist/dfmt`. `./dist/dfmt --version` prints a sane string.

### P0-T04 — Linter config [S]

- Commit `.golangci.yml` from spec §.
- Settings match `IMPLEMENTATION.md §2.3`.
- Fix any `make lint` findings on the skeleton.

**Depends on:** P0-T02.
**Acceptance:** `make lint` passes clean.

### P0-T05 — GitHub Actions CI [M]

- `.github/workflows/ci.yml`:
  - Matrix: {ubuntu, macos, windows} × {go 1.22, 1.23}.
  - Steps: checkout, setup-go, `go mod download`, `go test ./...`, `make lint`.
  - Cache modules and build output.
- `.github/workflows/release.yml`:
  - Triggered by git tag `v*`.
  - Cross-compile all 7 targets.
  - Sign with cosign keyless (OIDC, free for public repos).
  - Create GitHub Release with binaries, `sha256sums.txt`, `sha256sums.txt.sig`.

**Depends on:** P0-T03, P0-T04.
**Acceptance:** Every PR triggers CI; every tag produces a draft release with signed artifacts.

### P0-T06 — Core event types [M]

- Implement `internal/core/event.go` per `IMPLEMENTATION.md §3.1`.
- Canonical JSON marshal helper (stable field order, sorted map keys).
- `Sig` computation and validation.
- ULID generator (`internal/core/ulid.go`) per Appendix B, with tests against published vectors.
- Type, Priority, Source enums with `String()` and `ParseX()` helpers.

**Depends on:** P0-T02.
**Acceptance:** `TestEventCanonicalRoundTrip` passes. `TestULIDMonotonicity` passes. ≥ 90% coverage on these files.

### P0-T07 — Config loader [M]

- `internal/config/config.go` with all struct definitions from `IMPLEMENTATION.md §4.2`.
- `internal/config/defaults.go` returns a fully-populated `Config` with all defaults from spec §14.
- `internal/config/load.go` reads global then project YAML and merges (project wins).
- Unknown keys preserved on round-trip.
- Invalid values (bad duration strings, negative numbers) return clear errors.

**Depends on:** P0-T02.
**Acceptance:** `TestConfigDefaults`, `TestConfigMerge`, `TestConfigPreserveUnknown` all pass.

### P0-T08 — Project discovery [S]

- `internal/project/discover.go`: walk-up for `.dfmt/` then `.git/`, honor `DFMT_PROJECT` env.
- `internal/project/id.go`: 8-hex SHA-256 ShortID.
- `internal/project/registry.go`: read/write/append `projects.jsonl` with flock.

**Depends on:** P0-T06.
**Acceptance:** `TestDiscover`, `TestRegistryConcurrentAppend` pass.

---

## Phase 1 — Core Storage & Index (M1 prerequisite)

Goal: a working journal and index. No transports yet, no daemon yet. This is the foundation everything else sits on.

### P1-T01 — Journal writer [L]

- `internal/core/journal.go`: implement `Journal` interface from `IMPLEMENTATION.md §3.2`.
- Durable and batched modes.
- `Append` with `flock(LOCK_EX)`, `O_APPEND | O_SYNC` in durable mode.
- `Stream(from)` returns a channel; linear scan from the given cursor.
- `Checkpoint()` returns current HEAD ULID.
- Quarantine invalid lines to `quarantine/journal.corrupt.jsonl`.

**Depends on:** P0-T06.
**Acceptance:** `TestJournalAppendReadback`, `TestJournalQuarantine`, `TestJournalConcurrentAppend` pass. Benchmark `BenchmarkJournalAppend` hits 500 writes/sec durable, 50k/sec batched on SSD.

### P1-T02 — Journal rotation [M]

- `internal/core/journal_rotate.go`: rotation at `max_bytes`.
- Rename to `journal.<ULID>.jsonl`, open fresh.
- Gzip compression in background goroutine.
- `Stream` transparently reads across rotated segments.

**Depends on:** P1-T01.
**Acceptance:** `TestJournalRotate`, `TestStreamAcrossSegments` pass.

### P1-T03 — Tokenizer and stopwords [M]

- `internal/core/tokenize.go`: lowercase, split, length filter, stopword drop.
- Embedded English stopword list (~180 words).
- Embedded Turkish stopword list (~120 words).
- Config-provided external stopwords file support.

**Depends on:** P0-T06.
**Acceptance:** `TestTokenizeEnglish`, `TestTokenizeTurkish`, `TestTokenizeStopwords` pass.

### P1-T04 — Porter stemmer [L]

- `internal/core/porter.go`: port Porter's 1980 algorithm to Go.
- Reference-tested against the Porter test corpus (~20k words with expected stems, public).
- Zero dependencies.

**Depends on:** P0-T02.
**Acceptance:** `TestPorterCorpus` passes with ≥ 99.5% match against published expected stems. Coverage ≥ 95%.

### P1-T05 — BM25 scorer [S]

- `internal/core/bm25.go`: 30-line scorer per `IMPLEMENTATION.md §3.5`.
- Handle edge cases: zero TF, zero df, empty docs.
- Smoothed IDF to avoid negatives.

**Depends on:** P0-T02.
**Acceptance:** `TestBM25Monotonicity`, `TestBM25DuplicateTerms`, `TestBM25EmptyDoc` pass.

### P1-T06 — Trigram index [M]

- `internal/core/trigram.go`: generate 3-grams from tokens, maintain separate posting lists.
- Substring query resolves to trigram intersection.

**Depends on:** P1-T03.
**Acceptance:** `TestTrigramSubstringMatch` passes for both exact and partial matches.

### P1-T07 — Levenshtein fuzzy [S]

- `internal/core/levenshtein.go`: standard dynamic programming implementation.
- Returns edit distance; caller filters by threshold (default 2).

**Depends on:** P0-T02.
**Acceptance:** `TestLevenshtein` passes published test vectors.

### P1-T08 — Inverted index [L]

- `internal/core/index.go`: per `IMPLEMENTATION.md §3.3`.
- `Add(event)`: tokenize → stem → update posting lists and doc lengths.
- `SearchBM25`: iterate posting lists, accumulate scores in a map, top-K heap.
- `SearchTrigram`: substring match via trigram posting list.
- Thread-safe with `sync.RWMutex`.

**Depends on:** P1-T03, P1-T04, P1-T05, P1-T06.
**Acceptance:** `TestIndexAddSearch`, `TestIndexConcurrentRead` pass. `BenchmarkIndexBM25` hits <5 ms p99 at 10k events.

### P1-T09 — Index persistence [M]

- `internal/core/index_persist.go`: serialize `Index` to `index.gob`, load on startup.
- `index.cursor` file records the ULID of the last journal event included.
- On load, check tokenizer version; rebuild on mismatch.
- Rebuild from journal: stream events, call `Add` on each.

**Depends on:** P1-T08, P1-T01.
**Acceptance:** `TestIndexPersistRoundTrip`, `TestIndexRebuildFromJournal` pass. Cold-load 10k events in <200 ms.

### P1-T10 — Classifier [M]

- `internal/core/classifier.go`: default priority table + override rules.
- Rules loaded from `priority.yaml` (yaml.v3).
- Path glob matching via stdlib `filepath.Match`.

**Depends on:** P0-T06, P0-T07.
**Acceptance:** `TestClassifierDefaults`, `TestClassifierOverrides` pass.

---

## Phase 2 — POC Transport (M1)

Goal: `dfmt remember` writes to journal, `dfmt search` queries the index, both working end-to-end over a Unix socket. Demonstrates the core value.

### P2-T01 — JSON-RPC codec [M]

- `internal/transport/jsonrpc.go`: line-delimited JSON-RPC 2.0 over any `io.ReadWriter`.
- Server loop: decode → dispatch → encode.
- Client: send request, read response, match by ID.
- ~250 lines total.

**Depends on:** P0-T02.
**Acceptance:** `TestJSONRPCRoundTrip`, `TestJSONRPCError`, `TestJSONRPCBatch` pass.

### P2-T02 — Handlers [M]

- `internal/transport/handlers.go`: `Handlers` struct from `IMPLEMENTATION.md §7.1`.
- Implement `Remember` (write to journal, add to index), `Search` (query index, render results). Stubs for the rest.

**Depends on:** P1-T08, P1-T01, P2-T01.
**Acceptance:** `TestHandlersRemember`, `TestHandlersSearch` integration tests pass.

### P2-T03 — Socket server [M]

- `internal/transport/socket.go`: listen on `.dfmt/daemon.sock`, accept connections, serve JSON-RPC.
- Socket file permissions `0700`, parent dir `0700`.
- Graceful shutdown closes listener and removes socket file.

**Depends on:** P2-T01, P2-T02.
**Acceptance:** `TestSocketServerEndToEnd` connects and makes calls.

### P2-T04 — Client library [M]

- `internal/client/client.go`: connect to socket, make calls.
- Discovery via `internal/project/discover.go`.
- Auto-start hook (stub for now; full impl in Phase 5).

**Depends on:** P2-T03, P0-T08.
**Acceptance:** `TestClientConnect`, `TestClientCall` pass against a running socket server.

### P2-T05 — CLI dispatcher [M]

- `internal/cli/dispatch.go`: subcommand router.
- `internal/cli/flags.go`: minimal flag parser supporting common flags.
- `cmd/dfmt/main.go`: entry point calls dispatcher.

**Depends on:** P0-T02.
**Acceptance:** `./dist/dfmt` shows help; `./dist/dfmt status` (stub) exits 0.

### P2-T06 — `dfmt init` command [S]

- Create `.dfmt/` directory, write default `config.yaml`.
- Append `.dfmt/` to `.gitignore` if present.

**Depends on:** P2-T05, P0-T07.
**Acceptance:** `./dist/dfmt init` in a fresh tempdir produces correct files.

### P2-T07 — `dfmt daemon` command (minimal) [M]

- `internal/cli/cmd_daemon.go`: start daemon, bind socket, serve until signal.
- `internal/daemon/daemon.go` (skeleton): lock file, open journal, load/build index, run handlers.
- `--foreground` flag required for now; double-fork comes in Phase 5.

**Depends on:** P2-T03, P1-T09, P0-T07.
**Acceptance:** `dfmt daemon --foreground` runs, accepts socket connections, shuts down on SIGTERM.

### P2-T08 — `dfmt remember` and `dfmt search` commands [M]

- `internal/cli/cmd_remember.go`: build event, call Handlers via client.
- `internal/cli/cmd_search.go`: call Handlers, render results to stdout.
- Both honor `--json`.

**Depends on:** P2-T04, P2-T07.
**Acceptance:** End-to-end integration test: start daemon, remember 5 events, search, get back the right event IDs.

**🎯 Milestone M1 (POC) reached after P2-T08.**

---

## Phase 3 — Capture Layer (M2 prerequisite)

Goal: events arrive in the journal from five independent sources. This is what makes DFMT agent-agnostic.

### P3-T01 — Capturer interface [S]

- `internal/capture/capturer.go`: `Capturer` interface and `EventSink` interface.
- Shared helper: `SubmitToDaemon` dials the socket and calls `Remember`.

**Depends on:** P2-T04.
**Acceptance:** Interface compiles; mock capturer in test passes events through sink.

### P3-T02 — `dfmt capture` CLI [S]

- `internal/cli/cmd_capture.go`: subcommands `git`, `shell`, `env.cwd`, etc.
- Each parses args, builds event, submits via client. Background-safe (doesn't block parent).

**Depends on:** P3-T01, P2-T05.
**Acceptance:** `dfmt capture git commit "abc123 fix foo"` produces the right event.

### P3-T03 — Git hook scripts [M]

- Write `docs/hooks/git-*.sh` for commit, checkout, push, merge.
- `internal/cli/cmd_install_hooks.go`: reads these at build time via `go:embed`, writes to `.git/hooks/`.
- Hook versioning header, foreign-hook detection with `--force` override, backup.

**Depends on:** P3-T02.
**Acceptance:** `dfmt install-hooks` in a repo installs correct hooks. A subsequent `git commit` produces a `git.commit` event.

### P3-T04 — Shell integration snippets [S]

- `docs/hooks/zsh.sh`, `bash.sh`, `fish.fish`: embedded via `go:embed`.
- `internal/cli/cmd_shell_init.go`: `dfmt shell-init <shell>` emits the snippet to stdout.

**Depends on:** P3-T02.
**Acceptance:** `dfmt shell-init zsh | grep -q precmd` succeeds.

### P3-T05 — Gitignore parser [M]

- `internal/capture/gitignore.go`: minimal gitignore pattern matcher.
- Support: literal paths, `*`, `**`, `/` prefix, `!` negation, `#` comments.

**Depends on:** P0-T02.
**Acceptance:** `TestGitignoreStandard` passes against a corpus of git's own test patterns.

### P3-T06 — FS watcher (Linux) [L]

- `internal/capture/fswatch_linux.go`: `InotifyInit1`, `InotifyAddWatch`, read events.
- Recursive directory watching.
- Respects `config.capture.fs.ignore` + project `.gitignore` (P3-T05).
- Emits `file.create`, `file.edit`, `file.delete`.
- Debounces via `debounce_ms`.

**Depends on:** P3-T05.
**Acceptance:** `TestFSWatchLinuxBasic` creates/edits/deletes files in a tempdir, asserts events arrive.

### P3-T07 — FS watcher (macOS/BSD) [L]

- `internal/capture/fswatch_darwin.go` + `fswatch_freebsd.go` using `x/sys/unix.Kqueue`.
- Same public surface as Linux impl.

**Depends on:** P3-T05.
**Acceptance:** `TestFSWatchDarwinBasic` passes on macOS CI runner.

### P3-T08 — FS watcher (Windows) [L]

- `internal/capture/fswatch_windows.go` using `x/sys/windows.ReadDirectoryChangesW`.
- Handle Windows-specific path quirks (backslashes, long paths).

**Depends on:** P3-T05.
**Acceptance:** `TestFSWatchWindowsBasic` passes on windows CI runner.

### P3-T09 — FS watcher integration in daemon [M]

- Wire FS watcher as a `Capturer` in daemon startup.
- Config-driven enable/disable.
- Feeds events through `EventSink` to journal.

**Depends on:** P3-T06, P3-T07, P3-T08, P2-T07.
**Acceptance:** Start daemon, touch a file matching the watch glob, see a `file.edit` event in the journal.

### P3-T10 — Event deduplication [M]

- `internal/retrieve/dedup.go`: collapse same-path `file.edit` bursts.
- Coalesce FS bursts within 2s of a `git.checkout` event into the git event.

**Depends on:** P3-T09.
**Acceptance:** `TestDedupFileBurst`, `TestDedupGitCheckoutBurst` pass.

---

## Phase 4 — Retrieval (M2 prerequisite)

Goal: `dfmt recall` produces useful snapshots; `dfmt search` gains fuzzy fallback.

### P4-T01 — Three-layer search [M]

- `internal/retrieve/search.go`: orchestrate BM25 → trigram → Levenshtein layers.
- Layer tagging on results.
- Config-driven limits.

**Depends on:** P1-T08, P1-T06, P1-T07.
**Acceptance:** `TestSearchThreeLayers` asserts a typo query falls through to layer 3 and returns expected match.

### P4-T02 — Progressive throttle [S]

- `internal/retrieve/throttle.go`: per daemon, counts searches since last `prompt` event.
- Exposes allowed-limit and blocked status.

**Depends on:** P4-T01.
**Acceptance:** `TestThrottleTiers`, `TestThrottleResetOnPrompt` pass.

### P4-T03 — Snapshot builder [L]

- `internal/retrieve/snapshot.go` per `IMPLEMENTATION.md §6.1`.
- Session window resolution.
- Tier-ordered greedy fill within byte budget.
- Reserved byte budget for last prompt.
- Dedup integration (P3-T10).

**Depends on:** P1-T01, P3-T10.
**Acceptance:** `TestSnapshotRespectsBudget`, `TestSnapshotIncludesLastPrompt`, `TestSnapshotTierOrder` pass.

### P4-T04 — XML renderer [M]

- `internal/retrieve/render_xml.go`: `<session_snapshot>` per Appendix A.
- Uses `encoding/xml`.
- Golden test against fixture journal.

**Depends on:** P4-T03.
**Acceptance:** `TestRenderXMLGolden` passes.

### P4-T05 — JSON / MD / Guide renderers [M]

- Three more renderers matching the XML structure.
- Guide renderer produces the 15-section Session Guide narrative.
- Golden tests for each.

**Depends on:** P4-T03.
**Acceptance:** Three golden tests pass.

### P4-T06 — `dfmt recall` command [S]

- `internal/cli/cmd_recall.go`: flags for budget, format, since, include, exclude.
- Calls `Recall` handler, prints snapshot body.

**Depends on:** P4-T03.
**Acceptance:** `dfmt recall --budget 2048 --format md` produces valid markdown in CI fixture project.

### P4-T07 — `dfmt stats` command [S]

- Counts by type, tier, journal size, uptime, throttle state.
- JSON and table output.

**Depends on:** P2-T02.
**Acceptance:** `dfmt stats --json | jq .events_total` returns an int.

### P4-T08 — `dfmt tail` command [M]

- Stream events via `Stream()` or SSE-like over socket.
- `--follow` keeps streaming.
- `--format xml|json|md|text` for output shape.

**Depends on:** P2-T02, P1-T01.
**Acceptance:** `dfmt tail --follow` in one pane prints events as another pane generates them.

---

## Phase 5 — Daemon Lifecycle & MCP (M2)

Goal: daemon auto-starts transparently; MCP transport works; any MCP-capable agent can use DFMT.

### P5-T01 — Daemon lock [S]

- `internal/daemon/lock.go`: advisory flock on `.dfmt/lock`.
- LOCK_EX | LOCK_NB. Exit code 2 on contention.
- Released on clean shutdown.

**Depends on:** P2-T07.
**Acceptance:** `TestDaemonLockExclusive` starts two daemons; second exits 2.

### P5-T02 — Signal handling [S]

- `internal/daemon/signals.go`: SIGTERM, SIGINT trigger graceful shutdown.
- Drain in-flight writes, close transports, remove socket/PID, write index.gob.

**Depends on:** P5-T01.
**Acceptance:** `TestDaemonGracefulShutdown` asserts clean exit and recovered state.

### P5-T03 — Idle monitor [M]

- `internal/daemon/idle.go`: tracks last request + last FS event.
- Ticker every 60s checks idle; signals shutdown on trigger.

**Depends on:** P5-T02, P3-T09.
**Acceptance:** `TestIdleExit` starts daemon with 2s timeout, waits, asserts clean exit.

### P5-T04 — Auto-start (double-fork) [L]

- `internal/daemon/autostart.go`: `Spawn(project)` double-forks a detached daemon, waits for socket.
- Startup-race lock via `.dfmt/startup.lock`.
- Windows: `DETACHED_PROCESS` equivalent via `CreateProcess` from `x/sys/windows`.

**Depends on:** P5-T01.
**Acceptance:** `TestAutoStart` deletes socket, calls client, asserts daemon spawned and socket appears.

### P5-T05 — Client auto-start wire-up [M]

- `internal/client/client.go`: retry dial with auto-start fallback.
- 50 ms initial timeout, Spawn on fail, 2 s poll for socket.

**Depends on:** P5-T04.
**Acceptance:** `TestClientAutoStart` exercises full sequence.

### P5-T06 — MCP server [L]

- `internal/transport/mcp.go`: initialize, tools/list, tools/call, shutdown.
- Tool schemas per spec §8.1.
- Dispatches to `Handlers`.
- JSON Schema generation from Go structs.

**Depends on:** P2-T01, P2-T02.
**Acceptance:** `TestMCPInitialize`, `TestMCPListTools`, `TestMCPCallRemember` pass. Manual test against a real Claude Code instance succeeds.

### P5-T07 — `dfmt mcp` command [S]

- `internal/cli/cmd_mcp.go`: run MCP server on stdio.
- Auto-starts daemon if not running.
- Relays tool calls over socket.

**Depends on:** P5-T06, P5-T05.
**Acceptance:** `dfmt mcp` responds correctly to a fixture MCP handshake.

### P5-T08 — Register agent in Claude Code and Cursor [S]

- Document config snippets for each agent.
- Manual end-to-end verification.

**Depends on:** P5-T07.
**Acceptance:** Real Claude Code session can call `dfmt.remember`, `dfmt.recall`, `dfmt.search`.

**🎯 Milestone M2 (MVP) reached after P5-T08.**

---

## Phase 6 — Complete CLI Surface (M3 prerequisite)

Goal: every command from `SPECIFICATION.md §13` works.

### P6-T01 — `dfmt list` [M]

- Reads `projects.jsonl`, checks each PID.
- Prints table: project, id, pid, uptime, events, mem.

**Depends on:** P0-T08, P2-T07.
**Acceptance:** `dfmt list` shows running daemons accurately.

### P6-T02 — `dfmt doctor` [M]

- Diagnostic checklist: daemon running, socket reachable, journal writable, index loadable, FS watcher healthy, git hooks installed, shell integration detected.
- Each check reports ok/warn/fail with remediation.

**Depends on:** P3-T03, P3-T09, P5-T03.
**Acceptance:** `dfmt doctor` on a healthy project prints all-green; on a broken project, identifies the specific issue.

### P6-T03 — `dfmt stop` [S]

- Sends shutdown request to daemon.
- `--all` iterates all running daemons via registry.

**Depends on:** P5-T02, P6-T01.
**Acceptance:** `dfmt stop` cleanly shuts the daemon down.

### P6-T04 — `dfmt note`, `dfmt task`, `dfmt task done` [S]

- Convenience wrappers around `Remember` with fixed types.

**Depends on:** P2-T08.
**Acceptance:** `dfmt task "foo"` produces `task.create` event; `dfmt task done <id>` produces `task.done`.

### P6-T05 — `dfmt forget` [S]

- Emit tombstone event for given IDs.
- Downstream consumers exclude referenced events.

**Depends on:** P2-T02.
**Acceptance:** `dfmt forget <id>` then `dfmt search` no longer returns the event.

### P6-T06 — `dfmt config get/set` [M]

- Read and write config keys via dotted path.
- Validates against the config schema.

**Depends on:** P0-T07.
**Acceptance:** `dfmt config set lifecycle.idle_timeout 10m && dfmt config get lifecycle.idle_timeout` returns `10m`.

### P6-T07 — `dfmt rotate` [S]

- Force journal rotation.

**Depends on:** P1-T02.
**Acceptance:** Rotation happens; new segment appears.

### P6-T08 — `dfmt reindex` [S]

- Discard `index.gob`, rebuild from journal.

**Depends on:** P1-T09.
**Acceptance:** After reindex, all searches return equivalent results.

### P6-T09 — `dfmt export`, `dfmt import` [M]

- Export: stream journal to stdout in chosen format.
- Import: replay JSONL file into current project (with a warning).

**Depends on:** P1-T01.
**Acceptance:** Export-then-import round-trips to the same events.

### P6-T10 — `dfmt purge` [S]

- Delete `.dfmt/` for current project. `--global` also removes registry entry.
- Requires confirmation unless `--yes` passed.

**Depends on:** P0-T08.
**Acceptance:** `dfmt purge --yes` leaves no DFMT state for the project.

---

## Phase 9 — Sandbox Core (v0.9 prerequisite)

Goal: `dfmt.exec` works. Subprocess execution with resource limits, output capture, size-based handling. No security policy yet, no intent matching yet — just the raw mechanism.

### P9-T01 — Sandbox interface and types [S]

- `internal/sandbox/sandbox.go`: `Sandbox` interface, `ExecReq`/`ExecResp`/`ReadReq`/`ReadResp`/`FetchReq`/`FetchResp` types.
- Matches SPEC §7.5 and IMPLEMENTATION §11.1.

**Depends on:** P0-T06.
**Acceptance:** Interface compiles; mocks satisfy in tests.

### P9-T02 — Runtime detection [M]

- `internal/sandbox/runtime.go`: probe PATH for bash, sh, node, python, python3, go, ruby, perl, php, R, elixir.
- Capture version (first line of `--version` output, 500 ms timeout).
- Cache results; re-probe on SIGUSR1.
- `dfmt doctor` reports the runtime map.

**Depends on:** P9-T01.
**Acceptance:** `TestRuntimeProbe` detects installed runtimes; `TestRuntimeAbsent` reports missing langs correctly.

### P9-T03 — Exec executor (Unix) [L]

- `internal/sandbox/exec.go` + `exec_unix.go`: spawn subprocess with `os/exec`, apply resource limits via `syscall.Rlimit` on the child (through `exec.Cmd.SysProcAttr`).
- Separate stdout/stderr capture into bounded buffers.
- Timeout enforcement via `context.WithTimeout`.
- Exit code + duration capture.
- Default env curation: minimal whitelist, inject `DFMT_JOB_ID`.

**Depends on:** P9-T02.
**Acceptance:** `TestExecBasic` runs `echo hello`; `TestExecTimeout` enforces 1s timeout; `TestExecOutputCap` truncates at max_output.

### P9-T04 — Exec executor (Windows) [L]

- `internal/sandbox/exec_windows.go`: equivalent using job objects + `CreateProcessW` via `x/sys/windows`.
- Memory and CPU limits via `SetInformationJobObject`.
- Same public surface as Unix.

**Depends on:** P9-T02.
**Acceptance:** `TestExecBasicWindows` passes on windows CI runner with limits honored.

### P9-T05 — Output size disposition [M]

- In-memory routing: if stdout < `inline_threshold` (4 KB), return raw; if < `medium_threshold` (64 KB), store chunked; if above, chunk + index.
- Initial implementation stores in memory only; content store integration arrives in Phase 10.

**Depends on:** P9-T03 or P9-T04.
**Acceptance:** `TestExecDispositionSmall`, `TestExecDispositionMedium`, `TestExecDispositionLarge` assert the right return shape per size.

### P9-T06 — MCP tool `dfmt.exec` [M]

- Expose `dfmt.exec` via MCP and via HTTP POST `/v1/exec`.
- JSON Schema in tool registration.
- Emits `mcp.call` event on each invocation.

**Depends on:** P9-T05, P5-T06 (MCP server).
**Acceptance:** `TestMCPExecEndToEnd` via MCP client call returns expected ExecResp; exec event appears in journal.

### P9-T07 — CLI `dfmt exec` [S]

- `internal/cli/cmd_exec.go`: wrap the MCP tool for terminal use.
- `--lang`, `--intent`, `--timeout`, `--return` flags.

**Depends on:** P9-T06.
**Acceptance:** `dfmt exec --lang bash "echo hello"` prints `hello` and exit=0.

---

## Phase 10 — Content Store & Intent Matching (v0.9)

Goal: large sandbox outputs are chunked, indexed, and queryable. Intent-driven filtering returns targeted excerpts, not raw output.

### P10-T01 — Content store skeleton [M]

- `internal/content/store.go`: `Store` struct with LRU eviction, byte cap.
- `chunkset.go`: `ChunkSet` lifecycle, TTL handling.
- In-memory only in this task.

**Depends on:** P0-T06.
**Acceptance:** `TestContentStoreLRU` evicts oldest set under pressure; `TestContentStoreTTL` respects session-ephemeral default.

### P10-T02 — Chunkers [L]

- `internal/sandbox/chunk.go`: four chunkers — markdown (by heading), text (by paragraph or N lines), json (by top-level keys, nested), log (by N-line groups or timestamp gaps).
- MIME-type detection for auto-selection.
- Preservation of code fences in markdown chunks.

**Depends on:** P10-T01.
**Acceptance:** `TestChunkMarkdown`, `TestChunkJSON`, `TestChunkLogLines` produce chunks matching golden expectations.

### P10-T03 — Content-side index [M]

- `internal/content/index.go`: wrap a `core.Index` instance scoped to content.
- Per-set posting list; merged-search across sets.
- Clear on daemon exit unless `ttl: forever`.

**Depends on:** P10-T01, P1-T08.
**Acceptance:** `TestContentSearch` finds chunks by content text.

### P10-T04 — Intent matching pipeline [M]

- `internal/sandbox/intent.go`: given intent string + chunks, run BM25 against just this chunk set, return top-K matches with surrounding context.
- Vocabulary extraction: top-K terms in chunks not in intent.

**Depends on:** P10-T03.
**Acceptance:** `TestIntentMatch` against a 10 KB log returns the 3-5 lines matching the intent with vocabulary of 8-12 additional searchable terms.

### P10-T05 — Exec + content integration [M]

- `dfmt.exec` with large output now chunks → stores → indexes → optionally intent-matches.
- Response carries `chunk_set`, `matches`, `vocabulary`, `summary`.

**Depends on:** P9-T05, P10-T02, P10-T04.
**Acceptance:** `TestExecLargeOutputWithIntent` shows end-to-end: raw 50 KB output produces 2 KB response with relevant excerpts.

### P10-T06 — MCP tools for content retrieval [M]

- `dfmt.search_content`, `dfmt.get_chunk`, `dfmt.list_chunks`.
- HTTP endpoints: `/v1/content/search`, `/v1/content/chunks/{id}`, `/v1/content/sets/{id}`.

**Depends on:** P10-T03.
**Acceptance:** Agent can follow up on a prior exec call by searching its content, fetching specific chunks.

### P10-T07 — Content persistence (optional) [M]

- `internal/content/persist.go`: on opt-in (`ttl: forever`), write set to `<proj>/.dfmt/content/<set-id>.jsonl.gz`.
- Load on daemon startup if persist dir exists.

**Depends on:** P10-T01.
**Acceptance:** `TestContentPersistRoundTrip` survives daemon restart with the persisted set intact.

### P10-T08 — CLI `dfmt content` family [S]

- `dfmt content search`, `dfmt content get`, `dfmt content list`.

**Depends on:** P10-T06.
**Acceptance:** Commands round-trip against daemon.

---

## Phase 11 — Sandbox Read, Fetch & Security (v0.9)

Goal: complete the sandbox layer. File reads and URL fetches follow the same compress-and-index pipeline. Security policy is enforced.

### P11-T01 — `dfmt.read` [M]

- `internal/sandbox/read.go`: read file, detect encoding, apply same size disposition as exec.
- Lines/byte-range selection.
- Intent-driven filtering for large files.

**Depends on:** P10-T05.
**Acceptance:** `TestReadLargeFile` with intent returns only relevant sections.

### P11-T02 — HTTP client + `dfmt.fetch` [L]

- `internal/sandbox/fetch.go`: use stdlib `net/http` with custom `Transport` for timeout, redirect cap, body cap.
- Content-type detection.
- HTML → markdown conversion (P11-T03).
- Apply size disposition.

**Depends on:** P10-T05.
**Acceptance:** `TestFetchHTMLSite` converts a fixture HTML to markdown; `TestFetchJSON` indexes by key paths; `TestFetchTimeout` enforces.

### P11-T03 — HTML → Markdown converter [L]

- `internal/sandbox/htmlmd.go`: ~400 lines of Go using stdlib `html.Tokenize` (or `x/net/html` if ADR-0008 resolves to accept it).
- Headings, paragraphs, lists, links, code, tables, bold/italic, stripping.

**Depends on:** P0-T06.
**Acceptance:** `TestHTMLMDConvert` passes golden expectations against a corpus of real HTML samples.

### P11-T04 — Security policy parser [M]

- `internal/sandbox/security.go`: parse `permissions.yaml`.
- Compile glob patterns for deny/allow rules.
- Reload on SIGHUP.

**Depends on:** P0-T07.
**Acceptance:** `TestPolicyLoad` parses fixture policies; `TestPolicyMatchRules` evaluates example commands.

### P11-T05 — Command splitting + policy evaluator [M]

- Split commands on `&&`, `||`, `;`, `|`, subshells.
- Evaluate each part independently.
- Deny-wins-over-allow at the same specificity level.
- Default conservative policy when no file present.

**Depends on:** P11-T04.
**Acceptance:** `TestCommandSplit` handles complex chained commands correctly; `TestPolicyDenyWins` asserts deny dominance.

### P11-T06 — Shell-escape detection [M]

- For non-shell langs, scan code for patterns: `os.system`, `subprocess.`, `child_process.exec`, `Runtime.exec`, `Kernel#system`, etc.
- Reject exec if pattern found.

**Depends on:** P11-T04.
**Acceptance:** `TestShellEscapeDetected` rejects Python with `os.system`; `TestShellEscapeClean` accepts normal Python code.

### P11-T07 — Credential passthrough [M]

- `internal/sandbox/passthrough.go`: per-CLI rule (gh, aws, gcloud, kubectl, docker) for which env vars / config paths to pass.
- Detect which CLI is being invoked in the code; pass only those credentials.
- Paths are made available in the subprocess via env pointers, not via file copies.

**Depends on:** P11-T04.
**Acceptance:** `TestPassthroughGH` makes `GH_TOKEN` visible only when `gh` is in the code; `TestPassthroughIsolation` confirms other CLIs get nothing.

### P11-T08 — Policy integration into exec/read/fetch [M]

- Wire policy checks into the exec/read/fetch pipelines.
- Policy failure returns MCP error code 403 with a deny-reason.

**Depends on:** P9-T03, P11-T01, P11-T02, P11-T05.
**Acceptance:** `TestPolicyBlocksSudo`, `TestPolicyAllowsGit`, `TestPolicyBlocksDotenv` all pass.

### P11-T09 — `dfmt.batch_exec` [S]

- Multi-operation batching for exec/read/fetch.
- Max 10 items per batch; items run sequentially within the batch.

**Depends on:** P11-T08.
**Acceptance:** `TestBatchExec` runs 3 reads in one call, returns 3 results.

---

## Phase 12 — Hooks for Each Agent (v0.9 → v1.0)

Goal: DFMT intercepts native tool calls on every hook-capable agent, redirects to sandbox when appropriate.

### P12-T01 — Hook dispatcher core [S]

- `internal/hook/hook.go`: `dfmt hook <agent> <event>` entry point.
- Reads stdin JSON, dispatches to per-agent handler, writes stdout JSON.

**Depends on:** P2-T05.
**Acceptance:** `dfmt hook claude-code pretooluse` reads fixture JSON, returns fixture response.

### P12-T02 — Claude Code hooks [L]

- `internal/hook/claude.go`: five hook handlers (PreToolUse, PostToolUse, PreCompact, SessionStart, UserPromptSubmit).
- PreToolUse decision policy: block Bash/Read/WebFetch/Grep/Task under thresholds, redirect to dfmt.*.
- PostToolUse: extract event (file.edit, mcp.call, error).
- PreCompact: build and return session snapshot.
- SessionStart: inject snapshot on compact or resume.
- UserPromptSubmit: capture prompt + decision extraction.

**Depends on:** P12-T01, P4-T03.
**Acceptance:** Real Claude Code session exhibits routing: `Read(*.log)` gets redirected, a large PostToolUse stores event, PreCompact produces snapshot.

### P12-T03 — Gemini CLI hooks [M]

- `internal/hook/gemini.go`: BeforeTool, AfterTool, PreCompress, SessionStart.
- Same decision policies adapted to Gemini's hook payload shape.

**Depends on:** P12-T01, P4-T03.
**Acceptance:** Real Gemini CLI session routes correctly.

### P12-T04 — VS Code Copilot hooks [M]

- `internal/hook/copilot.go`: PreToolUse, PostToolUse, SessionStart, PreCompact.

**Depends on:** P12-T01.
**Acceptance:** Real VS Code Copilot session routes correctly.

### P12-T05 — OpenCode plugin [L]

- `plugins/opencode-plugin/`: TypeScript plugin wrapping `tool.execute.before`, `tool.execute.after`, `experimental.session.compacting`.
- Plugin calls `dfmt hook opencode <event>` as child process.
- Package as `@dfmt/opencode-plugin` (npm).
- Release workflow publishes on each tag.

**Depends on:** P12-T01.
**Acceptance:** OpenCode session with plugin installed routes correctly.

### P12-T06 — Hook integration tests [M]

- Fixture JSON payloads for each agent's hook events.
- Tests assert correct event translation, correct decision for blocking, correct response shape.

**Depends on:** P12-T02 through P12-T05.
**Acceptance:** `TestHookFixtures` passes for every agent × event combination.

---

## Phase 13 — `dfmt setup` & Per-Agent Configuration (v1.0)

Goal: one command configures every installed agent on the user's machine.

### P13-T01 — Agent detection [M]

- `internal/setup/detect.go`: filesystem probes per agent.
- Returns list of detected agents with confidence level.
- Honors `--agent` override.

**Depends on:** P0-T02.
**Acceptance:** `TestDetectClaudeCode`, `TestDetectCodex`, etc., against fake HOME directories, correctly identify each agent.

### P13-T02 — Config merging primitives [L]

- `internal/setup/writer.go`: `MergeJSON`, `MergeYAML`, `MergeTOML`.
- Each preserves original formatting, respects DFMT's version markers, backs up before write.
- TOML parser/emitter bundled (supports Codex's subset, ~300 lines).

**Depends on:** P0-T07.
**Acceptance:** `TestMergeJSONIdempotent`, `TestMergeYAMLPreserveUnknown`, `TestMergeTOMLCodex` all pass.

### P13-T03 — Manifest format [S]

- `internal/setup/manifest.go`: record every file and version-marked block DFMT writes.
- Used by `--uninstall` and `--verify`.

**Depends on:** P13-T02.
**Acceptance:** `TestManifestRoundTrip` reads back the same structure.

### P13-T04 — Instruction templates [M]

- `internal/setup/templates/*.tmpl`: embed via `go:embed`.
- Base template + per-agent voice adjustments.
- Tool-mapping table renders with agent-specific native tool names.

**Depends on:** P0-T02.
**Acceptance:** `TestTemplateRenderClaudeMD` produces expected CLAUDE.md section; same for each agent.

### P13-T05 — Claude Code configurer [M]

- `internal/setup/claude.go`: write `~/.claude/mcp.json`, `~/.claude/settings.json` hooks, project `CLAUDE.md`.
- Idempotent; respects foreign content.

**Depends on:** P13-T02, P13-T03, P13-T04.
**Acceptance:** `TestSetupClaudeCode` against fake `~/.claude` produces expected file contents and manifest entries.

### P13-T06 — Remaining agent configurers [L]

- Gemini CLI, VS Code Copilot, Codex CLI, Cursor, OpenCode, Zed, Continue.dev, Windsurf.
- One file each under `internal/setup/`.
- Integration tests mimicking each agent's config layout.

**Depends on:** P13-T05.
**Acceptance:** `TestSetupEachAgent` passes for all 9 agents against fake directories.

### P13-T07 — `dfmt setup` command [M]

- `internal/cli/cmd_setup.go`: detect → confirm → configure → manifest.
- `--dry-run`, `--agent`, `--force`, `--uninstall`, `--verify` flags.
- Interactive confirmation prompt (skippable with `--yes`).

**Depends on:** P13-T06.
**Acceptance:** `dfmt setup --dry-run` lists planned changes; `dfmt setup` (with `--yes`) applies them; `dfmt setup --verify` confirms; `dfmt setup --uninstall` reverts.

### P13-T08 — Post-setup verification [M]

- `dfmt setup --verify`: re-read each agent's config, assert DFMT entries are well-formed.
- Attempt MCP probe where supported.
- Report per-agent status with diagnostic hints.

**Depends on:** P13-T07.
**Acceptance:** On a correctly-configured system, `--verify` reports all-green. On a system where a user manually damaged one agent's config, `--verify` identifies which agent and which file.

### P13-T09 — Generic agent output [S]

- `dfmt setup --generic` produces a tarball of MCP entry, instruction file, README for users of unsupported agents.

**Depends on:** P13-T04.
**Acceptance:** Tarball contents are usable as a manual integration guide.

---

## Phase 14 — HTTP Transport & Advanced Features (was Phase 7)

[Existing Phase 7 content renumbered to Phase 14]

### P14-T01 — HTTP server [M]

- `internal/transport/http.go`: all endpoints from spec §8.2 (including sandbox endpoints from Phase 9-11).
- Port 0 resolution; write chosen port to `.dfmt/daemon.port`.
- Localhost-only by default.

**Depends on:** P2-T02, P9-T06, P10-T06, P11-T02.
**Acceptance:** `TestHTTPEndpoints` covers every route.

### P14-T02 — SSE event stream [M]

- `GET /v1/events` as Server-Sent Events.

**Depends on:** P14-T01.
**Acceptance:** `TestHTTPEventStream` asserts live streaming.

### P14-T03 — Metrics endpoint [M]

- `GET /v1/metrics` in Prometheus text format.
- Include sandbox metrics: exec count/latency/exit-code distribution; content store size; cache hits/misses.

**Depends on:** P14-T01.
**Acceptance:** `curl /v1/metrics | grep dfmt_sandbox_exec_duration` returns a histogram.

### P14-T04 — Redaction layer [M]

- `internal/redact/redact.go`: regex patterns + user-provided.
- Runs before journal write AND before content store write.

**Depends on:** P1-T01, P10-T01.
**Acceptance:** `TestRedactSecrets` across both stores.

---

## Phase 15 — Benchmarks & Performance (was Phase 8)

### P15-T01 — Benchmark suite [M]

[Original P8-T01 content, extended with sandbox benchmarks]

**Acceptance:** `make bench` runs and produces numbers.

### P15-T02 — Baseline + regression gate [M]

[Original P8-T02 content]

### P15-T03 — Hit all targets [L]

- Optimize until every row in SPEC §11 meets p50 and p99 targets.
- Focus particular attention on sandbox exec + intent-match, which are new and unoptimized.

**Acceptance:** Every target green.

---

### P9-T01 — Windows FS watcher polish [M]

- Handle edge cases: long paths, UNC paths, junction points.
- Named pipe ACL verification.

**Depends on:** P3-T08.
**Acceptance:** `TestWindowsPaths` passes; ACL test confirms owner-only access.

### P9-T02 — Arm64 testing [M]

- Spot-check macOS arm64 and linux arm64 runs.
- Fix any byte-order assumptions (there shouldn't be any in Go, but verify).

**Depends on:** P0-T05.
**Acceptance:** CI matrix green across all 7 target triples.

### P9-T03 — Install scripts [M]

- `scripts/install.sh` (POSIX): detect OS/arch, download, verify signature, install to `~/.local/bin`.
- `scripts/install.ps1` for Windows: equivalent.
- Hosted at `dfmt.dev/install.sh` via landing page.

**Depends on:** P0-T05 release workflow.
**Acceptance:** `curl -fsSL https://dfmt.dev/install.sh | sh` works on Linux and macOS.

### P9-T04 — Homebrew tap [S]

- `ersinkoc/homebrew-tap` repo, `Formula/dfmt.rb`.
- Auto-updated by release workflow via PR.

**Depends on:** P9-T03.
**Acceptance:** `brew install ersinkoc/tap/dfmt` works.

### P9-T05 — Scoop bucket [S]

- `ersinkoc/scoop-bucket` for Windows.
- Manifest generated and updated by release workflow.

**Depends on:** P9-T03.
**Acceptance:** `scoop install ersinkoc/dfmt` works.

### P9-T06 — Landing page [M]

- `dfmt.dev` static site.
- Install one-liners for each platform, demo gif, link to docs.
- Matches `ersinkoc` brand language (cf. DFMC site).

**Depends on:** P9-T03.
**Acceptance:** Site deployed, install links functional.

### P9-T07 — README polish [M]

- Project README with: 30-second pitch, install per platform, quickstart, agent config snippets for each supported agent, link to SPEC/IMPLEMENTATION/ADRs.
- Matches DFMC / Karadul / NothingDNS style.

**Depends on:** P9-T06.
**Acceptance:** A new user lands on the repo and can install + use DFMT in under 2 minutes.

### P9-T08 — v1.0.0 release [S]

- Tag, release notes, announcement posts.

**Depends on:** all.
**Acceptance:** v1.0.0 tag exists; release has signed binaries for 7 targets; announcement thread on X.

---

## Phase 16 — Cross-Platform & Release (v1.0)

Goal: signed binaries on every target platform, install scripts, package manager publishing, landing page, README.

### P16-T01 — Windows FS watcher polish [M]

- Handle edge cases: long paths, UNC paths, junction points.
- Named pipe ACL verification.

**Depends on:** P3-T08.
**Acceptance:** `TestWindowsPaths` passes; ACL test confirms owner-only access.

### P16-T02 — Arm64 testing [M]

- Spot-check macOS arm64 and linux arm64 runs.
- Fix any byte-order assumptions (there shouldn't be any in Go, but verify).

**Depends on:** P0-T05.
**Acceptance:** CI matrix green across all 7 target triples.

### P16-T03 — Install scripts [M]

- `scripts/install.sh` (POSIX): detect OS/arch, download, verify signature, install to `~/.local/bin`.
- `scripts/install.ps1` for Windows.
- Hosted at `dfmt.dev/install.sh` via landing page.

**Depends on:** P0-T05 release workflow.
**Acceptance:** `curl -fsSL https://dfmt.dev/install.sh | sh` works on Linux and macOS.

### P16-T04 — Homebrew tap [S]

- `ersinkoc/homebrew-tap` repo, `Formula/dfmt.rb`.
- Auto-updated by release workflow via PR.

**Depends on:** P16-T03.
**Acceptance:** `brew install ersinkoc/tap/dfmt` works.

### P16-T05 — Scoop bucket [S]

- `ersinkoc/scoop-bucket` for Windows.
- Manifest generated and updated by release workflow.

**Depends on:** P16-T03.
**Acceptance:** `scoop install ersinkoc/dfmt` works.

### P16-T06 — OpenCode npm plugin publishing [S]

- Publish `@dfmt/opencode-plugin` on each release tag.
- Version-pinned to match the DFMT binary.

**Depends on:** P12-T05.
**Acceptance:** `npm install @dfmt/opencode-plugin@<version>` works.

### P16-T07 — Landing page [M]

- `dfmt.dev` static site.
- Install one-liners for each platform, demo gif, link to docs.
- Matches `ersinkoc` brand language.

**Depends on:** P16-T03.
**Acceptance:** Site deployed, install links functional.

### P16-T08 — README polish [M]

- Project README with: 30-second pitch, install per platform, quickstart, `dfmt setup` example, agent config snippets, link to SPEC/IMPLEMENTATION/AGENT-INTEGRATION.
- Matches DFMC / Karadul / NothingDNS style.

**Depends on:** P16-T07.
**Acceptance:** A new user lands on the repo and can install + run `dfmt setup` in under 2 minutes.

### P16-T09 — v1.0.0 release [S]

- Tag, release notes, announcement posts.

**Depends on:** all prior phases.
**Acceptance:** v1.0.0 tag exists; release has signed binaries for 7 targets; announcement thread on X; DFMT works out of the box on all 9 supported agents.

**🎯 Milestone M4 (v1.0 Release) reached after P16-T09.**

---

## Cross-Cutting Concerns

These are not phase-specific; they run throughout.

### X-T01 — Documentation updates per feature

Every feature-adding task updates `README.md` quickstart, `docs/` reference pages, `AGENT-INTEGRATION.md` where relevant, and if architectural, an ADR.

### X-T02 — Schema docs for each event type

`docs/schemas/events/<type>.md` for every event type defined in spec §6.2. Schema, examples, capture sources. Written as each type is implemented.

### X-T03 — Changelog

`CHANGELOG.md` following Keep-a-Changelog format. Every PR merged to main appends an entry.

### X-T04 — Security review at M3 and before v1.0

- At M3: review redaction patterns, socket permissions, HTTP bind defaults.
- Before v1.0: review sandbox security policy, credential passthrough, shell-escape detection, permission rule evaluation. Produce `SECURITY.md` with the threat model, attack surface analysis, and known limitations.

### X-T05 — Dogfood

Run DFMT on its own development starting at M1 POC. Run the sandbox on its own development starting at v0.9. Iterate based on what's missing for the maintainer's own workflow.

### X-T06 — Benchmark baseline updates

Each phase that adds a performance-relevant component updates `bench/baseline.json` with the measured numbers from that phase, so future regressions are detected.

---

## Task Summary

| Phase | Title | Tasks | Size sum | Milestone |
| --- | --- | --- | --- | --- |
| 0 | Repository Scaffold | 8 | ~3 days | M0 Scaffold |
| 1 | Core Storage & Index | 10 | ~7 days | — |
| 2 | POC Transport | 8 | ~4 days | **M1 POC** |
| 3 | Capture Layer | 10 | ~8 days | — |
| 4 | Retrieval | 8 | ~4 days | — |
| 5 | Daemon Lifecycle & MCP | 8 | ~5 days | **M2 MVP** |
| 6 | Complete CLI Surface | 10 | ~3 days | — |
| 9 | Sandbox Core | 7 | ~4 days | — |
| 10 | Content Store & Intent Matching | 8 | ~5 days | — |
| 11 | Sandbox Read, Fetch & Security | 9 | ~6 days | **v0.9 Sandbox** |
| 12 | Hooks for Each Agent | 6 | ~5 days | — |
| 13 | `dfmt setup` & Per-Agent Config | 9 | ~6 days | — |
| 14 | HTTP Transport & Advanced | 4 | ~2 days | — |
| 15 | Benchmarks & Performance | 3 | ~3 days | **M3** |
| 16 | Cross-Platform & Release | 9 | ~4 days | **M4 v1.0** |

Total raw effort: ~69 days of focused work. Compressed substantially with Claude Code parallelization on independent tasks (capture sources, per-agent hooks, per-agent configurers are all independently parallelizable).

Reasonable calendar target:

- **M1 POC** — week 2
- **M2 MVP** — week 5
- **v0.9 Sandbox** — week 7
- **M3 (benchmarks + HTTP)** — week 8
- **M4 v1.0 Release** — week 10-11

Phases 9-11 (Sandbox, Content, Read/Fetch/Security) are the largest new investment, roughly 15 days total. Phase 12 (Hooks) and Phase 13 (Setup) are the user-facing multi-agent work that makes the sandbox actually accessible across every supported agent — together another 11 days. Phases 14-16 wrap up with the polish, packaging, and release motions.

---

*End of task breakdown.*
