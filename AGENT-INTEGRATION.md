# DFMT — Agent Integration Reference

| Field | Value |
| --- | --- |
| Document | `AGENT-INTEGRATION.md` |
| Status | Draft — v0.1 |
| Target Spec | `SPECIFICATION.md` v0.4 §18 |
| Date | 2026-04-20 |

This document is the operational reference for integrating DFMT with every supported AI coding agent. It defines, for each agent: where its configuration lives, what format it expects, what `dfmt setup` writes, what hooks or plugins are available, what instruction file is produced, and what restart is required.

Per-agent sections are intended to be read independently: the Claude Code section covers everything needed to integrate Claude Code, the Codex CLI section is self-contained for Codex users, and so on.

---

## 1. Quickstart

The one-command integration story:

```bash
# 1. Install DFMT
curl -fsSL https://dfmt.dev/install.sh | sh

# 2. Initialize the current project
cd my-project
dfmt init

# 3. Detect and configure every AI agent on this machine
dfmt setup
```

`dfmt setup` output:

```
Scanning for installed AI coding agents...

✓ Claude Code          found at ~/.claude/
✓ Gemini CLI           found at ~/.gemini/
✓ VS Code Copilot      found at ~/.vscode-server/ + project .vscode/
✓ Codex CLI            found at ~/.codex/
✓ Cursor               found at ~/.cursor/
○ OpenCode             not detected
○ Zed                  not detected
○ Continue.dev         not detected
○ Windsurf             not detected

For each detected agent, the following will be written:

  Claude Code
    - MCP registration:    ~/.claude/mcp.json              (new entry "dfmt")
    - Hooks:               ~/.claude/settings.json         (PreToolUse, PostToolUse, PreCompact, SessionStart, UserPromptSubmit)
    - Instruction file:    ./CLAUDE.md                     (append DFMT section)

  Gemini CLI
    - MCP + hooks:         ~/.gemini/settings.json         (mcpServers.dfmt + hooks)
    - Instruction file:    ./GEMINI.md                     (append DFMT section)

  VS Code Copilot
    - MCP:                 .vscode/mcp.json
    - Hooks:               .github/hooks/dfmt.json
    - Instruction file:    .github/copilot-instructions.md

  Codex CLI
    - MCP registration:    ~/.codex/config.toml            (add mcp_servers.dfmt)
    - Instruction file:    ./AGENTS.md                     (append DFMT section)
    (Codex does not support hooks; instruction file is the only enforcement)

  Cursor
    - MCP registration:    ~/.cursor/mcp.json
    - Instruction file:    ./.cursorrules

Proceed? [Y/n]
```

After the user confirms, each agent is configured. A manifest of changes is written to `$XDG_DATA_HOME/dfmt/setup-manifest.json` for later `--uninstall`.

The user then applies the configuration in each affected agent. Per-agent reload behavior (printed by `dfmt setup` after configuration):

| Agent | MCP reload | Hook reload | Instruction-file reload |
| --- | --- | --- | --- |
| Claude Code | `/reload-mcp-servers` | full restart | hot (re-read each message) |
| Cursor | full restart | — | hot |
| Codex CLI | full restart | — | hot (re-read each message) |
| Gemini CLI | full restart | full restart | hot |
| VS Code Copilot | reload window | reload window | hot |
| OpenCode | full restart | full restart (plugin reload) | hot |
| Zed | reload workspace | — | hot |
| Continue.dev | reload window | — | hot |
| Windsurf | full restart | — | hot |

DFMT's tool availability begins at the next agent session after reload. Work continues with DFMT's sandbox tools preferred over native tools from that point forward.

---

## 2. Setup Command Behavior

### 2.1 Agent Detection

For each supported agent, DFMT probes filesystem markers in order:

| Agent | Primary marker | Fallback marker |
| --- | --- | --- |
| Claude Code | `~/.claude/` directory | `which claude` |
| Gemini CLI | `~/.gemini/settings.json` | `which gemini` |
| VS Code Copilot | `.vscode/` in project | `~/.vscode-server/` or `~/.vscode/` |
| Codex CLI | `~/.codex/config.toml` | `which codex` |
| Cursor | `~/.cursor/` or `~/Library/Application Support/Cursor/` | — |
| OpenCode | `~/.config/opencode/` | `which opencode` |
| Zed | `~/.config/zed/settings.json` | `which zed` |
| Continue.dev | `~/.continue/config.yaml` | — |
| Windsurf | `~/.codeium/windsurf/` | `which windsurf` |

If both marker and fallback miss, the agent is reported as "not detected" and skipped. Users can force an agent with `dfmt setup --agent <name>` even if not detected (DFMT creates the config directory with appropriate permissions).

**Non-standard install paths.** Users who install an agent to a non-default location (custom `XDG_CONFIG_HOME`, portable install, container-mounted config, team-shared config) can override detection with any of:

- `dfmt setup --agent claude --config-dir /opt/custom/claude-config` — one-off path override for this setup run.
- An environment variable `DFMT_AGENT_<NAME>_CONFIG_DIR` (e.g., `DFMT_AGENT_CLAUDE_CONFIG_DIR`, `DFMT_AGENT_CODEX_CONFIG_DIR`). If set, used as the agent's config directory unconditionally on every run.
- A global override file at `$XDG_DATA_HOME/dfmt/agent-paths.yaml`:

  ```yaml
  agents:
    claude:
      config_dir: /opt/custom/claude
    gemini:
      config_dir: /var/lib/team-gemini/settings
    codex:
      config_file: /etc/codex/config.toml
  ```

  Consulted on every `dfmt setup` run before default probes.

The override file is preferred for long-lived non-standard paths — it survives across setup runs without the user remembering flags. The environment variable suits CI and temporary overrides. The flag is for one-off cases.

`dfmt setup --verify` honors the same overrides, so verification reads back from the paths it wrote to.

### 2.2 Idempotency Rules

Running `dfmt setup` multiple times must not corrupt configuration. Rules:

1. **Merge, don't replace.** When editing an existing JSON, YAML, or TOML config file, DFMT parses it, merges its entries into the existing structure, and writes back. It never truncates.
2. **Own its keys only.** DFMT touches only the keys it owns. `mcpServers.dfmt`, `hooks.PreToolUse[name=dfmt]`, `mcp_servers.dfmt` (TOML), etc. Foreign entries are preserved verbatim, including their formatting and ordering.
3. **Version-marker comments.** Every DFMT-written block carries a `# dfmt:v<N>` comment marker (or equivalent per format). On subsequent runs, DFMT identifies its own sections by this marker and regenerates them. Blocks without the marker are treated as foreign and left alone.
4. **Backups.** Before any edit, the original file is copied to `.dfmt.backup-<ISO8601>` in the same directory. Backups are pruned by any of these triggers (whichever fires first):
   - **Next `dfmt setup` run:** after successful completion, backups older than 7 days in any directory DFMT touched are removed.
   - **Daemon maintenance tick:** once per hour while the daemon is running, a background goroutine walks the agent-config directories listed in the setup manifest and prunes `.dfmt.backup-*` files older than 7 days.
   - **Explicit command:** `dfmt setup --clean-backups` removes all DFMT-created backups regardless of age, useful before archiving or sharing a project.
   - **Age ceiling:** no backup file is ever older than 30 days, regardless of pruning cadence. If a user has not run `dfmt setup` and has no daemon running, the next `dfmt` command of any kind checks the manifest's known backup locations and prunes anything past the ceiling as part of startup.

   Backups that were created by a since-uninstalled agent integration are preserved until their 30-day ceiling, then pruned. This protects the user from losing history if they briefly uninstall an agent.
5. **Merge conflict detection.** When DFMT detects that a previously-written block has been manually modified by the user (version marker present but content differs from what the current DFMT version would generate), it does **not** silently overwrite. It reports the conflict and offers three options: (a) `--force` accept DFMT's version, (b) leave it alone with a warning, (c) interactive merge. Default in non-interactive mode is (b) with warning to stderr and exit code 0 (success, but incomplete).
6. **File format preservation.** JSON: key order of untouched top-level keys preserved; DFMT keys emitted in canonical order at end of object. YAML: comments, blank lines, and anchor structure preserved via yaml.v3 `Node` round-trip. TOML: section order preserved; DFMT sections added at end if absent, regenerated in place if present. Formatting idiosyncrasies (indentation, quoting style) matched to the user's existing style where feasible.
7. **Atomicity.** Each file edit is atomic: DFMT writes to `<file>.dfmt-new`, verifies it parses cleanly, then renames over the original. A crash mid-edit leaves the original intact.

### 2.3 Instruction Files

Each agent reads a different instruction file name in a different format. DFMT generates the file content from a single internal template with per-agent adaptations (tone, voice, exact tool names the agent uses for native operations). Templates are embedded in the binary via `go:embed`.

When `dfmt setup` runs in a project root, the instruction file is written at the project level. When run outside a project, the user is asked whether to write to the agent's global instruction location (if supported) or skip instruction-file writing.

### 2.4 `--dry-run`

Prints the intended changes without writing anything. Users preview before committing. Output format matches the confirmation prompt shown in §1.

### 2.5 `--uninstall`

Reads the setup manifest, removes every DFMT-written block (identified by version markers), removes DFMT MCP entries, restores backup files where the DFMT edit was the only change. The binary itself remains installed.

### 2.6 `--force`

Overrides the foreign-config-preservation rule. Use with care; reserved for rare cases where a user has manually written a DFMT-like block and wants DFMT to take over.

---

## 3. Per-Agent Integration

### 3.1 Claude Code

**Config directory:** `~/.claude/`
**Project directives:** `CLAUDE.md` in project root
**Hook support:** Full (`PreToolUse`, `PostToolUse`, `PreCompact`, `SessionStart`, `UserPromptSubmit`)
**Expected routing compliance:** ~95%

#### MCP registration

Written to `~/.claude/mcp.json` (or merged into existing):

```json
{
  "mcpServers": {
    "dfmt": {
      "command": "dfmt",
      "args": ["mcp"]
    }
  }
}
```

Alternatively, if the user prefers project-level registration, `.mcp.json` in the project root.

#### Hook configuration

Written to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Read|WebFetch|Grep|Task",
        "hooks": [
          { "type": "command", "command": "dfmt hook claude-code pretooluse" }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "dfmt hook claude-code posttooluse" }
        ]
      }
    ],
    "PreCompact": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "dfmt hook claude-code precompact" }
        ]
      }
    ],
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "dfmt hook claude-code sessionstart" }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "dfmt hook claude-code userpromptsubmit" }
        ]
      }
    ]
  }
}
```

The `dfmt hook claude-code <event>` command is implemented in DFMT itself; it reads the hook JSON from stdin, processes it according to event type, and writes the expected response to stdout.

#### `CLAUDE.md` additions

DFMT appends (or creates) a section with the version marker:

```markdown
<!-- dfmt:v1 begin -->
## Context Discipline

This project uses DFMT to keep tool output from flooding the context window and to preserve session state across compactions. When working in this project, follow these rules.

### Tool Preferences

Prefer DFMT's sandboxed tools over native ones:

| Instead of | Use |
| --- | --- |
| Bash(cmd) | dfmt.exec with code and intent |
| Read(path) | dfmt.read with path and intent |
| WebFetch(url) | dfmt.fetch with url and intent |
| Grep(pattern, paths) | dfmt.exec with grep and intent |

The sandboxed tools execute the same operations, but the raw output stays in DFMT's content store. You get a summary and a chunk-set ID; if you need more, use dfmt.search_content to query the stored content by intent.

Always include an `intent` string that describes what you are looking for. Example:

  dfmt.exec(code="cat /var/log/nginx/access.log", intent="count 5xx errors in the last hour")

The `intent` lets DFMT return the relevant lines rather than the full file.

### Session Memory

DFMT tracks files you edit, tasks, decisions, errors, and git operations automatically. When context compacts or a fresh session starts, call dfmt.recall at the beginning to retrieve a summary of session state. Use dfmt.remember to record explicit decisions from the user ("use slog, not zap") so they survive compaction.

### When Native Tools Are Acceptable

Native Bash and Read are acceptable for outputs you know are small (< 2 KB) and will not be referenced again. For anything else, sandboxed tools are preferred.
<!-- dfmt:v1 end -->
```

#### Restart requirement

Claude Code supports hot reload via `/reload-plugins` or `/reload-mcp-servers` slash commands. After `dfmt setup` writes the configuration, the user can run `/reload-mcp-servers` inside Claude Code to pick up the new DFMT MCP server without restarting. For hook changes, a full restart is still required (Claude Code reads the hook file at startup). `dfmt setup` output distinguishes the two: "Hot reload available: run `/reload-mcp-servers`" vs "Full restart required for hook changes."

---

### 3.2 Gemini CLI

**Config directory:** `~/.gemini/`
**Project directives:** `GEMINI.md` in project root
**Hook support:** High (`BeforeTool`, `AfterTool`, `PreCompress`, `SessionStart`; no `UserPromptSubmit` equivalent)
**Expected routing compliance:** ~95%

#### MCP + hooks (single file)

Merged into `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "dfmt": {
      "command": "dfmt",
      "args": ["mcp"]
    }
  },
  "hooks": {
    "BeforeTool": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "dfmt hook gemini-cli beforetool" }]
      }
    ],
    "AfterTool": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "dfmt hook gemini-cli aftertool" }]
      }
    ],
    "PreCompress": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "dfmt hook gemini-cli precompress" }]
      }
    ],
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "dfmt hook gemini-cli sessionstart" }]
      }
    ]
  }
}
```

#### `GEMINI.md` additions

Same structure as `CLAUDE.md`, with the tool-mapping table adjusted for Gemini CLI's native tool names.

#### Restart

Gemini CLI must be restarted.

---

### 3.3 VS Code Copilot

**Config directory:** `~/.vscode-server/` (remote) or `~/.vscode/` (local) + project `.vscode/`
**Project directives:** `.github/copilot-instructions.md`
**Hook support:** Full (`PreToolUse`, `PostToolUse`, `SessionStart`, `PreCompact`)
**Expected routing compliance:** ~95%

#### MCP registration

Written to project-level `.vscode/mcp.json`:

```json
{
  "servers": {
    "dfmt": {
      "command": "dfmt",
      "args": ["mcp"]
    }
  }
}
```

#### Hook configuration

Written to `.github/hooks/dfmt.json`:

```json
{
  "hooks": {
    "PreToolUse":   [ { "type": "command", "command": "dfmt hook vscode-copilot pretooluse" } ],
    "PostToolUse":  [ { "type": "command", "command": "dfmt hook vscode-copilot posttooluse" } ],
    "PreCompact":   [ { "type": "command", "command": "dfmt hook vscode-copilot precompact" } ],
    "SessionStart": [ { "type": "command", "command": "dfmt hook vscode-copilot sessionstart" } ]
  }
}
```

#### `.github/copilot-instructions.md`

Same template as CLAUDE.md, tuned for Copilot's voice.

#### Restart

VS Code must be reloaded (Ctrl+Shift+P → "Developer: Reload Window").

---

### 3.4 Codex CLI

**Config directory:** `~/.codex/`
**Project directives:** `AGENTS.md` in project root (and optionally `~/.codex/AGENTS.md` for global)
**Hook support:** **None.** Codex CLI does not support hooks; PRs proposing hook support were closed without merge.
**Expected routing compliance:** ~65% (instruction file alone)

#### MCP registration

Added to `~/.codex/config.toml`:

```toml
[mcp_servers.dfmt]
command = "dfmt"
args    = ["mcp"]
```

#### `AGENTS.md`

Because Codex has no hooks, the instruction file is the only enforcement layer. The template is more emphatic:

```markdown
<!-- dfmt:v1 begin -->
# Context Discipline — REQUIRED

This project uses DFMT to keep large tool outputs from exhausting the context window. **Read this section at the start of every conversation in this project.**

## Rule 1 — Prefer DFMT tools over native tools

Always use DFMT's sandboxed tools when the output might exceed 2 KB:

  Native → DFMT replacement
  shell  → dfmt.exec
  read   → dfmt.read
  fetch  → dfmt.fetch

Include an `intent` argument on every call, describing what you are looking for. The `intent` enables DFMT to return the relevant portion of a large output without flooding the context.

## Rule 2 — Resume on session start

At the start of every session in this project, call dfmt.recall before taking any other action. Treat the returned snapshot as ground truth about the ongoing work.

## Rule 3 — Record user decisions

When the user states a preference or correction ("use X instead of Y", "do not modify Z"), call dfmt.remember with `type: decision` so the choice survives context compaction.

## Why these rules matter

Codex CLI does not provide hooks to enforce these rules automatically. **Compliance is your responsibility as the agent.** A single raw shell output above 8 KB can push earlier context out of the window, erasing the conversation's history. Following the rules above preserves it.
<!-- dfmt:v1 end -->
```

#### Restart

Codex CLI must be restarted.

---

### 3.5 Cursor

**Config directory:** `~/.cursor/` (or platform-specific equivalent)
**Project directives:** `.cursorrules` in project root
**Hook support:** Partial — Cursor exposes some MCP-adjacent extension points but not a reliable intercept API at the time of writing.
**Expected routing compliance:** ~70% (instruction-dominated)

#### MCP registration

Added to `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "dfmt": {
      "command": "dfmt",
      "args": ["mcp"]
    }
  }
}
```

#### `.cursorrules`

Cursor's rules file is plain text. DFMT appends its section with version markers in comment form:

```text
# dfmt:v1 begin
# Context Discipline
#
# This project uses DFMT. Prefer dfmt.exec, dfmt.read, dfmt.fetch over native
# Bash, Read, WebFetch. Include an intent string on every call.
# Call dfmt.recall at session start. Call dfmt.remember for user decisions.
# See full rules: AGENTS.md
# dfmt:v1 end
```

The fuller text lives in `AGENTS.md` for Cursor users who want a more detailed guide.

#### Restart

Cursor must be reloaded.

---

### 3.6 OpenCode

**Config directory:** `~/.config/opencode/` or project `opencode.json`
**Project directives:** `AGENTS.md`
**Hook support:** Via TypeScript plugin (`tool.execute.before`, `tool.execute.after`, `experimental.session.compacting`); no SessionStart yet.
**Expected routing compliance:** ~95%

#### MCP + plugin (single project file)

Added to project `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "dfmt": {
      "type": "local",
      "command": ["dfmt", "mcp"]
    }
  },
  "plugin": ["@dfmt/opencode-plugin"]
}
```

The plugin package `@dfmt/opencode-plugin` is a small TypeScript wrapper that delegates to DFMT's hook command. DFMT ships it as part of its release artifacts; `dfmt setup` drops it into a project-local `node_modules/@dfmt/opencode-plugin/` (OpenCode requires npm-style plugin resolution).

#### `AGENTS.md`

Same template as the Claude Code CLAUDE.md, adjusted for OpenCode tool names.

#### Restart

OpenCode must be restarted.

---

### 3.7 Zed

**Config directory:** `~/.config/zed/`
**Project directives:** Zed's AI rules feature (if enabled) or `AGENTS.md`
**Hook support:** None at the time of writing (MCP support is read-only from Zed's side).
**Expected routing compliance:** ~65%

#### MCP registration

Zed's extension system supports MCP servers via `settings.json`:

```json
{
  "assistant": {
    "mcp_servers": {
      "dfmt": {
        "command": "dfmt",
        "args": ["mcp"]
      }
    }
  }
}
```

#### Instruction file

Written to project `AGENTS.md` with the instruction-only (Codex-style) template.

#### Restart

Zed must be restarted.

---

### 3.8 Continue.dev

**Config directory:** `~/.continue/`
**Project directives:** Prompt embedded in `config.yaml`
**Hook support:** None; Continue.dev is primarily prompt-driven.
**Expected routing compliance:** ~65%

#### Config entry

Added to `~/.continue/config.yaml`:

```yaml
mcpServers:
  - name: dfmt
    command: dfmt
    args: ["mcp"]

prompts:
  - name: dfmt-discipline
    description: "DFMT context discipline for this project"
    prompt: |
      This project uses DFMT. Prefer dfmt.exec, dfmt.read, dfmt.fetch over native
      tools. Include intent strings on every call. Call dfmt.recall at session
      start. See AGENTS.md for the full rules.
```

The prompt is referenced automatically via Continue's rules-file feature when the user is in a DFMT-enabled project.

#### Restart

Continue.dev must be reloaded in the editor.

---

### 3.9 Windsurf

**Config directory:** `~/.codeium/windsurf/`
**Project directives:** `.windsurfrules`
**Hook support:** None at the time of writing.
**Expected routing compliance:** ~65%

#### MCP registration

Added to Windsurf's MCP config (location varies by version; DFMT probes and writes to the current location):

```json
{
  "mcpServers": {
    "dfmt": {
      "command": "dfmt",
      "args": ["mcp"]
    }
  }
}
```

#### `.windsurfrules`

Similar to `.cursorrules`, Codex-style emphatic template.

#### Restart

Windsurf must be restarted.

---

### 3.10 Any Other MCP-Capable Agent

Any agent that supports the MCP protocol and can be given a command-based server entry can use DFMT. The minimum viable integration is:

1. Register the MCP server:
   ```
   { "command": "dfmt", "args": ["mcp"] }
   ```
2. Add project instruction text (format varies by agent) explaining the DFMT tools.

Without hooks, routing is instruction-dependent (~60–65% compliance). Session memory works for agent-initiated events only; external capture (FS watcher, git hooks) still runs independently.

`dfmt setup --generic` produces a tarball with the MCP entry (JSON), the instruction file (markdown), and a README explaining where to place each in the user's agent of choice.

---

## 4. Post-Setup Verification

After running `dfmt setup` and restarting the affected agents, verify the integration:

```bash
# Confirm the daemon is running for this project
dfmt status

# Confirm each agent's configuration
dfmt setup --verify

# Confirm a specific agent
dfmt setup --verify --agent claude
```

`--verify` re-reads each agent's config, checks that DFMT's entries are present and well-formed, attempts a round-trip MCP probe where possible, and reports per-agent status.

Sample output:

```
Claude Code       ✓ MCP registered, hooks installed, CLAUDE.md up to date
Gemini CLI        ✓ settings.json merged, GEMINI.md up to date
VS Code Copilot   ⚠ MCP registered but hooks file missing (run dfmt setup again)
Codex CLI         ✓ config.toml merged, AGENTS.md up to date
Cursor            ✓ mcp.json merged, .cursorrules updated
```

---

## 5. Per-Agent Hook Command Reference

DFMT implements one binary with one hook command shape:

```
dfmt hook <agent> <event>
```

Where `<agent>` is one of `claude-code`, `gemini-cli`, `vscode-copilot`, `opencode`, and `<event>` is the agent's event name in lowercase. The command reads the hook payload from stdin as JSON and writes the response JSON to stdout.

Events per agent:

| Agent | Events |
| --- | --- |
| claude-code | pretooluse, posttooluse, precompact, sessionstart, userpromptsubmit |
| gemini-cli | beforetool, aftertool, precompress, sessionstart |
| vscode-copilot | pretooluse, posttooluse, precompact, sessionstart |
| opencode | tool_before, tool_after, session_compacting |

Each event command implements the specific semantics of that agent's hook API: what the payload contains, what response shape is expected, what fields are mandatory. These are normalized internally so that all agents produce the same event types (file.edit, git.commit, etc.) in the journal.

---

## 6. Troubleshooting

### "DFMT tools not appearing in Claude Code"

1. Confirm `dfmt` is on `PATH`: `which dfmt`. If not, add `~/.local/bin` to `PATH`.
2. Confirm MCP entry is present: `cat ~/.claude/mcp.json | jq .mcpServers.dfmt`.
3. Check Claude Code's MCP log: `~/.claude/logs/mcp.log`. Look for `dfmt` startup errors.
4. Manually test the MCP server: `dfmt mcp`. Send a JSON-RPC initialize request on stdin. Should respond with capabilities.
5. Run `dfmt doctor` — reports runtime status, hook status, journal integrity.

### "Hooks fire but nothing happens"

1. Confirm the daemon starts from hook context: hooks are child processes, may have a different environment. Run `dfmt status --json` from within a hook to verify.
2. Check daemon log: `.dfmt/daemon.log`.
3. The most common cause is `PATH` differing in the hook process. Use absolute path to `dfmt` if needed.

### "Model is ignoring the instruction file"

1. Confirm the file is in the right location for that agent (see per-agent sections above).
2. Read the instruction file and judge its persuasiveness; the template is deliberately emphatic but can be strengthened for specific projects.
3. Consider whether the project has an older, more detailed rules file that's contradicting DFMT's section — reconcile.
4. If the agent supports hooks (Claude Code, Gemini, Copilot, OpenCode), ensure hooks are configured; hooks are the reliable enforcement layer.

### "Want to disable DFMT temporarily without uninstalling"

```
dfmt stop                    # stop the daemon for the current project
```

With the daemon stopped, MCP tool calls from agents will fail (the socket is absent) and hooks will silently degrade. Native tools continue to work normally. Re-start with any DFMT command (auto-start) or `dfmt daemon`.

### "Want to uninstall DFMT entirely"

```
dfmt setup --uninstall       # remove DFMT config from all agents
dfmt purge --global          # remove DFMT state
# then remove the binary:
rm "$(which dfmt)"
```

---

## 7. Open Questions

Resolvable as real usage emerges:

1. Whether to ship a native Zed extension that would enable hook support, or wait for Zed's MCP integration to expose hook points upstream.
2. Whether Cursor will expose a real intercept API; if so, elevate Cursor routing compliance to ~95%.
3. Whether to add an OpenCode-plugin-equivalent for any JS-based MCP client (Continue.dev, Windsurf) via a common plugin wrapper.
4. Whether `dfmt setup --agent <custom>` should accept a user-supplied config spec for experimental agents not in the default list.

---

*End of integration reference.*
