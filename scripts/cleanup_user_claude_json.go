//go:build ignore

// One-shot cleanup: strip test-pollution entries from ~/.claude.json.
//
// Background: the cli package's TestDispatchInitWithExistingDfmt /
// TestRunInitCreatesClaudeSettings / TestRunInitWithExistingGitignore /
// TestDispatchInitWithGitignore tests called Dispatch("init", ...) without
// isolating HOME/USERPROFILE, so every CI run wrote project entries to the
// developer's real ~/.claude.json. TestMain in cli_test.go now isolates the
// home dir, but the historical leakage remains.
//
// Run once after pulling the TestMain fix:
//
//	go run scripts/cleanup_user_claude_json.go
//
// A timestamped backup is written next to the original before any mutation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fail("locate home: %v", err)
	}
	path := filepath.Join(home, ".claude.json")

	raw, err := os.ReadFile(path)
	if err != nil {
		fail("read %s: %v", path, err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fail("parse %s: %v", path, err)
	}

	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		fmt.Println("no projects map; nothing to clean")
		return
	}

	removed := []string{}
	for key := range projects {
		if isPollution(key) {
			delete(projects, key)
			removed = append(removed, key)
		}
	}

	if len(removed) == 0 {
		fmt.Println("nothing to remove")
		return
	}
	cfg["projects"] = projects

	// Marshal first so a marshalling failure cannot leave the file truncated.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fail("marshal: %v", err)
	}
	out = append(out, '\n')

	backupPath := fmt.Sprintf("%s.cleanup-%s.bak", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backupPath, raw, 0o600); err != nil {
		fail("write backup %s: %v", backupPath, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".claude.json.cleanup-*")
	if err != nil {
		fail("create temp: %v", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		fail("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		fail("close temp: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		fail("rename: %v", err)
	}

	sort.Strings(removed)
	fmt.Printf("backup: %s\n", backupPath)
	fmt.Printf("removed %d polluted project entries:\n", len(removed))
	for _, k := range removed {
		fmt.Printf("  - %s\n", k)
	}
}

// isPollution reports whether a `projects[<key>]` entry was created by a
// leaked test run rather than a real user project.
//
// The two original hard-coded test paths plus any tmp-dir entry created by
// Go's t.TempDir() under a Test* function name are dropped.
func isPollution(key string) bool {
	if key == "/proc/invalid/path" || key == "/tmp/test-dfmt-init" {
		return true
	}
	// Go's t.TempDir() always lands under <os.TempDir>/Test<TestName><n>/<NNN>.
	// On Windows that resolves to ...\AppData\Local\Temp\Test*, which Claude
	// stores with forward slashes.
	lower := strings.ToLower(key)
	if strings.Contains(lower, "/temp/test") {
		return true
	}
	// Linux/macOS variants in case future runs happen there.
	if strings.Contains(key, "/tmp/Test") || strings.Contains(key, "/var/folders/") && strings.Contains(key, "/Test") {
		return true
	}
	return false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
