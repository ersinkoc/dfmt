package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// legacyPreToolUseSettings is a ~/.claude/settings.json as dfmt <= 0.9.0
// left it: the user's own keys and hook, plus dfmt's routing hook.
const legacyPreToolUseSettings = `{
  "theme": "dark",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "./lint.sh"}]},
      {"matcher": "Bash|Read|WebFetch|Grep|Task|Edit|Write|Glob",
       "hooks": [{"type": "command", "command": "dfmt hook claude-code pretooluse", "timeout": 5}]}
    ]
  }
}`

// The routing hook printed fields Claude Code's hook protocol does not
// have, so it never redirected anything — while spawning dfmt before every
// native tool call. Setup now removes it, and only it.
func TestPurgeClaudeCodePreToolUseHookRemovesOnlyDFMT(t *testing.T) {
	claudeDir := t.TempDir()
	settings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settings, []byte(legacyPreToolUseSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PurgeClaudeCodePreToolUseHook(claudeDir); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	out, _ := os.ReadFile(settings)
	if strings.Contains(string(out), "pretooluse") {
		t.Errorf("dfmt hook survived:\n%s", out)
	}
	got := map[string]any{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("settings.json no longer valid JSON: %v", err)
	}
	if got["theme"] != "dark" || !strings.Contains(string(out), "./lint.sh") {
		t.Errorf("user's keys or hook lost:\n%s", out)
	}
}

func TestPurgeClaudeCodePreToolUseHookNeverCreatesFile(t *testing.T) {
	claudeDir := t.TempDir()
	if err := PurgeClaudeCodePreToolUseHook(claudeDir); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "settings.json")); !os.IsNotExist(err) {
		t.Errorf("settings.json created by a purge (stat err = %v)", err)
	}
}

// A settings.json that is not parseable must produce an error rather than
// being silently overwritten — the user's file may just be mid-edit.
func TestPurgeClaudeCodePreToolUseHookRefusesMalformedJSON(t *testing.T) {
	claudeDir := t.TempDir()
	settings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settings, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := PurgeClaudeCodePreToolUseHook(claudeDir); err == nil {
		t.Fatal("nil error for a malformed settings.json")
	}
	out, _ := os.ReadFile(settings)
	if string(out) != "{ this is not json" {
		t.Error("malformed settings.json was modified despite the error")
	}
}

// Project settings: the writer must not install the hook, and must strip
// one a previous version wrote.
func TestWriteProjectClaudeSettingsDropsPreToolUseHook(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(legacyPreToolUseSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectClaudeSettings(dir); err != nil {
		t.Fatalf("writeProjectClaudeSettings: %v", err)
	}
	out, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if strings.Contains(string(out), "pretooluse") {
		t.Errorf("project settings still carry the routing hook:\n%s", out)
	}
	if !strings.Contains(string(out), "./lint.sh") || !strings.Contains(string(out), "PreCompact") {
		t.Errorf("user hook or dfmt's working hooks lost:\n%s", out)
	}
}

// IsClaudeCodeInstalled probes well-known install locations. The contract
// under test is only that it answers without panicking on a host where
// Claude Code may or may not exist.
func TestIsClaudeCodeInstalledAnswers(t *testing.T) {
	_ = IsClaudeCodeInstalled()
}

// SET-4: `dfmt remove` takes dfmt's hooks out of .claude/settings.json; it
// used to run the markdown block stripper on it, a no-op.
func TestRemoveProjectStripsClaudeSettingsHooks(t *testing.T) {
	dir := t.TempDir()
	if err := writeProjectClaudeSettings(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProject(dir); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json")); err == nil &&
		strings.Contains(string(data), "dfmt") {
		t.Errorf("dfmt hooks left in settings.json:\n%s", data)
	}
}
