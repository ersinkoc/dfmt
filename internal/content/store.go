package content

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// ChunkKind represents the type of content.
type ChunkKind string

const (
	ChunkKindMarkdown ChunkKind = "markdown"
	ChunkKindCode     ChunkKind = "code"
	ChunkKindJSON     ChunkKind = "json"
	ChunkKindText     ChunkKind = "text"
	ChunkKindLogLines ChunkKind = "log-lines"
)

// Chunk represents a piece of content from a sandboxed tool execution.
type Chunk struct {
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id"` // ChunkSet ID
	Index    int       `json:"index"`
	Kind     ChunkKind `json:"kind"`
	Heading  string    `json:"heading,omitempty"`
	Lang     string    `json:"lang,omitempty"`
	Path     string    `json:"path,omitempty"`
	Body     string    `json:"body"`
	Tokens   int       `json:"tokens"`
	Created  time.Time `json:"created"`
}

// ChunkSet groups chunks from the same output.
type ChunkSet struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"` // "exec-stdout" | "file-read" | "fetch" | ...
	Source  string        `json:"source"`
	Intent  string        `json:"intent,omitempty"`
	Chunks  []string      `json:"chunks"` // chunk IDs in order
	Created time.Time     `json:"created"`
	TTL     time.Duration `json:"ttl"`
}

// Store manages the content chunk storage.
type Store struct {
	mu      sync.RWMutex
	chunks  map[string]*Chunk
	sets    map[string]*ChunkSet
	maxSize int64 // Maximum size in bytes
	curSize int64 // Current size in bytes
	path    string
}

// StoreOptions configures the content store.
type StoreOptions struct {
	Path       string
	MaxSize    int64 // Maximum size in bytes (default 64 MB)
	PersistTTL time.Duration
}

// NewStore creates a new content store.
func NewStore(opt StoreOptions) (*Store, error) {
	if opt.MaxSize == 0 {
		opt.MaxSize = 64 * 1024 * 1024 // 64 MB default
	}

	s := &Store{
		chunks:  make(map[string]*Chunk),
		sets:    make(map[string]*ChunkSet),
		maxSize: opt.MaxSize,
		path:    opt.Path,
	}

	if opt.Path != "" {
		if err := os.MkdirAll(opt.Path, 0755); err != nil {
			return nil, fmt.Errorf("create content dir: %w", err)
		}
	}

	return s, nil
}

// PutChunk stores a chunk and adds it to a chunk set.
func (s *Store) PutChunk(chunk *Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check size limit
	if s.curSize >= s.maxSize {
		if err := s.evict(); err != nil {
			return fmt.Errorf("evict chunks: %w", err)
		}
	}

	s.chunks[chunk.ID] = chunk
	s.curSize += int64(len(chunk.Body))

	// Add to chunk set
	if chunk.ParentID != "" {
		if set, ok := s.sets[chunk.ParentID]; ok {
			set.Chunks = append(set.Chunks, chunk.ID)
		}
	}

	return nil
}

// PutChunkSet stores a chunk set.
func (s *Store) PutChunkSet(set *ChunkSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sets[set.ID] = set

	// Persist if path is set and TTL is forever
	if s.path != "" && set.TTL == 0 {
		return s.persistChunkSet(set)
	}

	return nil
}

// GetChunk retrieves a chunk by ID.
func (s *Store) GetChunk(id string) (*Chunk, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	chunk, ok := s.chunks[id]
	return chunk, ok
}

// GetChunkSet retrieves a chunk set by ID.
func (s *Store) GetChunkSet(id string) (*ChunkSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.sets[id]
	return set, ok
}

// GetChunks retrieves all chunks in a chunk set.
func (s *Store) GetChunks(parentID string) ([]*Chunk, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	set, ok := s.sets[parentID]
	if !ok {
		return nil, false
	}

	chunks := make([]*Chunk, 0, len(set.Chunks))
	for _, id := range set.Chunks {
		if chunk, ok := s.chunks[id]; ok {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, true
}

// Search searches chunks by query.
func (s *Store) Search(query string, limit int) ([]*Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Simple search - tokenize query and match against chunk bodies
	queryTokens := core.Tokenize(query)
	if len(queryTokens) == 0 {
		return nil, nil
	}

	type scoredChunk struct {
		chunk *Chunk
		score float64
	}

	var results []scoredChunk
	for _, chunk := range s.chunks {
		score := s.matchScore(chunk.Body, queryTokens)
		if score > 0 {
			results = append(results, scoredChunk{chunk, score})
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	chunks := make([]*Chunk, len(results))
	for i, r := range results {
		chunks[i] = r.chunk
	}
	return chunks, nil
}

// matchScore calculates a simple match score between content and query tokens.
func (s *Store) matchScore(content string, queryTokens []string) float64 {
	contentLower := core.Tokenize(content)
	if len(contentLower) == 0 {
		return 0
	}

	var score float64
	for _, qt := range queryTokens {
		for _, ct := range contentLower {
			if ct == qt {
				score++
			}
		}
	}

	return score / float64(len(queryTokens))
}

// evict removes the least recently used chunk set.
func (s *Store) evict() error {
	if len(s.sets) == 0 {
		return nil
	}

	// Find oldest chunk set
	var oldest *ChunkSet
	for _, set := range s.sets {
		if oldest == nil || set.Created.Before(oldest.Created) {
			oldest = set
		}
	}

	if oldest == nil {
		return nil
	}

	// Remove chunks
	for _, id := range oldest.Chunks {
		if chunk, ok := s.chunks[id]; ok {
			s.curSize -= int64(len(chunk.Body))
			delete(s.chunks, id)
		}
	}
	delete(s.sets, oldest.ID)

	return nil
}

// persistChunkSet writes a chunk set to disk.
func (s *Store) persistChunkSet(set *ChunkSet) error {
	path := filepath.Join(s.path, set.ID+".json.gz")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	enc := json.NewEncoder(gz)
	return enc.Encode(set)
}

// LoadChunkSet loads a chunk set from disk.
func (s *Store) LoadChunkSet(id string) (*ChunkSet, error) {
	path := filepath.Join(s.path, id+".json.gz")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var set ChunkSet
	if err := json.NewDecoder(gz).Decode(&set); err != nil {
		return nil, err
	}

	return &set, nil
}

// Close closes the store and persists all chunk sets.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Persist all chunk sets with TTL == 0
	if s.path != "" {
		for _, set := range s.sets {
			if set.TTL == 0 {
				if err := s.persistChunkSet(set); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// Stats returns store statistics.
func (s *Store) Stats() (int, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks), s.curSize
}
