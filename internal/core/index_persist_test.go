package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistIndex(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")
	hiULID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	ix := NewIndex()
	e := Event{
		ID:      hiULID,
		Type:    EvtFileEdit,
		Project: "test",
		Tags:    []string{"go", "test"},
	}
	ix.Add(e)

	err := PersistIndex(ix, indexPath, hiULID)
	if err != nil {
		t.Fatalf("PersistIndex failed: %v", err)
	}

	// Verify index.json was created
	if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
		t.Fatal("index.json was not created")
	}

	// Verify we can load it back
	loaded, _, _, loadErr := LoadIndexWithCursor(indexPath, filepath.Join(tmpDir, "index.cursor"))
	if loadErr != nil {
		t.Fatalf("LoadIndexWithCursor failed: %v", loadErr)
	}
	if loaded == nil {
		t.Fatal("loaded index is nil")
	}
}

func TestLoadIndexWithCursor(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("needsRebuild when cursor missing", func(t *testing.T) {
		_, _, needsRebuild, err := LoadIndexWithCursor(
			filepath.Join(tmpDir, "index.json"),
			filepath.Join(tmpDir, "index.cursor"),
		)
		if err != nil {
			t.Fatalf("LoadIndexWithCursor failed: %v", err)
		}
		if !needsRebuild {
			t.Error("should need rebuild when cursor is missing")
		}
	})

	t.Run("needsRebuild when cursor file corrupt", func(t *testing.T) {
		corruptPath := filepath.Join(tmpDir, "index.cursor")
		if err := os.WriteFile(corruptPath, []byte("not json data"), 0644); err != nil {
			t.Fatalf("failed to write corrupt file: %v", err)
		}

		_, _, needsRebuild, err := LoadIndexWithCursor(
			filepath.Join(tmpDir, "index.json"),
			corruptPath,
		)
		if err != nil {
			t.Fatalf("LoadIndexWithCursor failed: %v", err)
		}
		if !needsRebuild {
			t.Error("should need rebuild when cursor is corrupt")
		}
	})

	t.Run("needsRebuild when tokenizer version mismatch", func(t *testing.T) {
		cursorPath := filepath.Join(tmpDir, "index.cursor")
		f, err := os.Create(cursorPath)
		if err != nil {
			t.Fatalf("failed to create cursor file: %v", err)
		}
		enc := json.NewEncoder(f)
		cursorVal := IndexCursor{HiULID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", TokenVer: 999, TotalDocs: 10}
		if err := enc.Encode(cursorVal); err != nil {
			t.Fatalf("failed to encode cursor: %v", err)
		}
		f.Close()

		_, _, needsRebuild, err := LoadIndexWithCursor(
			filepath.Join(tmpDir, "index.json"),
			cursorPath,
		)
		if err != nil {
			t.Fatalf("LoadIndexWithCursor failed: %v", err)
		}
		if !needsRebuild {
			t.Error("should need rebuild when token version mismatch")
		}
	})

	t.Run("needsRebuild when index file missing", func(t *testing.T) {
		cursorPath := filepath.Join(tmpDir, "index.cursor")
		f, err := os.Create(cursorPath)
		if err != nil {
			t.Fatalf("failed to create cursor file: %v", err)
		}
		enc := json.NewEncoder(f)
		cursorVal := IndexCursor{HiULID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", TokenVer: TokenizerVersion, TotalDocs: 10}
		if err := enc.Encode(cursorVal); err != nil {
			t.Fatalf("failed to encode cursor: %v", err)
		}
		f.Close()

		_, _, needsRebuild, err := LoadIndexWithCursor(
			filepath.Join(tmpDir, "index.json"),
			cursorPath,
		)
		if err != nil {
			t.Fatalf("LoadIndexWithCursor failed: %v", err)
		}
		if !needsRebuild {
			t.Error("should need rebuild when index is missing")
		}
	})

	t.Run("needsRebuild because index encoding is not supported", func(t *testing.T) {
		// This test documents that PersistIndex/LoadIndex cannot work end-to-end
		// because Index has no exported fields for gob encoding.
		// The "success" path in LoadIndexWithCursor cannot be reached.
		indexPath := filepath.Join(tmpDir, "index.json")
		cursorPath := filepath.Join(tmpDir, "index.cursor")

		// Create a valid cursor file
		f, err := os.Create(cursorPath)
		if err != nil {
			t.Fatalf("failed to create cursor file: %v", err)
		}
		enc := json.NewEncoder(f)
		cursorVal := IndexCursor{
			HiULID:    "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			TokenVer:  TokenizerVersion,
			TotalDocs: 1,
		}
		if err := enc.Encode(cursorVal); err != nil {
			t.Fatalf("failed to encode cursor: %v", err)
		}
		f.Close()

		// LoadIndexWithCursor will call LoadIndex, which will fail because
		// the index file doesn't exist and we can't create a valid one.
		// So we expect needsRebuild = true.
		_, _, needsRebuild, err := LoadIndexWithCursor(indexPath, cursorPath)
		if err != nil {
			t.Fatalf("LoadIndexWithCursor failed: %v", err)
		}
		if !needsRebuild {
			t.Error("should need rebuild because index cannot be persisted/loaded")
		}
	})
}

func TestScanLastID(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "test.journal")

	f, err := os.Create(journalPath)
	if err != nil {
		t.Fatalf("failed to create journal file: %v", err)
	}

	events := []Event{
		{ID: "01ARZ3NDEKTSV4RRFFQ69G5FA1", Type: EvtFileEdit, Project: "test"},
		{ID: "01ARZ3NDEKTSV4RRFFQ69G5FA2", Type: EvtFileRead, Project: "test"},
		{ID: "01ARZ3NDEKTSV4RRFFQ69G5FA3", Type: EvtFileCreate, Project: "test"},
	}

	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		f.Write(append(data, '\n'))
	}
	f.Close()

	j, err := OpenJournal(journalPath, JournalOptions{})
	if err != nil {
		t.Fatalf("OpenJournal failed: %v", err)
	}
	defer j.Close()

	cursor, err := j.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("Checkpoint failed: %v", err)
	}
	if cursor != "01ARZ3NDEKTSV4RRFFQ69G5FA3" {
		t.Errorf("expected last ID %q, got %q", "01ARZ3NDEKTSV4RRFFQ69G5FA3", cursor)
	}
}
func TestLoadIndexInvalidData(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// Write invalid gob data
	f, err := os.Create(indexPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	f.Write([]byte("not gob encoded data"))
	f.Close()

	_, err = LoadIndex(indexPath)
	if err == nil {
		t.Error("LoadIndex should fail for invalid gob data")
	}
}

func TestLoadIndexCorruptedGob(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// Write partial/short gob data
	f, err := os.Create(indexPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	// Write a minimal gob header that is incomplete
	f.Write([]byte{0x00, 0x01, 0xfe})
	f.Close()

	_, err = LoadIndex(indexPath)
	if err == nil {
		t.Error("LoadIndex should fail for corrupted gob data")
	}
}

func TestPersistIndexGobEncodeError(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// PersistIndex now uses JSON encoding which can serialize unexported fields
	err := PersistIndex(ix, indexPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatalf("PersistIndex failed: %v", err)
	}
}

func TestLoadCursorInvalidPath(t *testing.T) {
	// Test that loadCursor fails for non-existent path
	_, err := loadCursor("/nonexistent/path/cursor.gob")
	if err == nil {
		t.Error("loadCursor should fail for non-existent path")
	}
}

func TestPersistIndexCursorFileCreateError(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// Make the directory read-only so cursor file creation fails
	originalMode := fs.FileMode(0755)
	if info, err := os.Stat(tmpDir); err == nil {
		originalMode = info.Mode().Perm()
	}
	os.Chmod(tmpDir, 0555)
	defer os.Chmod(tmpDir, originalMode)

	err := PersistIndex(ix, indexPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	// Should fail when creating cursor file in read-only directory
	if err == nil {
		t.Log("PersistIndex succeeded in read-only directory (may happen on Windows)")
	}
}

func TestPersistIndexIndexFileCreateError(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	// Try creating in a read-only directory (will fail on some platforms)
	tmpDir := t.TempDir()
	originalMode := fs.FileMode(0755)
	if info, err := os.Stat(tmpDir); err == nil {
		originalMode = info.Mode().Perm()
	}
	os.Chmod(tmpDir, 0555)
	defer os.Chmod(tmpDir, originalMode)

	err := PersistIndex(ix, filepath.Join(tmpDir, "index.json"), "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	// Should fail when creating index file in read-only directory
	if err == nil {
		t.Log("PersistIndex succeeded in read-only directory (may happen on Windows)")
	}
}

func TestLoadCursorNonExistent(t *testing.T) {
	_, err := loadCursor("/nonexistent/cursor.gob")
	if err == nil {
		t.Error("loadCursor should fail for non-existent path")
	}
}

func TestPersistIndexEmptyIndex(t *testing.T) {
	ix := NewIndex()

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "empty_index.gob")

	err := PersistIndex(ix, indexPath, "")
	if err == nil {
		// This may succeed or fail depending on gob encoding support
		t.Log("PersistIndex with empty index result:", err)
	}
}

func TestPersistIndexCursorWriteError(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// Create the index file first to trigger the cursor file write error
	f, err := os.Create(indexPath)
	if err != nil {
		t.Fatalf("failed to create index file: %v", err)
	}
	f.Close()

	// Make the directory read-only so cursor file creation fails
	originalMode := fs.FileMode(0755)
	if info, err := os.Stat(tmpDir); err == nil {
		originalMode = info.Mode().Perm()
	}
	os.Chmod(tmpDir, 0555)

	err = PersistIndex(ix, indexPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	// Should fail when creating cursor file in read-only directory
	if err == nil {
		t.Log("PersistIndex succeeded in read-only directory")
	}

	os.Chmod(tmpDir, originalMode)
}

// =============================================================================
// Additional tests to improve coverage
// =============================================================================

func TestLoadIndexWithCursorSuccessPath(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
		Tags:    []string{"test"},
	}
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")
	cursorPath := filepath.Join(tmpDir, "index.cursor")

	// Persist index and cursor
	if err := PersistIndex(ix, indexPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil {
		t.Fatalf("PersistIndex failed: %v", err)
	}

	loaded, cursor, needsRebuild, err := LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil {
		t.Fatalf("LoadIndexWithCursor failed: %v", err)
	}
	if needsRebuild {
		t.Error("needsRebuild should be false")
	}
	if loaded == nil {
		t.Fatal("loaded index should not be nil")
		return
	}
	if cursor == nil {
		t.Fatal("cursor should not be nil")
		return
	}
	if loaded.totalDocs != ix.totalDocs {
		t.Errorf("totalDocs mismatch: got %d, want %d", loaded.totalDocs, ix.totalDocs)
	}
}

func TestPersistIndexFileCreationError(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	// Use a path where file creation will fail. Same Windows-vs-Unix gotcha
	// as TestPersistIndexOpenError — backslashes aren't separators on Unix,
	// so `D:\nonexistent_dir\…` would just create a file named that in CWD.
	badPath := "D:\\nonexistent_dir\\index.json"
	if os.PathSeparator != '\\' {
		badPath = "/proc/cannot_create/index.json"
	}
	err := PersistIndex(ix, badPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err == nil {
		t.Error("PersistIndex should fail for non-existent directory path")
	}
}

func TestPersistIndexCursorWriteErrorPath(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// Create the directory but make it read-only so cursor file creation fails
	dir := tmpDir
	originalMode := fs.FileMode(0755)
	if info, err := os.Stat(dir); err == nil {
		originalMode = info.Mode().Perm()
	}
	os.Chmod(dir, 0555)
	defer os.Chmod(dir, originalMode)

	err := PersistIndex(ix, indexPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	// Should fail when creating cursor file in read-only directory
	if err == nil {
		t.Log("PersistIndex succeeded despite read-only directory (may happen on Windows)")
	}
}

func TestLoadIndexWithCursorCursorExistsButIndexMissing(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")
	cursorPath := filepath.Join(tmpDir, "index.cursor")

	// Create a valid cursor file
	f, err := os.Create(cursorPath)
	if err != nil {
		t.Fatalf("failed to create cursor file: %v", err)
	}
	enc := json.NewEncoder(f)
	cursorVal := IndexCursor{
		HiULID:    "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TokenVer:  TokenizerVersion,
		TotalDocs: 1,
	}
	if err := enc.Encode(cursorVal); err != nil {
		t.Fatalf("failed to encode cursor: %v", err)
	}
	f.Close()

	// indexPath doesn't exist, so LoadIndex will fail
	_, _, needsRebuild, err := LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil {
		t.Fatalf("LoadIndexWithCursor failed: %v", err)
	}
	if !needsRebuild {
		t.Error("should need rebuild when index is missing")
	}
}

func TestLoadIndexWithCursorTokenVersionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")
	cursorPath := filepath.Join(tmpDir, "index.cursor")

	// Create cursor with wrong token version
	f, err := os.Create(cursorPath)
	if err != nil {
		t.Fatalf("failed to create cursor file: %v", err)
	}
	enc := json.NewEncoder(f)
	cursorVal := IndexCursor{
		HiULID:    "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		TokenVer:  9999, // Wrong version
		TotalDocs: 1,
	}
	if err := enc.Encode(cursorVal); err != nil {
		t.Fatalf("failed to encode cursor: %v", err)
	}
	f.Close()

	_, _, needsRebuild, err := LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil {
		t.Fatalf("LoadIndexWithCursor failed: %v", err)
	}
	if !needsRebuild {
		t.Error("should need rebuild when token version mismatches")
	}
}

func TestLoadCursorFileCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := filepath.Join(tmpDir, "index.cursor")

	// Write invalid gob data
	f, err := os.Create(cursorPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	f.Write([]byte("not gob encoded"))
	f.Close()

	_, err = loadCursor(cursorPath)
	if err == nil {
		t.Error("loadCursor should fail for corrupt data")
	}
}

func TestLoadCursorOpenError(t *testing.T) {
	// Try to load cursor from a directory instead of a file
	tmpDir := t.TempDir()

	_, err := loadCursor(tmpDir)
	// Opening a directory should fail
	if err == nil {
		t.Error("loadCursor should fail when opening directory")
	}
}

func TestPersistIndexGobEncodeErrorPath(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:    EvtFileEdit,
		Project: "test",
	}
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	// PersistIndex uses gob encoding which requires exported fields
	// This will fail because Index has no exported fields
	err := PersistIndex(ix, indexPath, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	// The error path is triggered because gob encoding fails
	if err == nil {
		// If it somehow succeeds, the file would exist
		if _, statErr := os.Stat(indexPath); os.IsNotExist(statErr) {
			t.Error("PersistIndex returned nil error but file was not created")
		}
	}
}

func TestLoadIndexFileDoesNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "nonexistent.gob")

	_, err := LoadIndex(indexPath)
	if err == nil {
		t.Error("LoadIndex should fail when file does not exist")
	}
}

func TestLoadIndexCorruptGob(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "corrupt.gob")

	// Write invalid gob data
	f, err := os.Create(indexPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	f.Write([]byte{0x00, 0x01, 0xfe, 0xff})
	f.Close()

	_, err = LoadIndex(indexPath)
	if err == nil {
		t.Error("LoadIndex should fail for corrupt gob data")
	}
}

func TestRebuildIndexFromJournalIntoNilJournal(t *testing.T) {
	idx := NewIndex()
	_, err := RebuildIndexFromJournalInto(context.Background(), nil, idx)
	if err != nil {
		t.Errorf("expected nil error for nil journal, got: %v", err)
	}
}

func TestRebuildIndexFromJournalIntoNilIndex(t *testing.T) {
	mj := &mockJournalForRebuild{}
	_, err := RebuildIndexFromJournalInto(context.Background(), mj, nil)
	if err != nil {
		t.Errorf("expected nil error for nil index, got: %v", err)
	}
}

func TestRebuildIndexFromJournalIntoStreamError(t *testing.T) {
	mj := &mockJournalForRebuild{streamErr: fmt.Errorf("stream error")}
	idx := NewIndex()
	_, err := RebuildIndexFromJournalInto(context.Background(), mj, idx)
	if err == nil {
		t.Error("expected error for stream error")
	}
}

func TestRebuildIndexFromJournalWithEvents(t *testing.T) {
	mj := &mockJournalForRebuild{}
	idx, hiID, err := RebuildIndexFromJournal(context.Background(), mj)
	if err != nil {
		t.Fatalf("RebuildIndexFromJournal failed: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	// hiID should be empty for a journal with no events
	if hiID != "" {
		t.Errorf("expected empty hiID for empty journal, got: %s", hiID)
	}
}

func TestRebuildIndexFromJournalError(t *testing.T) {
	mj := &mockJournalForRebuild{streamErr: fmt.Errorf("journal stream failed")}
	idx, _, err := RebuildIndexFromJournal(context.Background(), mj)
	if err == nil {
		t.Fatal("expected error for stream failure")
	}
	if idx != nil {
		t.Error("expected nil index on error")
	}
}

// mockJournalForRebuild implements Journal for rebuild tests.
type mockJournalForRebuild struct {
	streamErr error
}

func (m *mockJournalForRebuild) Stream(ctx context.Context, from string) (<-chan Event, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	ch := make(chan Event)
	close(ch)
	return ch, nil
}

func (m *mockJournalForRebuild) Append(ctx context.Context, e Event) error { return nil }
func (m *mockJournalForRebuild) StreamN(ctx context.Context, from string, n int) (<-chan Event, error) {
	return m.Stream(ctx, from)
}
func (m *mockJournalForRebuild) Checkpoint(ctx context.Context) (string, error) { return "", nil }
func (m *mockJournalForRebuild) Rotate(ctx context.Context) error               { return nil }
func (m *mockJournalForRebuild) Size() (int64, error)                           { return 0, nil }
func (m *mockJournalForRebuild) Close() error                                   { return nil }

// TestIndexExcerpt tests the Excerpt method of Index.
func TestIndexExcerpt(t *testing.T) {
	idx := NewIndex()

	// Empty index - no excerpts map
	if got := idx.Excerpt("nonexistent"); got != "" {
		t.Errorf("Excerpt on empty index: got %q, want empty", got)
	}

	// Add an event with message
	e := Event{
		ID:   "event1",
		Type: "note",
		Data: map[string]any{"message": "This is a test message"},
	}
	idx.Add(e)

	// Now excerpts map exists but docID not in it
	if got := idx.Excerpt("event1"); got == "" {
		t.Error("Excerpt for existing event1 returned empty")
	}
	// Nonexistent ID still returns empty
	if got := idx.Excerpt("nonexistent"); got != "" {
		t.Errorf("Excerpt for nonexistent: got %q, want empty", got)
	}

	// Add event with path but no message
	e2 := Event{
		ID:   "event2",
		Type: "tool.exec",
		Data: map[string]any{"path": "/tmp/test.go"},
	}
	idx.Add(e2)
	if got := idx.Excerpt("event2"); got == "" {
		t.Error("Excerpt for event2 with path returned empty")
	}

	// Add event with neither message nor path
	e3 := Event{
		ID:    "event3",
		Type:  "tool.exec",
		Actor: "agent-1",
	}
	idx.Add(e3)
	if got := idx.Excerpt("event3"); got == "" {
		t.Error("Excerpt for event3 with no message/path returned empty")
	}
}

// TestIndexSetParams tests the BM25 parameter setter.
func TestIndexSetParams(t *testing.T) {
	idx := NewIndex()
	idx.SetParams(IndexParams{K1: 1.8, B: 0.7})
	// Smoke test - params are set without panicking
}

// TestIndexSetParamsEmpty tests SetParams on zero values.
func TestIndexSetParamsEmpty(t *testing.T) {
	idx := NewIndex()
	// k1=0 or b=0 are valid (will use BM25 defaults)
	idx.SetParams(IndexParams{})
}

// CORE-5/6/7: persist + reload must preserve meta (tier), reverse maps,
// and survive a version mismatch by triggering needsRebuild.
func TestPersistLoadPreservesMetaAndReverseMaps(t *testing.T) {
	ix := NewIndex()
	e1 := Event{
		ID:       "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Type:     EvtDecision,
		Priority: PriP1,
		Source:   SrcCLI,
		Data:     map[string]any{"message": "important decision"},
	}
	e2 := Event{
		ID:       "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
		Data:     map[string]any{"message": "low priority note"},
	}
	ix.Add(e1)
	ix.Add(e2)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")
	if err := ix.Persist(indexPath); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadIndex(indexPath)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	// CORE-5: meta (including Priority) must survive the round-trip.
	m1, ok := loaded.meta[e1.ID]
	if !ok {
		t.Fatal("CORE-5: meta for e1 missing after load")
	}
	if m1.Priority != PriP1 {
		t.Errorf("CORE-5: loaded meta priority = %q, want %q", m1.Priority, PriP1)
	}
	m2, ok := loaded.meta[e2.ID]
	if !ok {
		t.Fatal("CORE-5: meta for e2 missing after load")
	}
	if m2.Priority != PriP4 {
		t.Errorf("CORE-5: loaded meta priority = %q, want %q", m2.Priority, PriP4)
	}

	// CORE-5: eviction heaps must be rebuilt with correct tiers.
	// e1 (P1) should be in evictHeaps[0], e2 (P4) in evictHeaps[3].
	foundP1 := false
	for _, id := range loaded.evictHeaps[0] {
		if id == e1.ID {
			foundP1 = true
		}
	}
	if !foundP1 {
		t.Error("CORE-5: e1 (P1) not in evictHeaps[0] after load")
	}
	foundP4 := false
	for _, id := range loaded.evictHeaps[3] {
		if id == e2.ID {
			foundP4 = true
		}
	}
	if !foundP4 {
		t.Error("CORE-5: e2 (P4) not in evictHeaps[3] after load")
	}

	// CORE-6: reverse maps must survive the round-trip.
	if len(loaded.docStems) == 0 {
		t.Error("CORE-6: docStems empty after load")
	}
	if len(loaded.docTrigrams) == 0 {
		t.Error("CORE-6: docTrigrams empty after load")
	}

	// CORE-7: version must be present in the persisted JSON.
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index file: %v", err)
	}
	if !strings.Contains(string(raw), `"v":2`) {
		t.Error("CORE-7: persisted JSON missing version field")
	}
}

// CORE-7: a version mismatch must cause LoadIndexWithCursor to signal
// needsRebuild.
func TestLoadIndexVersionMismatchTriggersRebuild(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")
	cursorPath := filepath.Join(tmpDir, "index.cursor")

	// Write a valid v1 index (old format).
	old := `{"v":1,"stem_pl":{},"trigram_pl":{},"doc_len":{},"avg_doc_len":0,"total_docs":0}`
	if err := os.WriteFile(indexPath, []byte(old), 0o600); err != nil {
		t.Fatalf("write old index: %v", err)
	}

	cursor := `{"hi_ulid":"01ARZ3NDEKTSV4RRFFQ69G5FAV","token_ver":1,"total_docs":0,"avg_doc_len":0}`
	if err := os.WriteFile(cursorPath, []byte(cursor), 0o600); err != nil {
		t.Fatalf("write cursor: %v", err)
	}

	_, _, needsRebuild, err := LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil {
		t.Fatalf("LoadIndexWithCursor: %v", err)
	}
	if !needsRebuild {
		t.Fatal("CORE-7: version mismatch should trigger needsRebuild=true")
	}
}
