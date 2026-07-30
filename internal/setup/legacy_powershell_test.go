package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// psHookBody is the exact SessionStart command dfmt wrote on Windows
// through v0.7.2. Claude Code runs hook bodies through bash on every
// platform, so this is a bash syntax error and the hook fails on every
// session start:
//
//	SessionStart:startup hook error [if (Test-Path .dfmt/last-recall.md) ...]:
//	/usr/bin/bash: -c: line 1: syntax error near unexpected token `('
const psHookBody = `if (Test-Path .dfmt/last-recall.md) { Write-Host '--- Previous session summary ---'; ` +
	`Get-Content .dfmt/last-recall.md; Write-Host '--- End of previous session ---' }`

// posixHookBody is the replacement, valid in bash on all platforms.
const posixHookBody = `if [ -f .dfmt/last-recall.md ]; then echo '--- Previous session summary ---' && ` +
	`cat .dfmt/last-recall.md && echo '--- End of previous session ---'; fi`

func sessionStartCommands(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	hooks, _ := cfg["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	var cmds []string
	for _, g := range groups {
		grp, _ := g.(map[string]any)
		inner, _ := grp["hooks"].([]any)
		for _, h := range inner {
			hc, _ := h.(map[string]any)
			cmd, _ := hc["command"].(string)
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// TestPurgeLegacy_PowerShellSessionStartRemoved is the regression for the
// reported failure: the PowerShell body must be purged outright, and the
// now-empty SessionStart event must not be left behind as scaffolding.
func TestPurgeLegacy_PowerShellSessionStartRemoved(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": psHookBody,
							"timeout": 10,
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
	if len(report.Removed) != 1 || report.Removed[0].Kind != LegacyPowerShellHook {
		t.Fatalf("Removed=%+v, want one LegacyPowerShellHook", report.Removed)
	}
	if report.Removed[0].Location != "hooks.SessionStart[0].hooks[0]" {
		t.Errorf("Location=%q", report.Removed[0].Location)
	}

	cfg := readJSON(t, path)
	if cmds := sessionStartCommands(t, cfg); len(cmds) != 0 {
		t.Errorf("SessionStart still has commands: %q", cmds)
	}
	// A group emptied of hooks must be dropped, and an event emptied of
	// groups removed -- otherwise every upgrade leaves more dead scaffolding.
	if hooks, ok := cfg["hooks"].(map[string]any); ok {
		if _, present := hooks["SessionStart"]; present {
			t.Errorf("empty SessionStart event left in place: %v", hooks)
		}
	}
}

// TestPurgeLegacy_PowerShellHookStackedWithPosix covers the observed
// upgrade path: mergeClaudeHook dedupes on the exact command string, so a
// Windows install that ran an old dfmt and then a new one ends up with
// BOTH bodies registered and keeps erroring. The purge must drop only the
// PowerShell one.
func TestPurgeLegacy_PowerShellHookStackedWithPosix(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": psHookBody, "timeout": 10},
					},
				},
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": posixHookBody, "timeout": 10},
					},
				},
			},
		},
	})

	if _, err := PurgeLegacyClaudeSettings(path, ""); err != nil {
		t.Fatalf("purge: %v", err)
	}

	cmds := sessionStartCommands(t, readJSON(t, path))
	if len(cmds) != 1 {
		t.Fatalf("SessionStart commands=%q, want exactly the POSIX one", cmds)
	}
	if cmds[0] != posixHookBody {
		t.Errorf("surviving command=%q, want POSIX body", cmds[0])
	}
}

// TestPurgeLegacy_PowerShellHookKeepsSiblings: when the fossil shares a
// group with a hook the user added, only the fossil goes and the group
// survives.
func TestPurgeLegacy_PowerShellHookKeepsSiblings(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": psHookBody, "timeout": 10},
						map[string]any{"type": "command", "command": "echo hello", "timeout": 5},
					},
				},
			},
		},
	})

	if _, err := PurgeLegacyClaudeSettings(path, ""); err != nil {
		t.Fatalf("purge: %v", err)
	}

	cmds := sessionStartCommands(t, readJSON(t, path))
	if len(cmds) != 1 || cmds[0] != "echo hello" {
		t.Errorf("commands=%q, want [echo hello]", cmds)
	}
}

// TestPurgeLegacy_UnrelatedTestPathHookUntouched: the detector is anchored
// on dfmt's own last-recall.md artifact, so a user hook that merely calls
// Test-Path is not dfmt's business to delete.
func TestPurgeLegacy_UnrelatedTestPathHookUntouched(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	userHook := `if (Test-Path build/output.log) { Get-Content build/output.log }`
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks":   []any{map[string]any{"type": "command", "command": userHook}},
				},
			},
		},
	})

	report, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Errorf("user hook removed: %+v", report.Removed)
	}
	if cmds := sessionStartCommands(t, readJSON(t, path)); len(cmds) != 1 || cmds[0] != userHook {
		t.Errorf("commands=%q, want the user hook intact", cmds)
	}
}

// TestPurgeLegacy_PowerShellHookIdempotent: a second purge is a no-op.
func TestPurgeLegacy_PowerShellHookIdempotent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	writeJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks":   []any{map[string]any{"type": "command", "command": psHookBody}},
				},
			},
		},
	})

	first, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge 1: %v", err)
	}
	if len(first.Removed) == 0 {
		t.Fatal("first purge made no changes")
	}
	second, err := PurgeLegacyClaudeSettings(path, "")
	if err != nil {
		t.Fatalf("purge 2: %v", err)
	}
	if len(second.Removed) != 0 || len(second.Adjusted) != 0 {
		t.Errorf("second purge made changes: %+v", second)
	}
}

// TestWriteProjectClaudeSettings_SessionStartIsPOSIX pins the writer: the
// emitted body must be POSIX on every platform (this test runs on Windows
// too), because Claude Code executes hooks through bash everywhere.
func TestWriteProjectClaudeSettings_SessionStartIsPOSIX(t *testing.T) {
	tmp := t.TempDir()
	if err := writeProjectClaudeSettings(tmp); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := readJSON(t, filepath.Join(tmp, ".claude", "settings.json"))
	cmds := sessionStartCommands(t, cfg)
	if len(cmds) != 1 {
		t.Fatalf("SessionStart commands=%q, want exactly one", cmds)
	}
	if cmds[0] != posixHookBody {
		t.Errorf("SessionStart=%q\nwant POSIX body %q", cmds[0], posixHookBody)
	}
	if strings.Contains(cmds[0], "Test-Path") || strings.Contains(cmds[0], "Write-Host") {
		t.Errorf("PowerShell syntax emitted: %q", cmds[0])
	}
}

// TestWriteProjectClaudeSettings_ReplacesPowerShellHook is the upgrade
// contract. mergeClaudeHook is keyed on the exact command string, so
// without an explicit purge the old body survives beside the new one and
// the session-start error survives the upgrade too.
func TestWriteProjectClaudeSettings_ReplacesPowerShellHook(t *testing.T) {
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{
							"type":          "command",
							"command":       psHookBody,
							"timeout":       10,
							"statusMessage": "Loading previous session summary...",
						},
					},
				},
			},
		},
	})

	if err := writeProjectClaudeSettings(tmp); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "Test-Path") {
		t.Errorf("PowerShell hook survived the upgrade:\n%s", raw)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmds := sessionStartCommands(t, cfg)
	if len(cmds) != 1 || cmds[0] != posixHookBody {
		t.Errorf("SessionStart commands=%q, want exactly the POSIX body", cmds)
	}
}
