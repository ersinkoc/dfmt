# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

DFMT is a local Go daemon that sits between AI coding agents and their tools. It provides sandboxed tool execution (exec, read, fetch) that returns intent-matched excerpts instead of raw output, and session memory across conversation compactions via an append-only event journal.

## Tool usage (mandatory)

This repository is the DFMT project itself. When working on it, you MUST use DFMT's own MCP tools instead of native Claude Code tools. This saves tokens and ensures session memory is recorded correctly.

| Native Tool | DFMT Replacement | Required `intent`? |
|-------------|------------------|-------------------|
| `Read` | `dfmt.read` | Yes — always provide `intent` |
| `Bash` | `dfmt.exec` | Yes — always provide `intent` |
| `WebFetch` | `dfmt.fetch` | Yes — always provide `intent` |

### Rules

1. **Never use native `Read`**, `Bash`, or `WebFetch` when DFMT is available.
2. **Always include `intent`** describing what you need from the output. DFMT returns only intent-matched excerpts, not full content.
3. **Record interactions** with `dfmt.remember` after significant LLM calls to track token usage.
4. **Search memory** with `dfmt.search` when you need context from earlier in the session.
5. **Build snapshots** with `dfmt.recall` when the context window is tight.

### Examples

- Read file: `dfmt.read(path="README.md", intent="installation steps")`
- Run tests: `dfmt.exec(code="go test ./...", intent="failing tests")`
- Fetch docs: `dfmt.fetch(url="...", intent="API auth endpoints")`

## Common commands

| Task | Command |
|------|---------|
| Build binaries (`dist/dfmt`, `dist/dfmt-bench`) | `make build` |
| Run all tests | `make test` (or `go test ./...`) |
| Run tests with race detector | `go test -race ./...` |
| Run a specific package's tests | `go test ./internal/core/...` |
| Lint | `make lint` (`golangci-lint run ./...`) |
| Format | `make fmt` (`go fmt ./...`) |
| Clean build artifacts and local state | `make clean` |
| Install to GOPATH/bin | `make install` |
| Cross-compile release binaries | `make release` |

## Architecture

### Entry points

- `cmd/dfmt/main.go` — CLI entry point. Parses `--project` flag, then dispatches via `internal/cli.Dispatch()`.
- `cmd/dfmt-bench/main.go` — Benchmarking binary for tokenization, indexing, BM25 search, and sandbox execution.

### Command dispatch

`internal/cli/dispatch.go` routes subcommands. Some commands (`init`, `setup`, `doctor`, `daemon`) run locally. Others (`remember`, `search`, `recall`, `exec`, `read`, `fetch`, `stats`, `tail`) communicate with the per-project daemon via `internal/client`, which connects over a Unix socket or TCP.

### Per-project daemon

`internal/daemon/` manages a single daemon per project directory. The daemon auto-starts on first command and idle-exits after a timeout. It owns the journal and index lifecycle.

### Core domain

`internal/core/` is the heart of the system:

- **Events** (`event.go`) — Typed events (file edits, git ops, tasks, decisions, MCP calls, etc.) with priority tiers (p1–p4) and sources (MCP, filesystem watcher, git hook, shell, CLI).
- **Journal** (`journal.go`) — Append-only JSONL file on disk. Durable writes are the default.
- **Index** (`index.go`, `index_persist.go`) — In-memory inverted index using BM25 scoring, with gob persistence. Includes Porter stemming, trigram indexing, and English/Turkish stopwords.
- **Tokenization** (`tokenize.go`) — Unicode-aware tokenization for search indexing.

### Transport layer

`internal/transport/` exposes multiple interfaces simultaneously:

- **MCP over stdio** — Primary agent integration (Claude Code, Cursor, Codex, etc.)
- **HTTP API** — JSON-RPC 2.0 endpoints plus an embedded HTML dashboard served from a string constant in `dashboard.go`
- **Unix socket / TCP** — Used by CLI commands and git hooks to talk to the daemon

### Sandbox

`internal/sandbox/` handles `exec`, `read`, and `fetch` requests. Raw output is stored in the ephemeral content store; only intent-matched excerpts plus a searchable vocabulary are returned to the caller.

### Content store

`internal/content/` — Ephemeral storage for sandbox output, separate from the durable journal. Content is summarized and searchable without polluting the agent's context window.

### Capture pipeline

`internal/capture/` defines five event-ingestion paths, but only two are currently wired into the running daemon:

- **MCP calls** — Routed through transport. **Live.**
- **CLI commands** — Manual `dfmt remember`, `dfmt task`, etc. **Live.**
- **Filesystem watcher** (`fswatch*.go`) — Platform-specific implementations for Linux and Windows. **Not wired**: `NewFSWatcher` is fully implemented and emits on an `Events()` channel, but nothing in the daemon currently consumes that channel.
- **Git hooks** (`git.go`) — post-commit, post-checkout, pre-push. **Stub**: `SubmitCommit`/`SubmitCheckout`/`SubmitPush` build events then discard them (`_ = e; return nil`). No git hook scripts are installed either.
- **Shell integration** (`shell.go`) — Tracks cwd, env vars, commands. **Stub**: `SubmitCommand` builds an event then discards it; comment reads `// Would submit to daemon here`.

The three non-live paths still live under `internal/capture/` with their own unit tests, but they don't contribute to the journal today.

### Agent setup

`internal/setup/` auto-detects installed AI agents (Claude Code, Cursor, VS Code Copilot, etc.) and writes MCP configurations, hooks, and instruction files. Changes are tracked in `~/.local/share/dfmt/setup-manifest.json` for clean uninstall.

### Supporting packages

- `internal/config/` — YAML configuration loading
- `internal/project/` — Project discovery and registry
- `internal/redact/` — Secret redaction (AWS keys, tokens, JWTs, etc.) from all stored content
- `internal/retrieve/` — Snapshot building and markdown rendering for `dfmt recall`
- `internal/logging/` — Internal logging

## Dependency policy

This project follows a strict stdlib-first policy. Only these external dependencies are permitted:

- `golang.org/x/sys` — system calls not in stdlib
- `golang.org/x/crypto` — cryptographic operations
- `gopkg.in/yaml.v3` — YAML configuration

Everything else (HTML parser, BM25, Porter stemmer, MCP protocol, JSON-RPC 2.0) is bundled directly. Adding any new dependency requires an ADR. Prohibited: SQLite, ORMs, web frameworks (Gin/Echo), CLI frameworks (Cobra), logging frameworks (Zap/Logrus).

## Test coverage thresholds

- `internal/core`: 90%+
- `internal/transport`: 85%+
- `internal/daemon`: 80%+
- `internal/cli`: 75%+

All new functionality must include tests; bug fixes must include regression tests.

## Linting

`golangci-lint` is configured in `.golangci.yml`. Key settings: max line length 120, max cyclomatic complexity 20, govet check-shadowing enabled. Run `make lint` before committing.

## ADR process

Architecture Decision Records live in `docs/adr/`. Create an ADR when adding a new component, changing component interactions, adopting a new library, or modifying behavior in a breaking way. Use `0000-adr-process.md` as the template.

## Local state

Each initialized project has a `.dfmt/` directory containing:

- `config.yaml` — Project configuration
- `journal.jsonl` — Append-only event log
- `index.gob` — Persisted search index
- `port` — Current daemon port/socket path
- `redact.yaml` — Custom secret redaction patterns

This directory is added to `.gitignore` by `dfmt init`.
