package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeJSON marshals v and writes it to path. Used to seed test fixtures.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v\nraw: %s", path, err, data)
	}
	return m
}

// TestCleanLegacyClaudeSettings_MissingFile is the no-op contract: a
// settings.json that does not exist must produce nil/nil so callers can
// invoke the cleaner unconditionally without first stat-checking.
func TestCleanLegacyClaudeSettings_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	entries, err := CleanLegacyClaudeSettings(path, "/usr/local/bin/dfmt")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries=%v, want empty", entries)
	}
}

// TestCleanLegacyClaudeSettings_EmptyFile mirrors the missing-file case:
// a 0-byte / whitespace-only file is parsed as an empty object, no
// fossils, no error.
func TestCleanLegacyClaudeSettings_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	entries, err := CleanLegacyClaudeSettings(path, "/usr/local/bin/dfmt")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries=%v, want empty", entries)
	}
}

// TestPurgeLegacy_DenyTriplet pins the v0.3.1 migration: a settings.json
// whose permissions.deny contains the legacy triplet must come back with
// the triplet stripped, a backup written, and report.Removed populated.
func TestPurgeLegacy_DenyTriplet(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"permissions": map[string]any{
			"deny": []any{"Bash", "WebFetch", "WebSearch"},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "/usr/local/bin/dfmt")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0].Kind != LegacyDenyTriplet {
		t.Errorf("Removed=%v, want one LegacyDenyTriplet", report.Removed)
	}
	if report.Backup == "" {
		t.Error("Backup path empty; expected one-shot .dfmt.bak written")
	}

	cfg := readJSON(t, path)
	perms, _ := cfg["permissions"].(map[string]any)
	if perms != nil {
		if _, present := perms["deny"]; present {
			t.Errorf("permissions.deny still present after purge: %v", perms["deny"])
		}
	}
}

// TestPurgeLegacy_DenyTripletPreservesUserExtras: if the user added an
// extra deny entry alongside the triplet, the triplet is removed but
// the user's entry survives.
func TestPurgeLegacy_DenyTripletPreservesUserExtras(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"permissions": map[string]any{
			"deny": []any{"Bash", "WebFetch", "WebSearch", "MyCustomTool"},
		},
	})

	if _, err := PurgeLegacyClaudeSettings(path, ""); err != nil {
		t.Fatalf("purge: %v", err)
	}

	cfg := readJSON(t, path)
	deny, _ := cfg["permissions"].(map[string]any)["deny"].([]any)
	if len(deny) != 1 || deny[0] != "MyCustomTool" {
		t.Errorf("deny=%v, want [MyCustomTool]", deny)
	}
}

// TestPurgeLegacy_DenyTripletNotApplied: a deny list missing one of the
// triplet names is treated as user-owned and left alone.
func TestPurgeLegacy_DenyTripletNotApplied(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"permissions": map[string]any{
			"deny": []any{"Bash", "WebFetch"}, // no WebSearch
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Errorf("Removed=%v, want empty", report.Removed)
	}

	cfg := readJSON(t, path)
	deny, _ := cfg["permissions"].(map[string]any)["deny"].([]any)
	if len(deny) != 2 {
		t.Errorf("deny=%v, want preserved (Bash, WebFetch)", deny)
	}
}

// TestPurgeLegacy_DottedMCPPerms strips the dotted-name MCP permission
// strings the v0.2.0 setup wrote (mcp__dfmt__dfmt.exec etc.).
func TestPurgeLegacy_DottedMCPPerms(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"permissions": map[string]any{
			"allow": []any{
				"mcp__dfmt__dfmt.exec", // legacy dotted -- strip
				"mcp__dfmt__dfmt.read", // legacy dotted -- strip
				"mcp__dfmt__dfmt_exec", // current underscore -- keep
				"Bash(git status)",     // unrelated user entry -- keep
			},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "/usr/local/bin/dfmt")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0].Kind != LegacyDottedMCP {
		t.Errorf("Removed=%v, want one LegacyDottedMCP", report.Removed)
	}

	cfg := readJSON(t, path)
	allow, _ := cfg["permissions"].(map[string]any)["allow"].([]any)
	for _, v := range allow {
		s, _ := v.(string)
		if s == "mcp__dfmt__dfmt.exec" || s == "mcp__dfmt__dfmt.read" {
			t.Errorf("dotted MCP perm %q survived purge", s)
		}
	}
	if len(allow) != 2 {
		t.Errorf("allow=%v, want length 2 (current MCP + user Bash)", allow)
	}
}

// TestPurgeLegacy_HookCmdDrift rewrites a PreToolUse hook whose command
// points at an old binary path to the canonical resolved form.
func TestPurgeLegacy_HookCmdDrift(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": strings.Join(CurrentPreToolUseMatchers, "|"),
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/old/path/dfmt hook claude-code pretooluse",
							"timeout": 5,
						},
					},
				},
			},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "/new/path/dfmt")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Adjusted) != 1 || report.Adjusted[0].Kind != LegacyHookCmdDrift {
		t.Fatalf("Adjusted=%v, want one LegacyHookCmdDrift", report.Adjusted)
	}

	cfg := readJSON(t, path)
	hooks := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	hc := hooks[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	want := "/new/path/dfmt hook claude-code pretooluse"
	if hc["command"].(string) != want {
		t.Errorf("command=%q, want %q", hc["command"], want)
	}
}

// TestPurgeLegacy_BareDfmtCmdNotDrifted: when the command is the
// PATH-resolved bare form ("dfmt hook claude-code pretooluse"), the
// purger must NOT rewrite it -- this is a valid form too.
func TestPurgeLegacy_BareDfmtCmdNotDrifted(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": strings.Join(CurrentPreToolUseMatchers, "|"),
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "dfmt hook claude-code pretooluse",
							"timeout": 5,
						},
					},
				},
			},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "/new/path/dfmt")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Adjusted) != 0 || len(report.Removed) != 0 {
		t.Errorf("expected no changes, got Adjusted=%v Removed=%v", report.Adjusted, report.Removed)
	}
}

// TestPurgeLegacy_MatcherSubsetExpanded promotes a narrow matcher
// (Bash only) to the full current set when the command is dfmt-shaped.
func TestPurgeLegacy_MatcherSubsetExpanded(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "dfmt hook claude-code pretooluse",
							"timeout": 5,
						},
					},
				},
			},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Adjusted) == 0 {
		t.Fatalf("expected matcher subset adjustment, got %v", report)
	}

	cfg := readJSON(t, path)
	got := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["matcher"].(string)
	want := strings.Join(CurrentPreToolUseMatchers, "|")
	if got != want {
		t.Errorf("matcher=%q, want %q", got, want)
	}
}

// TestPurgeLegacy_MatcherSubsetPreservesUserExtras: when the user added
// a custom tool name to the matcher, the expansion preserves it.
func TestPurgeLegacy_MatcherSubsetPreservesUserExtras(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash|MyCustomTool",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "dfmt hook claude-code pretooluse",
							"timeout": 5,
						},
					},
				},
			},
		},
	})

	if _, err := PurgeLegacyClaudeSettings(path, ""); err != nil {
		t.Fatalf("purge: %v", err)
	}

	cfg := readJSON(t, path)
	got := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["matcher"].(string)
	if !strings.Contains(got, "MyCustomTool") {
		t.Errorf("matcher=%q lost user-added MyCustomTool", got)
	}
	for _, want := range CurrentPreToolUseMatchers {
		if !strings.Contains(got, want) {
			t.Errorf("matcher=%q missing %q", got, want)
		}
	}
}

// TestPurgeLegacy_NonDfmtHookUntouched: a hook whose command is NOT
// dfmt-shaped must never be rewritten, even if its matcher is narrow.
func TestPurgeLegacy_NonDfmtHookUntouched(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/usr/local/bin/my-tool intercept",
							"timeout": 5,
						},
					},
				},
			},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "/new/path/dfmt")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Adjusted) != 0 || len(report.Removed) != 0 {
		t.Errorf("non-dfmt hook touched: %v", report)
	}

	cfg := readJSON(t, path)
	got := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["matcher"].(string)
	if got != "Bash" {
		t.Errorf("matcher=%q, want Bash unchanged", got)
	}
}

// TestPurgeLegacy_Idempotent: running the purger twice on the same file
// produces no changes the second time.
func TestPurgeLegacy_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"permissions": map[string]any{
			"deny":  []any{"Bash", "WebFetch", "WebSearch"},
			"allow": []any{"mcp__dfmt__dfmt.exec"},
		},
	})

	report1, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge 1: %v", err)
	}
	if len(report1.Removed) == 0 {
		t.Fatal("first purge made no changes")
	}

	report2, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge 2: %v", err)
	}
	if len(report2.Removed) != 0 || len(report2.Adjusted) != 0 {
		t.Errorf("second purge made changes: %v", report2)
	}
	if report2.Backup != "" {
		t.Errorf("second purge wrote a new backup: %q", report2.Backup)
	}
}
