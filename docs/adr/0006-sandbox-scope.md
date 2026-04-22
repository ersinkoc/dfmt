# ADR-0006: Include Sandboxed Tool Execution in DFMT's Scope

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | ADR-0007 |

## Context

Two distinct failure modes consume the context window of an AI coding agent:

1. **Tool output bloat during a session.** Native agent tools (Bash, Read, WebFetch) return raw output directly into the context. One 56 KB page snapshot, one 45 KB log file, one 60 KB API response each consume meaningful budget to answer questions for which a two-sentence summary would have sufficed.
2. **State loss at session boundaries.** Compaction or a fresh conversation drops working state (active files, tasks, decisions, last user request), forcing the agent to re-ask or drift.

Earlier DFMT drafts treated only the second problem as in-scope. The first was explicitly excluded (NG4 in SPECIFICATION v0.3) on the argument that "tool output sandboxing is a related but separable concern."

The decision this ADR records is the reversal of that position: DFMT now handles both.

## Decision

**Sandboxed tool execution is a first-class capability of DFMT.**

Concretely, DFMT now ships:

- An execution subsystem (§7.5) that spawns subprocesses with security policy, resource limits, and credential passthrough.
- Seven new MCP tools: `dfmt.exec`, `dfmt.read`, `dfmt.fetch`, `dfmt.batch_exec`, `dfmt.search_content`, `dfmt.get_chunk`, `dfmt.list_chunks`.
- A content store (§6.7) holding chunked output from sandboxed operations, separate from the durable event journal.
- Intent-driven filtering: when the caller provides an `intent` string, large outputs are indexed and the response contains only matching excerpts plus a vocabulary.
- A security policy (§7.5.4) with deny/allow rules, command splitting, shell-escape scanning for non-shell runtimes, and a conservative default.

This makes DFMT a full-spectrum context-saving daemon: it keeps tool output from flooding the context during a session, *and* it preserves working state across session boundaries.

## Alternatives Considered

### A. Keep the scopes separate (the v0.3 position)

DFMT handles session memory only. A separate sibling tool (DFMO — "Don't Fuck My Output") handles the sandbox. They share storage and transport infrastructure but ship as two binaries.

Rejected because:
- The two problems share almost all infrastructure: the daemon process, the indexing machinery, the security policy, the MCP transport, the CLI, the auto-setup flow. Splitting means duplicating non-trivial code or inventing a shared library layer that complicates distribution.
- A developer who wants token savings wants both solved. The product pitch "use DFMT to save tokens" becomes "use DFMT and DFMO together," which is cognitively worse and dilutes each tool's identity.
- The install story doubles: two binaries, two daemons per project, two sets of agent configurations. `dfmt setup` loses its "one command" claim.
- The value of DFMT alone is only half the value of the problem the user sees. Honest positioning would require saying "DFMT doesn't save tokens during your session, only across session boundaries," which undermines adoption.

### B. Ship DFMT with hooks that rewrite agent tool calls (MITM approach)

Instead of providing new MCP tools, intercept the agent's native Bash/Read/WebFetch calls at the hook layer, rewrite them to use DFMT's sandbox internally, and return the compressed result in the native tool's response shape.

Rejected because:
- Requires hook APIs that not all agents provide. Codex CLI would still be zero-coverage.
- The response shapes of native tools are not documented contracts; they change. Rewriting them risks breaking agent-side parsing.
- Some model behaviors depend on seeing raw output (e.g., the agent chooses to grep in the output as a next step). Silently substituting a summary changes agent behavior in ways that are hard to predict.
- Agents that do not support deep hook rewriting get nothing from this approach, while DFMT's own MCP tools work on every MCP-capable client.

### C. Focus on just session memory, delegate sandbox to each agent's native tool + post-hoc summarization

Let native tools flood the context. At the end of each agent turn, a hook summarizes the last tool output and removes the raw version. This is lazy, reactive, and easier to integrate.

Rejected because:
- Many agents don't expose a "replace last message content" capability. At best you can inject an additional summary, not remove the raw.
- Even when removal works, the tokens were already paid for by the model's processing of that turn. You save future tokens but not the ones that triggered the compaction you were trying to avoid.
- A model that has just consumed 56 KB of Playwright snapshot has also just reasoned over it. You cannot unroll that reasoning to make the conversation shorter.

### D. Build the sandbox, but skip content indexing (return raw-truncated)

Sandbox tools exist, but rather than indexing large outputs for on-demand retrieval, they simply truncate at a threshold.

Rejected because:
- Truncation is semantically lossy in ways the caller cannot predict. A 56 KB log with an important line at byte 45,000 becomes useless if truncated at 8 KB.
- The value of DFMT's sandbox vs. native tool + `head` is specifically that intent-driven search can find the relevant 200 bytes in a 56 KB output. Without indexing, the sandbox is just a truncator with extra steps.

## Consequences

### Positive

- **Real token savings.** The value proposition is honest: DFMT saves tokens during sessions (sandbox) and preserves state across them (memory). Both failure modes addressed.
- **One install, one daemon, one pitch.** A developer installs DFMT, runs `dfmt setup`, and gets the full experience on every AI agent on their machine.
- **Shared infrastructure is leveraged, not duplicated.** BM25 + tokenizer + Porter + trigram index all serve both event search and content search. Adding the sandbox reuses ~80% of existing machinery.
- **Larger moat.** A memory-only tool is easily replaceable. A memory + sandbox + setup + security tool is a more meaningful engineering bundle, harder to casually replicate.
- **Cleaner MCP story.** DFMT becomes the one MCP server an agent needs for context-aware tooling, rather than one of several specialized ones.

### Negative

- **Scope increase.** Estimated +1500–2500 lines of Go: executor, content store, runtimes detection, security policy engine, 7 new MCP tools, intent-matching pipeline. Adds roughly 3–4 days to the v1 timeline.
- **Security surface.** A tool that runs arbitrary code is a tool that must be careful. The security policy, sandbox escape detection, and credential-passthrough logic all need thorough testing. A bug here is not "returns wrong answer," it's "runs wrong command."
- **Runtime dependencies (external).** `dfmt.exec` with `lang: python` requires Python on the user's PATH. DFMT itself has no runtime dependencies; its value depends on the user's environment having the runtimes they want to use. `dfmt doctor` surfaces what's available.
- **Conceptual complexity.** DFMT is no longer "a memory tool." It's two things that share a daemon. Documentation, onboarding, and the landing page must explain both without confusing users who only want one.
- **Testing surface.** Exec with resource limits across Linux/macOS/Windows, with timeouts, signal handling, stdin/stdout/stderr separation, and credential environment inheritance — each platform has subtle differences. More platform-specific test matrix.

## Implementation Notes

- Runtimes are probed at daemon startup and cached. A language requested but missing returns MCP error code 424 ("Failed Dependency") with a message pointing to the runtime's install guide. Does not block the daemon; other runtimes continue to work.
- The inline / medium / large thresholds are the primary knobs for balancing latency against context savings. Defaults (4 KB / 64 KB) are conservative; high-volume users can tune down, agents that produce very small outputs can disable medium-tier indexing entirely.
- The content store uses the same `Index` type as the event journal but is a separate instance with its own posting lists, its own LRU eviction, and its own persistence path. See ADR-0007.
- Credential-passthrough rules are opt-in per CLI. A user who has never run `gh` on this machine gets no `GH_TOKEN` passed through, regardless of config.
- The `dfmt setup` command handles multi-agent configuration; operational details (exact paths, config formats) are maintained in the setup package rather than architecture docs, because they change more often than architectural decisions.

## Revisit

Revisit if:
- Adoption data shows users overwhelmingly want only the memory half, making the sandbox half a maintenance burden without proportional benefit. Unlikely but measurable post-launch.
- A security incident in the sandbox necessitates rearchitecting the isolation model. Mitigation: optional container-based sandbox as a future extension, with the current subprocess sandbox becoming the "trusted" tier.
- A parallel tool with deeper sandbox capability (real namespaces, seccomp) emerges and covers the compression half well enough that DFMT's value shifts fully to session memory. Mitigation: revert scope; ship memory-only DFMT alongside the other tool.
