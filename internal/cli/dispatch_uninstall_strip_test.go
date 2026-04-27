package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/setup"
)

// TestRunSetupUninstall_StripsInstructionFile pins the new uninstall
// flow: a manifest entry with Kind=FileKindStrip must trigger
// StripDFMTBlock (preserves user content, removes only our marker
// block) — NOT os.Remove (which would delete the user's whole
// CLAUDE.md). Without this routing, every uninstall would silently
// destroy the user's project notes.
func TestRunSetupUninstall_StripsInstructionFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifestDir := filepath.Join(tmpDir, "dfmt")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}

	// Build a project CLAUDE.md that mixes user content with a DFMT
	// block. After uninstall the user content must survive, the block
	// must be gone.
	projectDir := filepath.Join(tmpDir, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	claudeMdPath := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMdPath, []byte("# user notes\nimportant context\n"), 0o644); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}
	if err := setup.UpsertDFMTBlock(claudeMdPath, "dfmt routing rules\n"); err != nil {
		t.Fatalf("UpsertDFMTBlock: %v", err)
	}

	m := &setup.Manifest{
		Version: 1,
		Files: []setup.FileEntry{
			{Path: claudeMdPath, Agent: setup.AgentClaudeCode, Version: "v1", Kind: setup.FileKindStrip},
		},
	}
	if err := setup.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	prevProject := flagProject
	t.Cleanup(func() { flagProject = prevProject })
	flagProject = tmpDir

	if code := Dispatch([]string{"setup", "--uninstall"}); code != 0 {
		t.Fatalf("setup --uninstall returned %d, want 0", code)
	}

	got, err := os.ReadFile(claudeMdPath)
	if err != nil {
		t.Fatalf("CLAUDE.md missing after strip-uninstall — strip path treated it as delete: %v", err)
	}
	gs := string(got)
	if !strings.Contains(gs, "# user notes") {
		t.Errorf("user content lost after uninstall:\n%s", gs)
	}
	if !strings.Contains(gs, "important context") {
		t.Errorf("user context lost after uninstall:\n%s", gs)
	}
	if strings.Contains(gs, "dfmt routing rules") {
		t.Errorf("DFMT block survived strip-uninstall:\n%s", gs)
	}
	if strings.Contains(gs, "<!-- dfmt:v1") {
		t.Errorf("DFMT markers survived strip-uninstall:\n%s", gs)
	}
}

// TestRunSetupUninstall_StripDeletesEmptyFile: when DFMT was the sole
// resident of CLAUDE.md (init wrote it from scratch), strip-then-empty
// should remove the 0-byte file rather than leave it behind. Pins the
// StripDFMTBlock empty-file branch through the dispatch flow.
func TestRunSetupUninstall_StripDeletesEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifestDir := filepath.Join(tmpDir, "dfmt")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}

	projectDir := filepath.Join(tmpDir, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	claudeMdPath := filepath.Join(projectDir, "CLAUDE.md")
	if err := setup.UpsertDFMTBlock(claudeMdPath, "only dfmt content\n"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	m := &setup.Manifest{
		Version: 1,
		Files: []setup.FileEntry{
			{Path: claudeMdPath, Agent: setup.AgentClaudeCode, Version: "v1", Kind: setup.FileKindStrip},
		},
	}
	if err := setup.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	prevProject := flagProject
	t.Cleanup(func() { flagProject = prevProject })
	flagProject = tmpDir

	if code := Dispatch([]string{"setup", "--uninstall"}); code != 0 {
		t.Fatalf("setup --uninstall returned %d, want 0", code)
	}

	if _, err := os.Stat(claudeMdPath); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md should be removed (was 0-byte after strip); stat err = %v", err)
	}
}

// TestRunSetupUninstall_LegacyKindEmptyDeletes: backward compatibility.
// Manifests written before the Kind field existed have Kind=="" and
// must continue to use os.Remove + .dfmt.bak restore. A regression
// here would silently change uninstall semantics for existing
// installations.
func TestRunSetupUninstall_LegacyKindEmptyDeletes(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifestDir := filepath.Join(tmpDir, "dfmt")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}

	regularFile := filepath.Join(tmpDir, "regular.json")
	if err := os.WriteFile(regularFile, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}

	m := &setup.Manifest{
		Version: 1,
		Files: []setup.FileEntry{
			{Path: regularFile, Agent: "test", Version: "1"}, // Kind unset == legacy
		},
	}
	if err := setup.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	prevProject := flagProject
	t.Cleanup(func() { flagProject = prevProject })
	flagProject = tmpDir

	if code := Dispatch([]string{"setup", "--uninstall"}); code != 0 {
		t.Fatalf("setup --uninstall returned %d, want 0", code)
	}

	if _, err := os.Stat(regularFile); !os.IsNotExist(err) {
		t.Errorf("legacy-kind entry should be deleted; stat err = %v", err)
	}
}
