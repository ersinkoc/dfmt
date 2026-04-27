package content

import (
	"testing"
	"time"
)

// TestStore_DefaultTTLStampsIncomingSets verifies the opt-in default TTL
// is applied to TTL==0 sets at PutChunkSet time. Sets with explicit TTL
// must NOT be overridden — caller intent wins.
func TestStore_DefaultTTLStampsIncomingSets(t *testing.T) {
	s, err := NewStore(StoreOptions{DefaultChunkTTL: time.Hour})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// TTL==0 incoming → gets stamped with default.
	auto := &ChunkSet{ID: "auto1", Created: time.Now()}
	if err := s.PutChunkSet(auto); err != nil {
		t.Fatalf("PutChunkSet: %v", err)
	}
	got, ok := s.GetChunkSet("auto1")
	if !ok {
		t.Fatal("auto1 missing right after Put")
	}
	if got.TTL != time.Hour {
		t.Errorf("auto1 TTL = %v, want 1h (the default stamp)", got.TTL)
	}

	// Explicit TTL → preserved as-is.
	explicit := &ChunkSet{ID: "explicit1", Created: time.Now(), TTL: 5 * time.Minute}
	if err := s.PutChunkSet(explicit); err != nil {
		t.Fatalf("PutChunkSet: %v", err)
	}
	got2, _ := s.GetChunkSet("explicit1")
	if got2.TTL != 5*time.Minute {
		t.Errorf("explicit1 TTL was overwritten; got %v, want 5m", got2.TTL)
	}
}

// TestStore_NoDefaultTTLLeavesPersistentBehavior verifies the zero-default
// (backwards-compat) path: sets with TTL==0 stay TTL==0 and follow the
// persistent-on-disk pattern the existing code relies on.
func TestStore_NoDefaultTTLLeavesPersistentBehavior(t *testing.T) {
	s, err := NewStore(StoreOptions{}) // no DefaultChunkTTL
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	set := &ChunkSet{ID: "p1", Created: time.Now()}
	if err := s.PutChunkSet(set); err != nil {
		t.Fatalf("PutChunkSet: %v", err)
	}
	got, _ := s.GetChunkSet("p1")
	if got.TTL != 0 {
		t.Errorf("zero default must not stamp TTL; got %v", got.TTL)
	}
}

// TestStore_GetChunkSetEvictsExpired verifies lazy expiry: a set whose
// TTL window has passed disappears the next time anyone Get*s it.
func TestStore_GetChunkSetEvictsExpired(t *testing.T) {
	s, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Stamp the set with a 1ms TTL and a Created in the past so it's
	// already expired by the time we Get it. Avoids time.Sleep.
	set := &ChunkSet{
		ID:      "ephemeral1",
		Created: time.Now().Add(-time.Hour),
		TTL:     time.Millisecond,
	}
	if err := s.PutChunkSet(set); err != nil {
		t.Fatalf("PutChunkSet: %v", err)
	}
	if _, ok := s.GetChunkSet("ephemeral1"); ok {
		t.Error("expired set must miss on Get; instead it returned hit")
	}
	// And must be gone from the table.
	s.mu.RLock()
	_, present := s.sets["ephemeral1"]
	s.mu.RUnlock()
	if present {
		t.Error("expired set must have been reaped from table after lazy prune")
	}
}

// TestStore_GetChunkEvictsViaParent verifies expiry triggers from a chunk
// lookup too: chunks whose parent has expired return false and the parent
// (and its chunks) drop from the store.
func TestStore_GetChunkEvictsViaParent(t *testing.T) {
	s, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	parent := &ChunkSet{
		ID:      "parent1",
		Created: time.Now().Add(-time.Hour),
		TTL:     time.Millisecond,
	}
	if err := s.PutChunkSet(parent); err != nil {
		t.Fatal(err)
	}
	chunk := &Chunk{
		ID:       "chunkA",
		ParentID: "parent1",
		Body:     "hello",
		Kind:     ChunkKindText,
		Created:  time.Now(),
	}
	if err := s.PutChunk(chunk); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetChunk("chunkA"); ok {
		t.Error("chunk with expired parent must miss")
	}
	// Both chunk and parent must be reaped, curSize drained.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, present := s.chunks["chunkA"]; present {
		t.Error("chunk should be reaped along with its expired parent")
	}
	if _, present := s.sets["parent1"]; present {
		t.Error("parent set should be reaped on lazy prune")
	}
	if s.curSize != 0 {
		t.Errorf("curSize must drain to 0 after eviction; got %d", s.curSize)
	}
}

// TestStore_PruneExpiredCountsDropped verifies the bulk prune helper.
func TestStore_PruneExpiredCountsDropped(t *testing.T) {
	s, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// 3 expired, 2 live.
	now := time.Now()
	for i, exp := range []bool{true, true, false, true, false} {
		id := "id" + string(rune('1'+i))
		set := &ChunkSet{ID: id, Created: now.Add(-time.Hour), TTL: time.Hour}
		if exp {
			set.TTL = time.Millisecond // expired immediately
		}
		if err := s.PutChunkSet(set); err != nil {
			t.Fatal(err)
		}
	}
	dropped := s.PruneExpired()
	if dropped != 3 {
		t.Errorf("PruneExpired returned %d, want 3", dropped)
	}
	s.mu.RLock()
	remaining := len(s.sets)
	s.mu.RUnlock()
	if remaining != 2 {
		t.Errorf("expected 2 live sets after prune; got %d", remaining)
	}
}

// TestStore_PruneExpiredZeroTTLImmortal verifies persistent (TTL=0) sets
// never get pruned, regardless of how old they are.
func TestStore_PruneExpiredZeroTTLImmortal(t *testing.T) {
	s, err := NewStore(StoreOptions{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ancient := &ChunkSet{
		ID:      "ancient",
		Created: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		TTL:     0, // persistent
	}
	if err := s.PutChunkSet(ancient); err != nil {
		t.Fatal(err)
	}
	if dropped := s.PruneExpired(); dropped != 0 {
		t.Errorf("PruneExpired must NOT drop TTL=0 sets; got %d", dropped)
	}
	if _, ok := s.GetChunkSet("ancient"); !ok {
		t.Error("persistent set must survive prune")
	}
}
