---
name: dfmt-workflow
description: Use DFMT MCP tools for all file reads, command execution, and URL fetching to save tokens and maintain session memory.
allowed-tools: mcp__dfmt__dfmt.read mcp__dfmt__dfmt.exec mcp__dfmt__dfmt.fetch mcp__dfmt__dfmt.remember mcp__dfmt__dfmt.search mcp__dfmt__dfmt.recall mcp__dfmt__dfmt.stats
---

# DFMT Workflow

When this project has DFMT initialized (`.dfmt/` directory exists), you MUST use DFMT tools instead of native Claude Code tools for the following operations:

## Mandatory Replacements

| Native Tool | DFMT Replacement | Why |
|-------------|------------------|-----|
| `Read` | `dfmt.read` | Returns intent-matched excerpts only, saving tokens |
| `Bash` | `dfmt.exec` | Sandboxed execution with intent-filtered output |
| `WebFetch` | `dfmt.fetch` | Fetches URLs with intent-based content filtering |

## Usage Rules

1. **Always provide `intent`** when using `dfmt.read`, `dfmt.exec`, or `dfmt.fetch`. The intent tells DFMT what you need, so it returns only relevant excerpts instead of the full output.
2. **Use `dfmt.remember`** after significant LLM interactions to record token usage and decisions in the session journal.
3. **Use `dfmt.search`** to find past events in the session memory.
4. **Use `dfmt.recall`** to build a context snapshot when the conversation window is tight.
5. **Use `dfmt.stats`** to check token savings.

## Examples

- Reading a file: `dfmt.read` with `path="README.md"` and `intent="installation instructions"`
- Running tests: `dfmt.exec` with `code="go test ./..."` and `intent="failing test names"`
- Fetching docs: `dfmt.fetch` with `url="https://api.example.com/docs"` and `intent="authentication endpoints"`

## Important

- Do NOT use native `Read`, `Bash`, or `WebFetch` when DFMT is available.
- If a command fails in `dfmt.exec`, check the exit code and stderr in the response.
- DFMT's sandbox has a security policy; if a command is denied, the error message will explain why.
