package content

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// chunkIDPattern restricts chunk/chunk-set IDs to an ASCII-safe shape so a
// caller cannot smuggle '..', '/', '\\', or drive letters into a filesystem
// path via LoadChunkSet / persistChunkSet. Letters, digits, dash, and
// underscore are permitted — production callers supply ULIDs, but the
// intra-process API also accepts human-readable test IDs.
var chunkIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// validateID rejects any chunk or chunk-set ID that would escape the store
// directory.
func validateID(id string) error {
	if !chunkIDPattern.MatchString(id) {
		return fmt.Errorf("invalid content id %q: must match %s", id, chunkIDPattern)
	}
	return nil
}

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
	mu         sync.RWMutex
	chunks     map[string]*Chunk
	sets       map[string]*ChunkSet
	maxSize    int64         // Maximum size in bytes
	curSize    int64         // Current size in bytes
	defaultTTL time.Duration // 0 = no expiry (persist forever); >0 = ephemeral
	path       string
}

// StoreOptions configures the content store.
type StoreOptions struct {
	Path    string
	MaxSize int64 // Maximum size in bytes (default 64 MB)
	// DefaultChunkTTL, when >0, stamps every PutChunkSet that arrives with
	// TTL==0 so it expires after this duration. Existing logic treats
	// TTL>0 as ephemeral (no disk persist), so an opt-in default makes
	// the in-memory chunk store self-cleaning over a long agent session
	// without breaking the persistent-by-default behavior callers rely
	// on. Default 0 keeps backward compatibility.
	DefaultChunkTTL time.Duration
	PersistTTL      time.Duration
}

// NewStore creates a new content store.
func NewStore(opt StoreOptions) (*Store, error) {
	if opt.MaxSize == 0 {
		opt.MaxSize = 64 * 1024 * 1024 // 64 MB default
	}

	s := &Store{
		chunks:     make(map[string]*Chunk),
		sets:       make(map[string]*ChunkSet),
		maxSize:    opt.MaxSize,
		defaultTTL: opt.DefaultChunkTTL,
		path:       opt.Path,
	}

	if opt.Path != "" {
		// 0700 to match the parent .dfmt directory and the journal file's
		// permissions — content store holds redacted-but-still-sensitive
		// tool output and should not be readable by other local users.
		if err := os.MkdirAll(opt.Path, 0o700); err != nil {
			return nil, fmt.Errorf("create content dir: %w", err)
		}
	}

	return s, nil
}

// PutChunk stores a chunk and adds it to a chunk set.
func (s *Store) PutChunk(chunk *Chunk) error {
	if err := validateID(chunk.ID); err != nil {
		return err
	}
	if chunk.ParentID != "" {
		if err := validateID(chunk.ParentID); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict until the new chunk fits under maxSize. The prior single-shot
	// evict removed just one set, so a chunk larger than the oldest set's
	// footprint could leave curSize arbitrarily over maxSize. Bail if evict
	// makes no progress (e.g. all sets already gone).
	chunkSize := int64(len(chunk.Body))
	for s.curSize+chunkSize > s.maxSize {
		before := s.curSize
		if err := s.evict(); err != nil {
			return fmt.Errorf("evict chunks: %w", err)
		}
		if s.curSize >= before {
			break
		}
	}

	s.chunks[chunk.ID] = chunk
	s.curSize += chunkSize

	// Add to chunk set
	if chunk.ParentID != "" {
		if set, ok := s.sets[chunk.ParentID]; ok {
			set.Chunks = append(set.Chunks, chunk.ID)
		}
	}

	return nil
}

// PutChunkSet stores a chunk set. Disk persistence runs without the write
// lock held so a slow/failing content dir doesn't wedge readers.
//
// When the store's DefaultChunkTTL is non-zero and the incoming set has
// TTL==0, we stamp the default. This is what keeps long sessions from
// accumulating stale stash entries: every Exec/Read/Fetch creates a set
// that expires after DefaultChunkTTL and gets reaped by PruneExpired (or
// by lazy-prune on the next Get*). Callers that want a truly persistent
// set still set TTL explicitly to a sentinel value; callers that already
// pass TTL>0 are unaffected.
func (s *Store) PutChunkSet(set *ChunkSet) error {
	if err := validateID(set.ID); err != nil {
		return err
	}

	s.mu.Lock()
	if set.TTL == 0 && s.defaultTTL > 0 {
		set.TTL = s.defaultTTL
	}
	s.sets[set.ID] = set
	path := s.path
	shouldPersist := path != "" && set.TTL == 0
	// Snapshot a copy under the lock so the subsequent disk write cannot race
	// with a concurrent mutation of set.Chunks via PutChunk.
	var snap ChunkSet
	if shouldPersist {
		snap = *set
		snap.Chunks = append([]string(nil), set.Chunks...)
	}
	s.mu.Unlock()

	if shouldPersist {
		return persistChunkSetToDisk(path, &snap)
	}
	return nil
}

// expired reports whether set has crossed its TTL window. TTL==0 means
// the set is persistent and never expires from the in-memory store.
// Callers must hold at least the read lock; this function does not lock.
func (s *Store) expired(set *ChunkSet, now time.Time) bool {
	if set == nil || set.TTL == 0 {
		return false
	}
	return set.Created.Add(set.TTL).Before(now)
}

// dropSetLocked removes a chunk-set and all its chunks from the in-memory
// store, decrementing curSize. Caller must hold s.mu.Lock().
func (s *Store) dropSetLocked(id string) {
	set, ok := s.sets[id]
	if !ok {
		return
	}
	for _, cid := range set.Chunks {
		if c, ok := s.chunks[cid]; ok {
			s.curSize -= int64(len(c.Body))
			delete(s.chunks, cid)
		}
	}
	delete(s.sets, id)
}

// GetChunk retrieves a chunk by ID. If the chunk's parent set has expired,
// the lookup misses and the set is reaped — lazy expiry keeps the hot path
// cheap and bounds the prune work to "as you consume it".
func (s *Store) GetChunk(id string) (*Chunk, bool) {
	s.mu.RLock()
	chunk, ok := s.chunks[id]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	parent, hasParent := s.sets[chunk.ParentID]
	if hasParent && s.expired(parent, time.Now()) {
		s.mu.RUnlock()
		// Re-acquire as writer to drop the expired set; another goroutine
		// may have already done it, in which case the second Get* returns
		// false anyway.
		s.mu.Lock()
		s.dropSetLocked(chunk.ParentID)
		s.mu.Unlock()
		return nil, false
	}
	s.mu.RUnlock()
	return chunk, true
}

// GetChunkSet retrieves a chunk set by ID. Same lazy-expiry as GetChunk.
func (s *Store) GetChunkSet(id string) (*ChunkSet, bool) {
	s.mu.RLock()
	set, ok := s.sets[id]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	if s.expired(set, time.Now()) {
		s.mu.RUnlock()
		s.mu.Lock()
		s.dropSetLocked(id)
		s.mu.Unlock()
		return nil, false
	}
	s.mu.RUnlock()
	return set, true
}

// PruneExpired walks the entire chunk-set table and drops any set whose
// TTL window has passed, returning the count removed. Intended to be
// called periodically by a daemon idle tick or before a stats dump;
// O(|sets|) so cheap in practice. Callers do not need to hold any lock.
func (s *Store) PruneExpired() int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var dropped []string
	for id, set := range s.sets {
		if s.expired(set, now) {
			dropped = append(dropped, id)
		}
	}
	for _, id := range dropped {
		s.dropSetLocked(id)
	}
	return len(dropped)
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

// persistChunkSet writes a chunk set to disk using the store's root path.
// Kept for internal callers that already hold the lock and want the original
// behavior; new call sites should use persistChunkSetToDisk with a snapshot.
func (s *Store) persistChunkSet(set *ChunkSet) error {
	return persistChunkSetToDisk(s.path, set)
}

// persistChunkSetToDisk gzip-encodes set at rootPath/<id>.json.gz. The caller
// owns concurrency: pass a snapshot if the set may mutate.
func persistChunkSetToDisk(rootPath string, set *ChunkSet) error {
	if err := validateID(set.ID); err != nil {
		return err
	}
	path := filepath.Join(rootPath, set.ID+".json.gz")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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
	if err := validateID(id); err != nil {
		return nil, err
	}
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
	// Snapshot what needs to be persisted under the lock, then release it
	// before the gzip+disk work — same pattern as PutChunkSet. Holding
	// s.mu across N disk writes would block every concurrent reader for
	// the entire flush.
	s.mu.Lock()
	path := s.path
	var snaps []*ChunkSet
	if path != "" {
		for _, set := range s.sets {
			if set.TTL == 0 {
				c := *set
				c.Chunks = append([]string(nil), set.Chunks...)
				snaps = append(snaps, &c)
			}
		}
	}
	s.mu.Unlock()

	for _, snap := range snaps {
		if err := persistChunkSetToDisk(path, snap); err != nil {
			return err
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
