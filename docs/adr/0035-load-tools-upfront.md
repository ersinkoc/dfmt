# ADR-0035: Ask Hosts to Load dfmt's Tools Upfront

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-05 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

Claude Code defers MCP tools by default ("tool search"): at session start the
model sees only tool *names*, and must call `ToolSearch` to load a schema
before it can use the tool. An agent that does not search never uses the tool.

Measured from the Claude Code transcripts on one developer machine, four
projects with dfmt configured and its daemon healthy throughout:

| Project | Sessions | Sessions with a dfmt call |
| --- | --- | --- |
| NothingDNS | 11 | 0 |
| uwas | 4 | 0 |
| WireRift | 4 | 0 |
| OpenDaemon | 2 | 1 (35 calls, after the agent loaded the tools by hand) |

Every project's transcripts list the dfmt tools as deferred. The daemon,
proxies and journals were fine; the agent was never shown a tool it could
call, so it used native Read/Bash and dfmt saved nothing. `dfmt init`'s block
said "prefer `dfmt_*`" but never said how to load them.

## Decision

Three independent measures, any one of which would help on its own:

1. **`tools/list` marks every tool `_meta: {"anthropic/alwaysLoad": true}`**
   (documented for Claude Code v2.1.285+; other hosts ignore `_meta`). This
   fixes the cause for every user with no configuration.
2. **`initialize` returns `instructions`** (under 1 KiB). The host sends it to
   the model at session start even while tools are deferred. It names the
   tools, asks for `intent`, and says how to load deferred tools.
3. **The generated instruction blocks** (`CLAUDE.md`, `AGENTS.md`, Copilot)
   carry the exact `ToolSearch` query that loads every dfmt tool — the
   fallback for hosts or versions that honor neither of the above.

## Consequences

- Loading the schemas upfront costs the `tools/list` fixed cost (~1.7k tokens,
  already shown by `dfmt stats`) in every session. That is the price of the
  tool being usable at all; unused, dfmt saved zero.
- `tools/list` bytes grow by ~45 bytes per tool and the initialize reply by
  the instruction text.
- `_meta` and `instructions` are additive. A host that ignores them behaves
  as before.
- Not verified: the effect inside a live Claude Code session; the
  per-server `alwaysLoad` key in `~/.claude.json` (the docs show only
  `.mcp.json`), so `dfmt setup` does not write it.
