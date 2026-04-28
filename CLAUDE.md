# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

The canonical agent-onboarding document is **[AGENTS.md](AGENTS.md)** — this file mirrors the Claude-relevant subset. If the two diverge, trust `AGENTS.md` and update this file to match.

## Project

DFMT is a local Go daemon that sits between AI coding agents and their tools. It runs `exec` / `read` / `fetch` / `glob` / `grep` / `edit` / `write` on the agent's behalf, returns intent-matched excerpts instead of raw output, and persists every call in an append-only journal so a future agent can recall what was decided after context compaction.

This repository is DFMT itself. When working on it, you are dogfooding the daemon — its MCP tools must be used in place of native ones (rules below).

<!-- dfmt:v1 begin -->
## Context Discipline

This project uses DFMT to keep tool output from flooding the context
window and to preserve session state across compactions. When working
in this project, follow these rules.

### Tool preferences

Prefer DFMT's MCP tools over native ones:

| Native     | DFMT replacement | `intent` required? |
|------------|------------------|--------------------|
| `Bash`     | `dfmt_exec`      | yes                |
| `Read`     | `dfmt_read`      | yes                |
| `WebFetch` | `dfmt_fetch`     | yes                |
| `Glob`     | `dfmt_glob`      | yes                |
| `Grep`     | `dfmt_grep`      | yes                |
| `Edit`     | `dfmt_edit`      | n/a                |
| `Write`    | `dfmt_write`     | n/a                |

Every `dfmt_*` call MUST pass an `intent` parameter — a short phrase
describing what you need from the output (e.g. "failing tests",
"error message", "imports"). Without `intent` the tool returns raw
bytes and the token savings are lost.

On DFMT failure, report it to the user (one short line — which call,
what error) and then fall back to the native tool so the session is
not blocked. The ban is on *silent* fallback — every switch must be
announced. After a fallback, drop a brief `dfmt_remember` note tagged
`gap` when practical, so the journal records that a call was bypassed.
If the native tool is also denied (permission rule, sandbox refusal),
stop and ask the user; do not retry blindly.

### Session memory

DFMT tracks tool calls automatically. After substantive decisions or
findings, call `dfmt_remember` with descriptive tags (`decision`,
`finding`, `summary`) so future sessions can recall the context after
compaction.

### When native tools are acceptable

Native `Bash` and `Read` are acceptable for outputs you know are small
(< 2 KB) and will not be referenced again. For everything else, DFMT
tools are preferred.
<!-- dfmt:v1 end -->

## Common commands

| Task | Command |
|---|---|
| Build binaries | `make build` (produces `dist/dfmt`, `dist/dfmt-bench`) |
| Run all tests | `make test` (or `go test ./...`) |
| Run one package | `go test ./internal/core/...` |
| Run one test | `go test ./internal/core -run TestJournalAppend` |
| Race detector | `go test -race ./...` (Linux/macOS; add `CGO_ENABLED=1` on Windows) |
| Lint | `make lint` (`golangci-lint run ./...`) — max line 120, max cyclo 20, shadow-check on |
| Format | `make fmt` |
| Clean local state | `make clean` |
| Token-savings benchmark | `dfmt-bench tokensaving` |
| Wire-up health check | `dfmt doctor` (project state + per-agent MCP files + binary path) |
| Project bootstrap | `dfmt quickstart` (init + setup + verify) |

## Architecture (big picture)

### Entry points

- `cmd/dfmt/main.go` — CLI entry. Parses `--project`, dispatches via `internal/cli.Dispatch()`.
- `cmd/dfmt-bench/main.go` — token-savings benchmark binary.

### Two-process model

`internal/cli/dispatch.go` routes subcommands. Local-only commands (`init`, `setup`, `doctor`, `daemon`) run in-process; everything else talks to a **per-project daemon** via `internal/client` over a Unix socket (Linux/macOS) or loopback TCP (Windows). The daemon auto-starts on first call and idle-exits after a timeout. It owns the journal and index lifecycle.

### Core domain (`internal/core/`)

- **Events** (`event.go`) — typed events with priority tiers `p1`–`p4` and sources (MCP, fs watcher, git hook, shell, CLI).
- **Journal** (`journal.go`) — append-only JSONL on disk.
- **Index** (`index.go`, `index_persist.go`) — in-memory inverted index with BM25, Porter stemmer, trigrams, English+Turkish stopwords, custom JSON serialization.
- **Classifier** (`classifier.go`) — assigns priority by event type and tags. The "Tag conventions" table above is what this reads.

### Transport (`internal/transport/`)

Three faces, same daemon:
- MCP over stdio — primary agent integration (`dfmt mcp`).
- HTTP JSON-RPC + dashboard at `/dashboard`.
- Unix socket / loopback TCP — CLI ↔ daemon.

### Sandbox (`internal/sandbox/`)

Implements the seven tool primitives. Output is summarized, intent-matched against the BM25 index, and the raw bytes are stashed in `internal/content/` (the ephemeral content store). The default policy in `permissions.go::DefaultPolicy()` allows common dev tools (`git`, `npm`, `pnpm`, `pytest`, `cargo`, `go`, `node`, `python`, basic Unix read-only) and denies destructive ones (`sudo`, `rm -rf /`, `curl|sh`). Operators add overrides in `.dfmt/permissions.yaml`. Every denial error ends with a `hint:` line naming the file to edit and the network classes that cannot be opened up (loopback, RFC1918, cloud metadata).

### Capture pipeline (`internal/capture/`)

Five ingestion paths feed the journal: MCP calls (live), CLI commands like `dfmt remember` / `dfmt task` (live), filesystem watcher (opt-in), git hooks (opt-in via `dfmt install-hooks`), shell integration (opt-in via `dfmt shell-init`).

### Recall (`internal/retrieve/`)

`dfmt_recall` rebuilds a markdown snapshot under a byte budget. Per-tier streaming with FIFO eviction — lower-priority content drops first when the budget tightens.

### Agent setup (`internal/setup/`)

Auto-detects nine agents (Claude Code, Cursor, VS Code, Codex, Gemini, Windsurf, Zed, Continue, OpenCode) and writes their MCP configs. Tracks every change in `~/.local/share/dfmt/setup-manifest.json` for clean uninstall. `setup --uninstall` removes everything DFMT wrote and surgically strips Claude Code's keys from the shared `~/.claude.json`.

### Symlink-safe writes (`internal/safefs/`)

Use `safefs.WriteFile` / `safefs.WriteFileAtomic` for any new write site under a project-managed path. The helper closed an F-04/F-07/F-08/F-25 cluster around symlink traversal.

## Hard invariants

### Dependency policy — strict stdlib-first

Only two third-party Go modules are permitted in the runtime tree:
- `golang.org/x/sys` (syscalls)
- `gopkg.in/yaml.v3` (config)

Everything else — HTML parser, BM25, Porter stemmer, MCP wire format, JSON-RPC 2.0 — is bundled in-tree. Adding a dependency requires an ADR. **Prohibited**: SQLite, ORMs, web frameworks, CLI frameworks, logging frameworks.

### Test coverage thresholds

- `internal/core` ≥ 90 %
- `internal/transport` ≥ 85 %
- `internal/daemon` ≥ 80 %
- `internal/cli` ≥ 75 %

New functionality requires tests; bug fixes require regression tests.

### ADR required when

Adding a new component, changing component interactions, adopting a dependency, or making a breaking behavior change. ADRs live in `docs/adr/`; use `0000-adr-process.md` as the template.

### Local state (per-project `.dfmt/`)

`config.yaml`, `journal.jsonl`, `index.gob`, `port`, `lock`, optional `permissions.yaml` and `redact.yaml`. All `0o600`. `.dfmt/` is added to `.gitignore` automatically by `dfmt init`.

### Line endings

Repo is **LF in both index and working tree**. Do not renormalize to CRLF; if drift appears, repair direction is CRLF → LF.

## Where to find the rest

- **Canonical onboarding (agent-neutral)** → [AGENTS.md](AGENTS.md)
- **Contributing workflow** → [CONTRIBUTING.md](CONTRIBUTING.md)
- **System architecture diagrams** → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- **Architectural decisions** → [docs/adr/](docs/adr/)

When you change content here, mirror it in `AGENTS.md` in the same commit.
