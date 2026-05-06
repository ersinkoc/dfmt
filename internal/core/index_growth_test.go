package core

import (
	"strconv"
	"testing"
	"time"
)

// TestIndex_V13EvictsOldestAtCap pins the V-13 contract: when the
// document cap is reached, Add evicts the oldest doc (smallest ULID)
// before indexing the new one. Lower the cap to 3 in this test so we
// don't have to insert 100k events.
func TestIndex_V13EvictsOldestAtCap(t *testing.T) {
	originalCap := MaxIndexDocs
	MaxIndexDocs = 3
	t.Cleanup(func() { MaxIndexDocs = originalCap })

	ix := NewIndex()

	// IDs are constructed lexicographically increasing so Add() drives
	// the same eviction-by-smallest-ID path real ULIDs would take. The
	// ULID encoding is time-prefix Crockford base32, also lexicographic.
	add := func(id, msg string) {
		ix.Add(Event{
			ID:   id,
			TS:   time.Now(),
			Type: EvtNote,
			Data: map[string]any{"message": msg},
		})
	}

	add("01AAAAAAAAAA", "first")
	add("01BBBBBBBBBB", "second")
	add("01CCCCCCCCCC", "third")
	if got := ix.TotalDocs(); got != 3 {
		t.Fatalf("TotalDocs = %d; want 3", got)
	}

	// Adding the fourth must evict the first.
	add("01DDDDDDDDDD", "fourth")
	if got := ix.TotalDocs(); got != 3 {
		t.Errorf("TotalDocs = %d after eviction; want 3 (cap held)", got)
	}
	if got := ix.Excerpt("01AAAAAAAAAA"); got != "" {
		t.Errorf("oldest doc not evicted; Excerpt = %q", got)
	}
	if got := ix.Excerpt("01DDDDDDDDDD"); got == "" {
		t.Errorf("newest doc not indexed: Excerpt empty")
	}
}

// TestIndex_V13StressFloodStaysCapped pins the bound under a flood:
// inserting 10 × cap docs must leave the index size at exactly the cap,
// not somewhere in the middle. Catches a regression where the eviction
// path is conditional on something other than the cap.
func TestIndex_V13StressFloodStaysCapped(t *testing.T) {
	originalCap := MaxIndexDocs
	MaxIndexDocs = 50
	t.Cleanup(func() { MaxIndexDocs = originalCap })

	ix := NewIndex()
	const inserts = 500
	for i := 0; i < inserts; i++ {
		// Pad the ID so all 500 stay 4 digits and lexicographic order
		// matches numeric order.
		id := "id" + leftPad(strconv.Itoa(i), 4)
		ix.Add(Event{
			ID:   id,
			TS:   time.Now(),
			Type: EvtNote,
			Data: map[string]any{"message": "flood-" + id},
		})
	}

	if got := ix.TotalDocs(); got != MaxIndexDocs {
		t.Errorf("TotalDocs after flood = %d; want %d (cap)", got, MaxIndexDocs)
	}
	// The last `MaxIndexDocs` inserts must be present; everything earlier
	// must be evicted.
	for i := inserts - MaxIndexDocs; i < inserts; i++ {
		id := "id" + leftPad(strconv.Itoa(i), 4)
		if ix.Excerpt(id) == "" {
			t.Errorf("recent doc %s missing post-flood", id)
		}
	}
	for i := 0; i < inserts-MaxIndexDocs; i++ {
		id := "id" + leftPad(strconv.Itoa(i), 4)
		if ix.Excerpt(id) != "" {
			t.Errorf("evicted doc %s still present post-flood", id)
		}
	}
}

func leftPad(s string, width int) string {
	for len(s) < width {
		s = "0" + s
	}
	return s
}
