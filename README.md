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

| Agent | Auto-detected | MCP wire-up (Linux path; macOS/Windows follow each agent's convention) |
|---|---|---|
| Claude Code (CLI / desktop / IDE) | ✓ | `~/.claude.json` + `~/.claude/settings.json` |
| Cursor | ✓ | `~/.cursor/mcp.json` |
| VS Code (Copilot agent mode) | ✓ | `~/.config/Code/User/mcp.json` (`servers`) |
| GitHub Copilot CLI | ✓ | `~/.copilot/mcp-config.json` |
| Codex CLI / IDE extension | ✓ | `~/.codex/config.toml` (`[mcp_servers.dfmt]`) |
| Google Antigravity (IDE and `agy` CLI) | ✓ | `~/.gemini/config/mcp_config.json` |
| Devin Desktop (formerly Windsurf) | ✓ | `~/.config/devin/mcp_config.json` |
| Zed | ✓ | `~/.config/zed/settings.json` (`context_servers`) |
| OpenCode | ✓ | `~/.config/opencode/opencode.json` (`mcp`) |
| Kiro | ✓ | `~/.kiro/settings/mcp.json` |
| Cline (VS Code extension) | ✓ | `…/Code/User/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json` |

`dfmt setup` adds one `dfmt` entry to each file and leaves everything
else in it alone; `dfmt setup --uninstall` removes only that entry.
Files with comments (JSONC) are left untouched — setup says so, and you
add the entry by hand. `dfmt setup --verify` re-reads each file and
checks the entry is still there.

**No longer auto-configured:** Gemini CLI (Google stopped serving it for
individual accounts on 2026-06-18; Antigravity is its successor),
Windsurf (renamed Devin Desktop), and Continue.dev (development stopped
in 2026-06). They can still use DFMT through a manual entry.

Any MCP-capable client can use DFMT: register a stdio server that runs
`dfmt mcp`. The auto-detect list above just saves you the manual
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

**From source (requires Go 1.26+):**

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

If anything looks off, `dfmt doctor` runs twelve state checks plus
three cross-cutting passes (per-agent wire-up, instruction-block
staleness, and sandbox PATH visibility) and prints a one-line fix per
failure.

**Updating.** `dfmt update` replaces the binary in place with the latest
release (`--check` to only look, `--version vX.Y.Z` to pick one). The
download is verified against the release's `sha256sums.txt` and run once
before it replaces anything; the running daemon is stopped so the next
command uses the new code. dfmt never checks for updates on its own.

**The daemon goes away when unused.** It exits after
`lifecycle.idle_timeout` (default `30m`) with no calls and starts again
on the next command; set it in `~/.dfmt/config.yaml`, `0` to keep it
running.

## Why use it

**Token savings.** On the noisy workloads in `dfmt-bench tokensaving`
(progress bars, 200-line test runs, kubectl JSON, HTML pages), dfmt reduces
wire bytes 40–90 % vs. raw native output. Those are simulations of the cases
it is built for. Output under ~1 000 tokens is passed through whole, so on
ordinary sessions — mostly short commands and small reads — the saving is
much smaller and arrives in the occasional large output. Measure your own:
`dfmt stats` (estimated tokens saved, plus the fixed cost of the tool
declarations) and `dfmt-bench replay` (per-tool totals from your real
journal, including how many calls were too small to filter). The first
numbers from this repository's own journal: 2.6 % saved, 93 % of calls
small — see ADR-0032.

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
| `index.gob` | persisted search index (JSON payload — `.gob` extension retained for backwards compat) | 0o600 |
| `permissions.yaml` | optional custom allow/deny rules | 0o600 |
| `redact.yaml` | optional custom secret patterns | 0o600 |

The default policy permits common dev tools without per-project
overrides:

- **Version control & language toolchains:** `git`, `go`, `node`,
  `python`, `cargo`, `pytest`, `make`.
- **JS/TS package managers & runners:** `npm`, `pnpm`, `yarn`,
  `bun`, `npx`, `pnpx`, `bunx`, `deno`.
- **TypeScript toolchain:** `tsc`, `tsx`, `ts-node`.
- **JS test runners:** `vitest`, `jest`.
- **Linters & formatters:** `eslint`, `prettier`.
- **Bundlers & dev servers:** `vite`, `next`, `webpack`.
- **Read-only Unix basics:** `echo`, `ls`, `cat`, `find`, `grep`,
  `dir`, `pwd`, `whoami`, `wc`, `tail`.

Each entry ships as a pair (`<cmd>` and `<cmd> *`) — the trailing
space + `*` is the deliberate end-of-token boundary so a rule for
`git` does not accidentally match `git-shell`. See ARCHITECTURE.md
§9.1.1 for the full V-20 contract.

Denies destructive commands (`sudo *`, `rm -rf /*`, `curl * | sh`,
`shutdown *`, `mkfs *`, `dd if=*`, recursive `dfmt`). Add
site-specific overrides in:

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

`dfmt setup --uninstall` removes everything DFMT wrote into agent configs (and
surgically strips its keys from any shared user config like `~/.claude.json`).

`dfmt uninstall` goes all the way: that pass, plus each known project's
`.dfmt/` (journals, indexes) and instruction-file blocks, `~/.dfmt`, the setup
manifest, and the binary itself. It asks first; `--yes` skips the prompt,
`--keep-data` and `--keep-binary` narrow it.

## Project layout

- `cmd/dfmt/` — CLI binary
- `cmd/dfmt-bench/` — token-savings benchmarks
- `dev.sh` / `dev.ps1` — cross-platform dev bootstrap
- `scripts/` — helper scripts
- `install.sh` / `install.ps1` — one-command install
- `internal/core/` — events, journal, BM25 index, tokenizer, classifier
- `internal/daemon/` — daemon lifecycle, per-project resource cache, idle/shutdown
- `internal/sandbox/` — exec/read/fetch/glob/grep/edit/write policy gate
- `internal/transport/` — MCP, JSON-RPC, HTTP dashboard, socket
- `internal/client/` — CLI ↔ daemon RPC client
- `internal/cli/` — subcommand dispatch and runX entry points
- `internal/setup/` — agent auto-detection and config writers
- `internal/redact/` — secret redaction patterns
- `internal/capture/` — git hooks + filesystem watcher + shell hook
- `internal/content/` — ephemeral content store for raw tool output
- `internal/config/` — per-project `config.yaml` loading and merging
- `internal/project/` — project discovery and global-dir paths
- `internal/safefs/` — symlink-safe write helper
- `internal/safejson/` — depth-capped JSON decode (rejects nesting bombs)
- `internal/osutil/` — GOOS classification + case-aware path equality
- `internal/logging/` — leveled logger used by daemon-side code paths
- `internal/version/` — single-source release identity (`version.Current`)
- `internal/retrieve/` — reserved snapshot/markdown rendering scaffolding (path interning, multi-format output); not wired to the production `dfmt_recall` path yet — see `docs/ROADMAP.md` v0.7.

See `AGENTS.md` for the canonical agent-onboarding instructions and
`CLAUDE.md` (which now points at AGENTS.md). Other top-level docs:

- `CHANGELOG.md` — release-by-release user-visible changes.
- `SECURITY.md` — vulnerability reporting + supported versions +
  threat model summary.
- `docs/ROADMAP.md` — v0.6.x / v0.7.0 / v1.0.0 milestone plan.
- `docs/ARCHITECTURE.md` — system architecture deep dive.
- `docs/adr/` — Architecture Decision Records (22 ADRs as of v0.6.8).

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
