# AGENTS.md

This file is the canonical onboarding doc for any AI coding agent
working **on the DFMT project itself**. If you are an agent reading
this, follow the rules here whether you arrived via Claude Code,
Cursor, Codex, Gemini, Windsurf, Zed, Continue, OpenCode, or any
other MCP-capable client.

`CLAUDE.md` mirrors this file under the name Claude Code's CLI looks
for. Keep the two in sync — see "Updating these files" at the bottom.

## Project overview

DFMT is a local Go daemon that sits between AI coding agents and their
tools. It provides:

- sandboxed tool execution (`exec`, `read`, `fetch`, `glob`, `grep`,
  `edit`, `write`) that returns intent-matched excerpts instead of raw
  output;
- session memory across conversation compactions via an append-only
  event journal.

## Tool usage (mandatory when working on this repo)

When working on the DFMT codebase, you **must** use DFMT's own MCP
tools instead of native ones. This saves tokens and keeps the journal
honest.

| Native tool | DFMT replacement | Required `intent`? |
|---|---|---|
| `Bash` | `dfmt_exec` | Yes — always provide `intent` |
| `Read` | `dfmt_read` | Yes — always provide `intent` |
| `WebFetch` | `dfmt_fetch` | Yes — always provide `intent` |
| `Glob` | `dfmt_glob` | Yes — always provide `intent` |
| `Grep` | `dfmt_grep` | Yes — always provide `intent` |
| `Edit` | `dfmt_edit` | n/a (no filtering) |
| `Write` | `dfmt_write` | n/a (no filtering) |

### Enforcement and limitations

This rule is enforced by reading. No agent harness has a native
mechanism to disable its own built-in tools. The rules above work
because each major agent **respects this onboarding file**:

- Claude Code reads `CLAUDE.md` automatically.
- Codex CLI, Cursor, and similar MCP clients read `AGENTS.md` (or
  honor it if you tell them to).
- Other agents may need an explicit instruction at the start of a
  session: "follow the rules in AGENTS.md".

If you are running on an agent that uses native tools alongside
DFMT's MCP overlay, DFMT's journal still captures the operations that
go through MCP, but token savings depend on you choosing the DFMT
tool every time.

### Why this matters

DFMT is not just a token filter — it is the **mandatory proxy layer**
for all operations on this repo:

- **Token savings**: intent-matched excerpts cut context window use 40–90 %.
- **Session memory**: every operation is logged so a future agent in
  this repo can `dfmt_recall` what was decided.
- **Intent filtering**: you get exactly what you asked for, not raw
  output pollution.

Using native tools bypasses DFMT entirely. The session becomes a dumb
terminal.

### Error handling

| DFMT error | Required action |
|---|---|
| Tool returns error | Report to user. Do NOT fall back to native tool. |
| Tool times out | Retry once with shorter intent, then report. |
| Tool unavailable | Report to user immediately. |

**Never do this**: `Failed to use dfmt_exec, falling back to native Bash.`

### Rules

1. **Mandatory**: every tool call uses a DFMT MCP tool. Native
   `Bash` / `Read` / `WebFetch` / `Glob` / `Grep` / `Edit` / `Write`
   are prohibited on this repo.
2. **Intent required**: every DFMT call includes `intent` describing
   exactly what you need from the output. Without `intent` the tool
   returns raw bytes — defeating the savings.
3. **On DFMT failure**: report. Do not silently fall back.
4. **Token savings**: DFMT returns excerpts, not full output. The
   `intent` field is the lever.
5. **Session memory**: after a substantive decision (closed bug,
   chose between approaches, learned an invariant), call
   `dfmt_remember` with `type: "note"` and tags that signal value.
   See "Tag conventions for `dfmt_remember`" below.

### Examples

- Read a file → `dfmt_read` with `path="README.md"`,
  `intent="installation steps"`.
- Run tests → `dfmt_exec` with `code="go test ./..."`,
  `intent="failing tests"`.
- Fetch docs → `dfmt_fetch` with `url="..."`,
  `intent="API auth endpoints"`.

> Tool names use underscores (`dfmt_exec`) not dots — MCP spec
> restricts tool names to `^[a-zA-Z][a-zA-Z0-9_-]*$`. The HTTP /
> socket JSON-RPC transports still accept the dotted names
> (`dfmt.exec`) for back-compat with non-MCP clients.

### Tag conventions for `dfmt_remember`

Tags drive priority in the recall snapshot. The classifier elevates
notes that carry these tags:

| Tags | Priority | Use when |
|---|---|---|
| `summary`, `decision`, `strengths`, `ledger` | **P2** | Session-spanning context the next agent must keep |
| `audit`, `finding`, `followup`, `preserve` | **P3** | Individual findings, more numerous |
| (none, or unrelated) | P4 | Incidental observation |

Untagged notes rank equal to a `tool.read` event in the byte-budget
recall — the snapshot may drop them. Tag accordingly.

## Common commands

| Task | Command |
|---|---|
| Build binaries | `make build` (produces `dist/dfmt`, `dist/dfmt-bench`) |
| Run tests | `make test` (or `go test ./...`) |
| Race detector | `CGO_ENABLED=1 go test -race ./...` |
| Lint | `make lint` (`golangci-lint run ./...`) |
| Format | `make fmt` |
| Clean state | `make clean` |
| Install to GOPATH/bin | `make install` |
| Cross-compile release binaries | `make release` |
| Token-savings report | `dfmt-bench tokensaving` |
| Diagnostic bundle | `dfmt bundle` |
| Install git hooks | `dfmt install-hooks` |
| Shell integration snippet | `dfmt shell-init bash\|zsh\|fish` |

## Architecture

### Entry points

- `cmd/dfmt/main.go` — CLI entry. Parses `--project`, dispatches via
  `internal/cli.Dispatch()`.
- `cmd/dfmt-bench/main.go` — benchmarking binary.

### Command dispatch

`internal/cli/dispatch.go` routes subcommands. Some run locally
(`init`, `setup`, `doctor`, `daemon`). Others talk to the per-project
daemon via `internal/client` over Unix socket or TCP.

### Per-project daemon

`internal/daemon/` runs a single daemon per project directory.
Auto-starts on first command, idle-exits after a timeout. Owns the
journal and index lifecycle.

### Core domain

`internal/core/`:

- **Events** (`event.go`) — typed events with priority tiers (p1–p4)
  and sources (MCP, fs watcher, git hook, shell, CLI).
- **Journal** (`journal.go`) — append-only JSONL on disk.
- **Index** (`index.go`, `index_persist.go`) — in-memory inverted
  index with BM25 scoring, custom JSON serialization, Porter stemming,
  trigram support, English/Turkish stopwords.
- **Classifier** (`classifier.go`) — assigns priority by event type
  and tags (see "Tag conventions" above).

### Transport layer

`internal/transport/`:

- MCP over stdio (primary agent integration).
- HTTP JSON-RPC + dashboard (`/dashboard`).
- Unix socket / loopback TCP (CLI ↔ daemon).

### Sandbox

`internal/sandbox/` handles `exec`, `read`, `fetch`, `glob`, `grep`,
`edit`, `write`. Output is summarized, intent-matched, and stashed in
the content store. The default policy allows common dev tools and
denies destructive ones; see `permissions.go::DefaultPolicy()` for
the full list and the godoc explaining how operators add
site-specific rules.

### Capture pipeline

`internal/capture/` defines five event-ingestion paths. Four are
wired today:

- **MCP calls** — routed through transport. Live.
- **CLI commands** — `dfmt remember`, `dfmt task`, etc. Live.
- **Filesystem watcher** — opt-in via `capture.fs.enabled=true`.
- **Git hooks** — opt-in via `dfmt install-hooks`.
- **Shell integration** — opt-in via `dfmt shell-init`.

### Session memory

Events are prioritized (p1–p4). On compaction, `dfmt_recall` rebuilds
a snapshot under a byte budget; lower-tier content drops first.

### Agent setup

`internal/setup/` auto-detects nine agents and writes MCP configs.
Changes are tracked in `~/.local/share/dfmt/setup-manifest.json` for
clean uninstall. `setup --uninstall` removes everything DFMT wrote
and surgically strips Claude Code's shared `~/.claude.json` (other
agents have dedicated `mcp.json` files that get full delete).

## Dependency policy

Strict stdlib-first. Only these external deps are permitted:

- `golang.org/x/sys` — syscalls not in stdlib
- `gopkg.in/yaml.v3` — YAML config

Everything else (HTML parser, BM25, Porter stemmer, MCP protocol,
JSON-RPC 2.0) is bundled directly. Adding a dependency requires an
ADR. **Prohibited**: SQLite, ORMs, web frameworks, CLI frameworks,
logging frameworks.

## Test coverage thresholds

- `internal/core` ≥ 90 %
- `internal/transport` ≥ 85 %
- `internal/daemon` ≥ 80 %
- `internal/cli` ≥ 75 %

All new functionality requires tests; bug fixes require regression
tests.

## Linting

`golangci-lint` is configured in `.golangci.yml`. Key settings:
max line length 120, max cyclomatic complexity 20, govet
check-shadowing on. Run `make lint` before committing.

## ADR process

Architecture Decision Records live in `docs/adr/`. Create one when:

- adding a new component,
- changing component interactions,
- adopting a new dependency,
- modifying behavior in a breaking way.

Use `0000-adr-process.md` as the template.

## Local state

Per-project `.dfmt/` directory:

- `config.yaml` — project configuration (0o600)
- `journal.jsonl` — append-only event log (0o600)
- `index.gob` — persisted search index (0o600)
- `port` — current daemon port / socket path (0o600)
- `lock` — advisory daemon lock (0o600)
- `permissions.yaml` — optional custom policy (0o600)
- `redact.yaml` — optional custom redaction patterns (0o600)

`.dfmt/` is added to `.gitignore` automatically by `dfmt init`.

## Updating these files

`AGENTS.md` and `CLAUDE.md` carry the same content. When you change
one, update the other in the same commit. The format is:

- `AGENTS.md` is the canonical version (agent-neutral).
- `CLAUDE.md` exists because Claude Code looks for that exact
  filename. It can either duplicate the content or be a one-liner
  pointing at AGENTS.md — currently it duplicates so Claude Code
  works without an indirection.

If the divergence between the two files grows, prefer collapsing to a
pointer-style CLAUDE.md and keeping all content here.
