# DFMT — Implementation Plan

| Field | Value |
| --- | --- |
| Document | `IMPLEMENTATION.md` |
| Status | Draft — v0.1 |
| Target Spec | `SPECIFICATION.md` v0.7 |
| Language | Go 1.22+ |
| Date | 2026-04-20 |

This document translates `SPECIFICATION.md` into a concrete Go codebase structure. It defines package boundaries, the core interfaces, the key type definitions, and implementation notes for non-obvious components. It does not reproduce the specification; read the spec first.

The scope is v1. Anything marked `// TODO(v1.x)` in the code is allowed to be a stub now.

---

## 1. Repository Layout

```
dfmt/
├── cmd/
│   ├── dfmt/                    # main binary entry point
│   │   └── main.go
│   └── dfmt-bench/              # benchmark runner (separate binary)
│       └── main.go
│
├── internal/
│   ├── core/                    # section 3 of this doc
│   │   ├── event.go             # Event type, EventType enum, Priority enum
│   │   ├── ulid.go              # ULID generator (bundled)
│   │   ├── journal.go           # append-only JSONL writer/reader
│   │   ├── journal_rotate.go    # segment rotation
│   │   ├── index.go             # in-memory inverted index
│   │   ├── index_persist.go     # gob serialization
│   │   ├── tokenize.go          # lowercase + split + stopword
│   │   ├── porter.go            # Porter stemmer (bundled)
│   │   ├── bm25.go              # BM25 scorer
│   │   ├── trigram.go           # trigram posting list + query
│   │   ├── levenshtein.go       # fuzzy correction
│   │   ├── classifier.go        # priority tier assignment
│   │   └── stopwords/           # embedded stopword files
│   │       ├── en.txt
│   │       └── tr.txt
│   │
│   ├── project/
│   │   ├── discover.go          # find project root (section 6.6 of spec)
│   │   ├── registry.go          # projects.jsonl read/write
│   │   └── id.go                # project ID (8-hex SHA-256 of path)
│   │
│   ├── config/
│   │   ├── config.go            # struct definitions
│   │   ├── load.go              # global + project merge, yaml.v3
│   │   └── defaults.go          # default values
│   │
│   ├── capture/
│   │   ├── capturer.go          # Capturer interface, EventSink interface
│   │   ├── mcp.go               # MCP capture (in-daemon)
│   │   ├── fswatch.go           # dispatches to platform-specific files
│   │   ├── fswatch_linux.go     # inotify via x/sys/unix
│   │   ├── fswatch_darwin.go    # kqueue via x/sys/unix
│   │   ├── fswatch_freebsd.go   # kqueue
│   │   ├── fswatch_windows.go   # ReadDirectoryChangesW via x/sys/windows
│   │   ├── gitignore.go         # minimal .gitignore parser
│   │   ├── git.go               # git event envelope (for `dfmt capture git ...`)
│   │   ├── shell.go             # shell event envelope
│   │   └── cli.go               # CLI capture dispatch
│   │
│   ├── sandbox/                 # NEW — section 7 of this doc
│   │   ├── sandbox.go           # Sandbox interface; entry points for exec/read/fetch
│   │   ├── exec.go              # subprocess executor with runtime dispatch
│   │   ├── exec_unix.go         # setrlimit, setsid, signal handling (Unix)
│   │   ├── exec_windows.go      # job objects, CreateProcess (Windows)
│   │   ├── runtime.go           # runtime detection + caching
│   │   ├── read.go              # file read with intent-driven filtering
│   │   ├── fetch.go             # URL fetch + HTML→markdown conversion
│   │   ├── htmlmd.go            # markdown walker (consumes htmltok output)
│   │   ├── htmltok.go           # bundled HTML tokenizer (ADR-0008)
│   │   ├── chunk.go             # chunking strategies per content type
│   │   ├── summarize.go         # summary generation (exit codes, top phrases)
│   │   ├── intent.go            # intent-matching pipeline
│   │   ├── security.go          # permissions.yaml parser + policy evaluator
│   │   └── passthrough.go       # credential passthrough rules
│   │
│   ├── content/                 # NEW
│   │   ├── store.go             # ephemeral in-memory chunk store with LRU
│   │   ├── chunkset.go          # ChunkSet lifecycle
│   │   ├── persist.go           # optional on-disk persistence (gzipped JSONL)
│   │   └── index.go             # content-side Index instance (reuses core.Index)
│   │
│   ├── hook/                    # NEW — agent hook dispatchers
│   │   ├── hook.go              # shared entry point for `dfmt hook <agent> <event>`
│   │   ├── claude.go            # claude-code events
│   │   ├── gemini.go            # gemini-cli events
│   │   ├── copilot.go           # vscode-copilot events
│   │   └── opencode.go          # opencode events
│   │
│   ├── setup/                   # NEW — `dfmt setup` command implementation
│   │   ├── setup.go             # detection + orchestration
│   │   ├── detect.go            # agent probes
│   │   ├── writer.go            # config merge + write with version markers
│   │   ├── manifest.go          # setup-manifest.json
│   │   └── templates/           # embedded instruction-file templates (go:embed)
│   │       ├── claude.md.tmpl
│   │       ├── gemini.md.tmpl
│   │       ├── agents.md.tmpl
│   │       ├── copilot-instructions.md.tmpl
│   │       └── cursorrules.tmpl
│   │
│   ├── retrieve/
│   │   ├── snapshot.go          # budget-aware snapshot builder
│   │   ├── render_xml.go
│   │   ├── render_json.go
│   │   ├── render_md.go
│   │   ├── render_guide.go      # Session Guide prose renderer
│   │   ├── search.go            # three-layer search facade (events + content)
│   │   ├── throttle.go          # progressive throttling state
│   │   └── dedup.go             # in-window event deduplication
│   │
│   ├── transport/
│   │   ├── jsonrpc.go           # JSON-RPC 2.0 codec
│   │   ├── mcp.go               # MCP protocol over jsonrpc/stdio
│   │   ├── socket.go            # Unix socket / named pipe
│   │   ├── http.go              # optional HTTP server
│   │   └── handlers.go          # shared business logic all transports call
│   │
│   ├── daemon/
│   │   ├── daemon.go            # Daemon struct, lifecycle
│   │   ├── lock.go              # flock-based single-daemon guard
│   │   ├── autostart.go         # double-fork spawn (called by client)
│   │   ├── idle.go              # idle-exit ticker
│   │   └── signals.go           # SIGTERM/SIGINT graceful shutdown
│   │
│   ├── client/
│   │   ├── client.go            # auto-start + connect + JSON-RPC call
│   │   └── discover.go          # socket path resolution
│   │
│   ├── cli/
│   │   ├── dispatch.go          # subcommand router
│   │   ├── flags.go             # minimal flag parser
│   │   ├── cmd_daemon.go        # dfmt daemon
│   │   ├── cmd_stop.go
│   │   ├── cmd_status.go
│   │   ├── cmd_list.go
│   │   ├── cmd_doctor.go
│   │   ├── cmd_init.go
│   │   ├── cmd_remember.go
│   │   ├── cmd_note.go
│   │   ├── cmd_task.go
│   │   ├── cmd_capture.go       # dfmt capture git|shell|env.cwd|...
│   │   ├── cmd_recall.go
│   │   ├── cmd_search.go
│   │   ├── cmd_stats.go
│   │   ├── cmd_tail.go
│   │   ├── cmd_mcp.go           # run MCP stdio server
│   │   ├── cmd_hook.go          # dfmt hook <agent> <event>
│   │   ├── cmd_setup.go         # dfmt setup (multi-agent config)
│   │   ├── cmd_exec.go          # dfmt exec — sandbox exec from CLI
│   │   ├── cmd_read.go          # dfmt read — sandbox read from CLI
│   │   ├── cmd_fetch.go         # dfmt fetch — sandbox fetch from CLI
│   │   ├── cmd_content.go       # dfmt content search|get|list
│   │   ├── cmd_install_hooks.go
│   │   ├── cmd_shell_init.go
│   │   ├── cmd_config.go
│   │   ├── cmd_export.go
│   │   ├── cmd_import.go
│   │   ├── cmd_rotate.go
│   │   ├── cmd_reindex.go
│   │   └── cmd_purge.go
│   │
│   ├── redact/
│   │   └── redact.go            # secret pattern redaction
│   │
│   └── logging/
│       └── logger.go            # slog-based setup
│
├── docs/
│   ├── adr/
│   │   ├── 0000-adr-process.md
│   │   ├── 0001-per-project-daemon.md
│   │   ├── 0002-mit-license.md
│   │   ├── 0003-jsonl-and-custom-index.md
│   │   ├── 0004-stdlib-only-deps.md
│   │   ├── 0005-multi-source-capture.md
│   │   ├── 0006-sandbox-scope.md
│   │   ├── 0007-content-store-separation.md
│   │   └── 0008-html-parser.md
│   ├── schemas/
│   │   └── events/              # per-type event schemas (markdown)
│   └── hooks/
│       ├── git-post-commit.sh
│       ├── git-post-checkout.sh
│       ├── git-pre-push.sh
│       ├── git-post-merge.sh
│       ├── zsh.sh
│       ├── bash.sh
│       └── fish.fish
│
├── bench/
│   └── bench_test.go            # benchmarks gating performance targets
│
├── testdata/
│   ├── journals/                # fixture journals for golden tests
│   └── corpora/                 # replay corpora for ranking regressions
│
├── go.mod
├── go.sum
├── Makefile
├── .golangci.yml
├── .github/
│   └── workflows/
│       ├── ci.yml
│       └── release.yml
├── SPECIFICATION.md
├── IMPLEMENTATION.md
├── TASKS.md
├── AGENT-INTEGRATION.md
├── BRANDING.md
├── README.md
├── LICENSE                      # MIT
└── CHANGELOG.md
```

No `vendor/` directory. No `pkg/` directory. Everything that is not `cmd/` or a top-level doc is in `internal/` so that nothing leaks as a public API. DFMT is a binary, not a library.

---

## 2. Module and Build Configuration

### 2.1 `go.mod`

```
module github.com/ersinkoc/dfmt

go 1.22

require (
    golang.org/x/sys v0.x.y
    golang.org/x/crypto v0.x.y       // reserved; imported in v1.2 for at-rest encryption
    gopkg.in/yaml.v3 v3.0.1
)
```

`go.sum` is committed. Dep updates go through a PR with an ADR noting the reason.

### 2.2 Build targets

`Makefile` provides:

```
build           # go build with standard ldflags
release         # cross-compile release binaries
test            # go test ./...
bench           # run benchmarks, fail if regressed vs baseline
lint            # golangci-lint
fmt             # gofmt -s -w
install         # go install
clean
```

Release build command:

```
CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)" \
  -o dist/dfmt \
  ./cmd/dfmt
```

Cross-compile targets in `release.yml` GitHub workflow: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, `windows/arm64`, `freebsd/amd64`. Each binary is signed with cosign (keyless, OIDC).

### 2.3 `.golangci.yml`

Strict. `errcheck`, `gosimple`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gofmt`, `goimports`, `misspell`, `revive`, `gocyclo` (threshold 15), `funlen` (60 lines), `gocognit` (20). Warnings are errors in CI.

---

## 3. Core Package (`internal/core`)

### 3.1 Event Type

```go
package core

import "time"

type EventType string

const (
    EvtFileRead    EventType = "file.read"
    EvtFileEdit    EventType = "file.edit"
    EvtFileCreate  EventType = "file.create"
    EvtFileDelete  EventType = "file.delete"
    EvtTaskCreate  EventType = "task.create"
    EvtTaskUpdate  EventType = "task.update"
    EvtTaskDone    EventType = "task.done"
    EvtDecision    EventType = "decision"
    EvtError       EventType = "error"
    EvtGitCommit   EventType = "git.commit"
    EvtGitCheckout EventType = "git.checkout"
    EvtGitPush     EventType = "git.push"
    EvtGitStash    EventType = "git.stash"
    EvtGitDiff     EventType = "git.diff"
    EvtEnvCwd      EventType = "env.cwd"
    EvtEnvVars     EventType = "env.vars"
    EvtEnvInstall  EventType = "env.install"
    EvtPrompt      EventType = "prompt"
    EvtMCPCall     EventType = "mcp.call"
    EvtSubagent    EventType = "subagent"
    EvtSkill       EventType = "skill"
    EvtRole        EventType = "role"
    EvtIntent      EventType = "intent"
    EvtDataRef     EventType = "data.ref"
    EvtNote        EventType = "note"
    EvtTombstone   EventType = "tombstone"
)

type Priority string

const (
    PriP1 Priority = "p1"
    PriP2 Priority = "p2"
    PriP3 Priority = "p3"
    PriP4 Priority = "p4"
)

type Source string

const (
    SrcMCP     Source = "mcp"
    SrcFSWatch Source = "fswatch"
    SrcGitHook Source = "githook"
    SrcShell   Source = "shell"
    SrcCLI     Source = "cli"
)

type Event struct {
    ID       string            `json:"id"`
    TS       time.Time         `json:"ts"`
    Project  string            `json:"project"`
    Type     EventType         `json:"type"`
    Priority Priority          `json:"priority"`
    Source   Source            `json:"source"`
    Actor    string            `json:"actor,omitempty"`
    Data     map[string]any    `json:"data,omitempty"`
    Refs     []string          `json:"refs,omitempty"`
    Tags     []string          `json:"tags,omitempty"`
    Sig      string            `json:"sig"`
}
```

Event is marshaled to canonical JSON — field order is controlled by struct tag order, map keys within `Data` are sorted at marshal time via a custom encoder helper. The `Sig` field is the first 16 hex chars of the SHA-256 of the canonical JSON of the event with `Sig` set to empty string.

### 3.2 Journal Interface

```go
type Journal interface {
    Append(ctx context.Context, e Event) error
    Stream(ctx context.Context, from string) (<-chan Event, error)
    Checkpoint(ctx context.Context) (string, error)
    Rotate(ctx context.Context) error
    Close() error
}

type journalImpl struct {
    path     string
    file     *os.File
    mu       sync.Mutex
    durable  bool
    batchMS  int
    pending  []Event
    rotation struct {
        maxBytes int64
        compress bool
    }
    hiCursor string // last appended ULID
}

func OpenJournal(path string, opt JournalOptions) (Journal, error)
```

Key implementation notes:

- `Append` writes under `j.mu`. In durable mode, writes are flushed with `file.Sync()` on each append. In batched mode, writes are buffered and `Sync()`'d by a goroutine every `batchMS`.
- `Stream` opens a second read-only handle and scans. It does not share the write handle. For live tailing, it falls back to polling the file size every 100 ms after reaching EOF; stdlib alone does not offer `inotify`-on-file cleanly, and using the `fswatch` package here would be architecturally backwards.
- `Rotate` is called by the caller (journal size check happens at Append time; when over `maxBytes`, `Append` returns special error, caller invokes `Rotate`). Rotation closes the file, renames to `journal.<ULID>.jsonl`, gzips in a goroutine, opens a new `journal.jsonl`.

### 3.3 Index

```go
type PostingList struct {
    IDs []string  // ULIDs, sorted
    TFs []uint16  // term frequencies, parallel to IDs
}

type Index struct {
    mu         sync.RWMutex
    stemPL     map[string]*PostingList
    trigramPL  map[string]*PostingList
    docLen     map[string]int
    avgDocLen  float64
    total      int
    tokenVer   int // bumped on tokenizer change, forces rebuild
}

type IndexSearch struct {
    Term    string
    Type    string // "stem" | "trigram"
    Results []ScoredHit
}

type ScoredHit struct {
    ID    string
    Score float64
    Layer int
}

func NewIndex() *Index
func (ix *Index) Add(e Event)
func (ix *Index) Remove(id string)            // for tombstones
func (ix *Index) SearchBM25(query string, limit int, k1, b float64) []ScoredHit
func (ix *Index) SearchTrigram(query string, limit int) []ScoredHit
func (ix *Index) Persist(path string) error
func LoadIndex(path string) (*Index, error)
```

Posting lists are kept sorted by ULID (which is chronological). BM25 iterates over the smallest posting list first and accumulates scores in a `map[string]float64`. Heap of top-K results at the end.

### 3.4 Tokenizer

```go
func Tokenize(s string, stopwords map[string]struct{}) []string {
    // 1. unicode lowercase (strings.ToLower is fine; no locale-specific need)
    // 2. split on non-(letter|digit|underscore) using unicode.Is*
    // 3. drop tokens of length <2 or >64
    // 4. drop stopwords
    // 5. caller applies stemming separately (core/porter.go)
}
```

Stopwords are loaded from embedded files via `//go:embed stopwords/en.txt stopwords/tr.txt`. Users can add custom stopwords via `config.yaml`'s `index.stopwords_path`.

### 3.5 BM25

```go
// Okapi BM25 with Lucene-compatible defaults.
// k1 controls term-frequency saturation. b controls length normalization.
func scoreBM25(tf int, docLen int, avgDocLen float64, df, N int, k1, b float64) float64 {
    idf := math.Log(float64(N-df)+0.5)/(float64(df)+0.5) + 1.0) // smoothed; avoids negatives
    norm := float64(tf) * (k1 + 1) / (float64(tf) + k1*(1 - b + b*float64(docLen)/avgDocLen))
    return idf * norm
}
```

Heading boost is applied at index-build time by multiplying the TF of heading-origin tokens by 5 before storing. The scorer itself knows nothing about it.

### 3.6 Classifier

```go
type RuleMatch struct {
    Type         EventType
    PathGlob     string
    MessageRegex *regexp.Regexp
}

type Rule struct {
    Match    RuleMatch
    Priority Priority
}

type Classifier struct {
    defaults map[EventType]Priority
    rules    []Rule
}

func (c *Classifier) Classify(e Event) Priority
```

The classifier is built from the embedded defaults table plus the user's `priority.yaml` rules. First matching rule wins; falls through to default.

---

## 4. Project and Config (`internal/project`, `internal/config`)

### 4.1 Project Discovery

```go
func Discover(cwd string) (root string, err error) {
    if env := os.Getenv("DFMT_PROJECT"); env != "" {
        return env, nil
    }
    if r := walkUp(cwd, ".dfmt"); r != "" {
        return r, nil
    }
    if r := walkUp(cwd, ".git"); r != "" {
        return r, nil
    }
    return cwd, nil
}

func ShortID(path string) string {
    h := sha256.Sum256([]byte(path))
    return hex.EncodeToString(h[:4]) // 8 hex chars
}
```

### 4.2 Config

Two-level merge: global (`$XDG_DATA_HOME/dfmt/config.yaml`) then project (`<proj>/.dfmt/config.yaml`). Project wins on every conflict. Unknown keys are preserved on round-trip (yaml.v3 `Node` capture).

```go
type Config struct {
    Version int `yaml:"version"`
    Capture struct {
        MCP struct {
            Enabled bool `yaml:"enabled"`
        } `yaml:"mcp"`
        FS struct {
            Enabled    bool     `yaml:"enabled"`
            Watch      []string `yaml:"watch"`
            Ignore     []string `yaml:"ignore"`
            DebounceMS int      `yaml:"debounce_ms"`
        } `yaml:"fs"`
        Git   struct{ Enabled bool `yaml:"enabled"` } `yaml:"git"`
        Shell struct{ Enabled bool `yaml:"enabled"` } `yaml:"shell"`
    } `yaml:"capture"`
    Storage struct {
        Durability       string `yaml:"durability"` // "durable" | "batched"
        MaxBatchMS       int    `yaml:"max_batch_ms"`
        JournalMaxBytes  int64  `yaml:"journal_max_bytes"`
        CompressRotated  bool   `yaml:"compress_rotated"`
    } `yaml:"storage"`
    Retrieval struct {
        DefaultBudget int    `yaml:"default_budget"`
        DefaultFormat string `yaml:"default_format"`
        Throttle      struct {
            FirstTierCalls    int `yaml:"first_tier_calls"`
            SecondTierCalls   int `yaml:"second_tier_calls"`
            ResultsFirstTier  int `yaml:"results_first_tier"`
            ResultsSecondTier int `yaml:"results_second_tier"`
        } `yaml:"throttle"`
    } `yaml:"retrieval"`
    Index struct {
        RebuildInterval string  `yaml:"rebuild_interval"` // duration
        BM25K1          float64 `yaml:"bm25_k1"`
        BM25B           float64 `yaml:"bm25_b"`
        HeadingBoost    float64 `yaml:"heading_boost"`
        StopwordsPath   string  `yaml:"stopwords_path"`
    } `yaml:"index"`
    Transport struct {
        MCP    struct{ Enabled bool `yaml:"enabled"` } `yaml:"mcp"`
        HTTP   struct {
            Enabled bool   `yaml:"enabled"`
            Bind    string `yaml:"bind"`
        } `yaml:"http"`
        Socket struct{ Enabled bool `yaml:"enabled"` } `yaml:"socket"`
    } `yaml:"transport"`
    Lifecycle struct {
        IdleTimeout     string `yaml:"idle_timeout"`
        ShutdownTimeout string `yaml:"shutdown_timeout"`
    } `yaml:"lifecycle"`
    Privacy struct {
        Telemetry         bool   `yaml:"telemetry"`
        RemoteSync        string `yaml:"remote_sync"`
        AllowNonlocalHTTP bool   `yaml:"allow_nonlocal_http"`
    } `yaml:"privacy"`
    Logging struct {
        Level  string `yaml:"level"`
        Format string `yaml:"format"`
    } `yaml:"logging"`
}
```

---

## 5. Capture Layer (`internal/capture`)

### 5.1 Capturer Interface

```go
type Capturer interface {
    Name() string
    Start(ctx context.Context, sink EventSink) error
    Stop(ctx context.Context) error
}

type EventSink interface {
    Submit(ctx context.Context, e Event) error
}
```

The daemon holds a slice of active capturers. Each is started on daemon start, stopped on daemon shutdown. Start errors are logged and the capturer is skipped; other capturers proceed.

### 5.2 FS Watcher

Three platform-specific files implement the same internal interface:

```go
type fsWatcher interface {
    Start(ctx context.Context, paths []string, excludes []string, out chan<- fsEvent) error
    Stop() error
}

type fsEvent struct {
    Path string
    Kind fsEventKind // Create, Edit, Delete
    TS   time.Time
}
```

Linux implementation (`fswatch_linux.go`):

- Uses `golang.org/x/sys/unix` for `InotifyInit1`, `InotifyAddWatch`, `Read`.
- Watches each directory matching the glob patterns.
- Recursively adds new subdirectories on `IN_CREATE | IN_ISDIR`.
- Filters events against the user's `ignore` list and a manual `.gitignore` parser.
- Debounces by `capture.fs.debounce_ms` (default 500 ms): events on the same path within the window coalesce.

Darwin/FreeBSD (`fswatch_darwin.go`, `fswatch_freebsd.go`):

- Uses `unix.Kqueue` and `unix.Kevent_t`.
- Opens a file descriptor per watched directory.
- Detects adds by comparing directory contents on change.

Windows (`fswatch_windows.go`):

- Uses `golang.org/x/sys/windows.ReadDirectoryChangesW`.
- One handle per watched directory, I/O completion port for scaling.

All three emit `fsEvent` to the same channel. `fswatch.go` (platform-neutral) translates `fsEvent` to `core.Event` and submits.

### 5.3 Git Hooks

Small shell scripts generated by `dfmt install-hooks`. Example `post-commit`:

```sh
#!/bin/sh
# dfmt-hook-version: 1
# Do not edit; regenerated by `dfmt install-hooks`
exec dfmt capture git commit "$(git log -1 --format='%H %s')" 2>/dev/null &
```

Backgrounded (`&`) and stderr-suppressed so the git operation is never blocked or affected.

`install-hooks` rules:
- Read each existing hook file; if it has `# dfmt-hook-version:` comment with a lower version, overwrite.
- If foreign (no version comment, or a comment from another tool), refuse without `--force`.
- If `--force`, write the DFMT hook, save the original as `<hook>.dfmt-backup`.

### 5.4 Shell Integration

`dfmt shell-init <shell>` emits a snippet to stdout. The user is responsible for sourcing it in their rc file. Example (zsh):

```sh
# Emitted by: dfmt shell-init zsh
autoload -U add-zsh-hook
_dfmt_precmd() {
  local st=$?
  [[ $st -ne 0 ]] && dfmt capture error "exit=$st cmd=$(fc -ln -1 | tr -d '\n')" 2>/dev/null
}
_dfmt_chpwd() { dfmt capture env.cwd "$PWD" 2>/dev/null; }
add-zsh-hook precmd _dfmt_precmd
add-zsh-hook chpwd _dfmt_chpwd
```

Bash and fish variants live in `docs/hooks/` and are emitted similarly.

---

## 6. Retrieval Layer (`internal/retrieve`)

### 6.1 Snapshot Builder

```go
type SnapshotRequest struct {
    Budget  int
    Format  string    // "xml" | "json" | "md" | "guide"
    Since   time.Time // zero = default (last prompt or 24h)
    Include []EventType
    Exclude []EventType
}

type Snapshot struct {
    Format     string
    Bytes      int
    Body       string
    EventCount int
    TierCounts map[Priority]int
}

type SnapshotBuilder struct {
    journal Journal
    config  Config
}

func (b *SnapshotBuilder) Build(ctx context.Context, req SnapshotRequest) (Snapshot, error)
```

Algorithm (matches spec §7.3.1):

1. Resolve `Since`. If zero, find the last `prompt` event's TS; fall back to `now - 24h`.
2. Scan journal backward from HEAD until `TS < Since`.
3. Group events by priority; within each priority, by type and, where appropriate, by primary path.
4. Deduplicate: same-path `file.edit`s collapse to one "edited N times, last at T" summary.
5. Pack events P1 → P2 → P3 → P4 into budget with greedy fill. Reserve ~256 bytes for the last `prompt` event, which is always included.
6. Render via format-specific renderer.

Renderers produce strings directly — no template engine. Each renderer is 100-200 lines. XML uses `encoding/xml`; JSON uses `encoding/json`; markdown and guide are direct string builders.

### 6.2 Search

```go
type SearchRequest struct {
    Query    string
    Limit    int
    Types    []EventType
    Since    time.Time
    MinScore float64
}

type SearchResult struct {
    ID      string
    Type    EventType
    TS      time.Time
    Score   float64
    Layer   int // 1, 2, or 3
    Snippet string
    Event   *Event
}

type SearchResponse struct {
    Results              []SearchResult
    Throttled            bool
    NextLayerAvailable   bool
}

type Searcher struct {
    index    *Index
    journal  Journal
    throttle *ThrottleState
}

func (s *Searcher) Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
```

Three-layer fallback:

1. BM25 on stemmed terms. Return if results ≥ `limit/2`.
2. Trigram substring search. Merge with layer-1 results, tag with `Layer: 2`.
3. If still under limit, run Levenshtein correction: for each query term, find dictionary terms with edit distance ≤ 2, substitute, re-run layer 1. Tag with `Layer: 3`.

Snippet extraction: find the first occurrence of a query term in the event's searchable text, return ±80 chars around it. Multiple matches: pick the one with highest local term density.

### 6.3 Throttle

```go
type ThrottleState struct {
    mu             sync.Mutex
    windowStart    time.Time
    callsInWindow  int
    config         ThrottleConfig
}

type ThrottleConfig struct {
    FirstTierCalls    int
    SecondTierCalls   int
    ResultsFirstTier  int
    ResultsSecondTier int
}

func (t *ThrottleState) Consume() (allowedLimit int, blocked bool)
func (t *ThrottleState) Reset()
```

Reset is called by the daemon whenever a `prompt` event is captured — a new user turn resets the search budget.

---

## 7. Transport Layer (`internal/transport`)

### 7.1 Handlers

Single source of truth for business logic. Every transport is a thin adapter.

```go
type Handlers struct {
    journal  core.Journal
    index    *core.Index
    snap     *retrieve.SnapshotBuilder
    search   *retrieve.Searcher
    throttle *retrieve.ThrottleState
    cfg      config.Config
    project  string
}

func (h *Handlers) Remember(ctx context.Context, req RememberReq) (RememberResp, error)
func (h *Handlers) Recall(ctx context.Context, req RecallReq) (RecallResp, error)
func (h *Handlers) Search(ctx context.Context, req SearchReq) (SearchResp, error)
func (h *Handlers) Stats(ctx context.Context) (StatsResp, error)
func (h *Handlers) Forget(ctx context.Context, req ForgetReq) (ForgetResp, error)
// ... and the rest
```

### 7.2 JSON-RPC 2.0

```go
type Request struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      any             `json:"id,omitempty"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      any             `json:"id,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *ErrObject      `json:"error,omitempty"`
}

type ErrObject struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}

func Serve(rw io.ReadWriter, router map[string]HandlerFunc) error
```

250 lines of stdlib Go. Serves line-delimited JSON-RPC from any `ReadWriter` — stdio, Unix socket, TCP.

### 7.3 MCP

MCP is JSON-RPC 2.0 with specific method names (`initialize`, `tools/list`, `tools/call`, `shutdown`) and a specific capabilities handshake. Implementation: ~300 lines wrapping the JSON-RPC layer.

```go
type MCPServer struct {
    handlers *Handlers
    rpc      *jsonrpc.Server
    ready    bool
}

func (m *MCPServer) Serve(in io.Reader, out io.Writer) error
```

Tools are registered with JSON Schema definitions. The server responds to `tools/list` with the schema map; `tools/call` dispatches to the appropriate `Handlers` method.

### 7.4 Socket

```go
type SocketServer struct {
    path     string
    ln       net.Listener
    handlers *Handlers
}

func (s *SocketServer) Serve(ctx context.Context) error
```

Unix socket on Linux/macOS, named pipe on Windows (via `\\.\pipe\dfmt-<project-id>`). Each connection gets one JSON-RPC session.

### 7.5 HTTP

```go
type HTTPServer struct {
    bind     string
    handlers *Handlers
    actual   string // actual bind address after port 0 resolution
}

func (s *HTTPServer) Serve(ctx context.Context) error
```

Uses stdlib `net/http` with a hand-rolled router (no third-party). Routes match spec §8.2. On port 0, the actual chosen port is written to `.dfmt/daemon.port` and logged.

---

## 8. Daemon (`internal/daemon`)

### 8.1 Daemon Struct

```go
type Daemon struct {
    project   string
    cfg       config.Config
    journal   core.Journal
    index     *core.Index
    handlers  *transport.Handlers
    capturers []capture.Capturer
    mcp       *transport.MCPServer
    socket    *transport.SocketServer
    http      *transport.HTTPServer
    idle      *IdleMonitor
    lock      *FileLock
    logger    *slog.Logger
}

func (d *Daemon) Start(ctx context.Context) error
func (d *Daemon) Stop() error
```

Startup sequence:
1. Acquire `.dfmt/lock` (flock LOCK_EX, LOCK_NB). Exit 2 on fail.
2. Write `.dfmt/daemon.pid`.
3. Load config.
4. Open journal. Load or rebuild index.
5. Start capturers.
6. Start socket server, MCP server (if enabled), HTTP server (if enabled).
7. Start idle monitor.
8. Wait on shutdown signal.

### 8.2 Auto-Start

Called by the client library, not by the daemon itself:

```go
func Spawn(project string) error {
    // 1. os.Executable() to get dfmt binary path
    // 2. exec.Command with --project flag
    // 3. setsid and detach (double-fork pattern on Unix; Windows uses DETACHED_PROCESS)
    // 4. poll .dfmt/daemon.sock for up to 2s at 25ms intervals
    // 5. return nil if socket appears, error if not
}
```

The auto-start race is protected by a separate lock at `.dfmt/startup.lock`. Two concurrent clients both calling Spawn: the second acquires the lock after the first finishes, re-checks the socket, sees it's up, proceeds without spawning.

### 8.3 Idle Monitor

```go
type IdleMonitor struct {
    mu          sync.Mutex
    lastRequest time.Time
    lastFSEvent time.Time
    timeout     time.Duration
    trigger     chan<- struct{}
}

func (i *IdleMonitor) TouchRequest()
func (i *IdleMonitor) TouchFS()
```

A goroutine ticks every 60s, checks both `lastRequest` and `lastFSEvent`. If the newer of the two is older than `timeout`, signals `trigger`. The daemon's main select listens and initiates graceful shutdown.

---

## 9. Client (`internal/client`)

The client library is used by every CLI command except `dfmt daemon` itself.

```go
type Client struct {
    project string
    conn    net.Conn
    codec   *jsonrpc.Codec
    timeout time.Duration
}

func Connect(ctx context.Context, project string) (*Client, error) {
    // 1. Discover socket path: <project>/.dfmt/daemon.sock
    // 2. Dial with 50ms timeout
    // 3. On ECONNREFUSED or ENOENT: call daemon.Spawn; retry dial up to 2s
    // 4. Return *Client on success
}

func (c *Client) Call(ctx context.Context, method string, params, result any) error
func (c *Client) Close() error
```

The client is short-lived: commands open a connection, make one or two calls, close. No pooling. No reuse. The daemon handles connection churn fine.

---

## 10. CLI (`internal/cli`)

### 10.1 Flag Parser

No cobra, no urfave/cli. Custom flag parser, ~200 lines. The structure:

```go
type Command struct {
    Name        string
    Summary     string
    Flags       *flag.FlagSet
    Run         func(ctx context.Context, args []string) error
    Subcommands []*Command
}

var root = &Command{
    Name: "dfmt",
    Subcommands: []*Command{
        daemonCmd, stopCmd, statusCmd, listCmd,
        initCmd, rememberCmd, noteCmd, taskCmd,
        captureCmd, recallCmd, searchCmd, statsCmd,
        tailCmd, mcpCmd, installHooksCmd, shellInitCmd,
        configCmd, exportCmd, importCmd, rotateCmd,
        reindexCmd, purgeCmd, doctorCmd,
    },
}

func Dispatch(ctx context.Context, args []string) error
```

Common flags (`--project`, `--json`, `--verbose`) are registered on the root and inherited. Each command has its own `flag.FlagSet` for command-specific flags.

### 10.2 Output

Every command respects `--json`. With the flag set, all output is a single JSON object on stdout. Without it, human-friendly text.

Human output uses a small helper (`internal/cli/output.go`) that detects if stdout is a TTY (via `isatty` reimplemented with `x/sys/unix.IsaTerminal`). Colors on TTY, plain on pipe.

---

## 11. Sandbox Layer (`internal/sandbox`)

The sandbox layer runs exec/read/fetch operations and produces content chunks. See SPEC §7.5 for the external contract.

### 11.1 Sandbox Interface

```go
type Sandbox interface {
    Exec(ctx context.Context, req ExecReq) (ExecResp, error)
    Read(ctx context.Context, req ReadReq) (ReadResp, error)
    Fetch(ctx context.Context, req FetchReq) (FetchResp, error)
    BatchExec(ctx context.Context, items []any) ([]any, error)
}

type ExecReq struct {
    Code    string
    Lang    string            // "bash" | "sh" | "node" | "python" | ...
    Intent  string
    Timeout time.Duration
    Env     map[string]string
    Return  string            // "auto" | "raw" | "summary" | "search"
}

type ExecResp struct {
    Exit       int
    Stdout     string         // inline if small, else empty
    Stderr     string
    ChunkSet   string         // set ID, if output was chunked
    Summary    string         // human-readable summary
    Matches    []content.Match // intent-matched excerpts
    Vocabulary []string       // distinctive terms
    DurationMs int
}
```

`Read` and `Fetch` types follow the same pattern with their own request fields.

### 11.2 Runtime Detection

```go
type Runtime struct {
    Lang       string
    Executable string            // resolved PATH
    Version    string            // captured at probe
    Available  bool
}

type Runtimes struct {
    mu    sync.RWMutex
    cache map[string]Runtime
}

func (r *Runtimes) Probe(ctx context.Context) error
func (r *Runtimes) Get(lang string) (Runtime, bool)
```

Probed at daemon startup via `exec.LookPath`. Re-probed on SIGUSR1 (or when `dfmt doctor` runs). Version captured by invoking `<exe> --version` with a 500 ms timeout and storing the first line of stdout. Caching avoids re-probing on every request.

### 11.3 Exec Pipeline

```go
func (s *Sandbox) Exec(ctx context.Context, req ExecReq) (ExecResp, error) {
    // 1. Policy check (req.Code + req.Lang against security.Policy)
    // 2. Resolve runtime: Runtimes.Get(req.Lang) or error 424
    // 3. Build command: for shell langs, exec shell -c 'code'; for others,
    //    write code to a temp file and invoke runtime on it.
    // 4. Apply credential passthrough env based on detected CLIs in the code.
    // 5. Spawn subprocess with resource limits (setrlimit / job object).
    // 6. Capture stdout/stderr with size cap.
    // 7. On completion, decide output disposition:
    //      - small: return inline
    //      - medium: store in content + return first chunk + summary
    //      - large (or intent provided): chunk, index, return intent matches
    // 8. Emit an event: type=mcp.call, data includes duration, exit, chunk_set.
    // 9. Return ExecResp.
}
```

Resource limits use `syscall.Rlimit` via `x/sys/unix` on Unix, `windows.AssignProcessToJobObject` on Windows (wrapped in `exec_windows.go`).

### 11.4 HTML→Markdown Converter

Bundled in `sandbox/htmlmd.go`, ~400 lines. Handles:

- Headings (h1–h6 → `#` through `######`)
- Paragraphs, line breaks
- Bold, italic, code, links
- Ordered/unordered lists
- Code blocks (preserving `<pre><code>` with language hint from class)
- Tables (basic GFM table syntax)
- Stripping of script, style, nav, header, footer, form elements
- Text-only extraction as fallback if parsing fails

Uses stdlib `encoding/xml`-style streaming approach via a bundled tokenizer (`internal/sandbox/htmltok.go`, ~350 lines). **ADR-0008 accepted: bundle the tokenizer, do not depend on `x/net/html`.**

### 11.5 Security Policy

```go
type Policy struct {
    Version              int
    Deny                 []Rule
    Allow                []Rule
    CredentialPassthrough map[string][]string // CLI name -> env vars/paths to pass
    Limits               Limits
}

type Rule struct {
    Kind    string // "exec" | "read" | "fetch"
    Pattern string
    Glob    glob.Pattern // compiled
}

func LoadPolicy(path string) (*Policy, error)
func (p *Policy) CheckExec(code, lang string) error
func (p *Policy) CheckRead(path string) error
func (p *Policy) CheckFetch(url string) error
```

Command splitting: `code` is tokenized into commands by scanning for `&&`, `||`, `;`, `|`, and backtick/subshell boundaries. Each resulting command is checked independently against exec rules.

Shell-escape detection: for non-shell langs, the code is scanned for patterns known to invoke shells: `os.system`, `subprocess.`, `child_process.`, `Runtime.exec`, `Kernel#system`, etc. A match causes the check to fail.

### 11.6 Intent Matching

```go
func MatchIntent(ctx context.Context, intent string, chunks []Chunk, idx *core.Index) []Match
```

Runs the intent as a BM25 query against a fresh index of just this call's chunks. Returns top-K matches (default 3), with surrounding context (±200 chars within the chunk). Also returns a vocabulary: the top-K most distinctive terms in the chunks that were *not* in the intent query, computed by ranking term frequencies in the chunks relative to the intent's tokens.

---

## 12. Content Store (`internal/content`)

### 12.1 Store

```go
type Store struct {
    mu           sync.RWMutex
    sets         map[string]*ChunkSet      // set ID -> set
    chunks       map[string]*Chunk         // chunk ID -> chunk (for fast lookup)
    lru          *list.List                // LRU order of set IDs
    lruPos       map[string]*list.Element
    index        *core.Index               // content-side index, distinct instance
    maxBytes     int64
    curBytes     int64
    persistDir   string
}

func NewStore(maxBytes int64, persistDir string) *Store
func (s *Store) Put(set *ChunkSet, chunks []Chunk) error
func (s *Store) Get(chunkID string) (*Chunk, bool)
func (s *Store) ListSet(setID string) ([]Chunk, bool)
func (s *Store) Search(query string, scope string, limit int) []Match
func (s *Store) Evict(bytesNeeded int64) int // returns sets evicted
func (s *Store) Clear()                       // called on daemon exit unless persist=true
```

Eviction: when `curBytes + incoming > maxBytes`, evict LRU sets until enough space. Set-granular eviction only — never partial.

### 12.2 Chunking

```go
type Chunker interface {
    Chunk(content []byte, hints ChunkHints) []Chunk
}

// Per content-type chunkers:
type markdownChunker struct{}  // by heading boundaries, preserving code blocks
type textChunker struct{}      // by paragraph groups, or fixed N lines if no blank lines
type jsonChunker struct{}      // by top-level keys, nested to depth N
type logChunker struct{}       // by N-line groups or time-stamp gaps
```

The `ChunkHints` struct carries the content's MIME type (if detected), size, and a flag for whether line numbering matters (useful for file reads).

### 12.3 Persistence

Only when a chunk set is stored with `TTL = forever`. Written as `<persist_dir>/<set-id>.jsonl.gz`. On daemon startup, if `persist_dir` exists, all sets in it are loaded back into the store (counts toward byte budget; sets are LRU-moved on first access).

---

## 13. Hook Dispatcher (`internal/hook`)

### 13.1 Entry Point

```go
// dfmt hook <agent> <event>
// Reads JSON from stdin (the agent's hook payload).
// Writes JSON to stdout (the agent's expected response shape).

func Dispatch(ctx context.Context, agent, event string, stdin io.Reader, stdout io.Writer) error
```

### 13.2 Per-Agent Files

Each agent has one Go file. The file contains one function per event the agent supports. Each function:

1. Decodes the stdin payload using the agent's JSON schema.
2. Translates the payload into DFMT-internal events (file.edit, mcp.call, prompt, etc.).
3. Submits events to the daemon via the client library.
4. For PreToolUse-equivalent events: decides whether to block the native tool and redirect to DFMT's sandbox. Writes the appropriate response JSON.
5. For PreCompact-equivalent events: triggers a snapshot build and returns the snapshot as the response for injection.

### 13.3 Decision Policy for Blocking

When a native tool call is intercepted:

- **Bash / shell:** block if `code` is non-trivial (more than one word, or contains pipes/redirects), inject a system-message suggesting `dfmt.exec`. Allow trivial commands (e.g., `pwd`, `date`) to pass through.
- **Read:** block if file size > 4 KB or path matches a known large-log pattern (`.log`, `.sql`, `.csv`). Suggest `dfmt.read`.
- **WebFetch:** always block; suggest `dfmt.fetch`. Native WebFetch rarely produces small output.
- **Grep:** always block; suggest `dfmt.exec` with grep in the code. (Grep output can be unbounded.)

Each threshold is configurable via `config.yaml` under `intercept.*`.

---

## 14. Setup Command (`internal/setup`)

### 14.1 Detection

```go
type AgentSpec struct {
    Name       string
    Probes     []string          // filesystem paths to check
    WhichName  string            // binary name for `which`
    Configure  func(ctx context.Context, s *Setup) (Changes, error)
    Uninstall  func(ctx context.Context, s *Setup) error
}

var agents = []AgentSpec{
    claudeCodeSpec, geminiCLISpec, copilotSpec, codexSpec,
    cursorSpec, opencodeSpec, zedSpec, continueSpec, windsurfSpec,
}
```

Each spec in its own file (`claude.go`, `gemini.go`, etc.). The `Configure` func performs the writes for its agent; `Uninstall` undoes them by consulting the manifest.

### 14.2 Config Merging

```go
// writer.go
func MergeJSON(path string, modify func(doc *orderedmap.OrderedMap) error) error
func MergeYAML(path string, modify func(root *yaml.Node) error) error
func MergeTOML(path string, modify func(tree map[string]any) error) error
```

Each preserves the original formatting (as much as the format allows), inserts or replaces DFMT-owned keys (identified by `# dfmt:v1` comment markers or top-level `dfmt` keys), and writes back. A backup is written before every modify.

For TOML (Codex), DFMT ships a small parser/emitter (~300 lines) rather than taking a dependency. It supports only the subset used by Codex's config.

### 14.3 Manifest

```go
type Manifest struct {
    Version   int                 `json:"version"`
    Timestamp time.Time           `json:"ts"`
    Changes   []AgentChange       `json:"changes"`
}

type AgentChange struct {
    Agent  string   `json:"agent"`
    Files  []string `json:"files"`     // files created or modified
    Blocks []Block  `json:"blocks"`    // version-marked sections written
}
```

Stored at `$XDG_DATA_HOME/dfmt/setup-manifest.json`. `dfmt setup --uninstall` walks it to remove every entry.

### 14.4 Templates

Instruction-file content comes from templates in `setup/templates/`, embedded via `//go:embed`. Each template is a Go text/template with fields for project name, version, date, and any agent-specific variables.

A single base template (`_base.md.tmpl`) expresses the shared DFMT directive. Agent-specific templates include the base and add agent-specific tool-mapping tables and voice adjustments.

---

## 15. Testing Strategy

### 15.1 Coverage

- `internal/core`: ≥ 90%. This is the heart; exhaustive testing warranted.
- `internal/sandbox`, `internal/content`: ≥ 85%. Security-sensitive; strong coverage.
- `internal/hook`, `internal/setup`: ≥ 80%. Platform integration; integration tests do much of the work.
- `internal/retrieve`, `internal/transport`, `internal/capture`: ≥ 80%.
- `internal/cli`: ≥ 60%. CLI dispatchers are thin; integration tests do the heavy lifting.
- Bundled libraries (Porter, ULID, BM25, HTML→MD converter): ≥ 95%. Reference-test each against published vectors.

### 15.2 Test Types

- **Unit tests** in `_test.go` next to each source file.
- **Table-driven tests** for tokenizer, stemmer, classifier, BM25, HTML→MD, security policy rules.
- **Property tests** using stdlib `testing/quick` for ULID monotonicity, journal round-trip, BM25 idempotence under duplicate terms, chunking stability.
- **Integration tests** in `tests/integration/` spin up a real daemon against a tempdir project. Exercise MCP, socket, HTTP in sequence.
- **Sandbox integration tests** exercise real subprocess spawning (bash, python, node) with resource limits; verify stdout/stderr separation, timeout enforcement, credential-passthrough.
- **Agent-integration tests** drive `dfmt setup` against fake agent config directories (fixtures mimicking Claude Code, Gemini, Codex layouts) and assert resulting files are well-formed.
- **Golden tests** for snapshot renderers, hook event translations, instruction-file templates.
- **Ranking regression tests**: fixed query set against fixed corpora; assert top-K IDs match baseline. Baseline updated only with explicit review.

### 15.3 Benchmarks

`bench/bench_test.go` exercises:
- `BenchmarkJournalAppend` (durable, batched)
- `BenchmarkIndexBM25` (1k, 10k, 100k events)
- `BenchmarkSnapshotBuild` (budgets 512, 2048, 8192)
- `BenchmarkFSWatcher` (per-platform)
- `BenchmarkSandboxExec` (small, medium, large outputs)
- `BenchmarkContentChunk` (markdown, json, log)
- `BenchmarkIntentMatch` (intent query against 64 KB chunks)

CI runs these and compares against `bench/baseline.json`. A regression of >20% fails the build.

### 15.4 Platform CI

GitHub Actions matrix:
- `ubuntu-latest`, `macos-latest`, `windows-latest`
- Go 1.22, 1.23 (latest stable)
- `amd64`, `arm64` (via qemu on non-native runners)

Each combination runs unit + integration tests. Benchmarks run on `ubuntu-latest / amd64` only (stability).

Additional per-agent smoke tests run on the Ubuntu lane only, against containerized fake-agent fixtures, exercising `dfmt setup` → configure → verify round-trips.

---

## 16. Milestones

Implementation sequencing is in `TASKS.md`. Rough milestones:

- **POC (week 1-2).** Core journal + index + BM25 + Unix socket + `dfmt remember` + `dfmt search` + `dfmt recall`. Single-command demo.
- **MVP (weeks 3-5).** All capture sources (MCP, FS, git, CLI). Snapshot builder with all 4 formats. MCP transport. Auto-start. Idle-exit. `dfmt setup` for Claude Code + Codex.
- **v0.9 (weeks 6-7).** Sandbox layer (exec/read/fetch). Content store. Intent-driven filtering. Security policy engine. Hooks for Claude Code, Gemini, Copilot, OpenCode.
- **v1.0 (weeks 8-9).** `dfmt setup` for all 9 agents. HTTP transport. Shell integration. `dfmt doctor`, `dfmt list`, `dfmt stats`. Benchmarks hitting targets. Cross-platform CI green.
- **v1.0 release.** Docs polish. GitHub Releases with signed binaries. Homebrew/Scoop. Landing page. README with install-on-every-platform quickstart.

---

## 17. Open Implementation Questions

The following are tactical decisions deferred to implementation time. Each has a reasonable default; record the final choice in a follow-up ADR if nontrivial.

1. **Go version floor.** Propose `go 1.22` (stable since Feb 2024, broadly available). Higher floor = newer features; lower = wider install base.
2. **Named-pipe security on Windows.** ACL configuration via `x/sys/windows` is fiddly. Validate that our ACL actually restricts access to the owner in CI.
3. **FS watcher debounce semantics.** Exact dedup algorithm for the "thousands of events on git checkout" case. Propose: coalesce any FS events in a 2-second window following a `git.checkout` event from the git hook.
4. **Snapshot cache invalidation granularity.** The cache key includes `session_window_end`. Every new event invalidates. Optimization: only invalidate caches whose `Since` ≤ new event's TS. Defer to profiler data.
5. **Tokenizer versioning.** When we change tokenization, old `index.gob` files must be discarded. Propose: bake `tokenVer int` into the gob header; mismatch triggers full rebuild on load with a log line.
6. **MCP protocol version.** Track the MCP spec's `2025-06-18` baseline at start; upgrade when a target agent requires newer.

---

*End of implementation plan.*
