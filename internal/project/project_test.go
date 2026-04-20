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
	// Empty temp dir with no .dfmt or .git
	tmpDir := t.TempDir()

	_, err := Discover(tmpDir)
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

func TestNoProjectError(t *testing.T) {
	err := ErrNoProjectFound
	if err.Error() == "" {
		t.Error("ErrNoProjectFound should have an error message")
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

func TestRegistryNew(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(tmpDir)
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}
	if reg == nil {
		t.Fatal("NewRegistry returned nil")
	}
}

func TestRegistryAdd(t *testing.T) {
	tmpDir := t.TempDir()
	reg, _ := NewRegistry(tmpDir)

	entry := RegistryEntry{
		ID:       "test123",
		Path:     "/test/path",
		PID:      12345,
		Socket:   "/test/path/.dfmt/daemon.sock",
		LastSeen: 1234567890,
	}

	err := reg.Add(entry)
	if err != nil {
		t.Fatalf("Registry.Add failed: %v", err)
	}
}

func TestRegistryList(t *testing.T) {
	tmpDir := t.TempDir()
	reg, _ := NewRegistry(tmpDir)

	// List empty registry
	entries, err := reg.List()
	if err != nil {
		t.Fatalf("Registry.List failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List() = %d entries, want 0", len(entries))
	}

	// Add entry and list again
	entry := RegistryEntry{
		ID:       "test123",
		Path:     "/test/path",
		Socket:   "/test/path/.dfmt/daemon.sock",
		LastSeen: 1234567890,
	}
	reg.Add(entry)

	entries, err = reg.List()
	if err != nil {
		t.Fatalf("Registry.List after Add failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("List() = %d entries, want 1", len(entries))
	}
	if entries[0].ID != "test123" {
		t.Errorf("List()[0].ID = %q, want 'test123'", entries[0].ID)
	}
}

func TestRegistryGet(t *testing.T) {
	tmpDir := t.TempDir()
	reg, _ := NewRegistry(tmpDir)

	entry := RegistryEntry{
		ID:       "gettest",
		Path:     "/test/path",
		Socket:   "/test/path/.dfmt/daemon.sock",
		LastSeen: 1234567890,
	}
	reg.Add(entry)

	// Get existing
	got, err := reg.Get("gettest")
	if err != nil {
		t.Fatalf("Registry.Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("Registry.Get returned nil for existing entry")
	}
	if got.ID != "gettest" {
		t.Errorf("got.ID = %q, want 'gettest'", got.ID)
	}

	// Get nonexistent
	got, err = reg.Get("nonexistent")
	if err != nil {
		t.Fatalf("Registry.Get failed for nonexistent: %v", err)
	}
	if got != nil {
		t.Error("Registry.Get should return nil for nonexistent ID")
	}
}

func TestRegistryRemove(t *testing.T) {
	tmpDir := t.TempDir()
	reg, _ := NewRegistry(tmpDir)

	entry := RegistryEntry{
		ID:       "removetest",
		Path:     "/test/path",
		Socket:   "/test/path/.dfmt/daemon.sock",
		LastSeen: 1234567890,
	}
	reg.Add(entry)

	// Remove existing
	err := reg.Remove("removetest")
	if err != nil {
		t.Fatalf("Registry.Remove failed: %v", err)
	}

	// Verify removed
	got, _ := reg.Get("removetest")
	if got != nil {
		t.Error("Entry still exists after Remove")
	}

	// Remove nonexistent should not error
	err = reg.Remove("nonexistent")
	if err != nil {
		t.Fatalf("Registry.Remove failed for nonexistent: %v", err)
	}
}

func TestRegistryUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	reg, _ := NewRegistry(tmpDir)

	entry1 := RegistryEntry{
		ID:       "updatetest",
		Path:     "/test/path1",
		PID:      100,
		Socket:   "/test/path1/.dfmt/daemon.sock",
		LastSeen: 1000,
	}
	reg.Add(entry1)

	entry2 := RegistryEntry{
		ID:       "updatetest",
		Path:     "/test/path2",
		PID:      200,
		Socket:   "/test/path2/.dfmt/daemon.sock",
		LastSeen: 2000,
	}
	reg.Add(entry2)

	// Should have only one entry with updated values
	got, _ := reg.Get("updatetest")
	if got.PID != 200 {
		t.Errorf("got.PID = %d, want 200", got.PID)
	}
	if got.LastSeen != 2000 {
		t.Errorf("got.LastSeen = %d, want 2000", got.LastSeen)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	reg, _ := NewRegistry(tmpDir)

	// Add entries concurrently
	done := make(chan bool)
	for i := 0; i < 5; i++ {
		go func(id int) {
			entry := RegistryEntry{
				ID:       "concurrent",
				Path:     "/test/path",
				Socket:   "/test/path/.dfmt/daemon.sock",
				LastSeen: int64(id),
			}
			reg.Add(entry)
			done <- true
		}(i)
	}

	for i := 0; i < 5; i++ {
		<-done
	}
}
