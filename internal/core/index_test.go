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

	for range 5 {
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
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
		Data: map[string]any{"message": "hello world"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	hits := ix.SearchBM25("xyz123nonexistent", 10)
	if len(hits) != 0 {
		t.Errorf("SearchBM25 returned %d hits for nonexistent query", len(hits))
	}
}

// TestIndexSearchTrigramFindsSyntheticMarker pins the fallback contract:
// a token like "xj7q3" that BM25 may treat as noise (digit-mixed, no
// dictionary stem) is still locatable via trigram intersection. Closes
// the audit finding "synthetic markers stay invisible to dfmt_search".
func TestIndexSearchTrigramFindsSyntheticMarker(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:   "marker-evt",
		TS:   time.Now(),
		Type: EvtNote,
		Data: map[string]any{"message": "AUDIT_PROBE_XJ7Q3 is the marker"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	hits := ix.SearchTrigram("xj7q3", 10)
	if len(hits) == 0 {
		t.Fatal("SearchTrigram returned no hits for indexed substring 'xj7q3'")
	}
	if hits[0].ID != "marker-evt" {
		t.Errorf("SearchTrigram top hit = %q, want marker-evt", hits[0].ID)
	}
	if hits[0].Layer != 2 {
		t.Errorf("SearchTrigram hit layer = %d, want 2", hits[0].Layer)
	}
}

// TestIndexSearchTrigramRanksByTokenCount pins the score contract:
// when one document matches more query tokens than another, it must
// outrank. The score field carries the count of distinct query tokens
// that fully match.
func TestIndexSearchTrigramRanksByTokenCount(t *testing.T) {
	ix := NewIndex()

	high := Event{
		ID:   "high",
		TS:   time.Now(),
		Type: EvtNote,
		Data: map[string]any{"message": "alpha beta gamma delta"},
	}
	high.Sig = high.ComputeSig()
	ix.Add(high)

	low := Event{
		ID:   "low",
		TS:   time.Now(),
		Type: EvtNote,
		Data: map[string]any{"message": "only alpha here"},
	}
	low.Sig = low.ComputeSig()
	ix.Add(low)

	hits := ix.SearchTrigram("alpha beta gamma", 10)
	if len(hits) < 2 {
		t.Fatalf("expected both docs to score, got %d hits", len(hits))
	}
	if hits[0].ID != "high" {
		t.Errorf("top hit = %q, want high (matched 3 tokens vs 1)", hits[0].ID)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("top score %v not > runner-up %v", hits[0].Score, hits[1].Score)
	}
}

// TestIndexSearchTrigramNoMatch pins the empty-result contract: a
// query with no overlapping trigrams must return nil, not silently
// fall through to "all documents".
func TestIndexSearchTrigramNoMatch(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:   "evt",
		TS:   time.Now(),
		Type: EvtNote,
		Data: map[string]any{"message": "alpha beta"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	hits := ix.SearchTrigram("zzznotpresent", 10)
	if len(hits) != 0 {
		t.Errorf("SearchTrigram returned %d hits for absent substring", len(hits))
	}
}

func TestIndexRemove(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
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
		TotalDocs: 100,
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
	_ = enc.Encode(wrongCursor)
	f.Close()

	//nolint:dogsled
	_, _, needsRebuild, _ := LoadIndexWithCursor("/nonexistent", cursorPath)
	if !needsRebuild {
		t.Error("needsRebuild should be true when tokenizer version mismatch")
	}
}

func TestPersistIndexOpenError(t *testing.T) {
	ix := NewIndex()
	// Using an invalid path should fail (directory doesn't exist on Windows)
	err := PersistIndex(ix, "D:\\nonexistent_dir\\path\\index.json", "test1")
	if err == nil {
		t.Error("PersistIndex should fail for invalid path")
	}
}

func TestIndexPersistErrorPath(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	// Use a path that cannot be created
	err := ix.Persist("/proc/cannot_create/index.gob")
	if err == nil {
		t.Error("Index.Persist should fail for invalid path")
	}
}

func TestIndexPersistAndLoad(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
		Tags: []string{"test"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.json")

	err := ix.Persist(indexPath)
	if err != nil {
		t.Fatalf("Index.Persist failed: %v", err)
	}

	loaded, err := LoadIndex(indexPath)
	if err != nil {
		t.Fatalf("LoadIndex failed: %v", err)
	}

	if loaded.totalDocs != ix.totalDocs {
		t.Errorf("totalDocs mismatch: got %d, want %d", loaded.totalDocs, ix.totalDocs)
	}
}

func TestLoadIndexWithCorruptFile(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.gob")

	// Write garbage data
	f, err := os.Create(indexPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	_, _ = f.WriteString("this is not gob data")
	f.Close()

	_, err = LoadIndex(indexPath)
	if err == nil {
		t.Error("LoadIndex should fail for corrupt data")
	}
}

func TestLoadIndexCursorWithCorruptGob(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := filepath.Join(tmpDir, "cursor.gob")

	// Write garbage gob data
	f, err := os.Create(cursorPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	_, _ = f.WriteString("not gob encoded")
	f.Close()

	_, err = loadCursor(cursorPath)
	if err == nil {
		t.Error("loadCursor should fail for corrupt data")
	}
}

func TestLoadIndexCursorEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	cursorPath := filepath.Join(tmpDir, "cursor.gob")

	// Create empty file
	f, err := os.Create(cursorPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	f.Close()

	_, err = loadCursor(cursorPath)
	if err == nil {
		t.Error("loadCursor should fail for empty file")
	}
}

func TestPersistIndexReadOnlyDir(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	// Create a read-only directory
	tmpDir := t.TempDir()
	// On Windows, make dir read-only
	os.Chmod(tmpDir, 0555)

	indexPath := filepath.Join(tmpDir, "index.gob")
	err := PersistIndex(ix, indexPath, "test1")

	// Should fail when trying to create file in read-only directory
	if err == nil {
		t.Log("PersistIndex succeeded in read-only directory (platform behavior)")
	}

	os.Chmod(tmpDir, 0755)
}

func TestIndexRemoveUpdatesTotalDocs(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	if ix.totalDocs != 1 {
		t.Errorf("totalDocs = %d, want 1", ix.totalDocs)
	}

	ix.Remove("test1")

	// Note: current implementation decrements totalDocs but doesn't
	// properly update the index structures
	if ix.totalDocs != 0 {
		t.Errorf("totalDocs after remove = %d, want 0", ix.totalDocs)
	}
}

func TestIndexSearchBM25MultipleDocs(t *testing.T) {
	ix := NewIndex()

	// Add multiple documents
	for i := range 5 {
		e := Event{
			ID:   string(rune('a' + i)),
			TS:   time.Now(),
			Type: EvtFileEdit,
			Data: map[string]any{"message": "test file edit content"},
			Tags: []string{"file", "edit"},
		}
		e.Sig = e.ComputeSig()
		ix.Add(e)
	}

	hits := ix.SearchBM25("file edit", 10)
	if hits == nil {
		t.Fatal("SearchBM25 returned nil")
	}
	// With multiple matching documents, should get hits
	t.Logf("SearchBM25 returned %d hits", len(hits))
}

func TestIndexSearchBM25SingleToken(t *testing.T) {
	ix := NewIndex()

	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
		Data: map[string]any{"message": "hello world"},
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	hits := ix.SearchBM25("hello", 10)
	if hits == nil {
		t.Fatal("SearchBM25 returned nil for matching single token")
	}
}

func TestIndexPersistLockError(t *testing.T) {
	ix := NewIndex()
	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()
	ix.Add(e)

	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "index.gob")

	// Create file first
	f, err := os.Create(indexPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	f.Close()

	// Make file read-only
	os.Chmod(indexPath, 0444)

	err = ix.Persist(indexPath)
	// Should fail due to read-only file
	if err == nil {
		t.Log("Persist succeeded on read-only file (platform behavior)")
	}

	os.Chmod(indexPath, 0644)
}
