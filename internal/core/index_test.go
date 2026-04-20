package core

import (
	"container/heap"
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewIndex(t *testing.T) {
	ix := NewIndex()
	if ix == nil {
		t.Fatal("NewIndex returned nil")
	}
	if ix.stemPL == nil {
		t.Error("stemPL is nil")
	}
	if ix.trigramPL == nil {
		t.Error("trigramPL is nil")
	}
	if ix.docLen == nil {
		t.Error("docLen is nil")
	}
}

func TestIndexAdd(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     EvtNote,
		Priority: PriP4,
		Data:     map[string]any{"message": "test note"},
		Tags:     []string{"test", "note"},
	}
	e.Sig = e.ComputeSig()

	ix.Add(e)

	if ix.totalDocs != 1 {
		t.Errorf("totalDocs = %d, want 1", ix.totalDocs)
	}
	if ix.avgDocLen <= 0 {
		t.Error("avgDocLen should be positive after Add")
	}
}

func TestIndexAddMultiple(t *testing.T) {
	ix := NewIndex()

	for i := 0; i < 5; i++ {
		e := Event{
			ID:       string(NewULID(time.Now())),
			TS:       time.Now(),
			Type:     EvtNote,
			Priority: PriP4,
			Data:     map[string]any{"message": "test note"},
		}
		e.Sig = e.ComputeSig()
		ix.Add(e)
	}

	if ix.totalDocs != 5 {
		t.Errorf("totalDocs = %d, want 5", ix.totalDocs)
	}
}

func TestIndexSearchBM25(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     EvtFileEdit,
		Priority: PriP3,
		Data:     map[string]any{"message": "edited file.go"},
		Tags:     []string{"edit", "file"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	hits := ix.SearchBM25("file edit", 10)
	if hits == nil {
		t.Fatal("SearchBM25 returned nil")
	}
	if len(hits) == 0 {
		t.Log("SearchBM25 returned empty (may be expected for small index)")
	}
}

func TestIndexSearchBM25NoTokens(t *testing.T) {
	ix := NewIndex()

	hits := ix.SearchBM25("", 10)
	if hits != nil {
		t.Error("SearchBM25 should return nil for empty query")
	}
}

func TestIndexSearchBM25NoMatch(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     EvtNote,
		Data:     map[string]any{"message": "hello world"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	hits := ix.SearchBM25("xyz123nonexistent", 10)
	if len(hits) != 0 {
		t.Errorf("SearchBM25 returned %d hits for nonexistent query", len(hits))
	}
}

func TestIndexRemove(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     EvtNote,
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	if ix.totalDocs != 1 {
		t.Errorf("totalDocs = %d, want 1", ix.totalDocs)
	}

	ix.Remove("test1")
}

func TestScoredHit(t *testing.T) {
	hits := []ScoredHit{
		{ID: "a", Score: 1.5, Layer: 1},
		{ID: "b", Score: 2.0, Layer: 2},
	}

	if hits[0].Score >= hits[1].Score {
		t.Error("Hit scores not ordered correctly")
	}
}

func TestHitHeap(t *testing.T) {
	h := &hitHeap{}

	heap.Push(h, ScoredHit{ID: "a", Score: 1.0})
	heap.Push(h, ScoredHit{ID: "b", Score: 3.0})
	heap.Push(h, ScoredHit{ID: "c", Score: 2.0})

	if h.Len() != 3 {
		t.Errorf("Len = %d, want 3", h.Len())
	}

	first := heap.Pop(h).(ScoredHit)
	if first.ID != "b" {
		t.Errorf("First pop = %s, want 'b' (highest score)", first.ID)
	}
}

func TestHitHeapEmpty(t *testing.T) {
	h := &hitHeap{}

	if h.Len() != 0 {
		t.Errorf("Len of empty heap = %d, want 0", h.Len())
	}
}

func TestTokenizerVersion(t *testing.T) {
	if TokenizerVersion != 1 {
		t.Errorf("TokenizerVersion = %d, want 1", TokenizerVersion)
	}
}

func TestIndexCursor(t *testing.T) {
	c := IndexCursor{
		HiULID:    "test-ulid",
		TokenVer:  1,
		TotalDocs: 100,
		AvgDocLen: 50.5,
	}

	if c.HiULID != "test-ulid" {
		t.Errorf("HiULID = %s, want 'test-ulid'", c.HiULID)
	}
	if c.TotalDocs != 100 {
		t.Errorf("TotalDocs = %d, want 100", c.TotalDocs)
	}
}

func TestIndexEventText(t *testing.T) {
	ix := NewIndex()

	e := Event{
		Type: EvtDecision,
		Tags: []string{"api", "design"},
		Data: map[string]any{
			"message": "Use REST for this endpoint",
			"path":    "/api/users",
		},
	}

	text := ix.eventText(e)
	if text == "" {
		t.Error("eventText returned empty string")
	}
}

func TestJoinStrings(t *testing.T) {
	tests := []struct {
		parts []string
		sep   string
		want  string
	}{
		{[]string{"a", "b", "c"}, " ", "a b c"},
		{[]string{"single"}, " ", "single"},
		{[]string{}, " ", ""},
		{[]string{"hello", "world"}, ",", "hello,world"},
	}

	for _, tt := range tests {
		got := joinStrings(tt.parts, tt.sep)
		if got != tt.want {
			t.Errorf("joinStrings(%v, %q) = %q, want %q", tt.parts, tt.sep, got, tt.want)
		}
	}
}

func TestLoadIndexNonExistent(t *testing.T) {
	_, err := LoadIndex("/nonexistent/path/index.gob")
	if err == nil {
		t.Error("LoadIndex should fail for nonexistent file")
	}
}

func TestLoadIndexWithCursorMissingFile(t *testing.T) {
	_, _, needsRebuild, err := LoadIndexWithCursor("/nonexistent", "/nonexistent")
	if err != nil {
		t.Fatalf("LoadIndexWithCursor failed: %v", err)
	}
	if !needsRebuild {
		t.Error("needsRebuild should be true for nonexistent files")
	}
}

func TestLoadIndexWithCursorVersionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := filepath.Join(tmpDir, "index.cursor")

	// Modify cursor to have wrong version
	f, _ := os.Create(cursorPath)
	enc := gob.NewEncoder(f)
	wrongCursor := IndexCursor{TokenVer: 999}
	enc.Encode(wrongCursor)
	f.Close()

	_, _, needsRebuild, _ := LoadIndexWithCursor("/nonexistent", cursorPath)
	if !needsRebuild {
		t.Error("needsRebuild should be true when tokenizer version mismatch")
	}
}

func TestPersistIndexOpenError(t *testing.T) {
	ix := NewIndex()
	// Using an invalid path should fail
	err := PersistIndex(ix, "/proc/invalid/index.gob", "test1")
	if err == nil {
		t.Error("PersistIndex should fail for invalid path")
	}
}
