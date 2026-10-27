package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StripClaudeSettings removes what dfmt merged into a Claude Code
// settings.json — its hooks (PreCompact snapshot, SessionStart summary,
// and the retired PreToolUse routing hook) and its `mcp__dfmt__*` allow entries — and leaves
// everything else. It reports whether the file changed. A file left
// empty (`{}`) is removed; a file that does not parse is refused rather
// than rewritten.
//
// SET-4: uninstall and `dfmt remove` used to leave these behind
// (RemoveProject ran the marker-block stripper on a JSON file, a no-op),
// so after dfmt was gone Claude Code kept running
// `dfmt hook claude-code pretooluse` before every Bash/Read/Edit call.
func StripClaudeSettings(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return false, nil
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	// The legacy purges go first: they can leave empty containers, which
	// the two strippers below then drop.
	changed := purgeDenyTriplet(cfg)
	if purgePowerShellHooks(cfg) {
		changed = true
	}
	if stripDFMTClaudeHooks(cfg) {
		changed = true
	}
	if stripDFMTPermissions(cfg) {
		changed = true
	}
	if !changed {
		return false, nil
	}
	if perms, ok := cfg["permissions"].(map[string]any); ok && len(perms) == 0 {
		delete(cfg, "permissions")
	}

	if len(cfg) == 0 {
		if err := os.Remove(path); err != nil {
			return false, err
		}
		return true, nil
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	mode := os.FileMode(0o600)
	if st, serr := os.Stat(path); serr == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings.json.dfmt-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	if _, werr := tmp.Write(out); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return false, werr
	}
	if werr := tmp.Close(); werr != nil {
		_ = os.Remove(tmpPath)
		return false, werr
	}
	_ = os.Chmod(tmpPath, mode)
	if werr := os.Rename(tmpPath, path); werr != nil {
		_ = os.Remove(tmpPath)
		return false, werr
	}
	return true, nil
}

// isDFMTClaudeHookCommand reports whether a hook command is one dfmt
// writes: `<dfmt> hook …` or `<dfmt> recall --save …` with the binary
// bare or as a (possibly quoted) path, or the SessionStart snippet that
// prints .dfmt/last-recall.md.
func isDFMTClaudeHookCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if strings.Contains(cmd, ".dfmt/last-recall.md") {
		return true
	}
	var exe, rest string
	if strings.HasPrefix(cmd, `"`) {
		end := strings.Index(cmd[1:], `"`)
		if end < 0 {
			return false
		}
		exe, rest = cmd[1:end+1], cmd[end+2:]
	} else {
		exe, rest, _ = strings.Cut(cmd, " ")
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(exe, `\`, "/")))
	if base != "dfmt" && base != "dfmt.exe" {
		return false
	}
	rest = strings.TrimSpace(rest)
	return strings.HasPrefix(rest, "hook ") || strings.HasPrefix(rest, "recall --save")
}

// isDFMTPreToolUseHook matches the retired PreToolUse routing hook.
func isDFMTPreToolUseHook(cmd string) bool {
	return isDFMTClaudeHookCommand(cmd) && strings.Contains(cmd, "hook claude-code pretooluse")
}

// stripDFMTClaudeHooks drops dfmt's hook commands, then any group and
// event left empty by that, then an empty "hooks" object.
func stripDFMTClaudeHooks(cfg map[string]any) bool {
	return stripClaudeHookCommands(cfg, isDFMTClaudeHookCommand)
}

// stripClaudeHookCommands drops every hook whose command matches, then any
// group and event left empty by that, then an empty "hooks" object.
func stripClaudeHookCommands(cfg map[string]any, match func(string) bool) bool {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	changed := false
	for event, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var keptGroups []any
		for _, g := range groups {
			grp, _ := g.(map[string]any)
			inner, ok := grp["hooks"].([]any)
			if grp == nil || !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			var kept []any
			for _, h := range inner {
				hc, _ := h.(map[string]any)
				if cmd, _ := hc["command"].(string); hc != nil && match(cmd) {
					changed = true
					continue
				}
				kept = append(kept, h)
			}
			if len(kept) == 0 {
				continue
			}
			grp["hooks"] = kept
			keptGroups = append(keptGroups, grp)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	if len(hooks) == 0 {
		delete(cfg, "hooks")
	}
	return changed
}

// stripDFMTPermissions drops `mcp__dfmt__*` entries from permissions.allow,
// then an empty allow list and an empty permissions object.
func stripDFMTPermissions(cfg map[string]any) bool {
	perms, _ := cfg["permissions"].(map[string]any)
	if perms == nil {
		return false
	}
	allow, ok := perms["allow"].([]any)
	if !ok {
		return false
	}
	var kept []any
	for _, v := range allow {
		if s, _ := v.(string); strings.HasPrefix(s, "mcp__dfmt__") {
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == len(allow) {
		return false
	}
	if len(kept) == 0 {
		delete(perms, "allow")
	} else {
		perms["allow"] = kept
	}
	if len(perms) == 0 {
		delete(cfg, "permissions")
	}
	return true
}
