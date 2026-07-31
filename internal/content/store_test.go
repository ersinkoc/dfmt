package content

import (
	"fmt"
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
	s := store
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
			ID:       fmt.Sprintf("chunk-%d", i),
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
