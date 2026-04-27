# CLAUDE.md

This file exists because Claude Code looks for the literal filename
`CLAUDE.md` at repo root. The canonical onboarding document is
**[AGENTS.md](AGENTS.md)** — read that.

Everything below mirrors the rules Claude needs to follow when working
on this repository, but the master copy is `AGENTS.md`. If the two ever
diverge, trust `AGENTS.md` and update this file to match.

## Quick rules (full detail in AGENTS.md)

This repository is the DFMT project itself. When working on it, you
**must** use DFMT's own MCP tools instead of native ones.

| Native | DFMT replacement | `intent` required? |
|---|---|---|
| `Bash` | `dfmt_exec` | yes |
| `Read` | `dfmt_read` | yes |
| `WebFetch` | `dfmt_fetch` | yes |
| `Glob` | `dfmt_glob` | yes |
| `Grep` | `dfmt_grep` | yes |
| `Edit` | `dfmt_edit` | n/a |
| `Write` | `dfmt_write` | n/a |

- Every call passes an `intent` string. Without one, the tool returns
  raw bytes and you lose the savings.
- On DFMT failure, report it to the user. Do **not** silently fall
  back to native tools.
- After substantive decisions, call `dfmt_remember` with tags that
  signal value (`summary`, `decision`, `audit`, `finding`, etc. — see
  AGENTS.md for the elevation table).

## Where to find the rest

- **Onboarding, architecture, common commands, ADR process, local
  state layout** → [AGENTS.md](AGENTS.md)
- **Contributing workflow** → [CONTRIBUTING.md](CONTRIBUTING.md)
- **System architecture diagrams** → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- **Architectural decisions** → [docs/adr/](docs/adr/)
