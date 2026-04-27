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
	wantCmd := ResolveDFMTCommand()
	if dfmt["command"] != wantCmd || dfmt["type"] != "stdio" {
		t.Errorf("mcp entry wrong: %#v (want command=%q type=stdio)", dfmt, wantCmd)
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
	wantCmd := ResolveDFMTCommand()
	if dfmt["command"] != wantCmd {
		t.Errorf("command = %v, want %q", dfmt["command"], wantCmd)
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

// On Windows the filesystem is case-insensitive but ~/.claude.json may end
// up with two case-variant keys for the same project. Patching must collapse
// them into the canonical key so PowerShell's case-insensitive JSON parser
// stops choking on the file.
func TestPatchClaudeCodeUserJSON_CollapsesWindowsCaseVariants(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-variant collapsing is Windows-only")
	}
	home := t.TempDir()
	setHome(t, home)

	projectPath := `D:\Codebox\PROJECTS\demo`
	canonical := normalizeProjectKey(projectPath) // "D:/Codebox/PROJECTS/demo"
	variant := strings.ToUpper(canonical[:2]) + strings.ReplaceAll(canonical[2:], "Codebox", "CODEBOX")
	if variant == canonical {
		t.Fatalf("variant key not distinct from canonical: %q", canonical)
	}

	initial := map[string]any{
		"projects": map[string]any{
			variant: map[string]any{
				"hasTrustDialogAccepted": true,
				"customKept":             "stay",
			},
			canonical: map[string]any{
				"customNew": "also-stay",
			},
		},
	}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, data, 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := PatchClaudeCodeUserJSON(projectPath, false); err != nil {
		t.Fatalf("patch: %v", err)
	}

	cfg := readClaudeJSON(t, home)
	projects := getMap(t, cfg, "projects")

	// Variant key must be gone.
	if _, ok := projects[variant]; ok {
		t.Errorf("variant key %q still present after patch", variant)
	}
	// Canonical key must hold the merged entry.
	merged, ok := projects[canonical].(map[string]any)
	if !ok {
		t.Fatalf("canonical key %q missing after patch", canonical)
	}
	if merged["customKept"] != "stay" {
		t.Errorf("variant-only field dropped: customKept = %v", merged["customKept"])
	}
	if merged["customNew"] != "also-stay" {
		t.Errorf("canonical-only field dropped: customNew = %v", merged["customNew"])
	}
	// Trust flags must be set.
	for _, flag := range []string{
		"hasTrustDialogAccepted",
		"hasClaudeMdExternalIncludesApproved",
		"hasClaudeMdExternalIncludesWarningShown",
	} {
		if v, _ := merged[flag].(bool); !v {
			t.Errorf("flag %s = %v, want true", flag, merged[flag])
		}
	}
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

// TestUnpatchClaudeCodeUserJSON_MissingFile verifies the uninstall path is
// a no-op when ~/.claude.json doesn't exist (fresh install of dfmt that
// never wrote the file).
func TestUnpatchClaudeCodeUserJSON_MissingFile(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	if err := UnpatchClaudeCodeUserJSON(); err != nil {
		t.Fatalf("UnpatchClaudeCodeUserJSON returned error on missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("UnpatchClaudeCodeUserJSON should not create the file; stat = %v", err)
	}
}

// TestUnpatchClaudeCodeUserJSON_RemovesUserScopeMCPOnly seeds a config
// with both top-level mcpServers.dfmt and unrelated keys, runs unpatch,
// and asserts dfmt is removed while everything else survives.
func TestUnpatchClaudeCodeUserJSON_RemovesUserScopeMCPOnly(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	initial := map[string]any{
		"mcpServers": map[string]any{
			"dfmt":      map[string]any{"command": "/old/path/dfmt"},
			"otherTool": map[string]any{"command": "/keep/me"},
		},
		"unrelatedKey": "preserved",
	}
	seed, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, seed, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UnpatchClaudeCodeUserJSON(); err != nil {
		t.Fatalf("unpatch: %v", err)
	}

	cfg := readClaudeJSON(t, home)
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers map removed entirely; expected otherTool to survive (got %#v)", cfg["mcpServers"])
	}
	if _, present := servers["dfmt"]; present {
		t.Error("mcpServers.dfmt was not removed")
	}
	if _, present := servers["otherTool"]; !present {
		t.Error("unrelated mcpServers.otherTool was incorrectly removed")
	}
	if cfg["unrelatedKey"] != "preserved" {
		t.Errorf("unrelated top-level key was modified: %v", cfg["unrelatedKey"])
	}
}

// TestUnpatchClaudeCodeUserJSON_RemovesProjectScopeMCPLeavesTrustFlags
// pins the deliberate behavior: project-scope mcpServers.dfmt is removed
// but the trust flags DFMT may have set are LEFT alone (we cannot tell
// whether the user accepted them independently of our setup).
func TestUnpatchClaudeCodeUserJSON_RemovesProjectScopeMCPLeavesTrustFlags(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	projectKey := "/some/project"
	initial := map[string]any{
		"projects": map[string]any{
			projectKey: map[string]any{
				"hasTrustDialogAccepted":                  true,
				"hasClaudeMdExternalIncludesApproved":     true,
				"hasClaudeMdExternalIncludesWarningShown": true,
				"customKey":                               "preserved",
				"mcpServers": map[string]any{
					"dfmt":      map[string]any{"command": "/old/dfmt"},
					"otherTool": map[string]any{"command": "/keep"},
				},
			},
		},
	}
	seed, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, seed, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UnpatchClaudeCodeUserJSON(); err != nil {
		t.Fatalf("unpatch: %v", err)
	}

	cfg := readClaudeJSON(t, home)
	projects := getMap(t, cfg, "projects")
	entry, ok := projects[projectKey].(map[string]any)
	if !ok {
		t.Fatalf("project entry removed entirely; want preserved")
	}

	// Trust flags must remain (deliberate; not our call to revoke).
	for _, flag := range []string{
		"hasTrustDialogAccepted",
		"hasClaudeMdExternalIncludesApproved",
		"hasClaudeMdExternalIncludesWarningShown",
	} {
		if v, _ := entry[flag].(bool); !v {
			t.Errorf("flag %s = %v, want true (must not be revoked on uninstall)", flag, entry[flag])
		}
	}
	// Custom key must remain.
	if entry["customKey"] != "preserved" {
		t.Errorf("custom key dropped: %v", entry["customKey"])
	}
	// dfmt entry gone, sibling kept.
	servers, ok := entry["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers map removed; expected otherTool to survive")
	}
	if _, present := servers["dfmt"]; present {
		t.Error("project mcpServers.dfmt was not removed")
	}
	if _, present := servers["otherTool"]; !present {
		t.Error("unrelated project mcpServers.otherTool was incorrectly removed")
	}
}

// TestUnpatchClaudeCodeUserJSON_PrunesEmptyMaps verifies that mcpServers
// becomes absent (not an empty `{}` stub) when the dfmt entry was its only
// member. Same for the per-project map.
func TestUnpatchClaudeCodeUserJSON_PrunesEmptyMaps(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	projectKey := "/some/project"
	initial := map[string]any{
		"mcpServers": map[string]any{
			"dfmt": map[string]any{"command": "/old/dfmt"},
		},
		"projects": map[string]any{
			projectKey: map[string]any{
				"customKey": "stays",
				"mcpServers": map[string]any{
					"dfmt": map[string]any{"command": "/old/dfmt"},
				},
			},
		},
	}
	seed, _ := json.Marshal(initial)
	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, seed, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UnpatchClaudeCodeUserJSON(); err != nil {
		t.Fatalf("unpatch: %v", err)
	}

	cfg := readClaudeJSON(t, home)
	if _, present := cfg["mcpServers"]; present {
		t.Error("top-level mcpServers stub was not pruned after dfmt removal")
	}
	projects := getMap(t, cfg, "projects")
	entry, ok := projects[projectKey].(map[string]any)
	if !ok {
		t.Fatalf("project entry was removed; expected customKey to survive")
	}
	if _, present := entry["mcpServers"]; present {
		t.Error("project mcpServers stub was not pruned after dfmt removal")
	}
	if entry["customKey"] != "stays" {
		t.Errorf("custom key dropped: %v", entry["customKey"])
	}
}

// TestUnpatchClaudeCodeUserJSON_NoChangeWhenAbsent confirms the file is
// not rewritten if it never contained dfmt entries — avoids touching
// mtime / file content needlessly on every uninstall attempt.
func TestUnpatchClaudeCodeUserJSON_NoChangeWhenAbsent(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	initial := []byte(`{"mcpServers":{"otherTool":{"command":"/x"}},"unrelated":1}` + "\n")
	claudePath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudePath, initial, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	beforeStat, err := os.Stat(claudePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := UnpatchClaudeCodeUserJSON(); err != nil {
		t.Fatalf("unpatch: %v", err)
	}

	afterStat, err := os.Stat(claudePath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	// File size and mtime must be unchanged when no dfmt entry was present.
	if beforeStat.Size() != afterStat.Size() {
		t.Errorf("file size changed despite no dfmt entries: before=%d after=%d", beforeStat.Size(), afterStat.Size())
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("file mtime changed despite no dfmt entries: before=%v after=%v", beforeStat.ModTime(), afterStat.ModTime())
	}
}
