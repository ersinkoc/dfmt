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

DFMT is a strong preference, not a hard dependency. The agent must not be blocked when DFMT misbehaves.

| DFMT error | Required action |
|---|---|
| Tool returns error | Report to user (one line — which call, what error), then fall back to the native tool. |
| Tool times out | Retry once with a shorter intent. If that fails, report and fall back. |
| Tool unavailable / connection refused | Report immediately and fall back. |

The rule is "no **silent** fallback" — every switch to a native tool must be announced. After a fallback, drop a brief `dfmt_remember` note tagged `gap` when practical, so the journal records that a call was bypassed.

**Don't do this**: silently switch to native `Bash` after a DFMT timeout. **Do this**: "dfmt_exec timed out on the test run; falling back to native Bash for this call."

**Both layers denied.** If the native tool is also blocked (permission rule, interactive deny, sandbox refusal), stop. Report both failures in one concise message and ask the user how to proceed — allow the native tool, fix DFMT, or skip the step. Two refusals is a real block, not a puzzle to solve silently. Do not retry the same call, and do not attempt to route around either layer.

### Rules

1. **Mandatory**: every tool call uses a DFMT MCP tool. Native
   `Bash` / `Read` / `WebFetch` / `Glob` / `Grep` / `Edit` / `Write`
   are prohibited on this repo.
2. **Intent required**: every DFMT call includes `intent` describing
   exactly what you need from the output. Without `intent` the tool
   returns raw bytes — defeating the savings.
3. **On DFMT failure**: report the failure (one line) and then fall back to the native tool so the session isn't blocked. The ban is on *silent* fallback — every switch must be announced. If the native tool is also denied, stop and ask the user; don't retry blindly.
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
| Race detector | `go test -race ./...` (Linux/macOS); add `CGO_ENABLED=1` on Windows |
| Lint | `make lint` (`golangci-lint run ./...`) |
| Format | `make fmt` |
| Clean state | `make clean` |
| Install to GOPATH/bin | `make install` |
| Cross-compile release binaries | `make release` |
| Token-savings report | `dfmt-bench tokensaving` |
| One-shot project setup | `dfmt quickstart` (init + per-agent setup + verify) |
| Per-agent wire-up health check | `dfmt doctor` (project state + per-agent MCP files + binary path) |
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
the content store. The default policy allows common dev tools (incl.
the JS/TS toolchain — `tsc`, `tsx`, `ts-node`, `vitest`, `jest`,
`bun`, `deno`, `yarn`, `npx`/`pnpx`/`bunx`, `eslint`, `prettier`,
`vite`, `next`, `webpack`, `make`) and denies destructive ones; see
`permissions.go::DefaultPolicy()` for the full list and the godoc
explaining how operators add site-specific rules.

Custom rules go in `.dfmt/permissions.yaml` — entries take the form
`allow:exec:<base-cmd> *`, `deny:read:**/secrets/**`, etc. Every
sandbox denial error ends with a `hint:` line pointing at this file
and naming which network classes (loopback, RFC1918, cloud metadata)
cannot be opened up via project config.

**Allow-rule contract — `<base-cmd>` is matched literally** (V-20).
The trailing space + `*` in `allow:exec:git *` matters: it means "the
literal token `git` followed by any arguments." A rule of just
`allow:exec:git` (without space and star) matches *only* the bare
command with no arguments, which is rarely useful. A rule of
`allow:exec:git*` (no space) would also match `git-shell`,
`git-receive-pack`, and any other binary whose name starts with `git`
— almost never what you want. **Always include the trailing ` *`** on
exec allow rules so the boundary is the end-of-token, not a substring
match.

Before responses reach the policy filter, `NormalizeOutput`
(`internal/sandbox/intent.go`) runs an 8-stage pipeline:

1. **Binary refusal** — non-UTF-8 / magic-number-detected bodies
   become a one-line `(binary; type=…; sha256=…)` summary.
2. **ANSI strip** — CSI/OSC escape sequences gone.
3. **CR-rewrite collapse** — progress bars / spinners reduced to
   their final state.
4. **RLE** — ≥4 identical adjacent lines compacted with a
   "(repeated N times)" annotation.
5. **Stack-trace path collapse** — Python/Go traces with ≥3
   same-path frames replace continuation paths with a `"…"` marker.
6. **Git diff** — `index <hash>..<hash> <mode>` lines stripped.
7. **Structured-output compaction** (ADR-0010) — JSON / NDJSON /
   YAML noise fields (`created_at`, `*_url`, `_links`,
   `creationTimestamp`, `selfLink`, `managedFields`, pagination
   metadata, null/empty values). Markdown frontmatter stripped from
   leading `---` blocks.
8. **HTML → markdown** (ADR-0008) — full tokenizer + walker; drops
   `<script>`/`<style>`/`<nav>`/`<footer>`/`<aside>`/`<head>`/
   `<noscript>`/`<svg>`/`<form>`/`<button>`/`<iframe>`; emits
   markdown for headings, lists, code blocks (with language hint),
   tables (with GFM separator), blockquotes, definition lists,
   links, images.

The policy filter (`ApplyReturnPolicy`) gates inline / summary /
big-tier on **approximated tokens** (ADR-0012):
`ApproxTokens(s) = ascii_bytes/4 + non_ascii_runes`. CJK and English
bodies hit the same agent-cost threshold. I/O hard caps
(`MaxFetchBodyBytes` 8 MiB, `MaxRawBytes` 256 KB Windows truncation,
`maxRPCResponseBytes` 16 MiB) stay byte-based — they protect
network/system invariants where bytes are the right unit.

Cross-call wire dedup (ADR-0009 / ADR-0011): a `content_id` already
emitted to the agent in this session returns
`(unchanged; same content_id)` instead of repeating the bytes.
Session ID flows through `context.Context` via
`transport.WithSessionID`, so two distinct callers maintain
independent dedup histories.

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
a snapshot under a byte budget; lower-tier content drops first. Path
interning (Refs table at the top + `[rN]` token references, kicks in
at ≥3 occurrences) is implemented in `internal/retrieve/render_md.go`
but **not yet wired** to the production recall handler in
`internal/transport/handlers.go::Recall`; wiring is on the v0.3
roadmap.

`dfmt_search` returns hits with a short `excerpt` field (≤80 bytes,
rune-aligned) drawn from the event's `message` / `path` / `type` —
agents can decide whether to drill in without a follow-up
`dfmt_recall` round-trip.

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
- `index.gob` — persisted search index (0o600). On-disk payload is JSON despite the `.gob` extension (`internal/core/index_persist.go::writeJSONAtomic`); the filename is retained for backwards compat.
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

<!-- dfmt:v1 begin -->
# Context Discipline — REQUIRED

This project uses DFMT to keep large tool outputs from exhausting the
context window. **Read this section at the start of every conversation
in this project.**

## Rule 1 — Prefer DFMT tools over native tools

Always use DFMT's MCP tools when an output might exceed 2 KB:

| Native     | DFMT replacement |
|------------|------------------|
| `Bash`     | `dfmt_exec`      |
| `Read`     | `dfmt_read`      |
| `WebFetch` | `dfmt_fetch`     |
| `Glob`     | `dfmt_glob`      |
| `Grep`     | `dfmt_grep`      |
| `Edit`     | `dfmt_edit`      |
| `Write`    | `dfmt_write`     |

Include an `intent` argument on every call, describing what you need
from the output. The `intent` lets DFMT return the relevant portion of
a large output without flooding the context.

## Rule 2 — On DFMT failure, report and fall back

DFMT is a strong preference, not a hard dependency. If a `dfmt_*` tool
errors, times out, or is unavailable, report the failure to the user
(one short line — which call, what error) and continue with the native
equivalent so the session is not blocked. The ban is on *silent*
fallback — every switch must be announced. After a fallback, drop a
brief `dfmt_remember` note tagged `gap` when practical. If the native
tool is also denied (permission rule, sandbox refusal), stop and ask
the user; do not retry blindly.

## Rule 3 — Record user decisions

When the user states a preference or correction ("use X instead of Y",
"do not modify Z"), call `dfmt_remember` with a `decision` tag so the
choice survives context compaction.

## Why these rules matter

Some agents do not provide hooks to enforce these rules automatically.
**Compliance is your responsibility as the agent.** A single raw shell
output above 8 KB can push earlier context out of the window, erasing
the conversation's history. Following the rules above preserves it.
<!-- dfmt:v1 end -->

