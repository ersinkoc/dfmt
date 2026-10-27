package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/setup"
)

// checkInstructionBlockStaleness has one verdict per instruction file:
// unreadable, markers missing, drifted, or current. Every existing test
// fed it an empty or absent manifest, so none of the verdicts ran.
func TestCheckInstructionBlockStalenessVerdicts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HOME", filepath.Join(root, "home"))

	canonical := setup.ProjectBlockBodyForAgent(setup.AgentClaudeCode)
	if canonical == "" {
		t.Skip("no canonical block for claude-code")
	}
	current := filepath.Join(root, "current.md")
	drifted := filepath.Join(root, "drifted.md")
	noMarkers := filepath.Join(root, "plain.md")
	if err := setup.UpsertDFMTBlock(current, canonical); err != nil {
		t.Fatal(err)
	}
	if err := setup.UpsertDFMTBlock(drifted, "an old body\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noMarkers, []byte("# no dfmt block here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := func(p string) setup.FileEntry {
		return setup.FileEntry{Path: p, Agent: setup.AgentClaudeCode, Version: "v1", Kind: setup.FileKindStrip}
	}
	if err := setup.SaveManifest(&setup.Manifest{Version: 1, Files: []setup.FileEntry{
		entry(current), entry(drifted), entry(noMarkers),
	}}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, checkInstructionBlockStaleness)
	for _, want := range []string{"drifted.md — drift", "plain.md — markers missing"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "current.md") {
		t.Errorf("a current block was reported:\n%s", out)
	}

	// All current → the all-good line.
	if err := setup.SaveManifest(&setup.Manifest{Version: 1, Files: []setup.FileEntry{entry(current)}}); err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, checkInstructionBlockStaleness); !strings.Contains(out, "Instruction blocks current") {
		t.Errorf("all-current output: %q", out)
	}
}
