package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMergeMCPServerEntryPreservesForeignEntries is the V-04 regression: the
// pre-fix writer overwrote the file outright with `{"mcpServers":{"dfmt":...}}`
// and silently dropped any other MCP servers (playwright, context7, …) the
// user had configured for that agent. The merge-aware writer must preserve
// them.
func TestMergeMCPServerEntryPreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "mcp.json")

	original := map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{
				"command": "/usr/local/bin/playwright-mcp",
				"args":    []any{"--port", "9999"},
			},
			"context7": map[string]any{
				"command": "/usr/local/bin/context7-mcp",
			},
		},
		"unrelated_top_level_key": "must-survive",
	}
	originalBytes, _ := json.MarshalIndent(original, "", "  ")
	if err := os.WriteFile(mcpPath, originalBytes, 0o600); err != nil {
		t.Fatalf("seed mcp.json: %v", err)
	}

	if err := MergeMCPServerEntry(mcpPath, TargetOSUnix); err != nil {
		t.Fatalf("MergeMCPServerEntry: %v", err)
	}

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("parse merged file: %v", err)
	}

	servers, ok := merged["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or wrong type: %v", merged["mcpServers"])
	}
	for _, want := range []string{"playwright", "context7", "dfmt"} {
		if _, present := servers[want]; !present {
			t.Errorf("foreign or new entry %q missing from mcpServers; got keys %v", want, mapKeys(servers))
		}
	}
	if got := merged["unrelated_top_level_key"]; got != "must-survive" {
		t.Errorf("unrelated_top_level_key = %v; want \"must-survive\"", got)
	}

	// Pristine backup must exist exactly once and contain the original bytes.
	backup, err := os.ReadFile(mcpPath + ".dfmt.bak")
	if err != nil {
		t.Fatalf("backup .dfmt.bak: %v", err)
	}
	if string(backup) != string(originalBytes) {
		t.Errorf("backup contents diverged from original")
	}
}

// TestMergeMCPServerEntryCreatesFileWhenAbsent covers the cold-start path —
// the bug was specifically about overwriting EXISTING files, but the helper
// must still produce a valid config when called on a missing path.
func TestMergeMCPServerEntryCreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "mcp.json")

	if err := MergeMCPServerEntry(mcpPath, TargetOSUnix); err != nil {
		t.Fatalf("MergeMCPServerEntry: %v", err)
	}

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse created file: %v", err)
	}
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing")
	}
	if _, present := servers["dfmt"]; !present {
		t.Errorf("dfmt entry missing from cold-start file")
	}

	// No backup should be created when there was nothing to back up.
	if _, err := os.Stat(mcpPath + ".dfmt.bak"); !os.IsNotExist(err) {
		t.Errorf("unexpected backup created on cold start: err=%v", err)
	}
}

// TestUnmergeMCPServerEntryPreservesForeignEntries covers the uninstall side:
// removing only the dfmt entry must leave any other MCP server intact.
func TestUnmergeMCPServerEntryPreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "mcp.json")

	seed := map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{"command": "/x"},
			"dfmt":       map[string]any{"command": "/y", "args": []any{"mcp"}},
		},
		"foo": "bar",
	}
	seedBytes, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(mcpPath, seedBytes, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UnmergeMCPServerEntry(mcpPath); err != nil {
		t.Fatalf("UnmergeMCPServerEntry: %v", err)
	}

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if _, present := servers["dfmt"]; present {
		t.Errorf("dfmt entry still present after unmerge")
	}
	if _, present := servers["playwright"]; !present {
		t.Errorf("foreign entry playwright dropped during unmerge")
	}
	if cfg["foo"] != "bar" {
		t.Errorf("unrelated key 'foo' lost during unmerge")
	}
}

// TestUnmergeMCPServerEntryDeletesEmptyFile checks that we don't leave an
// empty `{}` (or `{"mcpServers":{}}`) shell on disk after the last entry
// goes away.
func TestUnmergeMCPServerEntryDeletesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "mcp.json")

	seed := map[string]any{
		"mcpServers": map[string]any{
			"dfmt": map[string]any{"command": "/y", "args": []any{"mcp"}},
		},
	}
	seedBytes, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(mcpPath, seedBytes, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UnmergeMCPServerEntry(mcpPath); err != nil {
		t.Fatalf("UnmergeMCPServerEntry: %v", err)
	}
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Errorf("expected file deletion when last entry removed; stat err=%v", err)
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
