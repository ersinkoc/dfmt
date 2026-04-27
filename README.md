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

```sh
# 1. Install (requires Go 1.25+)
go install github.com/ersinkoc/dfmt/cmd/dfmt@latest

# 2. Initialize the project you're working in
cd ~/path/to/your/project
dfmt init

# 3. Wire DFMT into every detected AI agent on this machine
dfmt setup

# 4. Verify everything works
dfmt doctor
```

Then **restart your AI agent** (so it re-reads its MCP config). Ask it
to read a file or run a command — it will route through DFMT
automatically.

To check that the tool actually went through DFMT:

```sh
dfmt stats
```

You should see non-zero `events_total` and a non-zero `bytes_saved`.

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

## Troubleshooting

**"DFMT MCP server failed to start" in my agent.** Run `dfmt doctor`
in the project root. The most common causes: dfmt binary not on
`PATH`, project not initialized (`dfmt init` first), or the daemon
lockfile is stale (`dfmt doctor` will surface and offer to clean it).

**"operation denied by policy" when running a command.** DFMT's
default sandbox refuses anything not in the allow list. Add the
command to `.dfmt/permissions.yaml`:

```yaml
allow:exec:make *
```

**Non-loopback bind refused.** DFMT only binds to loopback by design
(no auth layer). If you set `transport.http.bind` to `0.0.0.0:NNNN`
or a LAN IP, the daemon refuses to start. Use `127.0.0.1:NNNN`
instead.

**Sandbox can't find my binary (`go: command not found` etc.).** The
sandbox passes through the daemon's `PATH`. Make sure the daemon was
started in a shell that sees the binary. Restart the daemon with
`dfmt daemon stop && dfmt daemon start` if you've installed new
toolchains since.

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
- `internal/core/` — events, journal, BM25 index, tokenizer
- `internal/sandbox/` — exec/read/fetch/glob/grep policy gate
- `internal/transport/` — MCP, JSON-RPC, HTTP dashboard
- `internal/setup/` — agent auto-detection and config writers
- `internal/redact/` — secret redaction patterns
- `internal/safefs/` — symlink-safe write helper
- `internal/capture/` — git hooks + filesystem watcher

See `AGENTS.md` for the canonical agent-onboarding instructions and
`CLAUDE.md` for the same content scoped to Claude Code's CLAUDE.md
convention. Architecture decisions live under `docs/adr/`.

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
and `make lint` before pushing.

## License

See `LICENSE` (TBD).
