# DFMT

**Your tokens, your work, your agent — undisturbed.**

DFMT is a local daemon that keeps AI coding agents from wasting your context window — and from losing your working state when the conversation resets.

[![MIT License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Go Report](https://goreportcard.com/badge/github.com/ersinkoc/dfmt)](https://goreportcard.com/report/github.com/ersinkoc/dfmt)
[![Release](https://img.shields.io/github/v/release/ersinkoc/dfmt)](https://github.com/ersinkoc/dfmt/releases)

---

## The problem

Every AI coding session fails in two predictable ways:

**1. Tool output floods your context window.** Your agent runs a shell command, reads a file, fetches a URL — raw output lands in the context and stays there. After 45 minutes, half the window is consumed by stale data the agent no longer needs but can't forget.

**2. Compaction destroys your working state.** When the context fills, the agent compacts the conversation. Your last request, active tasks, user decisions — gone. The agent starts asking questions it asked an hour ago.

## What DFMT does

DFMT is a local daemon that sits between your AI coding agent and its tools. It solves both problems with one process.

**Sandboxed tool execution.** Instead of the agent running `Bash` and dumping 56 KB of output into context, it calls `dfmt.exec(code: "...", intent: "...")`. The subprocess runs locally; the raw output is indexed in an ephemeral store; your context receives only the intent-matched excerpts plus a searchable vocabulary.

**Session memory across compactions.** DFMT captures events as they happen — files edited, tasks created, user decisions, git operations, errors. When the conversation compacts or starts fresh, DFMT rebuilds a budget-capped snapshot of working state. The agent continues without asking you to repeat yourself.

**One daemon per project.** Auto-starts on first command, idle-exits after 30 minutes. No globally running background process. No manual lifecycle.

**Works everywhere.** MCP + hooks + CLI on all major AI coding agents. `dfmt setup` detects what you have installed and configures each.

## Install

### macOS and Linux

```bash
curl -fsSL https://dfmt.dev/install.sh | sh
```

### Homebrew (macOS, Linux)

```bash
brew install ersinkoc/tap/dfmt
```

### Windows (Scoop)

```powershell
scoop bucket add ersinkoc https://github.com/ersinkoc/scoop-bucket
scoop install dfmt
```

### From source

```bash
go install github.com/ersinkoc/dfmt/cmd/dfmt@latest
```

No runtime dependencies. Single 8 MB static binary. Works on Alpine, standard Linux, macOS (Intel and Apple Silicon), Windows, FreeBSD.

## Quickstart

```bash
# In your project directory
cd my-project

# Initialize (creates .dfmt/ and adds it to .gitignore)
dfmt init

# Auto-configure every AI coding agent on your machine
dfmt setup

# Done. Restart your agent. DFMT's sandbox is preferred from the next session.
```

`dfmt setup` output:

```
Scanning for installed AI coding agents...

✓ Claude Code          ~/.claude/           (hook support: full)
✓ Cursor               ~/.cursor/           (hook support: partial)
✓ Codex CLI            ~/.codex/            (hook support: none — instructions only)
○ Gemini CLI           not detected
✓ VS Code Copilot      project .vscode/     (hook support: full)

For each detected agent, will write:
  - MCP server registration
  - Hook configuration (where supported)
  - Instruction file (CLAUDE.md / AGENTS.md / .cursorrules)

Proceed? [Y/n]
```

After confirmation, a manifest of every change is stored at `~/.local/share/dfmt/setup-manifest.json` for clean `dfmt setup --uninstall`.

## How it works

### Sandbox tools (keep tokens out of your context)

Your agent calls DFMT's sandbox instead of native tools:

| Native tool | DFMT replacement | Effect |
| --- | --- | --- |
| `Bash(cmd)` | `dfmt.exec(code, intent)` | 56 KB stdout → 2 KB intent-matched excerpt |
| `Read(path)` | `dfmt.read(path, intent)` | 200-line file → 20-line relevant section |
| `WebFetch(url)` | `dfmt.fetch(url, intent)` | 60 KB HTML → markdown summary + chunks |

The `intent` argument is key. "I want to find auth failures in this log" returns the matching lines plus a vocabulary of other interesting terms ("rate-limit," "timeout," "5xx"). The agent can follow up with `dfmt.search_content` to dig deeper without re-loading the raw output.

### Session memory (keep your work across compactions)

While you work, DFMT captures events from five sources: MCP tool calls, filesystem watcher, git hooks, shell integration, CLI commands. Events flow into a local append-only JSONL journal. Every event type has a priority tier (critical user decisions, important file edits and git ops, normal tool calls, informational stats).

When the agent compacts the conversation or you start a fresh session, DFMT's `dfmt.recall` rebuilds a snapshot under a byte budget — critical events first, dropping lower-tier content if the budget is tight — and hands it back for injection. The agent continues from your last prompt without a "wait, what were we doing?" turn.

### Agent-agnostic architecture

DFMT does not depend on any single agent's extension model. It exposes:

- **MCP server** over stdio for any MCP-capable agent (Claude Code, Cursor, Codex, Gemini, Copilot, OpenCode, Zed, Continue.dev, Windsurf, anything new).
- **Unix socket** for CLI and git hook integration.
- **HTTP API** (opt-in) for custom scripts and CI.
- **CLI commands** for terminal use and debugging.

Even with no agent at all — just a developer editing files and making commits — DFMT captures a useful session record. The FS watcher and git hooks work without any agent present.

## Supported agents

| Agent | MCP | Hooks | Sandbox routing | Session memory |
| --- | --- | --- | --- | --- |
| Claude Code | ✓ | ✓ | ~95% | Full |
| Gemini CLI | ✓ | ✓ | ~95% | High |
| VS Code Copilot | ✓ | ✓ | ~95% | High |
| OpenCode | ✓ | plugin | ~95% | High |
| Cursor | ✓ | partial | ~70% | Limited |
| Zed | ✓ | — | ~65% | Limited |
| Continue.dev | ✓ | — | ~65% | Limited |
| Windsurf | ✓ | — | ~65% | Limited |
| Codex CLI | ✓ | — | ~65% | — |

"Sandbox routing" is the percentage of native tool calls redirected to DFMT's sandbox when the agent is using DFMT. Agents with hook support can enforce routing programmatically; agents without hooks rely on instruction files (CLAUDE.md, AGENTS.md, etc.) which are persuasive but not binding.

See [AGENT-INTEGRATION.md](AGENT-INTEGRATION.md) for per-agent setup, config paths, restart requirements, and troubleshooting.

## Commands

```bash
# Project lifecycle
dfmt init                          # initialize project
dfmt init --preset node            # with framework preset (node | python | go | rust | rails | jvm | php | docker | strict | permissive)
dfmt init --sub                    # mark this directory as a sub-project (for monorepos)
dfmt setup                         # configure installed agents
dfmt setup --agent claude          # configure one specific agent
dfmt setup --dry-run               # preview without writing
dfmt setup --uninstall             # remove DFMT from all agents

# Daemon lifecycle (usually automatic)
dfmt status                        # current project daemon status
dfmt stop                          # stop this project's daemon
dfmt list                          # all running daemons across projects
dfmt doctor                        # diagnostic checks

# Memory and retrieval
dfmt remember <type> <body>        # record an event manually
dfmt note <body>                   # shorthand for a note event
dfmt task <body>                   # create a task
dfmt recall                        # print current session snapshot
dfmt search <query>                # search session events
dfmt tail --follow                 # stream events live
dfmt stats                         # show stats or open dashboard

# Sandbox tools (usually called by the agent)
dfmt exec <code> --lang bash --intent "..."
dfmt read <path> --intent "..."
dfmt fetch <url> --intent "..."
dfmt content search <query>        # query the ephemeral content store

# Support
dfmt bundle                        # create a redacted support bundle for bug reports
dfmt --licenses                    # show third-party license attribution
```

All commands support `--json` for machine-readable output and `--project <path>` to target a specific project.

## Dashboard

DFMT includes a web dashboard for visualizing your session statistics.

**Access the dashboard:**

```bash
# Start the daemon and visit the dashboard URL
dfmt daemon &
# Then open: http://localhost:<port>/dashboard

# Or use the stats command for instructions
dfmt stats
```

**Dashboard features:**
- Total event count
- Events by type (bar chart)
- Events by priority tier (P1-P4)
- Session duration and timeline
- Real-time refresh

**HTTP API:**

The daemon exposes an HTTP API for custom integrations:

```bash
# Get session statistics
curl -X POST http://localhost:25076/api/stats \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"dfmt.stats","params":{},"id":1}'

# Response:
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "events_total": 142,
    "events_by_type": {
      "file.edit": 45,
      "git.commit": 12,
      "task.done": 8
    },
    "events_by_priority": {
      "p1": 15,
      "p2": 38,
      "p3": 67,
      "p4": 22
    },
    "session_start": "2024-01-21T10:30:00Z",
    "session_end": "2024-01-21T11:45:00Z"
  }
}
```

**Available API endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/dashboard` | GET | HTML dashboard UI |
| `/api/stats` | POST | JSON stats via JSON-RPC 2.0 |
| `/` | POST | Main JSON-RPC endpoint (dfmt.remember, dfmt.search, dfmt.recall) |

## Benchmarks

Raw numbers from real sessions:

| Scenario | Without DFMT | With DFMT | Reduction |
| --- | --- | --- | --- |
| Playwright snapshot | 56 KB | 299 B | 99% |
| GitHub issues list (20 items) | 60 KB | 1.1 KB | 98% |
| Access log (500 requests) | 45 KB | 155 B | 100% |
| Package-manager install output | 40 KB | 2 KB | 95% |
| Large JSON API response | 7.5 MB | 0.9 KB | 99% |

Session-level impact: a typical 30-minute coding session that would normally consume 60% of a 200 KB context window consumes 10-15% with DFMT. Time to first compaction extends from ~30 minutes to ~3 hours.

Query performance (local, 2023 laptop, SSD):

- `dfmt.remember` durable write: 1.5 ms p50, 5 ms p99
- `dfmt.search` @ 10k events: 1 ms p50, 5 ms p99
- `dfmt.recall` snapshot build: 3 ms p50, 10 ms p99
- Daemon cold start: 100 ms typical (includes index rebuild)

## Design principles

DFMT is opinionated. A few positions that matter:

- **Local-first.** Nothing leaves your machine. No accounts, no cloud, no telemetry.
- **One daemon per project.** Crash-isolated, auto-started, idle-exits. No global long-running process.
- **Stdlib-first.** Pure Go standard library plus `x/sys`, `x/crypto`, `yaml.v3`. Everything else — HTML parser, BM25, Porter stemmer, MCP protocol — bundled. No supply-chain sprawl.
- **Inspectable storage.** The journal is append-only JSONL. `cat`, `grep`, `jq` work. No SQLite to attach, no opaque binary format.
- **One-command setup across every agent.** You run `dfmt setup` once. Every installed AI coding agent on your machine picks up DFMT's tools. No per-agent manual configuration.

Full architecture and the reasoning behind each decision: [SPECIFICATION.md](SPECIFICATION.md). The nine Architecture Decision Records in [docs/adr/](docs/adr/) capture why DFMT is shaped the way it is, with the alternatives considered and rejected.

## Privacy

DFMT is MIT-licensed, local-only, and has no telemetry. Zero data leaves your machine by design. The daemon binds to `127.0.0.1` only. The Unix socket has `0700` permissions. The event journal is readable by your user account and no one else.

Secret patterns (AWS keys, GitHub/OpenAI/Anthropic/Stripe tokens, JWTs, URL-embedded credentials) are redacted from all stored content — journal, sandbox output, file reads — before persistence. User-configurable patterns via `.dfmt/redact.yaml`.

## Support and bugs

- **Issues:** [github.com/ersinkoc/dfmt/issues](https://github.com/ersinkoc/dfmt/issues)
- **Discussions:** [github.com/ersinkoc/dfmt/discussions](https://github.com/ersinkoc/dfmt/discussions)
- **Email:** `info@dfmt.dev

When filing a bug, run `dfmt bundle` to produce a redacted diagnostic tarball with logs, config, and last 1000 events. Attach it to the issue. The bundle is never transmitted automatically — you control what gets shared.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development workflow, test requirements, and the ADR process.

Short form: tests are required, the dependency policy is strict (see [ADR-0004](docs/adr/0004-stdlib-only-deps.md)), decisions that change structure need an ADR.

## Part of the ersinkoc open-source portfolio

DFMT is one of a family of infrastructure tools under the `ersinkoc` GitHub org. Sibling projects include:

- **[DFMC](https://github.com/ersinkoc/dfmc)** — Don't Fuck My Code. Code-quality companion for AI-assisted development.
- **[Karadul](https://github.com/ersinkoc/karadul)** — Self-hosted mesh VPN. Tailscale alternative.
- **[NothingDNS](https://github.com/ersinkoc/nothingdns)** — Full DNS server. Nothing but DNS.
- **[Kervan](https://github.com/ersinkoc/kervan)** — Multi-protocol file transfer server.
- **[Argus](https://github.com/ersinkoc/argus)** — Database firewall.

All share the same philosophy: single binary, zero external dependencies, MIT-licensed, documentation-first.

## License

MIT. See [LICENSE](LICENSE) and [LICENSE-THIRD-PARTY.md](LICENSE-THIRD-PARTY.md) for attribution of bundled components.

---

Built by [Ersin Koç](https://github.com/ersinkoc) at [ECOSTACK TECHNOLOGY OÜ](https://ecostack.ee).

If DFMT saves you tokens, that's enough. A star on GitHub or a post about your experience keeps the project visible to others who need it.
