// Package setup provides helpers for configuring detected AI coding agents
// with DFMT's MCP server and per-project trust flags.
package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// dfmtMCPServerEntry returns the canonical MCP server entry that DFMT writes
// into Claude Code's ~/.claude.json configuration. The `command` is resolved
// to an absolute path (via ResolveDFMTCommand) so the MCP launch does not
// depend on Claude Code inheriting an up-to-date PATH from its parent shell.
func dfmtMCPServerEntry() map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": ResolveDFMTCommand(),
		"args":    []any{"mcp"},
		"env":     map[string]any{},
	}
}

// normalizeProjectKey normalizes a filesystem path into the form that
// Claude Code uses for keys inside `projects` in ~/.claude.json. On all
// platforms Claude Code stores the path with forward slashes, even on
// Windows where the drive letter prefix (e.g. "D:") is retained as-is.
func normalizeProjectKey(projectPath string) string {
	cleaned := filepath.Clean(projectPath)
	return strings.ReplaceAll(cleaned, "\\", "/")
}

// claudeUserJSONPath returns the path to ~/.claude.json.
func claudeUserJSONPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", fmt.Errorf("empty home directory")
	}
	return filepath.Join(home, ".claude.json"), nil
}

// PatchClaudeCodeUserJSON updates ~/.claude.json so Claude Code CLI
// picks up the dfmt MCP server and, when projectPath is non-empty,
// marks that project as trusted. It is safe against missing/empty files
// and preserves all unrelated keys. The previous file is saved as
// ~/.claude.json.dfmt.bak before the first successful write.
//
// If setUserScopeMCP is true, top-level mcpServers.dfmt is set.
// If projectPath is non-empty, projects[<normalized>].mcpServers.dfmt and
// the three trust flags (hasTrustDialogAccepted,
// hasClaudeMdExternalIncludesApproved,
// hasClaudeMdExternalIncludesWarningShown) are set to true.
func PatchClaudeCodeUserJSON(projectPath string, setUserScopeMCP bool) error {
	path, err := claudeUserJSONPath()
	if err != nil {
		return fmt.Errorf("locate claude user config: %w", err)
	}

	// Load existing config, if any. Missing file is fine. An empty file
	// is treated as an empty object so unrelated keys are preserved when
	// present.
	cfg := map[string]any{}
	existed := false
	var raw []byte
	if data, readErr := os.ReadFile(path); readErr == nil {
		existed = true
		raw = data
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			if uerr := json.Unmarshal(data, &cfg); uerr != nil {
				return fmt.Errorf("parse %s: %w", path, uerr)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", path, readErr)
	}

	// Top-level user-scope MCP server entry.
	if setUserScopeMCP {
		servers, _ := cfg["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["dfmt"] = dfmtMCPServerEntry()
		cfg["mcpServers"] = servers
	}

	// Per-project trust flags and MCP entry.
	if projectPath != "" {
		key := normalizeProjectKey(projectPath)

		projects, _ := cfg["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
		}

		// On Windows the filesystem is case-insensitive, but Claude Code keys
		// `projects` by whatever case the cwd had at the time. Two case
		// variants of the same path can therefore coexist (e.g. "D:/Codebox"
		// and "D:/CODEBOX") even though they refer to the same project. PS
		// JSON parsers refuse such files outright, so collapse the variants
		// into the canonical key before patching.
		if runtime.GOOS == "windows" {
			canonical, _ := projects[key].(map[string]any)
			if canonical == nil {
				canonical = map[string]any{}
			}
			for existingKey, val := range projects {
				if existingKey == key || !strings.EqualFold(existingKey, key) {
					continue
				}
				if other, ok := val.(map[string]any); ok {
					for k, v := range other {
						if _, has := canonical[k]; !has {
							canonical[k] = v
						}
					}
				}
				delete(projects, existingKey)
			}
			projects[key] = canonical
		}

		entry, _ := projects[key].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}

		entry["hasTrustDialogAccepted"] = true
		entry["hasClaudeMdExternalIncludesApproved"] = true
		entry["hasClaudeMdExternalIncludesWarningShown"] = true

		projectServers, _ := entry["mcpServers"].(map[string]any)
		if projectServers == nil {
			projectServers = map[string]any{}
		}
		projectServers["dfmt"] = dfmtMCPServerEntry()
		entry["mcpServers"] = projectServers

		projects[key] = entry
		cfg["projects"] = projects
	}

	// Marshal final config.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude user config: %w", err)
	}
	// MarshalIndent does not add a trailing newline; Claude Code's own
	// writer does. Keep parity.
	out = append(out, '\n')

	// Back up the original (pre-DFMT) file exactly once. safefs.WriteFile
	// refuses if backupPath or any parent is a symlink (closes the F-07
	// pristine-backup symlink-plant variant against ~/.claude.json.dfmt.bak).
	backupPath := path + ".dfmt.bak"
	if existed {
		if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
			if werr := safefs.WriteFile(filepath.Dir(path), backupPath, raw, 0600); werr != nil {
				return fmt.Errorf("write backup %s: %w", backupPath, werr)
			}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("stat backup %s: %w", backupPath, statErr)
		}
	}

	// Atomic write via tmp file + rename, within the same directory.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".claude.json.dfmt-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		// Non-fatal on some platforms (Windows), but try.
		_ = err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp: %w", err)
	}

	return nil
}

// UnpatchClaudeCodeUserJSON removes the dfmt MCP server entries from
// ~/.claude.json. It strips:
//
//   - top-level `mcpServers.dfmt` (set by `setUserScopeMCP`),
//   - per-project `projects[*].mcpServers.dfmt`.
//
// Trust flags (`hasTrustDialogAccepted`, `hasClaudeMdExternalIncludes-
// Approved`, `hasClaudeMdExternalIncludesWarningShown`) are deliberately
// left in place — DFMT cannot tell whether the user accepted them
// independently of our setup, and removing them would silently un-trust
// projects the user already trusts. Closes F-G-INFO-2 from the security
// audit (uninstall left stale `mcpServers.dfmt` entries that surfaced as
// "failed to start MCP server" in Claude Code after `dfmt` was removed).
//
// Safe against missing files (returns nil), empty files (no-op), and
// concurrent writers (atomic tmp + rename in the same directory). Empty
// `mcpServers` maps are pruned so the file doesn't grow `"mcpServers": {}`
// stubs across uninstall/reinstall cycles.
func UnpatchClaudeCodeUserJSON() error {
	path, err := claudeUserJSONPath()
	if err != nil {
		return fmt.Errorf("locate claude user config: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}

	cfg := map[string]any{}
	if uerr := json.Unmarshal(data, &cfg); uerr != nil {
		return fmt.Errorf("parse %s: %w", path, uerr)
	}

	changed := false

	// Top-level user-scope MCP entry.
	if servers, ok := cfg["mcpServers"].(map[string]any); ok {
		if _, present := servers["dfmt"]; present {
			delete(servers, "dfmt")
			changed = true
		}
		if len(servers) == 0 {
			delete(cfg, "mcpServers")
		} else {
			cfg["mcpServers"] = servers
		}
	}

	// Per-project MCP entries.
	if projects, ok := cfg["projects"].(map[string]any); ok {
		for key, val := range projects {
			entry, ok := val.(map[string]any)
			if !ok {
				continue
			}
			servers, ok := entry["mcpServers"].(map[string]any)
			if !ok {
				continue
			}
			if _, present := servers["dfmt"]; !present {
				continue
			}
			delete(servers, "dfmt")
			changed = true
			if len(servers) == 0 {
				delete(entry, "mcpServers")
			} else {
				entry["mcpServers"] = servers
			}
			projects[key] = entry
		}
	}

	if !changed {
		return nil
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude user config: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".claude.json.dfmt-unpatch-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	_ = os.Chmod(tmpPath, 0o600)
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp: %w", err)
	}

	return nil
}
