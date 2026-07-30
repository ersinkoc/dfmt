package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// RemoveProject and WriteClaudeCodeSettingsHook are the two halves of
// `dfmt remove` / `dfmt setup` that touch a user's own files. Both were
// uncovered, which is the worst place to have no tests: a bug here does not
// return an error, it damages a settings.json the user did not ask us to
// rewrite.

func TestRemoveProjectDeletesDfmtDir(t *testing.T) {
	proj := t.TempDir()
	dfmtDir := filepath.Join(proj, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dfmtDir, "journal.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := RemoveProject(proj); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, err := os.Stat(dfmtDir); !os.IsNotExist(err) {
		t.Errorf(".dfmt/ still present after RemoveProject (err=%v)", err)
	}
}

// Removing a project that was never initialized must be a no-op, not an
// error — `dfmt remove` is the thing a user reaches for when they are not
// sure what state they are in.
func TestRemoveProjectIsIdempotent(t *testing.T) {
	proj := t.TempDir()
	if err := RemoveProject(proj); err != nil {
		t.Fatalf("RemoveProject on a clean dir: %v", err)
	}
	if err := RemoveProject(proj); err != nil {
		t.Fatalf("RemoveProject called twice: %v", err)
	}
}

// The hook writer must merge into an existing settings.json rather than
// replacing it. Clobbering a user's Claude Code settings would destroy
// configuration DFMT has no business touching.
func TestWriteClaudeCodeSettingsHookPreservesExistingKeys(t *testing.T) {
	claudeDir := t.TempDir()
	settings := filepath.Join(claudeDir, "settings.json")

	existing := map[string]any{
		"theme":       "dark",
		"customThing": map[string]any{"a": float64(1)},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(settings, data, 0o600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := WriteClaudeCodeSettingsHook(claudeDir); err != nil {
		t.Fatalf("WriteClaudeCodeSettingsHook: %v", err)
	}

	got := map[string]any{}
	out, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("settings.json is no longer valid JSON: %v", err)
	}
	if got["theme"] != "dark" {
		t.Errorf("theme = %v, want dark — pre-existing keys must survive", got["theme"])
	}
	if _, ok := got["customThing"]; !ok {
		t.Error("customThing was dropped; the writer replaced instead of merging")
	}
}

func TestWriteClaudeCodeSettingsHookCreatesFileWhenAbsent(t *testing.T) {
	claudeDir := t.TempDir()
	if err := WriteClaudeCodeSettingsHook(claudeDir); err != nil {
		t.Fatalf("WriteClaudeCodeSettingsHook: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Errorf("created settings.json is not valid JSON: %v", err)
	}
}

// A settings.json that is not parseable must produce an error rather than
// being silently overwritten — the user's file may just be mid-edit.
func TestWriteClaudeCodeSettingsHookRefusesMalformedJSON(t *testing.T) {
	claudeDir := t.TempDir()
	settings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settings, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := WriteClaudeCodeSettingsHook(claudeDir); err == nil {
		t.Fatal("nil error for a malformed settings.json; the user's file would be replaced")
	}
	out, _ := os.ReadFile(settings)
	if string(out) != "{ this is not json" {
		t.Error("malformed settings.json was modified despite the error")
	}
}

// IsClaudeCodeInstalled probes well-known install locations. The contract
// under test is only that it answers without panicking on a host where
// Claude Code may or may not exist.
func TestIsClaudeCodeInstalledAnswers(t *testing.T) {
	_ = IsClaudeCodeInstalled()
}
