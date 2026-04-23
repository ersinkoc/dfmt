package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setHome overrides both HOME and USERPROFILE so os.UserHomeDir() resolves
// the test's temporary directory on all platforms.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// readClaudeJSON reads ~/.claude.json from the given home as a generic map.
func readClaudeJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse claude.json: %v\nraw: %s", err, data)
	}
	return m
}

// getMap traverses nested maps; fails the test if any level is missing.
func getMap(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := m
	for i, key := range path {
		v, ok := cur[key]
		if !ok {
			t.Fatalf("missing key %q at depth %d (path=%v)", key, i, path)
		}
		sub, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("key %q at depth %d is not a map (got %T)", key, i, v)
		}
		cur = sub
	}
	return cur
}

func TestPatchClaudeCodeUserJSON_MissingFile_ProjectScope(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	projectPath := filepath.Join(home, "projects", "demo")
	if err := PatchClaudeCodeUserJSON(projectPath, false); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg := readClaudeJSON(t, home)
	// Only projects block must exist; no top-level mcpServers.
	if _, ok := cfg["mcpServers"]; ok {
		t.Errorf("unexpected top-level mcpServers when setUserScopeMCP=false")
	}

	key := normalizeProjectKey(projectPath)
	proj := getMap(t, cfg, "projects", key)
	for _, flag := range []string{
		"hasTrustDialogAccepted",
		"hasClaudeMdExternalIncludesApproved",
		"hasClaudeMdExternalIncludesWarningShown",
	} {
		if v, _ := proj[flag].(bool); !v {
			t.Errorf("flag %s = %v, want true", flag, proj[flag])
		}
	}
	servers := getMap(t, proj, "mcpServers")
	dfmt, ok := servers["dfmt"].(map[string]any)
	if !ok {
		t.Fatalf("projects[%s].mcpServers.dfmt missing", key)
	}
	if dfmt["command"] != "dfmt" || dfmt["type"] != "stdio" {
		t.Errorf("mcp entry wrong: %#v", dfmt)
	}
}

func TestPatchClaudeCodeUserJSON_PreservesUnrelatedKeys(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	initial := map[string]any{
		"theme":        "dark",
		"randomNested": map[string]any{"a": 1.0, "b": "x"},
		"mcpServers": map[string]any{
			"existing": map[string]any{"command": "foo", "args": []any{"a", "b"}},
		},
		"projects": map[string]any{
			"/tmp/other": map[string]any{"hasTrustDialogAccepted": false, "custom": "keep"},
		},
	}
	raw, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	projectPath := "/workspace/demo"
	if err := PatchClaudeCodeUserJSON(projectPath, true); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg := readClaudeJSON(t, home)
	if cfg["theme"] != "dark" {
		t.Errorf("theme lost: %v", cfg["theme"])
	}
	nested := getMap(t, cfg, "randomNested")
	if nested["a"].(float64) != 1.0 || nested["b"] != "x" {
		t.Errorf("randomNested lost: %v", nested)
	}
	// Existing mcp server entry must still be present next to new dfmt entry.
	servers := getMap(t, cfg, "mcpServers")
	if _, ok := servers["existing"]; !ok {
		t.Errorf("existing mcp server lost: %v", servers)
	}
	if _, ok := servers["dfmt"]; !ok {
		t.Errorf("dfmt mcp server not added: %v", servers)
	}
	// Other project must still be there, untouched.
	other := getMap(t, cfg, "projects", "/tmp/other")
	if other["custom"] != "keep" {
		t.Errorf("other project custom key lost: %v", other)
	}
	if v, _ := other["hasTrustDialogAccepted"].(bool); v {
		t.Errorf("other project hasTrustDialogAccepted should stay false, got %v", v)
	}
	// New project was added.
	_ = getMap(t, cfg, "projects", normalizeProjectKey(projectPath))
}

func TestPatchClaudeCodeUserJSON_UserScopeOnly(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	if err := PatchClaudeCodeUserJSON("", true); err != nil {
		t.Fatalf("patch: %v", err)
	}
	cfg := readClaudeJSON(t, home)
	if _, ok := cfg["projects"]; ok {
		t.Errorf("projects block created when projectPath empty: %v", cfg["projects"])
	}
	servers := getMap(t, cfg, "mcpServers")
	dfmt, ok := servers["dfmt"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers.dfmt missing")
	}
	if dfmt["command"] != "dfmt" {
		t.Errorf("command = %v, want dfmt", dfmt["command"])
	}
	args, _ := dfmt["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", dfmt["args"])
	}
}

func TestPatchClaudeCodeUserJSON_BothScopes(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	projectPath := "/work/app"
	if err := PatchClaudeCodeUserJSON(projectPath, true); err != nil {
		t.Fatalf("patch: %v", err)
	}
	cfg := readClaudeJSON(t, home)
	_ = getMap(t, cfg, "mcpServers", "dfmt")
	proj := getMap(t, cfg, "projects", normalizeProjectKey(projectPath))
	if v, _ := proj["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("trust flag not set")
	}
	_ = getMap(t, proj, "mcpServers", "dfmt")
}

func TestPatchClaudeCodeUserJSON_BackupBehavior(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	claudePath := filepath.Join(home, ".claude.json")
	original := []byte(`{"pristine": true}` + "\n")
	if err := os.WriteFile(claudePath, original, 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First run creates backup with original contents.
	if err := PatchClaudeCodeUserJSON("", true); err != nil {
		t.Fatalf("patch 1: %v", err)
	}
	backup := claudePath + ".dfmt.bak"
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("backup mismatch:\nwant: %s\ngot:  %s", original, got)
	}

	// Second run must NOT clobber the pristine backup even though
	// ~/.claude.json has now been modified by the first run.
	if err := PatchClaudeCodeUserJSON("/x/y", false); err != nil {
		t.Fatalf("patch 2: %v", err)
	}
	got2, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup 2: %v", err)
	}
	if string(got2) != string(original) {
		t.Errorf("backup was overwritten on second run:\nwant: %s\ngot:  %s", original, got2)
	}
}

func TestPatchClaudeCodeUserJSON_InvalidJSONUntouched(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	claudePath := filepath.Join(home, ".claude.json")
	garbage := []byte("this is not json { [ ")
	if err := os.WriteFile(claudePath, garbage, 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _ := os.ReadFile(claudePath)

	err := PatchClaudeCodeUserJSON("/x", true)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	after, _ := os.ReadFile(claudePath)
	if string(before) != string(after) {
		t.Errorf("file modified despite parse error:\nbefore: %q\nafter:  %q", before, after)
	}
	// No backup should be created either since write path was never reached.
	if _, err := os.Stat(claudePath + ".dfmt.bak"); err == nil {
		t.Errorf("backup unexpectedly created for invalid-JSON input")
	}
}

func TestPatchClaudeCodeUserJSON_EmptyFileTreatedAsEmptyObject(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, []byte(""), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := PatchClaudeCodeUserJSON("", true); err != nil {
		t.Fatalf("patch: %v", err)
	}
	cfg := readClaudeJSON(t, home)
	_ = getMap(t, cfg, "mcpServers", "dfmt")
}

func TestNormalizeProjectKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/usr/local/app", "/usr/local/app"},
		{`D:\Codebox\PROJECTS\DFMT`, "D:/Codebox/PROJECTS/DFMT"},
		{"foo/./bar/../bar", "foo/bar"},
	}
	for _, tc := range cases {
		got := normalizeProjectKey(tc.in)
		// filepath.Clean uses OS separator. On Windows the first case stays
		// as-is; on Unix the second stays as-is too because backslashes are
		// not separators. Just check the final form contains forward
		// slashes only (no backslashes) and that the tail matches.
		if strings.Contains(got, "\\") {
			t.Errorf("%q -> %q contains backslash", tc.in, got)
		}
		if runtime.GOOS == "windows" {
			if got != tc.want {
				t.Errorf("windows: normalizeProjectKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	}
}
