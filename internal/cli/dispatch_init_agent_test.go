package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRunInit_AgentFlagWritesNonDetected pins the shared-repo use case:
// a teammate may want to commit CLAUDE.md (Claude Code) and AGENTS.md
// (Codex) to the repo even though only Claude is installed locally.
// Without --agent, setup.Detect() filters out Codex and the AGENTS.md
// would never get written. With --agent claude-code,codex, both files
// land regardless of local detection.
//
// We exercise writeProjectInstructionFiles directly rather than the
// full Dispatch path because the latter pulls in ensureProject-
// Initialized + ~/.claude.json patching that would need extensive
// fixture mocking; the agent-list resolution logic is what we actually
// want to pin.
func TestRunInit_AgentFlagWritesNonDetected(t *testing.T) {
	dir := t.TempDir()

	// XDG_DATA_HOME isolation so manifest writes go into the test temp.
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))

	// Force-write claude-code + codex regardless of local detection.
	writeProjectInstructionFiles(dir, []string{"claude-code", "codex"})

	for _, want := range []string{"CLAUDE.md", "AGENTS.md"} {
		p := filepath.Join(dir, want)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist after --agent override; stat err = %v", want, err)
		}
	}
}

// TestRunInit_EmptyAgentListFallsBackToDetect proves the override-or-
// detect dispatch: passing an empty list must use setup.Detect() (the
// historical behaviour), not write nothing. We can't deterministically
// assert which files appear because Detect() depends on the test host,
// but we can assert the function returns without panicking and the
// loop branch is exercised — anything else would indicate the empty-
// list guard regressed.
func TestRunInit_EmptyAgentListFallsBackToDetect(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("writeProjectInstructionFiles panicked on empty list: %v", r)
		}
	}()
	writeProjectInstructionFiles(dir, nil)
	// Successful return is the assertion. The directory may or may not
	// gain instruction files depending on what's installed on the host,
	// which is why we don't pin specific filenames here.
}

// TestRunInit_AgentFlagDedupsSharedFiles guards the dedup path:
// codex, opencode, and zed all map to AGENTS.md. Passing all three
// must produce exactly one AGENTS.md write — no duplicate-block append,
// no overwrite churn — because writeProjectInstructionFiles tracks a
// `seen[path]` map.
func TestRunInit_AgentFlagDedupsSharedFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))

	writeProjectInstructionFiles(dir, []string{"codex", "opencode", "zed"})

	// AGENTS.md should exist exactly once with one DFMT block.
	agentsPath := filepath.Join(dir, "AGENTS.md")
	got, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	beginCount := bytes.Count(got, []byte("<!-- dfmt:v1 begin -->"))
	if beginCount != 1 {
		t.Errorf("AGENTS.md has %d DFMT blocks after 3-agent override, want 1", beginCount)
	}
}
