# DFMT Architecture Security Report

**Project:** DFMT — Daemon Framework for Model Tools  
**Language:** Go 1.25.0 (100%)  
**Dependencies:** `golang.org/x/sys v0.43.0`, `gopkg.in/yaml.v3 v3.0.1`  
**Build:** `go build -ldflags "-X ..." ./cmd/dfmt` via Makefile  
**Generated:** 2026-05-02 (updated from 2026-05-01 scan)

---

## 1. Technology Stack Detection

### Languages
- **Go** is the only language in this project. No TypeScript, Python, or other languages present.
- Source files: `D:/Codebox/PROJECTS/DFMT/**/*.go` — all 35+ packages are Go.

### Frameworks & Libraries
- `net/http` — HTTP transport only
- `net` (unix sockets / TCP) — primary control plane
- `os/exec` — subprocess execution in sandbox
- `encoding/json` — JSON-RPC 2.0 serialization
- `gopkg.in/yaml.v3` — permissions.yaml parsing
- `golang.org/x/sys` — system calls (file locking, signals, Windows console)

No external web frameworks (chi, gin, echo, etc.) — per project rules.

### Build Tools
- `go.mod` / `go.sum` — Go module management
- `Makefile` — build, test, lint, cross-compile targets
- `cmd/dfmt/main.go` — CLI entry point
- `cmd/dfmt-bench/main.go` — benchmarking tool

---

## 2. Application Type Classification

DFMT is a **local multi-transport daemon** with three operational modes:

| Mode | Transport | Purpose |
|------|-----------|---------|
| CLI daemon | Unix socket (Linux/macOS) or loopback TCP (Windows) | Primary control plane for Claude Code CLI |
| HTTP dashboard | `localhost` only, HTTP | Optional metrics/status dashboard |
| MCP stdio | Standard input/output | Direct agent integration (Claude Code MCP mode) |

All three transports share identical operation semantics; only wire formats differ.

**Classified as:** CLI Tool + Daemon + MCP Server

---

## 3. Entry Points Mapping

### CLI Entry Point
**File:** `D:/Codebox/PROJECTS/DFMT/cmd/dfmt/main.go` (54 lines)

```
main() → cli.Dispatch(cleaned) → os.Exit(code)
```

- Handles global flags `--project` and `--json` before dispatch
- `version.Current` injected at build time via `-X` ldflags
- Subcommands routed through `cli.Dispatch()` in `internal/cli/dispatch.go`

### MCP over Stdio
**File:** `D:/Codebox/PROJECTS/DFMT/internal/transport/mcp.go` (839 lines)

- `ServeJSONRPC()` handler reads line-delimited JSON-RPC 2.0 from stdin
- Dispatches to `s.handlers` (type `*Handlers`) which wraps sandbox operations
- Methods: `tools/call`, `initialize`, `shutdown`, `ping`

### HTTP Server (Dashboard)
**File:** `D:/Codebox/PROJECTS/DFMT/internal/transport/http.go` (846 lines)

- `ServeHTTP()` on `localhost` only (not network-exposed)
- Routes: `GET /metrics`, `GET /stats`, `GET /dashboard`
- JSON-RPC 2.0 over HTTP POST at `/rpc`
- Error codes: `-32603` (Internal), `-32700` (Parse), `-32601` (Method not found), `-32602` (Invalid params)

### Unix Socket Server (Primary Control Plane)
**File:** `D:/Codebox/PROJECTS/DFMT/internal/transport/socket.go` (328 lines)

- `ListenSocket()` creates socket with `umask` to enforce `chmod 0600` (line 286: "socket file is never world-readable")
- Line-delimited JSON-RPC 2.0
- `Stop()` drains connections with bounded timeout, then removes socket file
- Grace period: `stopDrainTimeout` — prevents hung handlers from blocking daemon shutdown

### Daemon Lifecycle
**File:** `D:/Codebox/PROJECTS/DFMT/internal/daemon/daemon.go` (676 lines)

```
Daemon.Run() → NewSocketServer() + NewHTTPServer() + capture.Start()
Daemon.Stop() → journal.Close() + index.Persist()
```

- Idle timeout monitor: fires `Stop()` after `config.Lifecycle.IdleTimeout` of inactivity (default 30 min, min 1 sec, max 1 min tick)
- Registers with `client.GetRegistry()` on start; unregisters on stop
- Data directory: `{project}/.dfmt/` containing `journal.jsonl` and `index.gob`

---

## 4. Data Flow Map

### MCP Tool Call Flow
```
Agent (stdio) 
  → mcp.go:ServeJSONRPC() 
  → socket.go:handle() 
  → handlers.go:dispatch()
  → Handlers.{Exec,Read,Glob,Grep,Edit,Write,Fetch}()
  → acquireLimiter() [semaphore: 4 exec, 16 read]
  → sandbox.go:*Sandbox methods
  → policy.go:PolicyCheck() [allow/deny evaluation]
  → journal.go:Append() [event persisted]
  → index.Update() [search index updated]
  → NormalizeOutput() [8-stage pipeline]
  → response serialized and returned
```

### CLI-to-Daemon Flow
```
dfmt CLI (client.go:Client)
  → socket.jsonrpcClient() or httpClient()
  → transport.Request JSON-RPC 2.0
  → Daemon socket/HTTP handlers
  → Same sandbox → journal → index pipeline
```

### Fetch Call Flow
```
handlers.go:Fetch()
  → sandbox.go:Fetch()
  → policy.go:PolicyCheck("fetch", normalizeURL)
  → http.Client (with redirect limits, MaxResponseBytes)
  → content/store.go (LRU cache, optional persistence)
  → NormalizeOutput()
  → response
```

### Output Normalization Pipeline (8 Stages)
Defined in `internal/sandbox/intent.go:NormalizeOutput()` (line 440):

1. **CompactBinary** (`binary.go:71`) — Detects PNG/PDF/gzip magic numbers, NUL bytes, invalid UTF-8; replaces with `(binary; type=...; N bytes; sha256=...)`
2. **RemoveANSI** — Strips ANSI escape sequences (color, cursor movement)
3. **ConvertUTF16LEToUTF8** (`permissions.go:2247`) — Detects BOM or heuristic (Git Bash on Windows); decodes UTF-16LE to UTF-8
4. **RemoveNoise** (`structured.go:walkDropNoise`) — Drops null, empty strings, noise fields from structured output
5. **Truncate** — Applies token-budget truncation (TailTokens = 512 tokens ≈ 2 KB)
6. **RLE** (Run-Length Encoding) — Collapses 1000+ identical consecutive lines to single line
7. **CollapseWhitespace** — Reduces multiple spaces/tabs to single space
8. **ConvertHTMLToMarkdown** (`htmlmd.go`) — HTML → Markdown conversion

### Journal Event Flow
```
logEvent() [handlers.go]
  → journalImpl.Append() [journal.go:547 lines]
  → JSONL append with ULID, timestamp, priority, source, tags
  → crash-recovery: scanLastID() on startup
  → partial-write detection: last byte must be '\n'
```

---

## 5. Trust Boundaries

### Sandbox Policy
**File:** `D:/Codebox/PROJECTS/DFMT/internal/sandbox/permissions.go` (2325 lines)

#### Default Exec Allowlist (base commands)
```go
// permissions.go:318 — hardDenyExecBaseCommands map
// Default exec whitelist (line ~390):
allow:exec:git *
allow:exec:go *
allow:exec:node *
allow:exec:npm *
allow:exec:pnpm *
allow:exec:yarn *
allow:exec:python *
allow:exec:python3 *
allow:exec:make *
allow:exec:bash *
allow:exec:sh *
```

#### Hard-Deny Base Commands (cannot be overridden)
```go
// permissions.go:307
var hardDenyExecBaseCommands = map[string]struct{}{
    "sudo":    {},
    "su":      {},
    "chmod":   {},
    "chown":   {},
    "setfacl": {},
    // ... likely others
}
```

#### Policy Evaluation
- Default-deny: any exec not matching an allow rule is denied
- Override file: `.dfmt/permissions.yaml` can add allow rules but cannot re-enable hard-deny base commands (line 308: "override cannot re-enable via allow:exec:…")
- Path normalization: backslash → forward slash on Windows before policy check
- Command arguments checked separately from base command

### Network Boundaries
- HTTP server: `localhost` only (not reachable from other machines)
- Unix socket: `chmod 0600` enforced via umask on creation
- Fetch: URL normalized before policy check; blocklist for internal/cloud metadata endpoints
  - `normalizeFetchURLForPolicy()` (line 1354) strips scheme/host variations
  - Example block: `HTTP://METADATA.GOOGLE.INTERNAL/foo` — normalized, then denied

### Write Operations
- `safefs.WriteFile()` and `safefs.WriteFileAtomic()` — symlink check before write
- `CheckNoSymlinks(baseDir, path)` — Lstat check prevents writing through symlinks
- Write journal entry: SHA-256 first 8 bytes + byte count only (NOT raw content) — line 1559 in handlers.go: `content_sha16` field

---

## 6. External Integrations

### Unix Domain Sockets (Linux/macOS)
- Path: `{project}/.dfmt/dfmt.sock`
- Permissions: `chmod 0600` enforced at socket creation
- Protocol: line-delimited JSON-RPC 2.0

### Loopback TCP (Windows — no Unix sockets)
- Address: `127.0.0.1:0` (OS-assigned port)
- HTTP transport: `http://{address}/rpc`

### File System Journal
**File:** `D:/Codebox/PROJECTS/DFMT/internal/core/journal.go` (547 lines)

- Path: `{project}/.dfmt/journal.jsonl`
- Format: JSONL with ULID event IDs
- Crash recovery: scanLastID() on startup; partial-write detection
- Durability modes: "batched" (default), "sync" (O_SYNC), "none" (memory-only)

### Search Index
**File:** `D:/Codebox/PROJECTS/DFMT/internal/core/index.go` (538 lines)

- Path: `{project}/.dfmt/index.gob` (gob serialization)
- Content: trigram + BM25 inverted index, document lengths, excerpts
- Persistence: JSON marshal via index.MarshalJSON → writeRawAtomic (tmp+fsync+rename)

### Content Store
**File:** `D:/Codebox/PROJECTS/DFMT/internal/content/content.go` (717 lines)

- LRU cache: default 64 MB bounded
- Optional persistence: `.dfmt/content/` as gzipped JSONL
- Summarization: produces human-readable summaries with warnings and top phrases

### Capture Sources (Inotify/ReadDirectoryChangesW, Git hooks, Shell integration)
**File:** `D:/Codebox/PROJECTS/DFMT/internal/capture/capture.go` (423 lines)

- Filesystem watcher: kernel-level notifications (inotify Linux, ReadDirectoryChangesW Windows)
- Git hooks: post-commit, post-checkout, pre-push
- Shell integration: bash/zsh/fish prompt hooks

---

## 7. Authentication Architecture

**No traditional authentication** — DFMT is a single-user local daemon.

### CLI → Daemon Authentication
- Unix socket permissions (`chmod 0600`) prevent other local users from connecting
- On Windows: loopback TCP with no authentication, but restricted to localhost
- No token, no credentials — relies on OS-level socket permission

### Agent Auto-Detection
**File:** `D:/Codebox/PROJECTS/DFMT/internal/setup/detect.go`

- Probes for Claude Code installation
- Writes MCP config to `.mcp.json` in project directory
- `record_agent.go` — tracks agent identity in setup manifest

### Session Tracking
- Session ID passed via `X-DFMT-Session` HTTP header (when using HTTP transport)
- No session auth — daemon lifetime-bound

---

## 8. File Structure Analysis

```
D:/Codebox/PROJECTS/DFMT/
├── cmd/
│   ├── dfmt/main.go            # CLI entry point (54 lines)
│   └── dfmt-bench/main.go      # Benchmarking tool
├── internal/
│   ├── cli/
│   │   ├── cli.go              # Version re-export
│   │   ├── dispatch.go         # CLI command dispatch (3045 lines)
│   │   └── hooks_embed.go      # Embedded shell hook scripts
│   ├── client/
│   │   ├── client.go           # Socket/HTTP client for CLI (609 lines)
│   │   └── registry.go         # Daemon registry by project path
│   ├── capture/
│   │   ├── capture.go          # Event capture sources (423 lines)
│   │   ├── fswatch_linux.go    # inotify watcher
│   │   ├── fswatch_windows.go  # ReadDirectoryChangesW
│   │   └── git.go              # Git hook integration
│   ├── content/
│   │   ├── content.go          # LRU chunk storage + summarization
│   │   └── summarize.go        # Summary generation
│   ├── core/
│   │   ├── core.go             # Constants, priority tiers, source types
│   │   ├── journal.go          # JSONL event store (547 lines)
│   │   ├── index.go            # Trigram+BM25 search index
│   │   ├── index_persist.go    # gob persistence
│   │   ├── classifier.go       # Intent classification
│   │   └── tokenize.go         # Text tokenization
│   ├── daemon/
│   │   └── daemon.go           # Daemon lifecycle, idle timeout monitor (676 lines)
│   ├── safefs/
│   │   └── safefs.go           # Symlink-safe write helpers (252 lines)
│   ├── sandbox/
│   │   ├── sandbox.go          # Request/response types, timeouts (191 lines)
│   │   ├── permissions.go      # Policy engine, exec allowlist (2325 lines)
│   │   ├── runtime.go          # Shell detection, Git Bash on Windows (137 lines)
│   │   ├── exec.go             # (methods on *Sandbox in permissions.go)
│   │   ├── binary.go           # CompactBinary stage (113 lines)
│   │   ├── intent.go           # NormalizeOutput, intent classification (630 lines)
│   │   ├── htmlmd.go           # HTML→Markdown converter (545 lines)
│   │   ├── structured.go       # walkDropNoise, JSON noise removal (482 lines)
│   │   ├── tokens.go           # ApproxTokens heuristic (65 lines)
│   │   └── signals.go          # Signal handling for exec'd processes
│   └── transport/
│       ├── transport.go        # Package docs: MCP, HTTP, Unix socket
│       ├── mcp.go              # MCP/JSON-RPC stdio server (839 lines)
│       ├── socket.go           # Unix socket server (328 lines)
│       ├── http.go             # HTTP server + dashboard (846 lines)
│       ├── handlers.go         # RPC method handlers (1575 lines)
│       ├── dashboard.go        # Embedded HTML dashboard (231 lines)
│       └── session.go          # Per-connection session tracking
├── docs/
│   ├── ARCHITECTURE.md         # Full architecture document
│   └── adr/                    # Architectural Decision Records
├── go.mod
└── Makefile
```

---

## 9. Detected Security Controls

### Sandbox Exec Allowlist
**File:** `internal/sandbox/permissions.go:318-390`

- `hardDenyExecBaseCommands` map — commands that cannot be allowed under any override
- `DefaultPolicy()` — returns base allowlist (git, go, node, npm, pnpm, yarn, python, make, bash, sh)
- Per-project override: `.dfmt/permissions.yaml`
- Policy check timing: O(allow_rules + deny_rules) with LRU cache for regex matching

### Output Normalization Pipeline (8 Stages)
**File:** `internal/sandbox/intent.go:440` — `NormalizeOutput()`

| Stage | Function | File | Behavior |
|-------|----------|------|----------|
| 1 | CompactBinary | binary.go:71 | Magic number detection, replaces binary with summary |
| 2 | RemoveANSI | intent.go | Strip ANSI escape codes |
| 3 | ConvertUTF16LEToUTF8 | permissions.go:2247 | BOM detection + heuristic fallback |
| 4 | walkDropNoise | structured.go | Remove null/empty JSON fields |
| 5 | Truncate | intent.go | Token-budget truncation (TailTokens=512) |
| 6 | RLE | intent.go | Collapse 1000+ identical lines |
| 7 | CollapseWhitespace | htmlmd.go:471 | Reduce whitespace runs |
| 8 | ConvertHTMLToMarkdown | htmlmd.go | HTML→Markdown walker |

### Redaction
- No `redact.yaml` file found in project root
- Write journal entries use SHA-256 truncated to 8 bytes + byte count (not raw content) — F-11 fix
- `content_sha16` field in `tool.write` events

### Symlink Protection
**File:** `internal/safefs/safefs.go` (252 lines)

- `CheckNoSymlinks(baseDir, path)` — Lstat check before any write
- `WriteFile()` — uses CheckNoSymlinks + os.WriteFile
- `WriteFileAtomic()` — uses temp file + rename (atomically replaces symlink target)
- TOCTOU documented as residual race window

### Semantic Concurrency Limits
**File:** `internal/transport/handlers.go`

- `writeSem` semaphore: max 4 concurrent write/edit operations
- `readSem` semaphore: max 16 concurrent read/grep operations
- `acquireLimiter()` with context cancellation support

### Fetch Security
- URL normalization before policy check (line 1354)
- Metadata endpoint blocklist (e.g., `HTTP://METADATA.GOOGLE.INTERNAL`)
- Max response bytes: 16 MB (soft cap, configurable)
- Redirect following with limits

---

## 10. Language Detection Summary

**Go: 100%** — This is a pure Go project. No other languages detected.

### Go Version
- `go 1.25.0` in `go.mod` — using latest features

### No Generated Code
- No `.pb.go` (protobuf), no `*_gen.go` (code generation), no `yacc`/lex files
- All `.go` files are hand-written

### Package Count
- 35+ packages across `cmd/`, `internal/`
- Largest files: `permissions.go` (2325 lines), `dispatch.go` (3045 lines), `handlers.go` (1575 lines)