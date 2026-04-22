# ADR-0005: Multi-Source Capture Layer

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | — |

## Context

Session-memory tools need to capture events: "a file was edited," "a task was completed," "the user decided to use X instead of Y," "a commit was made," "an error occurred." The question is where these events come from.

The natural first answer is: agent hooks. When the AI agent calls a tool or receives a user prompt, a hook fires; the hook extracts the event and records it. This is clean when the agent exposes the right hook points — typically pre-tool-use, post-tool-use, user-prompt-submit, pre-compaction, session-start. When those hooks exist and are reliable, hook-driven capture is high-fidelity.

But agent extension models vary widely. Some agents expose all five hook points; others expose two; some expose none. Different agents expose different hook point *semantics* even when the names match. And even in an agent with rich hooks, some important information — a file edited in the editor while the agent is idle, a commit made from the terminal — is outside the hook system entirely.

The decision is whether DFMT's capture layer should depend exclusively on agent hooks, or whether capture should draw from multiple independent sources.

## Decision

**Five independent capture sources feed the same journal:**

1. **MCP tool.** Agent explicitly calls `dfmt.remember(type, body)` for semantic events it wants to record. Supported by any MCP-capable agent.
2. **Filesystem watcher.** Native syscall-based watcher (`inotify`, `kqueue`, `ReadDirectoryChangesW`). Emits `file.edit`, `file.create`, `file.delete` events. Agent-independent; runs in the daemon.
3. **Git hooks.** Small shell scripts installed into `.git/hooks/` that call `dfmt capture git <op>` over the Unix socket. Emits `git.commit`, `git.checkout`, `git.push`, etc.
4. **Shell integration.** Optional `precmd`/`chpwd` hooks for zsh/bash/fish that emit `env.cwd`, `error` (on non-zero exit), and `env.install` (on package manager invocations).
5. **CLI direct.** `dfmt note`, `dfmt task`, `dfmt remember`. User-initiated capture.

Each source is an independent `Capturer` implementation. Disabling any one in config does not affect the others. The daemon serializes writes from all sources to the single journal.

## Alternatives Considered

### A. Hook-only capture

Every event comes from the agent's hook system. If the agent has hooks with the right granularity, it works; if not, it doesn't.

Rejected because:
- Every agent has a different hook API, a different set of hook points, and different extraction semantics. Each one is a separate implementation target. The coverage matrix is a permanent maintenance cost.
- Some agents — particularly terminal-native assistants — expose no hook API at all. Users of those agents get zero capture.
- Even well-hooked agents miss hook events for activity outside the agent's awareness: files edited in the editor during a thinking pause, commits made from the terminal, tasks noted on a whiteboard and never typed into the chat.
- A user who wants to do some work themselves — edit files, commit, run tests — without involving an AI agent at all should still benefit from DFMT's memory. Hook-only capture gives them nothing.

### B. MCP-only

DFMT is an MCP server, full stop. Agents call its tools to record events. Nothing else.

Rejected because:
- Non-MCP tools (Codex CLI) are excluded entirely.
- Coverage of events the agent never sees (a developer editing files in VS Code without an AI active) is zero.
- Reduces DFMT to a memory library, losing the filesystem awareness that makes FS-watch-based resumption uniquely powerful.

### C. Kernel-level capture via eBPF / dtrace

System-level tracing to capture all file writes and process events regardless of what tool triggered them.

Rejected because:
- Requires elevated privileges. A dev tool that asks for root fails the trust test.
- Wildly cross-platform-inconsistent (eBPF on Linux only, dtrace mostly on macOS, nothing comparable on Windows).
- Captures too much (every `node_modules/` write during `npm install`), requires aggressive filtering.
- Debug complexity is enormous.

### D. LSP-based capture

Hook into the user's Language Server Protocol traffic to see edits, diagnostics, and navigation events.

Rejected because:
- Every editor's LSP client integration is different. VS Code, Neovim, Zed, JetBrains — five separate capture stories.
- LSP doesn't see all file modifications (only those made through the editor).
- Adds a complex protocol to implement.

## Consequences

### Positive

- **Agent-agnostic baseline.** Even with zero agent integration — user editing files, making commits, running tests from the terminal — DFMT captures a useful session record. A user who later says "Claude, look at what I did this afternoon and help me document it" gets a meaningful snapshot.
- **Graceful degradation across agents.** The more hook-capable the agent, the richer the capture. Agents without MCP or similar tool integration lose semantic capture (decisions, prompts) but keep mechanical capture (files, git, errors). No agent is "not supported" — coverage simply varies along a smooth continuum determined by which sources are active.
- **No hook politics.** DFMT doesn't argue with each agent's authors about what extension API shape to expose. We capture what we can capture from universal sources; agent-initiated capture via MCP is additive, not required.
- **Privacy by composition.** Users can turn off any source. Someone who wants only explicit captures sets `capture.fs.enabled: false` and `capture.git.enabled: false`, uses only `dfmt remember` from the CLI. Same codebase, different privacy profile.
- **Debug locality.** When a specific event is missing, the user can check exactly one source (the one that should have produced it). No cross-source contamination.

### Negative

- **Multiple implementations.** Five capture sources is four more codepaths than "hook-only." Each has its own OS quirks.
- **FS watcher is not trivial.** Cross-platform filesystem notification has well-known gotchas: atomic-rename-on-save patterns (vim, some editors), debouncing noise during `git checkout`, handling moves vs. delete+create, watching directories that get deleted mid-watch.
- **Shell integration requires user action.** `dfmt shell-init` must be copied into `.zshrc`/`.bashrc`. Users who skip this step miss `env.cwd` and error events. Mitigated by making shell integration optional and the default path working fine without it.
- **Git hooks can conflict with existing hooks.** Users with pre-existing git hooks need a merge story. Mitigated by `dfmt install-hooks` detecting foreign hooks and refusing to overwrite without `--force`.
- **Potential event duplication.** A git checkout triggers thousands of FS events that would duplicate the single `git.checkout` event if both capturers fire. Mitigated by a debouncer that coalesces FS bursts when correlated with a git event within a 2-second window.

## Implementation Notes

- Capture sources are Go interfaces (`Capturer`) in `internal/capture/`. Each has `Start(ctx, sink)` and `Stop(ctx)` methods.
- The MCP capture runs inside the daemon as part of the MCP transport. The FS watcher also runs inside the daemon as a goroutine. Git hooks and shell integration run as external processes that submit events over the Unix socket.
- The CLI dispatcher in `dfmt capture <args>` is the write path for out-of-process capturers. It is intentionally minimal: parse args, connect to socket, send JSON, exit.
- A capture source's failure never kills the daemon. On Start error, the source logs and disables itself; other sources continue.
- The event submission API at the core is idempotent on `(type, timestamp, body)` tuples within 100ms windows, providing natural deduplication for sources that might race.
- The FS watcher respects `.gitignore` via a manual parser. Implementing this ourselves (rather than pulling a `gitignore` library) fits the zero-dep policy. Parser is ~150 lines.

## Revisit

Revisit if:
- A sixth meaningful capture source emerges that we hadn't considered (e.g., browser-extension capture for web-based agents).
- Coverage analytics show FS watcher producing too much noise vs. signal, requiring a fundamentally different strategy.
- Cross-source deduplication becomes a frequent bug source, requiring a more formal event-reconciliation layer.
