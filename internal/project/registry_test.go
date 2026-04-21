package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewRegistry(t *testing.T) {
	tmpDir := t.TempDir()

	reg, err := NewRegistry(tmpDir)
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}
	if reg == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if reg.path == "" {
		t.Error("Registry path should not be empty")
	}
}

func TestNewRegistryCreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "subdir", "nested")

	reg, err := NewRegistry(nestedDir)
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}
	if reg == nil {
		t.Fatal("NewRegistry returned nil")
	}

	// Verify directory was created
	if _, err := os.Stat(nestedDir); os.IsNotExist(err) {
		t.Error("Directory should have been created")
	}
}

func TestNewRegistryInvalidPath(t *testing.T) {
	// Test with a path that cannot be created (on Unix this would be /proc or similar)
	// On Windows, we might use a reserved name
	_, err := NewRegistry("/invalid:/path/that/cannot/be/created")
	if err == nil {
		t.Error("NewRegistry should fail for invalid path")
	}
}

func TestRegistryAddAndList(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entry := RegistryEntry{
		ID:       "test-id",
		Path:     "/test/path",
		PID:      12345,
		Socket:   "/tmp/test.sock",
		LastSeen: 1234567890,
	}

	if err := reg.Add(entry); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	entries, err := reg.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("List returned %d entries, want 1", len(entries))
	}
}

func TestRegistryAddUpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entry1 := RegistryEntry{
		ID:       "test-id",
		Path:     "/test/path1",
		LastSeen: 1000,
	}
	reg.Add(entry1)

	entry2 := RegistryEntry{
		ID:       "test-id",
		Path:     "/test/path2",
		LastSeen: 2000,
	}
	reg.Add(entry2)

	entries, _ := reg.List()
	if len(entries) != 1 {
		t.Errorf("List returned %d entries, want 1 (updated)", len(entries))
	}
	if entries[0].Path != "/test/path2" {
		t.Errorf("Path was not updated, got %s", entries[0].Path)
	}
}

func TestRegistryRemoveEntry(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entry := RegistryEntry{
		ID:       "test-id",
		Path:     "/test/path",
		LastSeen: 1234567890,
	}
	reg.Add(entry)

	if err := reg.Remove("test-id"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	entries, _ := reg.List()
	if len(entries) != 0 {
		t.Errorf("List after remove returned %d entries, want 0", len(entries))
	}
}

func TestRegistryGetEntry(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entry := RegistryEntry{
		ID:       "test-id",
		Path:     "/test/path",
		LastSeen: 1234567890,
	}
	reg.Add(entry)

	found, err := reg.Get("test-id")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found == nil {
		t.Fatal("Get returned nil for existing entry")
	}
	if found.ID != "test-id" {
		t.Errorf("ID = %s, want test-id", found.ID)
	}
}

func TestRegistryGetNotFoundEntry(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	found, err := reg.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found != nil {
		t.Error("Get for nonexistent should return nil")
	}
}

func TestRegistryListEmpty(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entries, err := reg.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if entries != nil && len(entries) != 0 {
		t.Errorf("List for empty registry returned %d entries, want 0", len(entries))
	}
}

func TestWriteAllAndReadAll(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entries := []RegistryEntry{
		{ID: "id1", Path: "/path1", LastSeen: 1000},
		{ID: "id2", Path: "/path2", LastSeen: 2000},
	}

	if err := reg.writeAll(entries); err != nil {
		t.Fatalf("writeAll failed: %v", err)
	}

	readEntries, err := reg.readAll()
	if err != nil {
		t.Fatalf("readAll failed: %v", err)
	}
	if len(readEntries) != 2 {
		t.Errorf("readAll returned %d entries, want 2", len(readEntries))
	}
}

func TestWriteAllCreatesFile(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entries := []RegistryEntry{
		{ID: "id1", Path: "/path1", LastSeen: 1000},
	}

	reg.writeAll(entries)

	// Verify file was created
	if _, err := os.Stat(reg.path); os.IsNotExist(err) {
		t.Error("Registry file should have been created")
	}
}

func TestReadAllNonExistent(t *testing.T) {
	tmpDir := t.TempDir()

	reg, _ := NewRegistry(tmpDir)

	entries, err := reg.readAll()
	if err != nil {
		t.Fatalf("readAll failed: %v", err)
	}
	// Non-existent file should return nil entries
	if entries != nil && len(entries) != 0 {
		t.Errorf("readAll for non-existent file returned %d entries, want 0", len(entries))
	}
}
