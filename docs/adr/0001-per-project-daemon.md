# ADR-0001: Per-Project Daemon Model

| Field | Value |
| --- | --- |
| Status | Superseded by [ADR-0019](0019-global-daemon.md) on 2026-05-06 |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | [ADR-0019](0019-global-daemon.md) |

> **Superseded.** Phase 2 (v0.4.0) replaced the per-project daemon with one host-wide global daemon at `~/.dfmt/{port,daemon.sock,daemon.pid,lock}`. See [ADR-0019](0019-global-daemon.md) for context, decision, and migration. Legacy per-project mode remains available through v0.4.x for back-compat; v0.5.0 will remove it.

## Context

DFMT is a long-running local service. It holds an in-memory index, watches the filesystem, and serves requests over MCP stdio, Unix socket, and optional HTTP. A developer typically has several projects checked out (client work, side projects, open source) and may jump between them within the same day. The question is: does one daemon process serve all projects, or does each project run its own daemon?

This decision touches nearly every other design choice — storage layout, socket paths, memory footprint, crash semantics, transport port allocation, client connection logic. Getting it wrong late is expensive.

## Decision

**One daemon per project.** The daemon is bound to a single project root at startup and never serves another. Its Unix socket (`.dfmt/daemon.sock`), PID file (`.dfmt/daemon.pid`), and log (`.dfmt/daemon.log`) live inside the project's own `.dfmt/` directory. A global registry at `$XDG_DATA_HOME/dfmt/projects.jsonl` tracks known projects for enumeration (`dfmt list`) but holds no operational state.

The daemon is **auto-started** by any client call: `dfmt <cmd>` discovers the project root, tries to connect to `.dfmt/daemon.sock` with a 50 ms timeout, and double-forks a daemon if the socket is absent or refuses. Startup takes ~100 ms on a cold cache, absorbed transparently by the client.

The daemon **idle-exits** after 30 minutes of no requests **and** no FS watcher activity. A user actively editing code keeps the daemon up through FS events; a user who has closed the editor and walked away gets their memory back.

## Alternatives Considered

### A. Global singleton daemon

One long-running process serving all projects, indexed by project ID. Simpler operationally (one `systemctl status`, one upgrade target), potential for cross-project features.

Rejected because:
- Every request carries routing overhead ("which project?"), adding code complexity to every handler.
- A crash in one project's capture logic kills capture for all projects.
- Memory scales with the set of projects ever touched, not the set being worked on. Eviction logic is needed, which brings cache-invalidation semantics.
- Socket location (`$XDG_RUNTIME_DIR/dfmt.sock`) collides if the user runs multiple DFMT versions side-by-side during upgrade.
- Permission scoping is awkward: the daemon sees across project boundaries by construction, which conflicts with the privacy-by-default goal.

### B. No daemon; stateless per-call execution

Each `dfmt <cmd>` spawns, reads the journal, answers, exits. Maximally simple. Zero memory when idle.

Rejected because:
- The FS watcher requires a persistent process — there is no workable way to catch `inotify` events without one.
- Cold-start cost per call (~200 ms to rebuild the index for 10k events) compounds badly when a git hook fires 4 times during a rebase.
- The progressive-throttle feature depends on remembering recent-call state across calls.

### C. Per-project daemon under a supervisor tree

A single parent "supervisor" spawns one child per project, manages their lifecycles, forwards requests. This is the Erlang/BEAM model.

Rejected because:
- Requires a new top-level process to exist always. The whole point of per-project isolation is that no process exists for idle projects.
- Supervisor becomes a single point of failure reintroducing the problems of option A.
- No meaningful gain over direct per-project daemons.

## Consequences

### Positive

- **Crash isolation.** A bug triggered by pattern matching in project A cannot affect capture in project B. Each daemon is a separate process with its own memory.
- **Simple routing.** Every handler knows its project at startup. Zero routing logic, zero mental overhead.
- **Lazy memory.** A user with 50 projects in history but 2 active has 2 daemons running. The other 48 consume zero RAM.
- **Natural lifecycle.** Open editor → daemon up (via auto-start on first edit). Close editor → daemon exits 30 minutes later. No explicit lifecycle management by the user.
- **Clean permission model.** Each daemon's socket sits in the project directory with `0700` permissions. No cross-project data flow is structurally possible.
- **Clear upgrade path.** Upgrading DFMT means each daemon restarts independently the next time its project is touched. No coordinated migration dance.

### Negative

- **N daemons for N active projects.** Memory cost is additive. Target ≤50 MB per daemon at 10k events; a developer with 5 active projects uses ~250 MB. Acceptable on a modern dev machine; may need tuning on Raspberry Pi-class hardware.
- **Cross-project features are structurally blocked.** A "search across all my projects" feature is not possible in v1. Deferred to v2 if ever.
- **HTTP port allocation is more complex.** Fixed port would collide across projects. Resolved by making HTTP opt-in per project and picking a free port at bind time (SPEC §7.4.2).
- **Startup cost visible to first call.** A cold daemon takes ~100 ms to spawn before the first request is answered. Masked by the client's auto-start logic, but measurable on micro-benchmarks.
- **`dfmt list` requires cross-process checking.** The enumeration command must check PIDs and sockets across the registry. Non-trivial but bounded.

## Implementation Notes

- The client library (`internal/client`) owns the auto-start sequence. All CLI commands, capture scripts, and the MCP shim use it.
- Advisory `flock` on `.dfmt/lock` prevents two daemons for the same project. Startup race lock at `.dfmt/startup.lock` prevents two concurrent clients from both spawning a daemon.
- Idle-exit's "FS activity counts as not-idle" rule is critical. Without it, a developer coding in their editor without touching DFMT commands would have the daemon die mid-session.
- `dfmt daemon --foreground` skips the double-fork and disables idle-exit, for running under `systemd` or `launchd` when users want that.

## Revisit

Revisit if any of these conditions become true:
- Users report memory pressure from 10+ concurrent projects.
- A clear cross-project use case emerges (e.g., "what did I decide across all my Go projects?").
- The client-side auto-start proves unreliable on Windows (named pipe semantics differ from Unix sockets).
