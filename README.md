# DFMT

> A local proxy daemon between AI coding agents and their tools. Returns
> intent-matched excerpts instead of raw output, and persists session
> memory across conversation compactions.

## What it does

When your AI agent reads a 60 KB file or runs a 300 KB-output build, the
agent's context window pays the full token cost — even though only a few
lines are actually relevant. DFMT sits between the agent and its tools,
runs the operation, then returns:

- a short summary,
- the lines that match the agent's stated `intent`,
- a chunk-set ID the agent can use to fetch the rest if it needs to.

Plus a journal that survives conversation compaction, so a second agent
in the same project can recall what the first one decided.

## Supported AI agents

| Agent | Auto-detected | MCP wire-up |
|---|---|---|
| Claude Code (CLI / desktop / IDE) | ✓ | `~/.claude.json` + `~/.claude/settings.json` |
| Cursor | ✓ | `~/.cursor/mcp.json` |
| VS Code (with Copilot or MCP-aware extension) | ✓ | `~/.vscode/mcp.json` |
| Codex CLI | ✓ | `~/.codex/mcp.json` |
| Gemini CLI | ✓ | `~/.gemini/mcp.json` |
| Windsurf | ✓ | `~/.windsurf/mcp.json` |
| Zed | ✓ | `~/.config/zed/mcp.json` |
| Continue | ✓ | `~/.config/continue/mcp.json` |
| OpenCode | ✓ | `~/.config/opencode/mcp.json` |

Any MCP-capable client can use DFMT — point its `mcp.json` at the
`dfmt mcp` command. The auto-detect list above just saves you the manual
config step.

## Quick start

**One-line install (Linux / macOS / FreeBSD):**

```sh
curl -fsSL https://raw.githubusercontent.com/ersinkoc/dfmt/main/install.sh | sh
```

**One-line install (Windows PowerShell):**

```powershell
iwr https://raw.githubusercontent.com/ersinkoc/dfmt/main/install.ps1 | iex
```

**From source (requires Go 1.25+):**

```sh
go install github.com/ersinkoc/dfmt/cmd/dfmt@latest
```

The installers also run `dfmt setup --force` on your behalf so every
detected agent's MCP config is wired up. Then in a project:

```sh
cd ~/path/to/your/project
dfmt quickstart       # init + per-agent setup + verify, in one shot
```

That's it. **Restart your AI agent** so it re-reads its MCP config, and
ask it to read a file or run a command — the call will route through
DFMT automatically.

To confirm the wire-up actually works:

```sh
dfmt stats            # non-zero events_total + bytes_saved means yes
```

If anything looks off, `dfmt doctor` runs ten checks (project state,
daemon liveness, per-agent wire-up, dfmt binary resolvable) and prints
a one-line fix per failure.

## Why use it

**Token savings.** On the workloads in `dfmt-bench tokensaving`, dfmt
reduces wire bytes 40–90 % vs. raw native output. The agent gets the
matched excerpts, not the haystack.

**Session memory across compaction.** When the agent's conversation is
compacted (most providers do this around 80 % context), DFMT's journal
keeps every tool call, edit, decision, and prompt. `dfmt recall` rebuilds
a snapshot under a byte budget — critical events first.

**Intent-driven filtering.** Every read / fetch / exec accepts an
`intent` string. DFMT does BM25 over the output and returns the top
matches, plus a small vocabulary so the agent can refine. No intent
means no filter.

## How it works

```
┌────────────┐   MCP / stdio   ┌──────────┐   subprocess / files / HTTP
│ AI agent   │ ──────────────► │ dfmt     │ ──────────────────────────►
│ (any of 9) │ ◄────────────── │ daemon   │ ◄──────────────────────────
└────────────┘  filtered out   └──────────┘     raw in
```

- `dfmt mcp` is what the agent launches. It speaks MCP over stdio.
- The first MCP call auto-starts a per-project daemon over a Unix
  socket (Linux/macOS) or loopback TCP (Windows).
- The daemon runs the actual operation, redacts secrets, applies the
  return-policy filter, and stashes the raw bytes in a content store.
- The agent sees the filtered output. The journal sees the redacted
  full record.

## Configuration

Per-project state lives in `.dfmt/`:

| File | Purpose | Mode |
|---|---|---|
| `config.yaml` | feature flags + capture toggles | 0o600 |
| `journal.jsonl` | append-only event log | 0o600 |
| `index.gob` | persisted search index | 0o600 |
| `permissions.yaml` | optional custom allow/deny rules | 0o600 |
| `redact.yaml` | optional custom secret patterns | 0o600 |

The default policy permits common dev tools (`git`, `npm`, `pnpm`,
`pytest`, `cargo`, `go`, `node`, `python`, plus the read-only Unix
basics) and denies destructive commands (`sudo`, `rm -rf /`,
`curl|sh`, etc.). Add site-specific rules with:

```yaml
# .dfmt/permissions.yaml
deny:read:creds/**
deny:read:**/private_keys/**
deny:write:creds/**
```

DFMT denial errors point at this file directly — every "operation
denied by policy" message ends with a `hint:` line telling you which
file to edit and which network classes are non-negotiable (loopback,
RFC1918, cloud metadata).

## Troubleshooting

**The wire-up isn't working.** Run `dfmt doctor` first — it now prints
a per-agent block:

```
AI agent wire-up:
✓ Claude Code — 3 file(s) in place
✗ Cursor — 1/2 files missing (run `dfmt setup --force` to restore)
    missing: /home/x/.cursor/mcp.json
✓ DFMT binary — /usr/local/bin/dfmt
```

Most "DFMT MCP server failed to start" reports come down to one of
the failing lines above.

**`go: command not found` from inside the sandbox.** The sandbox passes
through the daemon's `PATH`. Make sure the daemon was started in a shell
that sees the binary. Restart with `dfmt stop` (the daemon auto-starts
on the next call with fresh env).

**Setting up a new agent that's not auto-detected.** Point its MCP
config at the dfmt binary directly:

```json
{
  "mcpServers": {
    "dfmt": {
      "command": "/usr/local/bin/dfmt",
      "args": ["mcp"]
    }
  }
}
```

`dfmt setup --uninstall` removes everything DFMT wrote (and surgically
strips its keys from any shared user config like `~/.claude.json`).

## Project layout

- `cmd/dfmt/` — CLI binary
- `cmd/dfmt-bench/` — token-savings benchmarks
- `internal/core/` — events, journal, BM25 index, tokenizer, classifier
- `internal/sandbox/` — exec/read/fetch/glob/grep/edit/write policy gate
- `internal/transport/` — MCP, JSON-RPC, HTTP dashboard
- `internal/setup/` — agent auto-detection and config writers
- `internal/redact/` — secret redaction patterns
- `internal/safefs/` — symlink-safe write helper
- `internal/capture/` — git hooks + filesystem watcher + shell hook
- `internal/content/` — ephemeral content store for raw tool output
- `internal/retrieve/` — recall-snapshot building and markdown rendering

See `AGENTS.md` for the canonical agent-onboarding instructions and
`CLAUDE.md` (which now points at AGENTS.md). Architecture decisions
live under `docs/adr/`.

## Dependency policy

DFMT follows a strict stdlib-first rule. The runtime tree has exactly
two third-party Go modules:

- `golang.org/x/sys` (syscalls)
- `gopkg.in/yaml.v3` (config loading)

Everything else — HTML parsing, BM25, Porter stemmer, MCP wire format,
JSON-RPC 2.0 — is bundled in-tree. Adding a new dependency requires an
ADR.

## Contributing

Open an issue first if the change is non-trivial. Tests are required
for new behavior; bug fixes need a regression test. Run `make test`
and `make lint` before pushing. See `CONTRIBUTING.md` for the full
workflow and `docs/adr/0000-adr-process.md` for the ADR template.

## License

MIT — see `LICENSE`.
