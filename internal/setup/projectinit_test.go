package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureProjectInitializedIdempotentAndPreservesConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// First call: cold start — should create everything.
	if err := EnsureProjectInitialized(tmpDir); err != nil {
		t.Fatalf("first ensure: %v", err)
	}

	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if _, err := os.Stat(dfmtDir); err != nil {
		t.Fatalf(".dfmt/ not created: %v", err)
	}
	configPath := filepath.Join(dfmtDir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config.yaml not created: %v", err)
	}

	// Customize the config — second call MUST NOT overwrite it.
	customConfig := []byte("# user customisation\nversion: 99\n")
	if err := os.WriteFile(configPath, customConfig, 0o600); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	// Seed a .gitignore that already has a leading entry. After the first
	// call we should see `.dfmt/` appended; after the second, no duplicate.
	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("node_modules\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Second call: warm start — must be idempotent.
	if err := EnsureProjectInitialized(tmpDir); err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after second ensure: %v", err)
	}
	if string(got) != string(customConfig) {
		t.Errorf("config.yaml clobbered on re-run\nwant:\n%s\ngot:\n%s", customConfig, got)
	}

	// Third call: ensure no duplicate `.dfmt/` line in .gitignore.
	if err := EnsureProjectInitialized(tmpDir); err != nil {
		t.Fatalf("third ensure: %v", err)
	}
	gi, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if c := strings.Count(string(gi), ".dfmt/"); c != 1 {
		t.Errorf(".dfmt/ appeared %d times in .gitignore, want 1\ncontents:\n%s", c, gi)
	}
}

func TestWriteProjectClaudeSettingsPreservesUserContent(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	original := map[string]any{
		"theme":      "dark-mode",
		"statusLine": map[string]any{"type": "command", "command": "my-status"},
		"hooks": map[string]any{
			"PreCompact": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "my-custom-precompact",
							"timeout": 5,
						},
					},
				},
			},
			"UserPromptSubmit": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "my-prompt-hook",
						},
					},
				},
			},
		},
		"permissions": map[string]any{
			"allow": []any{"Read", "MyCustomTool"},
			"deny":  []any{"SomeBadTool"},
		},
	}
	originalBytes, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(settingsPath, originalBytes, 0o600); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := writeProjectClaudeSettings(tmpDir); err != nil {
		t.Fatalf("writeProjectClaudeSettings: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read merged settings: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("invalid JSON after merge: %v", err)
	}

	if got, _ := merged["theme"].(string); got != "dark-mode" {
		t.Errorf("theme = %q, want dark-mode (user value clobbered)", got)
	}
	if _, ok := merged["statusLine"]; !ok {
		t.Error("statusLine missing — user key dropped")
	}

	mergedHooks, _ := merged["hooks"].(map[string]any)
	if mergedHooks == nil {
		t.Fatal("hooks missing")
	}
	if _, ok := mergedHooks["UserPromptSubmit"]; !ok {
		t.Error("UserPromptSubmit hook lost — unrelated event dropped")
	}
	preCompact, _ := mergedHooks["PreCompact"].([]any)
	foundCustom, foundDfmt := false, false
	for _, g := range preCompact {
		grp, _ := g.(map[string]any)
		inner, _ := grp["hooks"].([]any)
		for _, h := range inner {
			hc, _ := h.(map[string]any)
			cmd, _ := hc["command"].(string)
			if cmd == "my-custom-precompact" {
				foundCustom = true
			}
			if strings.Contains(cmd, "dfmt recall --save") {
				foundDfmt = true
			}
		}
	}
	if !foundCustom {
		t.Error("user PreCompact hook lost")
	}
	if !foundDfmt {
		t.Error("dfmt PreCompact hook not added")
	}

	perms, _ := merged["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if !containsString(allow, "MyCustomTool") {
		t.Error("user allow entry lost")
	}
	if !containsString(allow, "Read") {
		t.Error("user allow entry 'Read' lost")
	}
	if !containsString(allow, "mcp__dfmt__dfmt_exec") {
		t.Error("dfmt allow entry not added")
	}
	deny, _ := perms["deny"].([]any)
	if !containsString(deny, "SomeBadTool") {
		t.Error("user deny entry lost")
	}
	if !containsString(deny, "Bash") {
		t.Error("dfmt deny entry not added")
	}
}

func TestWriteProjectClaudeSettingsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	if err := writeProjectClaudeSettings(tmpDir); err != nil {
		t.Fatalf("first write: %v", err)
	}
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	first, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	if err := writeProjectClaudeSettings(tmpDir); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("settings.json changed on idempotent re-run\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestWriteProjectClaudeSettingsRefusesUserHome(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", fakeHome)
	}

	preexisting := []byte(`{"theme":"user-theme"}`)
	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, preexisting, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := writeProjectClaudeSettings(fakeHome); err != nil {
		t.Fatalf("writeProjectClaudeSettings: %v", err)
	}

	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(got) != string(preexisting) {
		t.Errorf("user-home settings.json was modified\nbefore: %s\nafter:  %s", preexisting, got)
	}
}

// TestEnsureProjectInitializedPreservesRichSettings is the end-to-end regression
// test for the bug where the daemon client's lazy auto-init clobbered
// `.claude/settings.json` with a hardcoded template, dropping plugins, env vars,
// statusLine, extraKnownMarketplaces, and other top-level keys the user had set.
//
// Before the fix, `internal/client/client.go::autoInitProject` did
// `os.WriteFile(settingsPath, hardcodedJSON, 0o600)` with no read/merge step.
// After the fix, both the cli (`dfmt init`) and client (lazy auto-init) paths
// route through this exported function and merge into the existing file.
//
// The keys exercised here mirror what a real Claude Code user has in their
// project-level settings: enabledPlugins map, env block with multiple entries,
// statusLine, extraKnownMarketplaces, autoUpdatesChannel, and the boolean
// skipAutoPermissionPrompt — all of which were silently lost pre-fix.
func TestEnsureProjectInitializedPreservesRichSettings(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	original := map[string]any{
		"autoUpdatesChannel": "latest",
		"effortLevel":        "xhigh",
		"enabledPlugins": map[string]any{
			"github@claude-plugins-official":     true,
			"playwright@claude-plugins-official": true,
		},
		"env": map[string]any{
			"API_TIMEOUT_MS":      "1800000",
			"MAX_THINKING_TOKENS": "96000",
		},
		"extraKnownMarketplaces": map[string]any{
			"openai-codex": map[string]any{
				"source": map[string]any{"repo": "openai/codex-plugin-cc", "source": "github"},
			},
		},
		"statusLine": map[string]any{
			"type":    "command",
			"command": "npx -y ccstatusline@latest",
			"padding": float64(0),
		},
		"skipAutoPermissionPrompt":          true,
		"skipDangerousModePermissionPrompt": true,
	}
	originalBytes, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(settingsPath, originalBytes, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureProjectInitialized(tmpDir); err != nil {
		t.Fatalf("EnsureProjectInitialized: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read after init: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("invalid JSON after init: %v", err)
	}

	// Every top-level key the user had must still be present and unchanged.
	wantKeys := []string{
		"autoUpdatesChannel",
		"effortLevel",
		"enabledPlugins",
		"env",
		"extraKnownMarketplaces",
		"statusLine",
		"skipAutoPermissionPrompt",
		"skipDangerousModePermissionPrompt",
	}
	for _, k := range wantKeys {
		if _, ok := merged[k]; !ok {
			t.Errorf("user key %q dropped by EnsureProjectInitialized", k)
		}
	}
	if got, _ := merged["autoUpdatesChannel"].(string); got != "latest" {
		t.Errorf("autoUpdatesChannel = %q, want %q", got, "latest")
	}
	if got, _ := merged["skipAutoPermissionPrompt"].(bool); !got {
		t.Errorf("skipAutoPermissionPrompt flipped to false")
	}
	envMap, _ := merged["env"].(map[string]any)
	if got, _ := envMap["MAX_THINKING_TOKENS"].(string); got != "96000" {
		t.Errorf("env.MAX_THINKING_TOKENS = %q, want 96000", got)
	}

	// And dfmt's own entries must have been merged in.
	perms, _ := merged["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if !containsString(allow, "mcp__dfmt__dfmt_exec") {
		t.Error("dfmt allow entry not merged in")
	}
	hooks, _ := merged["hooks"].(map[string]any)
	if _, ok := hooks["PreCompact"]; !ok {
		t.Error("PreCompact hook not merged in")
	}
}

func containsString(list []any, want string) bool {
	for _, v := range list {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}
