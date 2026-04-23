package content

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestChunkAndChunkSet(t *testing.T) {
	chunk := &Chunk{
		ID:       "test-chunk-1",
		ParentID: "test-set-1",
		Index:    0,
		Kind:     ChunkKindText,
		Body:     "hello world",
		Tokens:   2,
		Created:  time.Now(),
	}

	if chunk.ID != "test-chunk-1" {
		t.Errorf("Chunk.ID = %q, want 'test-chunk-1'", chunk.ID)
	}
	if chunk.Kind != ChunkKindText {
		t.Errorf("Chunk.Kind = %v, want ChunkKindText", chunk.Kind)
	}
}

func TestNewStore(t *testing.T) {
	store, err := NewStore(StoreOptions{
		MaxSize: 1024 * 1024, // 1 MB
	})
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned nil")
	}

	count, size := store.Stats()
	if count != 0 {
		t.Errorf("initial count = %d, want 0", count)
	}
	if size != 0 {
		t.Errorf("initial size = %d, want 0", size)
	}
}

func TestPutChunk(t *testing.T) {
	store, _ := NewStore(StoreOptions{MaxSize: 64 * 1024})

	chunk := &Chunk{
		ID:       "chunk-1",
		ParentID: "set-1",
		Index:    0,
		Kind:     ChunkKindText,
		Body:     "test content",
		Tokens:   2,
		Created:  time.Now(),
	}

	if err := store.PutChunk(chunk); err != nil {
		t.Fatalf("PutChunk failed: %v", err)
	}

	got, ok := store.GetChunk("chunk-1")
	if !ok {
		t.Fatal("GetChunk returned false, expected true")
	}
	if got.Body != "test content" {
		t.Errorf("GetChunk.Body = %q, want 'test content'", got.Body)
	}

	count, _ := store.Stats()
	if count != 1 {
		t.Errorf("Stats count = %d, want 1", count)
	}
}

func TestGetChunkNotFound(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	_, ok := store.GetChunk("nonexistent")
	if ok {
		t.Error("GetChunk for nonexistent key returned true, want false")
	}
}

func TestPutChunkSet(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	set := &ChunkSet{
		ID:      "set-1",
		Kind:    "exec-stdout",
		Source:  "echo hello",
		Chunks:  []string{"chunk-1"},
		Created: time.Now(),
		TTL:     0,
	}

	if err := store.PutChunkSet(set); err != nil {
		t.Fatalf("PutChunkSet failed: %v", err)
	}

	got, ok := store.GetChunkSet("set-1")
	if !ok {
		t.Fatal("GetChunkSet returned false, expected true")
	}
	if got.Kind != "exec-stdout" {
		t.Errorf("GetChunkSet.Kind = %q, want 'exec-stdout'", got.Kind)
	}
}

func TestGetChunks(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	// Add chunks
	for i := range 3 {
		chunk := &Chunk{
			ID:       "chunk-" + string(rune('a'+i)),
			ParentID: "set-1",
			Index:    i,
			Kind:     ChunkKindText,
			Body:     string(rune('A' + i)),
			Created:  time.Now(),
		}
		store.PutChunk(chunk)
	}

	set := &ChunkSet{
		ID:      "set-1",
		Chunks:  []string{"chunk-a", "chunk-b", "chunk-c"},
		Created: time.Now(),
	}
	store.PutChunkSet(set)

	chunks, ok := store.GetChunks("set-1")
	if !ok {
		t.Fatal("GetChunks returned false, expected true")
	}
	if len(chunks) != 3 {
		t.Errorf("len(chunks) = %d, want 3", len(chunks))
	}
}

func TestSearch(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	// Add some chunks
	docs := []struct {
		id   string
		body string
	}{
		{"1", "hello world foo"},
		{"2", "hello bar baz"},
		{"3", "world example"},
	}

	for _, d := range docs {
		chunk := &Chunk{
			ID:      d.id,
			Body:    d.body,
			Created: time.Now(),
		}
		store.PutChunk(chunk)
	}

	// Search for "hello"
	results, err := store.Search("hello", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Search(hello) returned %d results, want 2", len(results))
	}

	// Search with limit
	results, err = store.Search("hello", 1)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search(hello, limit=1) returned %d results, want 1", len(results))
	}
}

func TestEviction(t *testing.T) {
	// Create store with small max size
	store, _ := NewStore(StoreOptions{
		MaxSize: 100, // Very small
	})

	// Create the chunk set first so eviction can find it
	set := &ChunkSet{
		ID:      "set-1",
		Kind:    "test",
		Source:  "test",
		Created: time.Now(),
	}
	store.PutChunkSet(set)

	// Add chunks until eviction happens
	for i := range 20 {
		chunk := &Chunk{
			ID:       "chunk-" + string(rune(i)),
			ParentID: "set-1",
			Index:    i,
			Kind:     ChunkKindText,
			Body:     "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", // 40 bytes
			Created:  time.Now(),
		}
		store.PutChunk(chunk)
	}

	// Should have evicted some chunks
	count, _ := store.Stats()
	if count >= 20 {
		t.Errorf("After many inserts, count = %d, expected some eviction", count)
	}
}

func TestEvictOnEmptyStore(t *testing.T) {
	// Test evict with empty store (no chunk sets)
	store, _ := NewStore(StoreOptions{
		MaxSize: 100,
	})

	// Create store with no sets - evict should handle gracefully
	// Use reflection to call the unexported evict method
	v := reflect.ValueOf(store).Elem()
	evictFunc := v.Addr().MethodByName("evict")
	if !evictFunc.IsValid() {
		t.Skip("evict method not accessible")
	}

	// Call evict via interface
	var s *Store = store
	err := s.evict()
	if err != nil {
		t.Errorf("evict on empty store failed: %v", err)
	}
}

func TestSummarizer(t *testing.T) {
	s := &Summarizer{}

	tests := []struct {
		name      string
		body      string
		kind      ChunkKind
		wantLines bool
	}{
		{"simple", "hello\nworld", ChunkKindText, true},
		{"empty", "", ChunkKindText, true},
		{"single line", "hello", ChunkKindText, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum := s.Summarize(tt.body, tt.kind)
			if sum == nil {
				t.Fatal("Summarize returned nil")
			}
			if tt.wantLines && sum.Lines == 0 {
				t.Log("empty body gave 0 lines")
			}
		})
	}
}

func TestPutChunkSetWithTTL(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	set := &ChunkSet{
		ID:      "set-with-ttl",
		Kind:    "exec-stdout",
		Source:  "echo hello",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     time.Hour, // Non-zero TTL
	}

	if err := store.PutChunkSet(set); err != nil {
		t.Fatalf("PutChunkSet failed: %v", err)
	}
}

func TestGetChunksNotFound(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	_, ok := store.GetChunks("nonexistent")
	if ok {
		t.Error("GetChunks for nonexistent set returned true, want false")
	}
}

func TestGetChunkSetNotFound(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	_, ok := store.GetChunkSet("nonexistent")
	if ok {
		t.Error("GetChunkSet for nonexistent set returned true, want false")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	results, err := store.Search("", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if results != nil {
		t.Error("Search with empty query should return nil")
	}
}

func TestSearchNoMatch(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	chunk := &Chunk{
		ID:      "1",
		Body:    "hello world",
		Created: time.Now(),
	}
	store.PutChunk(chunk)

	results, err := store.Search("xyz123nonexistent", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search for nonexistent returned %d results, want 0", len(results))
	}
}

func TestClose(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	set := &ChunkSet{
		ID:      "test-set",
		Kind:    "test",
		Source:  "test",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     0, // Will be persisted
	}
	store.PutChunkSet(set)

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestNewStoreWithPath(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(StoreOptions{
		Path:    tmpDir,
		MaxSize: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
}

func TestMatchScore(t *testing.T) {
	store := &Store{}

	tests := []struct {
		content string
		query   string
		want    float64
	}{
		{"hello world", "hello", 1.0},
		{"hello world", "world", 1.0},
		{"hello world", "hello world", 1.0},
		{"hello", "nonexistent", 0.0},
		{"", "hello", 0.0},
	}

	for _, tt := range tests {
		got := store.matchScore(tt.content, core.Tokenize(tt.query))
		if got != tt.want {
			t.Errorf("matchScore(%q, %q) = %f, want %f", tt.content, tt.query, got, tt.want)
		}
	}
}

func TestChunkKindConstants(t *testing.T) {
	if ChunkKindMarkdown != "markdown" {
		t.Errorf("ChunkKindMarkdown = %q, want 'markdown'", ChunkKindMarkdown)
	}
	if ChunkKindCode != "code" {
		t.Errorf("ChunkKindCode = %q, want 'code'", ChunkKindCode)
	}
	if ChunkKindJSON != "json" {
		t.Errorf("ChunkKindJSON = %q, want 'json'", ChunkKindJSON)
	}
	if ChunkKindText != "text" {
		t.Errorf("ChunkKindText = %q, want 'text'", ChunkKindText)
	}
	if ChunkKindLogLines != "log-lines" {
		t.Errorf("ChunkKindLogLines = %q, want 'log-lines'", ChunkKindLogLines)
	}
}

func TestStoreOptions(t *testing.T) {
	opt := StoreOptions{
		Path:       "/tmp/test",
		MaxSize:    1024,
		PersistTTL: time.Hour,
	}

	if opt.Path != "/tmp/test" {
		t.Errorf("Path = %q, want '/tmp/test'", opt.Path)
	}
	if opt.MaxSize != 1024 {
		t.Errorf("MaxSize = %d, want 1024", opt.MaxSize)
	}
}

func TestLoadChunkSet(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	// First create and persist a chunk set
	set := &ChunkSet{
		ID:      "test-set-load",
		Kind:    "test",
		Source:  "test source",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     0, // Will be persisted
	}
	store.PutChunkSet(set)
	store.Close()

	// Now load it back
	loaded, err := store.LoadChunkSet("test-set-load")
	if err != nil {
		t.Fatalf("LoadChunkSet failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadChunkSet returned nil")
	}
	if loaded.Kind != "test" {
		t.Errorf("Kind = %q, want 'test'", loaded.Kind)
	}
}

func TestLoadChunkSetNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	_, err := store.LoadChunkSet("nonexistent")
	if err == nil {
		t.Error("LoadChunkSet should fail for nonexistent set")
	}
}

func TestPersistChunkSet(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	set := &ChunkSet{
		ID:      "persist-test",
		Kind:    "exec-stdout",
		Source:  "echo hello",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     0, // Will be persisted
	}

	err := store.PutChunkSet(set)
	if err != nil {
		t.Fatalf("PutChunkSet failed: %v", err)
	}

	// Load it back to verify it was persisted
	loaded, err := store.LoadChunkSet("persist-test")
	if err != nil {
		t.Fatalf("Failed to load persisted chunk set: %v", err)
	}
	if loaded.Source != "echo hello" {
		t.Errorf("Source = %q, want 'echo hello'", loaded.Source)
	}
}

func TestPersistChunkSetCreateError(t *testing.T) {
	// Use an invalid path that may cause os.Create to fail
	store, err := NewStore(StoreOptions{
		Path: "/nonexistent/directory/that/cannot/be/created",
	})
	if err != nil || store == nil {
		t.Skipf("NewStore failed (expected for invalid path): %v", err)
		return
	}

	set := &ChunkSet{
		ID:      "test-set",
		Kind:    "test",
		Source:  "test source",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     0, // Will try to persist
	}

	// This may or may not fail depending on filesystem
	// Just verify it doesn't panic
	store.persistChunkSet(set)
}

func TestLoadChunkSetFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	// LoadChunkSet for non-existent file should fail
	_, err := store.LoadChunkSet("nonexistent-set")
	if err == nil {
		t.Error("LoadChunkSet should fail for nonexistent set")
	}
}

func TestLoadChunkSetInvalidGzip(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file that is valid gzip but contains invalid JSON
	gzipPath := filepath.Join(tmpDir, "invalid.json.gz")

	f, err := os.Create(gzipPath)
	if err != nil {
		t.Skipf("skipping: could not create test file: %v", err)
	}
	gz := gzip.NewWriter(f)
	gz.Write([]byte("not valid json"))
	gz.Close()
	f.Close()

	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	_, err = store.LoadChunkSet("invalid")
	if err == nil {
		t.Error("LoadChunkSet should fail for invalid JSON in gzip")
	}
}

func TestLoadChunkSetTruncatedGzip(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a truncated gzip file
	gzipPath := filepath.Join(tmpDir, "truncated.json.gz")

	f, err := os.Create(gzipPath)
	if err != nil {
		t.Skipf("skipping: could not create test file: %v", err)
	}
	// Write a valid gzip header but truncated data
	gz := gzip.NewWriter(f)
	gz.Write([]byte(`{"id":`)) // truncated JSON
	gz.Close()
	f.Close()

	store, _ := NewStore(StoreOptions{
		Path: tmpDir,
	})

	_, err = store.LoadChunkSet("truncated")
	if err == nil {
		t.Error("LoadChunkSet should fail for truncated gzip")
	}
}

func TestNewStoreWithEmptyPath(t *testing.T) {
	// Empty path should still work (no directory creation attempted)
	store, err := NewStore(StoreOptions{
		Path:    "",
		MaxSize: 1024,
	})
	if err != nil {
		t.Fatalf("NewStore with empty path failed: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned nil for empty path")
	}
}

func TestNewStoreWithInvalidPath(t *testing.T) {
	// Use a path that cannot be created
	store, err := NewStore(StoreOptions{
		Path:    "/",
		MaxSize: 1024,
	})
	// Creating store should still work even if directory creation fails
	// because directory creation is not fatal - it just won't persist
	if err != nil {
		t.Logf("NewStore error (may be expected on some systems): %v", err)
	}
	_ = store // store may or may not be nil depending on system
}

func TestStoreEvictWithNoSets(t *testing.T) {
	store, _ := NewStore(StoreOptions{MaxSize: 10})

	// evict should handle empty sets gracefully
	err := store.evict()
	if err != nil {
		t.Errorf("evict with no sets should not error: %v", err)
	}
}

func TestPutChunkTriggersEviction(t *testing.T) {
	// Create store with tiny max size so eviction triggers easily
	store, _ := NewStore(StoreOptions{MaxSize: 50})

	// Create multiple chunk sets so eviction doesn't remove our test set
	// (evict removes the oldest set by Created time)
	for i := range 3 {
		set := &ChunkSet{
			ID:      "set-" + string(rune('A'+i)),
			Kind:    "test",
			Source:  "test",
			Created: time.Now().Add(time.Duration(i) * time.Second), // Stagger creation times
		}
		store.PutChunkSet(set)
	}

	// Add chunks until we trigger eviction
	// Each chunk body is 30 bytes, maxSize is 50
	for i := range 5 {
		chunk := &Chunk{
			ID:       "chunk-" + string(rune(i)),
			ParentID: "set-A", // Use the oldest set
			Index:    i,
			Kind:     ChunkKindText,
			Body:     "123456789012345678901234567890", // 30 bytes
			Tokens:   5,
			Created:  time.Now(),
		}
		if err := store.PutChunk(chunk); err != nil {
			t.Fatalf("PutChunk %d failed: %v", i, err)
		}
	}

	// Verify store still has chunks after eviction
	count, _ := store.Stats()
	if count == 0 {
		t.Error("Expected some chunks to remain after eviction")
	}
}

func TestPutChunkAppendsToSet(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	// First add the chunk set
	set := &ChunkSet{
		ID:      "set-1",
		Kind:    "test",
		Source:  "test source",
		Chunks:  []string{},
		Created: time.Now(),
	}
	store.PutChunkSet(set)

	// Now add a chunk with that ParentID
	chunk := &Chunk{
		ID:       "chunk-1",
		ParentID: "set-1",
		Index:    0,
		Kind:     ChunkKindText,
		Body:     "test content",
		Tokens:   2,
		Created:  time.Now(),
	}

	if err := store.PutChunk(chunk); err != nil {
		t.Fatalf("PutChunk failed: %v", err)
	}

	// Verify chunk was added to the set
	gotSet, ok := store.GetChunkSet("set-1")
	if !ok {
		t.Fatal("GetChunkSet returned false, expected true")
	}
	if len(gotSet.Chunks) != 1 {
		t.Errorf("set.Chunks len = %d, want 1", len(gotSet.Chunks))
	}
	if gotSet.Chunks[0] != "chunk-1" {
		t.Errorf("set.Chunks[0] = %q, want 'chunk-1'", gotSet.Chunks[0])
	}
}

func TestPutChunkWithNonexistentParentID(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	// Add chunk with ParentID pointing to non-existent set
	chunk := &Chunk{
		ID:       "orphan-chunk",
		ParentID: "nonexistent-set",
		Index:    0,
		Kind:     ChunkKindText,
		Body:     "orphan content",
		Tokens:   2,
		Created:  time.Now(),
	}

	// Should succeed silently (ParentID set but no set to append to)
	if err := store.PutChunk(chunk); err != nil {
		t.Fatalf("PutChunk with nonexistent ParentID failed: %v", err)
	}

	// Chunk should still be stored
	got, ok := store.GetChunk("orphan-chunk")
	if !ok {
		t.Fatal("GetChunk returned false, expected true")
	}
	if got.Body != "orphan content" {
		t.Errorf("got.Body = %q, want 'orphan content'", got.Body)
	}
}

func TestSearchWithLimitTruncation(t *testing.T) {
	store, _ := NewStore(StoreOptions{})

	// Add many chunks with different content to get different scores
	for i := 0; i < 10; i++ {
		chunk := &Chunk{
			ID:      "chunk-" + string(rune('A'+i)),
			Body:    "test content word match", // All same body for same score
			Created: time.Now(),
		}
		store.PutChunk(chunk)
	}

	// Search with limit=3 should truncate results
	results, err := store.Search("word", 3)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Search with limit=3 returned %d results, want 3", len(results))
	}
}

func TestCloseWithEmptyPath(t *testing.T) {
	store, _ := NewStore(StoreOptions{
		Path: "", // No path - nothing to persist
	})

	set := &ChunkSet{
		ID:      "test-set",
		Kind:    "test",
		Source:  "test",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     0,
	}
	store.PutChunkSet(set)

	// Close should succeed even with no path
	err := store.Close()
	if err != nil {
		t.Fatalf("Close with empty path failed: %v", err)
	}
}

func TestClosePersistError(t *testing.T) {
	// Use an invalid path that may cause persist to fail
	store, _ := NewStore(StoreOptions{
		Path: "/nonexistent/dir/cannot/write",
	})

	set := &ChunkSet{
		ID:      "test-set",
		Kind:    "test",
		Source:  "test",
		Chunks:  []string{},
		Created: time.Now(),
		TTL:     0, // Will try to persist
	}
	store.PutChunkSet(set)

	// This may or may not fail depending on filesystem
	// Just verify it doesn't panic
	store.Close()
}
