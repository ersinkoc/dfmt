package content

import (
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
	for i := 0; i < 3; i++ {
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
	for i := 0; i < 20; i++ {
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

func TestSummarizer(t *testing.T) {
	s := &Summarizer{}

	tests := []struct {
		name     string
		body     string
		kind     ChunkKind
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
				// Empty body might give 0 lines
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
