package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover(t *testing.T) {
	// Create temp project with .dfmt dir
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	got, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if got != tmpDir {
		t.Errorf("Discover(%q) = %q, want %q", tmpDir, got, tmpDir)
	}
}

func TestDiscoverGit(t *testing.T) {
	// Create temp project with .git dir
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	got, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if got != tmpDir {
		t.Errorf("Discover(%q) = %q, want %q", tmpDir, got, tmpDir)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	// Empty temp dir with no .dfmt or .git. Discover walks up to the
	// filesystem root; if any ancestor (e.g. the user's home dir) contains
	// a .dfmt or .git directory, it will be picked up and this test becomes
	// environment-dependent. Skip in that case instead of flaking.
	tmpDir := t.TempDir()

	_, err := Discover(tmpDir)
	if err == nil {
		t.Skip("ancestor directory contains .dfmt/.git; test not applicable in this environment")
	}
	if err != ErrNoProjectFound {
		t.Errorf("Discover() error = %v, want ErrNoProjectFound", err)
	}
}

func TestDiscoverWalksUp(t *testing.T) {
	// Create nested dirs with .dfmt at root
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Discover from subdirectory
	subDir := filepath.Join(tmpDir, "sub", "path")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	got, err := Discover(subDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if got != tmpDir {
		t.Errorf("Discover(%q) = %q, want %q (root)", subDir, got, tmpDir)
	}
}

func TestDiscoverEnvOverride(t *testing.T) {
	// Create two dirs
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Only tmpDir2 has .dfmt
	dfmtDir := filepath.Join(tmpDir2, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Set DFMT_PROJECT to tmpDir2
	os.Setenv("DFMT_PROJECT", tmpDir2)
	defer os.Unsetenv("DFMT_PROJECT")

	// Discover from tmpDir1 should return tmpDir2
	got, err := Discover(tmpDir1)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if got != tmpDir2 {
		t.Errorf("Discover with DFMT_PROJECT = %q, got %q, want %q", tmpDir2, got, tmpDir2)
	}
}

func TestID(t *testing.T) {
	id1 := ID("/some/path")
	if len(id1) != 8 {
		t.Errorf("ID() length = %d, want 8", len(id1))
	}

	id2 := ID("/some/path")
	if id1 != id2 {
		t.Errorf("ID() not deterministic: %q vs %q", id1, id2)
	}

	id3 := ID("/different/path")
	if id1 == id3 {
		t.Error("ID() should produce different IDs for different paths")
	}
}

func TestSocketPath(t *testing.T) {
	proj := "/some/project"
	path := SocketPath(proj)
	expected := filepath.Join(proj, ".dfmt", "daemon.sock")
	if path != expected {
		t.Errorf("SocketPath(%q) = %q, want %q", proj, path, expected)
	}
}

func TestNoProjectErrorInterface(t *testing.T) {
	err := &NoProjectError{}
	if err.Error() == "" {
		t.Error("NoProjectError.Error() returned empty string")
	}
	if err.Unwrap() != nil {
		t.Error("NoProjectError.Unwrap() should return nil")
	}
}

func TestUserRuntimeDir(t *testing.T) {
	dir := userRuntimeDir()
	if dir == "" {
		t.Error("userRuntimeDir returned empty string")
	}
}

func TestUserTag(t *testing.T) {
	tag := userTag()
	if tag == "" {
		t.Error("userTag returned empty string")
	}
	if len(tag) != 8 {
		t.Errorf("userTag length = %d, want 8", len(tag))
	}
}

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "testdir", "subdir")

	err := EnsureDir(dirPath)
	if err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(dirPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Error("Created path is not a directory")
	}
}

func TestEnsureDirExisting(t *testing.T) {
	tmpDir := t.TempDir()
	// Should not fail for existing dir
	err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir failed for existing dir: %v", err)
	}
}

func TestDaemonDir(t *testing.T) {
	proj := "/some/project"
	dd := DaemonDir(proj)
	expected := filepath.Join(proj, ".dfmt")
	if dd != expected {
		t.Errorf("DaemonDir(%q) = %q, want %q", proj, dd, expected)
	}
}

func TestConstants(t *testing.T) {
	if SocketName != "daemon.sock" {
		t.Errorf("SocketName = %q, want 'daemon.sock'", SocketName)
	}
	if PIDFileName != "daemon.pid" {
		t.Errorf("PIDFileName = %q, want 'daemon.pid'", PIDFileName)
	}
	if ConfigFileName != "config.yaml" {
		t.Errorf("ConfigFileName = %q, want 'config.yaml'", ConfigFileName)
	}
	if JournalFileName != "journal.jsonl" {
		t.Errorf("JournalFileName = %q, want 'journal.jsonl'", JournalFileName)
	}
	if IndexFileName != "index.gob" {
		t.Errorf("IndexFileName = %q, want 'index.gob'", IndexFileName)
	}
	if CursorFileName != "index.cursor" {
		t.Errorf("CursorFileName = %q, want 'index.cursor'", CursorFileName)
	}
}
