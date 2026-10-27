package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/safefs"
)

// EnsureProjectInitialized makes dir into a usable DFMT project. It is
// idempotent and never destructive: existing config.yaml, .gitignore content,
// and Claude settings.json are preserved. Called from `dfmt init` (explicit),
// `dfmt mcp` startup (auto-init for any folder Claude is opened in), and from
// the daemon client's lazy auto-init on the first RPC for a fresh project.
//
// Steps (each is no-op if already satisfied):
//   - create .dfmt/ (0700)
//   - write .dfmt/config.yaml with defaults if missing
//   - append .dfmt/ line to existing .gitignore if not already ignored
//   - merge dfmt entries into .claude/settings.json (skipped in user home)
//
// Lives in `setup` rather than `cli` so the daemon client (`internal/client`)
// can call the same merge-safe path without importing `cli` (cycle: cli
// already imports client). Closes the regression where the client's lazy
// auto-init clobbered project-level .claude/settings.json with a hardcoded
// template, dropping plugins/env/statusLine/etc.
func EnsureProjectInitialized(dir string) error {
	// safefs.WriteFileAtomic requires an absolute baseDir (it uses absolute
	// paths to detect symlink planting on the parent chain). Callers pass
	// relative paths in some hot paths — most notably the client's lazy
	// auto-init when invoked from the CLI's working directory ("" or "."),
	// so resolve to absolute before any safefs call. os.MkdirAll itself
	// happily accepts relative paths but we keep the absolute form for
	// consistency in error messages.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	dfmtDir := filepath.Join(abs, ".dfmt")
	// 0700 matches journal/content dir permissions — indexed events, raw
	// tool output, and redact patterns all live under .dfmt, and must not
	// be world- or group-readable on shared hosts.
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dfmtDir, err)
	}

	// 0o600 to match other .dfmt/ artifacts. Only write when missing so a
	// user-customized config.yaml is never clobbered on re-run / auto-init.
	// V-20: route through safefs so a symlink planted at .dfmt/config.yaml
	// (between project init runs, or by an attacker who got write access
	// to .dfmt/ once) can't redirect the seed write.
	configPath := filepath.Join(dfmtDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if werr := safefs.WriteFileAtomic(dfmtDir, configPath, []byte(config.DefaultConfigYAML()), 0o600); werr != nil {
			return fmt.Errorf("write %s: %w", configPath, werr)
		}
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", configPath, err)
	}

	// Append `.dfmt/` to .gitignore only if a .gitignore already exists.
	// Don't create one — that's the user's call. Idempotent: skip if our
	// entry is already present.
	gitignorePath := filepath.Join(abs, ".gitignore")
	if content, rerr := os.ReadFile(gitignorePath); rerr == nil {
		if !project.IsDfmtIgnored(content) {
			f, oerr := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0o644)
			if oerr != nil {
				logging.Warnf("open %s for append: %v", gitignorePath, oerr)
			} else {
				if _, werr := f.WriteString("\n.dfmt/\n"); werr != nil {
					logging.Warnf("append to %s: %v", gitignorePath, werr)
				}
				_ = f.Close()
			}
		}
	} else if !os.IsNotExist(rerr) {
		logging.Warnf("read %s: %v", gitignorePath, rerr)
	}

	// writeProjectClaudeSettings is itself merge-safe and refuses to write
	// to ~/.claude/settings.json. Failure is non-fatal — surface as warning
	// so the operator can fix permissions/ACL issues.
	if err := writeProjectClaudeSettings(abs); err != nil {
		logging.Warnf("write project Claude settings: %v", err)
	}
	return nil
}

// RemoveProject undoes EnsureProjectInitialized and the instruction-file
// injection from `dfmt init`. It is the counterpart to EnsureProjectInitialized:
// - removes .dfmt/ directory and all contents
// - strips dfmt's hooks and permissions from .claude/settings.json
// - strips the DFMT block from CLAUDE.md and AGENTS.md
// Idempotent: re-running produces no additional changes.
func RemoveProject(dir string) error {
	// Remove .dfmt/ directory tree
	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.RemoveAll(dfmtDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove .dfmt/: %w", err)
	}

	// Strip dfmt's hooks and permissions from .claude/settings.json. This
	// used to call StripDFMTBlock, which looks for markdown markers a JSON
	// file never has — a no-op that left the hooks running (SET-4).
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if _, err := StripClaudeSettings(settingsPath); err != nil {
		return fmt.Errorf("strip settings.json: %w", err)
	}

	// Strip DFMT block from agent instruction files
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		path := filepath.Join(dir, name)
		if err := StripDFMTBlock(path); err != nil {
			return fmt.Errorf("strip %s: %w", name, err)
		}
	}
	return nil
}

// writeProjectClaudeSettings merges DFMT's tool-enforcement entries into a
// project-local .claude/settings.json. It NEVER overwrites the file: existing
// hooks, permissions, and unknown keys are preserved. dfmt-owned entries are
// added only when missing.
//
// User-scope is off-limits: if dir resolves to the user's home directory, the
// function is a no-op. dfmt's permissions/deny rules only make sense inside an
// initialized project, and clobbering ~/.claude/settings.json would destroy
// the user's global Claude Code configuration.
func writeProjectClaudeSettings(dir string) error {
	if isUserHome(dir) {
		return nil
	}

	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	cfg := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			if uerr := json.Unmarshal(data, &cfg); uerr != nil {
				return fmt.Errorf("parse %s: %w", settingsPath, uerr)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	preCompact := `dfmt recall --save --format md`
	// POSIX shell on every platform, Windows included. Claude Code does not
	// hand hook bodies to the host shell -- it runs them through bash
	// (`/usr/bin/bash -c` from the bundled Git Bash on Windows), so a
	// PowerShell body is a syntax error there, not a portability win. dfmt
	// branched on GOOS here through v0.7.2 and every Windows session start
	// failed the hook; purgePowerShellHooks migrates those configs.
	sessionStart := `if [ -f .dfmt/last-recall.md ]; then echo '--- Previous session summary ---' && cat .dfmt/last-recall.md && echo '--- End of previous session ---'; fi`
	// Strip the fossil first: mergeClaudeHook is keyed on the exact command
	// string, so without this an upgraded Windows install keeps the broken
	// PowerShell entry and gains the POSIX one beside it -- the hook error
	// survives the upgrade.
	purgePowerShellHooks(cfg)
	mergeClaudeHook(cfg, "PreCompact", preCompact, 30, "Saving session snapshot for next session...")
	mergeClaudeHook(cfg, "SessionStart", sessionStart, 10, "Loading previous session summary...")
	// No PreToolUse hook: see PurgeClaudeCodePreToolUseHook. Strip the one
	// earlier versions wrote.
	stripClaudeHookCommands(cfg, isDFMTPreToolUseHook)

	// MCP tool names changed from dotted (dfmt.exec) to underscored
	// (dfmt_exec) so Claude Code's MCP client (regex
	// ^[a-zA-Z][a-zA-Z0-9_-]*$) accepts them. The legacy dotted entries
	// would just be dead permission strings — Claude Code never receives
	// the dotted tools from tools/list, so it never offers them.
	mergeClaudePermission(cfg, "allow", []string{
		"mcp__dfmt__dfmt_exec",
		"mcp__dfmt__dfmt_read",
		"mcp__dfmt__dfmt_fetch",
		"mcp__dfmt__dfmt_remember",
		"mcp__dfmt__dfmt_search",
		"mcp__dfmt__dfmt_recall",
		"mcp__dfmt__dfmt_stats",
		"mcp__dfmt__dfmt_glob",
		"mcp__dfmt__dfmt_grep",
		"mcp__dfmt__dfmt_edit",
		"mcp__dfmt__dfmt_write",
	})
	// Deliberately do NOT inject any "deny" entries. DFMT's job is to
	// register itself as a tool provider and offer hooks/MCP routing —
	// it must never restrict which tools the host agent (Claude Code,
	// etc.) is allowed to call. The user owns the deny list; if they
	// want to forbid native Bash/WebFetch they'll add it themselves.
	pruneStaleDfmtDeny(cfg)

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(claudeDir, ".settings.json.dfmt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// 0600: this file grants MCP tool permissions. An attacker who can read
	// or race-write it controls what dfmt is launched with on next session.
	_ = os.Chmod(tmpPath, 0o600)
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		cleanup()
		return err
	}
	return nil
}

// isUserHome reports whether dir resolves to the current user's home
// directory. Used to refuse writing user-scope ~/.claude/settings.json.
func isUserHome(dir string) bool {
	home := HomeDir()
	if home == "" {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(absDir), filepath.Clean(absHome))
}

// mergeClaudeHook adds a dfmt hook to cfg["hooks"][eventName] only if no
// existing hook in that event already runs the same command. All other hooks
// (user-defined or otherwise) are preserved verbatim.
func mergeClaudeHook(cfg map[string]any, eventName, command string, timeoutSecs int, statusMsg string) {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	groups, _ := hooks[eventName].([]any)
	for _, g := range groups {
		grp, _ := g.(map[string]any)
		if grp == nil {
			continue
		}
		inner, _ := grp["hooks"].([]any)
		for _, h := range inner {
			hc, _ := h.(map[string]any)
			if hc == nil {
				continue
			}
			if cmd, _ := hc["command"].(string); cmd == command {
				return
			}
		}
	}
	newGroup := map[string]any{
		"matcher": "",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       command,
				"timeout":       timeoutSecs,
				"statusMessage": statusMsg,
			},
		},
	}
	hooks[eventName] = append(groups, newGroup)
	cfg["hooks"] = hooks
}

// PurgeClaudeCodePreToolUseHook removes the PreToolUse hook earlier dfmt
// versions wrote to <claudeDir>/settings.json, leaving every other key and
// hook alone. It never creates the file.
//
// The hook (`dfmt hook claude-code pretooluse`, matcher Bash|Read|…) was
// meant to redirect native tool calls to dfmt, but it printed
// {"block":false} / {"redirect":…} — fields Claude Code's hook protocol does
// not have — so it never changed anything. It did spawn a dfmt process and
// dial the daemon before every native call, and showed "Routing through dfmt
// for token savings..." while doing nothing. A working version would have
// to deny native tools, which breaks the documented fallback (on a dfmt
// failure the agent switches to the native tool), so it is gone instead.
func PurgeClaudeCodePreToolUseHook(claudeDir string) error {
	settingsPath := filepath.Join(claudeDir, "settings.json")

	data, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	cfg := map[string]any{}
	if uerr := json.Unmarshal(data, &cfg); uerr != nil {
		return fmt.Errorf("parse %s: %w", settingsPath, uerr)
	}
	if !stripClaudeHookCommands(cfg, isDFMTPreToolUseHook) {
		return nil
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(claudeDir, ".settings.json.dfmt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, werr := tmp.Write(out); werr != nil {
		_ = tmp.Close()
		cleanup()
		return werr
	}
	if werr := tmp.Close(); werr != nil {
		cleanup()
		return werr
	}
	_ = os.Chmod(tmpPath, 0o600)
	if werr := os.Rename(tmpPath, settingsPath); werr != nil {
		cleanup()
		return werr
	}
	return nil
}

// pruneStaleDfmtDeny removes the legacy {"Bash","WebFetch","WebSearch"} deny
// triplet that pre-v0.3.1 dfmt versions injected into permissions.deny.
// The detection + purge logic lives in legacy.go::purgeDenyTriplet so all
// settings.json fossil handling sits in one file; this wrapper preserves
// the existing call site in writeProjectClaudeSettings.
func pruneStaleDfmtDeny(cfg map[string]any) {
	purgeDenyTriplet(cfg)
}

// mergeClaudePermission adds dfmt entries to cfg["permissions"][key] without
// duplicating anything already present and without removing entries the user
// added themselves.
func mergeClaudePermission(cfg map[string]any, key string, additions []string) {
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	existing, _ := perms[key].([]any)
	seen := make(map[string]bool, len(existing))
	for _, v := range existing {
		if s, ok := v.(string); ok {
			seen[s] = true
		}
	}
	for _, add := range additions {
		if !seen[add] {
			existing = append(existing, add)
			seen[add] = true
		}
	}
	if len(existing) > 0 {
		perms[key] = existing
	}
	cfg["permissions"] = perms
}
