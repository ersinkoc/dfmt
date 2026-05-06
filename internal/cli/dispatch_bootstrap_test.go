package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSetupRefreshPurgesLegacyHook seeds a project + global Claude
// settings.json with v0.2.x-era fossils (legacy deny-triplet, dotted MCP
// permission, drifted hook command, narrow PreToolUse matcher) and then
// verifies that `dfmt setup --refresh`:
//
//  1. Detects and purges every fossil class.
//  2. Writes a one-shot .dfmt.bak alongside each settings file.
//  3. Re-emits the current PreToolUse template (full matcher set,
//     resolved DFMT command path) so a fresh quickstart-flavored shape
//     is on disk afterwards.
//
// The test is end-to-end at the dispatch layer (Dispatch("setup",
// "--refresh")) — it does not poke into the helpers directly. That's
// the blast radius the user-visible flag actually exercises.
func TestRunSetupRefreshPurgesLegacyHook(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "xdg"))
	// Chdir into tmp so the cwd-fallback in runSetupRefresh can't pick up
	// the test runner's own working directory and refresh DFMT's repo
	// settings as a side effect.
	t.Chdir(tmp)

	// Seed a project root with .dfmt/ so writeProjectClaudeSettings is
	// willing to operate on it (writeProjectClaudeSettings refuses when
	// dir == HomeDir; the project root must therefore differ).
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed project .dfmt: %v", err)
	}

	// Project-level settings.json with multiple fossil classes.
	projClaudeDir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(projClaudeDir, 0o755); err != nil {
		t.Fatalf("mkdir project .claude: %v", err)
	}
	projSettingsPath := filepath.Join(projClaudeDir, "settings.json")
	legacyProjBody := `{
	"permissions": {
		"deny": ["Bash", "WebFetch", "WebSearch"],
		"allow": ["mcp__dfmt__dfmt.exec", "mcp__dfmt__dfmt.read"]
	},
	"hooks": {
		"PreToolUse": [
			{
				"matcher": "Bash",
				"hooks": [
					{"type": "command", "command": "dfmt hook claude-code pretooluse", "timeout": 5}
				]
			}
		]
	}
}`
	if err := os.WriteFile(projSettingsPath, []byte(legacyProjBody), 0o600); err != nil {
		t.Fatalf("seed project settings.json: %v", err)
	}

	// Global ~/.claude/settings.json with the deny-triplet fossil.
	globalClaudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(globalClaudeDir, 0o700); err != nil {
		t.Fatalf("mkdir global .claude: %v", err)
	}
	globalSettingsPath := filepath.Join(globalClaudeDir, "settings.json")
	legacyGlobalBody := `{"permissions": {"deny": ["Bash", "WebFetch", "WebSearch"]}}`
	if err := os.WriteFile(globalSettingsPath, []byte(legacyGlobalBody), 0o600); err != nil {
		t.Fatalf("seed global settings.json: %v", err)
	}

	// Manifest must point at the project settings file so refresh discovers
	// the project root by stripping `/.claude/settings.json`.
	xdg := filepath.Join(tmp, "xdg", "dfmt")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatalf("mkdir xdg/dfmt: %v", err)
	}
	manifestBody := map[string]any{
		"version": 1,
		"files": []map[string]any{
			{"path": projSettingsPath, "agent": "claude-code", "version": "1"},
		},
	}
	manifestBytes, _ := json.Marshal(manifestBody)
	if err := os.WriteFile(filepath.Join(xdg, "setup-manifest.json"), manifestBytes, 0o600); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	// Run refresh.
	if code := Dispatch([]string{"setup", "--refresh"}); code != 0 {
		t.Fatalf("setup --refresh exited with %d, want 0", code)
	}

	// --- Project assertions ---
	projAfter := readJSONOrFail(t, projSettingsPath)

	// Deny triplet must be gone.
	if perms, ok := projAfter["permissions"].(map[string]any); ok {
		if deny, ok := perms["deny"].([]any); ok {
			for _, v := range deny {
				if s, ok := v.(string); ok {
					switch s {
					case "Bash", "WebFetch", "WebSearch":
						t.Errorf("project deny still contains legacy entry %q", s)
					}
				}
			}
		}
		// Dotted MCP permissions must be gone, underscored ones present.
		if allow, ok := perms["allow"].([]any); ok {
			for _, v := range allow {
				if s, ok := v.(string); ok && strings.HasPrefix(s, "mcp__dfmt__dfmt.") {
					t.Errorf("project allow still contains dotted MCP entry %q", s)
				}
			}
		}
	}

	// PreToolUse matcher must have been expanded to the current set.
	hooks, _ := projAfter["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	if len(pre) == 0 {
		t.Fatalf("project PreToolUse missing after refresh; got %#v", projAfter)
	}
	matcherFound := false
	for _, g := range pre {
		gm, _ := g.(map[string]any)
		matcher, _ := gm["matcher"].(string)
		// Current matcher set: Bash|Read|WebFetch|Grep|Task|Edit|Write|Glob.
		// We don't pin the exact ordering — checking superset coverage.
		want := []string{"Bash", "Read", "WebFetch", "Grep", "Task", "Edit", "Write", "Glob"}
		ok := true
		for _, m := range want {
			if !strings.Contains(matcher, m) {
				ok = false
				break
			}
		}
		if ok {
			matcherFound = true
			break
		}
	}
	if !matcherFound {
		t.Errorf("project PreToolUse matcher not expanded to current set; got %#v", pre)
	}

	// Backup written.
	if _, err := os.Stat(projSettingsPath + ".dfmt.bak"); err != nil {
		t.Errorf("project backup .dfmt.bak missing: %v", err)
	}

	// --- Global assertions ---
	globalAfter := readJSONOrFail(t, globalSettingsPath)
	if perms, ok := globalAfter["permissions"].(map[string]any); ok {
		if deny, ok := perms["deny"].([]any); ok {
			for _, v := range deny {
				if s, ok := v.(string); ok {
					switch s {
					case "Bash", "WebFetch", "WebSearch":
						t.Errorf("global deny still contains legacy entry %q", s)
					}
				}
			}
		}
	}

	// Global must now have a PreToolUse hook (WriteClaudeCodeSettingsHook
	// runs after purge).
	gHooks, _ := globalAfter["hooks"].(map[string]any)
	gPre, _ := gHooks["PreToolUse"].([]any)
	if len(gPre) == 0 {
		t.Errorf("global PreToolUse missing after refresh; got %#v", globalAfter)
	}

	// Global backup written.
	if _, err := os.Stat(globalSettingsPath + ".dfmt.bak"); err != nil {
		t.Errorf("global backup .dfmt.bak missing: %v", err)
	}
}

// TestRunSetupRefreshIdempotent runs --refresh twice. After the first run,
// re-running must not introduce additional changes (no new fossils, no
// duplicated hook entries) — the marker for fossil-free state.
func TestRunSetupRefreshIdempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "xdg"))
	t.Chdir(tmp)

	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed project .dfmt: %v", err)
	}
	projClaudeDir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(projClaudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	projSettingsPath := filepath.Join(projClaudeDir, "settings.json")
	if err := os.WriteFile(projSettingsPath, []byte(`{"permissions":{"deny":["Bash","WebFetch","WebSearch"]}}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	xdg := filepath.Join(tmp, "xdg", "dfmt")
	_ = os.MkdirAll(xdg, 0o700)
	manifestBody := map[string]any{
		"version": 1,
		"files": []map[string]any{
			{"path": projSettingsPath, "agent": "claude-code", "version": "1"},
		},
	}
	mb, _ := json.Marshal(manifestBody)
	_ = os.WriteFile(filepath.Join(xdg, "setup-manifest.json"), mb, 0o600)

	if code := Dispatch([]string{"setup", "--refresh"}); code != 0 {
		t.Fatalf("first refresh exited %d", code)
	}
	first, err := os.ReadFile(projSettingsPath)
	if err != nil {
		t.Fatalf("read after first refresh: %v", err)
	}

	if code := Dispatch([]string{"setup", "--refresh"}); code != 0 {
		t.Fatalf("second refresh exited %d", code)
	}
	second, err := os.ReadFile(projSettingsPath)
	if err != nil {
		t.Fatalf("read after second refresh: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("refresh not idempotent\n--- first ---\n%s\n--- second ---\n%s",
			string(first), string(second))
	}
}

func readJSONOrFail(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}
