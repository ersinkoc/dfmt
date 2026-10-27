# ADR-0029: Per-Call Project Routing for Unbound MCP Sessions

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-27 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

The global daemon (ADR-0019) already serves any project a call names:
every `*Params` type carries `project_id`, and `Daemon.Resources` lazily
loads that project's journal, index and sandbox. A `dfmt mcp` proxy pins
one project at startup (cwd walk-up, `--project`, `DFMT_PROJECT`) and
stamps it on every call.

Some MCP hosts have one global server config and no per-workspace
variables (Windsurf/Devin Desktop; GitHub issue #2). The proxy they spawn
starts outside any project, so it found none, left its backend nil, and
answered every call with `daemon not connected`. The tool schemas never
listed `project_id`, so a host building calls from `tools/list` could not
send one even though the daemon would have honored it.

Advertising the field as-is would not have been safe. `loadProjectResources`
creates `.dfmt/` in whatever directory it is given and makes that directory
the sandbox root, with no check, and resolves a relative path against the
daemon's own cwd. A model-supplied `project_id: "/"` or `"$HOME"` — for
example from a prompt-injected page the agent fetched — would re-root
exec, read and write outside any project.

## Decision

- **Unbound sessions.** When `dfmt mcp` finds no project it still connects
  to the global daemon, through `client.NewGlobalClient`, which never runs
  `EnsureProjectInitialized` (that would write `.dfmt/` and instruction
  files into the host's arbitrary cwd). `MCPProtocol.SetUnbound` makes
  `tools/list` add a **required** `project_id` to every tool, and a call
  without one is an `isError` result naming the fix.
- **Bound sessions are unchanged.** `project_id` is not advertised there:
  it would add ~1 KB to every session's `tools/list` for no use, and an
  agent bound to a project has no reason to name another one.
- **Every explicit `project_id` is validated in the MCP layer**, bound or
  not, before it reaches the daemon: an absolute path to an existing
  directory holding `.dfmt/` or `.git/` (the markers project discovery
  uses), after symlink resolution not the filesystem root and not the
  user's home directory. Failures are `isError` results.

The check sits at the MCP layer because that is where the value becomes
model-controlled. The daemon's own RPC is reachable only by token-holding
local clients (ADR-0023), which derive project ids from the filesystem.

## Consequences

- Windsurf-style hosts work with a single global config: the model passes
  the workspace root on each call.
- A directory with neither `.dfmt/` nor `.git/` cannot be targeted until
  `dfmt init` runs there. That is deliberate: it is the line between "a
  project" and "a directory".
- The validation is lexical plus `stat` and has a TOCTOU window. It closes
  the injected-path case, not an attacker who can already rewrite the
  filesystem between the check and the load.
