# DFMT Architecture

**DFMT** ("Don't Fuck My Tokens") is a local Go daemon that bridges AI coding agents with their execution environment. It provides sandboxed tool execution, intent-matched output excerpts, and durable session memory across conversation compactions.

---

## 1. High-Level Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        AI Coding Agent                               │
│   (Claude Code, Cursor, Codex, VS Code Copilot, Windsurf, etc.)       │
└──────────────────────────────────┬──────────────────────────────────┘
                                   │ MCP over stdio / HTTP API
                                   │
┌──────────────────────────────────▼──────────────────────────────────┐
│                          DFMT Daemon                                 │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                │
│  │ MCP Server   │  │ HTTP Server │  │ Unix Socket │                │
│  │ (stdio)     │  │ (dashboard)  │  │ (CLI hooks) │                │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                │
│         │                │                │                         │
│         └────────────────┼────────────────┘                         │
│                          │                                           │
│              ┌───────────▼───────────┐                              │
│              │   Request Handlers    │                              │
│              │  Remember, Search,    │                              │
│              │  Recall, Exec, Read,  │                              │
│              │  Fetch, Stats, Tail   │                              │
│              └───────────┬───────────┘                              │
│                          │                                           │
│    ┌─────────────────────┼─────────────────────┐                   │
│    │                     │                     │                     │
│    ▼                     ▼                     ▼                     │
│ ┌────────┐        ┌────────────┐        ┌──────────┐                │
│ │ Journal │        │   Index    │        │ Sandbox  │               │
│ │(JSONL) │        │ (BM25+tri) │        │(exec/read│               │
│ │        │        │            │        │ fetch)   │               │
│ └────────┘        └────────────┘        └──────────┘                │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐   │
│  │                    Capture Pipeline                        │   │
│  │  MCP calls │ CLI commands │ FS watcher │ Git hooks │ Shell │   │
│  └────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 2. Entry Points

### 2.1 CLI (`cmd/dfmt/main.go`)

```
dfmt <command> [flags]

Commands:
  init           Initialize a project (.dfmt/, config.yaml, .gitignore)
  remember       Record an event manually
  search         Query the journal via BM25 index
  recall         Build a session snapshot (markdown)
  exec           Run code in sandbox, return intent-matched excerpt
  read           Read file, return intent-matched excerpt
  fetch          HTTP fetch, return intent-matched excerpt
  stats          Show session statistics
  tail           Stream events in real-time
  daemon         Start the daemon (rarely called directly)
  setup          Auto-detect and configure AI agents
  install-hooks  Install git hooks (post-commit, post-checkout, pre-push)
  shell-init     Print shell integration snippet (bash/zsh/fish)
  capture        Internally by git hooks and shell integration
  mcp            Start MCP server over stdio (used by agent setup)
```

### 2.2 MCP Transport (`internal/transport/mcp.go`)

Model Context Protocol server over stdio. Primary integration path for Claude Code and similar agents. Tools exposed:

| Tool | Description |
|------|-------------|
| `dfmt.exec` | Sandboxed code execution with intent extraction |
| `dfmt.read` | File read with intent extraction |
| `dfmt.fetch` | HTTP fetch with intent extraction |
| `dfmt.remember` | Record events (llm.response, task, decision, etc.) |
| `dfmt.search` | BM25 search over journal |
| `dfmt.recall` | Build session snapshot |
| `dfmt.stats` | Session statistics |

### 2.3 HTTP API (`internal/transport/http.go`)

JSON-RPC 2.0 over HTTP + embedded HTML dashboard.

- `GET /` — Dashboard HTML
- `POST /api/rpc` — JSON-RPC 2.0 endpoint
- `GET /stats` — Session stats
- `GET /journal` — Stream journal events

---

## 3. Daemon Lifecycle (`internal/daemon/`)

```
┌─────────────────────────────────────────────────────┐
│                   daemon.Start()                    │
│                                                      │
│  1. Try to acquire file lock (flock)                │
│     └─ If locked by another process, connect to it  │
│                                                      │
│  2. Auto-init project if not initialized            │
│                                                      │
│  3. Load or rebuild search index                    │
│                                                      │
│  4. Start idle monitor (channel-based, no timer):   │
│     idleCh = make(chan struct{})                    │
│     for { select { case <-idleCh: break case <-ticker.C: ... } }
│                                                      │
│  5. Start capture sources:                          │
│     - FSWatcher (if capture.fs.enabled=true)        │
│     - MCP transport                                │
│                                                      │
│  6. Serve transport layer (MCP stdio + HTTP)       │
│                                                      │
│  7. On idle timeout → close idleCh → flush journal │
│                                                      │
│  8. On Stop() → sync ticker stops, final journal   │
│     flush, index persist                            │
└─────────────────────────────────────────────────────┘
```

**Idle signaling:** Uses `idleCh chan struct{}` — no `time.AfterFunc` (avoids race condition). Daemon idles after `idleTimeout` seconds of no requests, exits. On new request, idle channel is reset.

---

## 4. Journal (`internal/core/journal.go`)

Append-only JSONL event log. Crash-safe via periodic sync ticker (30s interval in batched mode).

```
.dfmt/
├── config.yaml      # Project configuration
├── journal.jsonl    # Append-only event log
├── index.gob        # Persisted search index (JSON since v0.1.0)
├── index.cursor     # Index cursor position
└── port             # Daemon port/socket path
```

### Event Types

| Type | Priority | Source | Description |
|------|----------|--------|-------------|
| `mcp.call` | p1 | MCP | Tool call from agent |
| `mcp.response` | p1 | MCP | Tool response |
| `llm.response` | p2 | MCP | LLM output |
| `file.edit` | p2 | filesystem | File modification |
| `git.commit` | p3 | git hook | Git commit |
| `task` | p3 | CLI | Task created/done |
| `note` | p4 | CLI | User note |
| `decision` | p3 | MCP | Significant decision |
| `env.cwd` | p4 | shell | Working directory change |

### Rotation

When `journal.jsonl` exceeds `MaxBytes` (default 10MB):

1. Write tombstone event: `{"type":"rotation","journal_id":"<id>"}`
2. Rename `journal.jsonl` → `journal_<timestamp>.jsonl`
3. Create new `journal.jsonl`
4. Truncate index cursor

### Durability Modes

- **Durable** (default): `SyncFile` after every write — survives kernel crash
- **Batched**: 30s periodic sync ticker — better performance, 30s data risk

---

## 5. Index (`internal/core/index.go`, `index_persist.go`)

In-memory inverted index with BM25 scoring, Porter stemming, trigram indexing.

```
┌─────────────────────────────────────────────────────────────┐
│                        Index                                 │
│                                                              │
│  ix.postings: map[string][]int         ← BM25 posting lists │
│  ix.docIDs:   map[string]string        ← doc ID lookup       │
│  ix.trigramPL: map[string]trigramPost  ← trigram postings    │
│  ix.docTokens: map[int]int             ← doc token count     │
│                                                              │
│  Index.Add(event):                                          │
│    1. Tokenize text (unicode-aware)                         │
│    2. Porter-stem each token (English + Turkish)            │
│    3. Build postings for each token                         │
│    4. Also index bigrams/trigrams for fuzzy matching        │
│    5. Store doc token count for BM25 normalization          │
│                                                              │
│  Index.Search(query, budget):                               │
│    1. Tokenize + stem query                                  │
│    2. Get posting lists for query terms                     │
│    3. Compute BM25 scores (k1=1.5, b=0.75)                 │
│    4. Apply trigram boosting for partial matches            │
│    5. Return top-N ranked doc IDs                           │
└─────────────────────────────────────────────────────────────┘
```

**Persistence:** Custom JSON serialization via `MarshalJSON/UnmarshalJSON` (unexported fields can't use `encoding/gob`). Persisted to `index.gob` (filename kept for compatibility).

**Stopwords:** English + Turkish (loaded from embedded constants, no external files).

---

## 6. Capture Pipeline (`internal/capture/`)

Five event-ingestion paths. Four are live, one is a stub.

```
┌──────────────────────────────────────────────────────────────────┐
│                     Capture Pipeline                               │
│                                                                  │
│  ┌────────────┐    ┌────────────┐    ┌────────────┐             │
│  │ MCP calls  │    │ CLI cmds   │    │ FS watcher │             │
│  │ (transport)│    │ dfmt rem-  │    │ (fswatch_) │             │
│  │   LIVE     │    │ ember/     │    │   LIVE     │             │
│  │            │    │ task/note  │    │            │             │
│  └──────┬─────┘    └──────┬─────┘    └──────┬─────┘             │
│         │                 │                 │                    │
│         └─────────────────┼─────────────────┘                    │
│                           │                                      │
│                           ▼                                      │
│              ┌────────────────────────┐                         │
│              │   daemon.journal.Add() │                         │
│              │   daemon.index.Add()    │                         │
│              └────────────────────────┘                         │
│                           │                                      │
│         ┌─────────────────┼─────────────────┐                   │
│         │                 │                 │                    │
│    ┌────▼────┐     ┌─────▼────┐    ┌──────▼──────┐            │
│    │ Git hook │     │ Shell     │    │ Stub types  │            │
│    │   LIVE   │     │ integration│   │ (capture/   │            │
│    │post-commit│    │   LIVE     │    │  git.go,    │            │
│    │post-checkout│  │ env.cwd    │    │  shell.go)  │            │
│    │pre-push   │    │            │    │             │            │
│    └───────────┘    └────────────┘    └─────────────┘            │
└──────────────────────────────────────────────────────────────────┘
```

### 6.1 Filesystem Watcher

Platform-specific implementations:

**Linux (`fswatch_linux.go`):** Uses `inotify` via `golang.org/x/sys`.

**Windows (`fswatch_windows.go`):** Uses `ReadDirectoryChangesW`. Optimization: tracks directory mod-time, only re-walks directories that changed since last scan (avoids O(tree_walk) every 2s on static trees).

**Debounce:** Non-blocking per-path debounce using `map[string]time.Time`. Each path has a 100ms cooldown. Cleanup goroutine removes stale entries.

### 6.2 Git Hooks

`dfmt install-hooks` writes embedded shell scripts to `.git/hooks/`:

- `post-commit` — calls `dfmt capture git commit <hash> <message>`
- `post-checkout` — calls `dfmt capture git checkout <branch>`
- `pre-push` — calls `dfmt capture git push <remote> <branch>`

Scripts use `command -v dfmt` check, pin dfmt path at install time (not at runtime).

### 6.3 Shell Integration

`dfmt shell-init bash|zsh|fish` prints integration snippet:

```bash
# For bash/zsh — prints to stdout
PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND ; }dfmt capture env.cwd $(pwd)"
```

```fish
# For fish
functions --query dfmt_env_cwd; or
    function dfmt_env_cwd
        dfmt capture env.cwd (pwd)
    end
    fish_prompt
end
```

---

## 7. Sandbox (`internal/sandbox/`)

Provides `exec`, `read`, `fetch` with **intent-matched excerpts** instead of raw output.

```
┌─────────────────────────────────────────────────────────────┐
│                      Sandbox                                 │
│                                                              │
│  Exec(code, lang, intent, timeout):                        │
│    1. Check language against Permissions policy              │
│    2. Run code in subprocess (bash/sh)                       │
│    3. Capture stdout/stderr                                 │
│    4. Store raw output in content store                     │
│    5. Apply intent extraction → return excerpt             │
│                                                              │
│  Read(path, intent, offset, limit):                         │
│    1. Read file contents                                    │
│    2. Store raw content in content store                    │
│    3. Apply intent extraction → return excerpt              │
│                                                              │
│  Fetch(url, intent, method, timeout):                       │
│    1. Execute HTTP request                                  │
│    2. Store raw response in content store                   │
│    3. Apply intent extraction → return excerpt              │
└─────────────────────────────────────────────────────────────┘
```

**Intent extraction:** Uses a simple keyword-match approach in `intent.go`. Looks for relevant keywords from intent string in output, returns surrounding context.

**Permissions:** `permissions.go` defines policy per language. Unknown languages are denied by default.

---

## 8. Transport Layer (`internal/transport/`)

Three simultaneous interfaces:

```
┌──────────────────────────────────────────────────────────────┐
│                    Transport Layer                            │
│                                                               │
│  MCP over stdio ──────────────────────────────────────────   │
│  Primary agent integration. JSON-RPC 2.0 over process I/O.   │
│  Tools: Remember, Search, Recall, Exec, Read, Fetch, Stats    │
│                                                               │
│  HTTP API (port) ──────────────────────────────────────────  │
│  JSON-RPC 2.0 + HTML dashboard.                              │
│  Endpoints: /, /api/rpc, /stats, /journal                    │
│  CSP: sha256 hashes for inline scripts (no unsafe-inline)   │
│                                                               │
│  Unix Socket / TCP ────────────────────────────────────────  │
│  CLI commands and git hooks connect via this.                │
│  Used by: dfmt remember, dfmt search, dfmt capture, etc.      │
└──────────────────────────────────────────────────────────────┘
```

---

## 9. Agent Setup (`internal/setup/`)

Auto-detects installed AI agents and writes MCP configuration:

| Agent | Config Path |
|-------|------------|
| Claude Code | `~/.claude/settings.json` |
| Cursor | `~/.cursor/mcp.json` |
| VS Code Copilot | `~/.vscode/mcp.json` |
| Codex CLI | `~/.codex/config.json` |
| Gemini CLI | `~/.gemini/mcp.json` |
| Windsurf | `~/.windsurf/mcp.json` |
| OpenCode | `~/.config/opencode/mcp.json` |

Also writes `.claude/settings.json` in project (with DFMT MCP allow-list) and `docs/DFMT-INSTRUCTIONS.md` for agents that support instruction files.

Uninstall removes all written files, tracked in `~/.local/share/dfmt/setup-manifest.json`.

---

## 10. Configuration (`internal/config/`)

YAML configuration via `config.yaml`:

```yaml
# Daemon
daemon.idleTimeout: 300      # seconds before daemon exits
daemon.port: 0               # 0 = auto-select

# Capture
capture.mcp: true           # Capture MCP tool calls (default: true)
capture.fs: false           # Enable filesystem watcher (default: false)
capture.fsDebounceMs: 100   # Per-path debounce milliseconds

# Index
index.maxTokens: 10000      # Max tokens per document

# Sandbox
sandbox.timeout: 30         # Default exec timeout (seconds)
sandbox.allow:
  - bash
  - python
  - node
  - go

# Storage
storage.durability: durable # durable | batched
storage.journalMaxMB: 10    # Max journal size before rotation
storage.compress: true      # Compress rotated journals
```

**Defaults:** `internal/config/defaults.go` exports `DefaultConfigYAML()` — used by both `dfmt init` and `daemon auto-init`. No duplication.

---

## 11. Data Flow Diagrams

### 11.1 Tool Call Flow (e.g., `dfmt exec`)

```
Agent                   DFMT Daemon                 Sandbox
  │                          │                         │
  │ dfmt.exec(code, lang)    │                         │
  │─────────────────────────►│                         │
  │                         │ Check permissions        │
  │                         │─────────────────────────►│
  │                         │                         │ Run subprocess
  │                         │                         │ Store raw output
  │                         │◄─────────────────────────│
  │                         │                         │
  │                         │ Intent extraction        │
  │                         │─────────────────────────►│
  │                         │                         │ (intent match)
  │                         │◄─────────────────────────│
  │                         │                         │
  │ {excerpt, vocab}        │                         │
  │◄─────────────────────────│                         │
```

### 11.2 Git Hook Capture Flow

```
Git                     .git/hooks/post-commit    dfmt capture       Daemon
 │                             │                       │                 │
 │ git commit                  │                       │                 │
 │────────────────────────────►│                       │                 │
 │                             │ dfmt capture git commit <hash>           │
 │                             │───────────────────────►│                 │
 │                             │                       │ client.Remember  │
 │                             │                       │────────────────►│
 │                             │                       │                 │
 │                             │                       │        journal.Add()
 │                             │                       │        index.Add()
 │                             │                       │◄────────────────│
 │                             │                       │                 │
 │                             │                       │ ack             │
 │                             │◄──────────────────────│                 │
```

### 11.3 Journal Persistence

```
┌─────────────────────────────────────────────────────────────────┐
│                     Journal Write Path                           │
│                                                                  │
│  journal.Add(event):                                            │
│    1. Encode event as JSON line                                │
│    2. Append to file (O_APPEND)                                │
│    3. If Durable mode: fsync() immediately                     │
│    4. If Batched mode: write to buffer                         │
│       └─ Every 30s: sync ticker fires → fsync()                │
│                                                                  │
│  journal.Rotate():                                              │
│    1. Write tombstone event                                     │
 │    2. fsync()                                                  │
│    3. Rename journal.jsonl → journal_<ts>.jsonl                 │
│    4. Create new journal.jsonl                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 12. Package Map

```
cmd/dfmt/main.go              CLI entry point, --project flag
cmd/dfmt-bench/main.go        Benchmarking (tokenize, index, search, sandbox)

internal/
├── cli/dispatch.go           Command routing, init, shell-init, install-hooks
├── client/client.go          Daemon communication (HTTP/Unix socket)
├── config/config.go          YAML loading
├── config/defaults.go        DefaultConfigYAML()
├── core/
│   ├── event.go              Event types, priority tiers, sources
│   ├── journal.go            Append-only JSONL, rotation, sync ticker
│   ├── index.go              BM25, Porter stemmer, trigram, stopwords
│   ├── index_persist.go      JSON serialization for index
│   └── tokenize.go           Unicode-aware tokenization
├── daemon/daemon.go          Daemon lifecycle, idle monitor, FSWatcher wiring
├── capture/
│   ├── capture.go            Capture type constants and helpers
│   ├── fswatch.go            Shared FSWatcher logic
│   ├── fswatch_linux.go      inotify implementation
│   ├── fswatch_windows.go    ReadDirectoryChangesW + mod-time optimization
│   ├── git.go                Stub: SubmitCommit, SubmitCheckout, SubmitPush
│   └── shell.go             Stub: SubmitCommand (env.cwd)
├── sandbox/
│   ├── sandbox.go           exec, read, fetch with intent extraction
│   ├── permissions.go       Language policy
│   └── intent.go            Intent keyword matching
├── content/
│   ├── store.go             Ephemeral content storage
│   └── summarize.go         Content summarization
├── transport/
│   ├── mcp.go               MCP protocol, tool definitions
│   ├── http.go              HTTP server + dashboard
│   ├── handlers.go          RPC request handlers
│   ├── jsonrpc.go           JSON-RPC 2.0 codec
│   ├── socket.go            Unix socket transport
│   └── tcp.go               TCP transport
├── setup/
│   ├── detect.go            Agent auto-detection
│   ├── setup.go             Configuration writer
│   └── claude.go            Claude Code-specific setup
├── project/
│   ├── discover.go          Project path discovery (env vars, git, cwd)
│   └── registry.go         Daemon registry (port files)
├── redact/redact.go         Secret redaction (AWS keys, tokens, JWTs)
├── retrieve/retrieve.go     Snapshot building
├── retrieve/render_md.go    Markdown rendering for recall
└── logging/logging.go        Internal logging (stdlib first)
```

---

## 13. Key Design Decisions

### Stdlib-First
No external dependencies except `golang.org/x/sys`, `golang.org/x/crypto`, `gopkg.in/yaml.v3`. Everything else (BM25, Porter stemmer, MCP protocol, JSON-RPC 2.0) is bundled.

### Intent-Matched Output
Agents never see raw tool output. Sandbox produces an excerpt matched to the agent's stated intent. Raw output goes to ephemeral content store.

### No SQLite / ORMs
Persistent state is plain files: JSONL for journal, JSON-encoded index, YAML for config. No database.

### Channel-Based Idle Signaling
Daemon uses `idleCh chan struct{}` instead of `time.AfterFunc` for idle timeout. Avoids timer goroutine leaks and race conditions.

### Custom JSON for Index
Go's `encoding/gob` can't encode unexported fields. Index uses `MarshalJSON`/`UnmarshalJSON` with an internal `indexJSON` struct that holds exported copies of all fields.

### Per-Path Non-Blocking Debounce
FSWatcher uses `map[string]time.Time` with a cleanup goroutine. No `time.Sleep` blocking the watcher loop.

---

## 14. Security

### Sandbox Policy
Languages must be explicitly allowed. Unknown languages are denied. Policy defined in `internal/sandbox/permissions.go`.

### Secret Redaction
`internal/redact/` scrubs secrets from all stored content:
- AWS access keys (`AKIA...`)
- Bearer tokens (`Bearer ...`, `token=...`)
- JWTs
- Generic API keys

### File Lock
Only one daemon per project directory. Uses `flock` (Unix) or named mutex (Windows). Prevents port conflicts and race conditions.

---

## 15. Tests

```
internal/core:       90%+ coverage
internal/transport:  85%+ coverage
internal/daemon:     80%+ coverage
internal/cli:        75%+ coverage
```

All new functionality requires tests. Bug fixes require regression tests.

---

*Generated for DFMT v0.1.2*