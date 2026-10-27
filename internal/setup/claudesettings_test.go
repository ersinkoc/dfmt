package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SET-4: a settings.json holding dfmt's hooks and permissions next to the
// user's own keeps exactly the user's part after StripClaudeSettings.
func TestStripClaudeSettingsKeepsUserEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	user := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(make *)"], "deny": ["Read(.env)"]},
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "./lint.sh"}]}]}
}`
	if err := os.WriteFile(path, []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectClaudeSettings(dir); err != nil {
		t.Fatalf("writeProjectClaudeSettings: %v", err)
	}
	if err := WriteClaudeCodeSettingsHook(filepath.Dir(path)); err != nil {
		t.Fatalf("WriteClaudeCodeSettingsHook: %v", err)
	}

	changed, err := StripClaudeSettings(path)
	if err != nil || !changed {
		t.Fatalf("StripClaudeSettings = %v, %v; want changed", changed, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "dfmt") {
		t.Errorf("dfmt entries survived:\n%s", data)
	}
	var got, want map[string]any
	_ = json.Unmarshal(data, &got)
	_ = json.Unmarshal([]byte(user), &want)
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("user settings changed:\n got %s\nwant %s", gj, wj)
	}

	if changed, err := StripClaudeSettings(path); err != nil || changed {
		t.Errorf("second strip = %v, %v; want unchanged", changed, err)
	}
}

// A file dfmt created holds nothing else once stripped and is removed.
func TestStripClaudeSettingsRemovesFileItCreated(t *testing.T) {
	dir := t.TempDir()
	if err := writeProjectClaudeSettings(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	if _, err := StripClaudeSettings(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		data, _ := os.ReadFile(path)
		t.Errorf("settings.json left behind:\n%s", data)
	}
}

func TestStripClaudeSettingsRefusesInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := "{ // comment\n}"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StripClaudeSettings(path); err == nil {
		t.Error("unparseable settings.json was not refused")
	}
	if data, _ := os.ReadFile(path); string(data) != orig {
		t.Errorf("file rewritten: %q", data)
	}
}

func TestIsDFMTClaudeHookCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"dfmt hook claude-code pretooluse":                                  true,
		"/home/u/.local/bin/dfmt hook claude-code pretooluse":               true,
		`"C:\Program Files\dfmt\dfmt.exe" hook claude-code pretooluse`:      true,
		"dfmt recall --save --format md":                                    true,
		"if [ -f .dfmt/last-recall.md ]; then cat .dfmt/last-recall.md; fi": true,
		"./lint.sh":           false,
		"dfmt-other hook x":   false,
		"echo dfmt hook":      false,
		"/usr/bin/dfmt stats": false,
	} {
		if got := isDFMTClaudeHookCommand(cmd); got != want {
			t.Errorf("isDFMTClaudeHookCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}
