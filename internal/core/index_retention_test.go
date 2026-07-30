package core

import (
	"fmt"
	"testing"
	"time"
)

// Retention regression: at MaxIndexDocs the index evicted strictly by age,
// so the OLDEST note went before the NEWEST routine tool call. On a real
// journal (193 automatic events to 1 deliberate note here) that retires the
// earliest decisions first — the ones most likely to explain why the code
// looks the way it does — while keeping every recent tool.read. The note
// stayed in the journal so dfmt_recall could still show it, but dfmt_search
// could no longer find it: memory decaying from the wrong end.

// addEvent indexes one event with a monotonically increasing ULID so "older"
// is unambiguous.
func addEvent(ix *Index, ts time.Time, typ EventType, tags []string, text string) string {
	id := string(NewULID(ts))
	ix.Add(Event{
		ID:       id,
		TS:       ts,
		Type:     typ,
		Priority: PriP3, // what dfmt_remember stores for every agent event
		Source:   SrcMCP,
		Tags:     tags,
		Data:     map[string]any{"message": text},
	})
	return id
}

func indexed(ix *Index, id string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	_, ok := ix.docLen[id]
	return ok
}

func TestEvictionKeepsNotesAndDropsToolCalls(t *testing.T) {
	orig := MaxIndexDocs
	MaxIndexDocs = 10
	defer func() { MaxIndexDocs = orig }()

	ix := NewIndex()
	base := time.Now().Add(-time.Hour)

	// The oldest document in the index is a decision — exactly what age-only
	// eviction threw away first.
	noteID := addEvent(ix, base, EvtNote, []string{"decision"}, "we chose the global daemon over per-project daemons")

	// Fill past the cap with routine tool calls, all newer than the note.
	var toolIDs []string
	for i := range 20 {
		toolIDs = append(toolIDs, addEvent(ix, base.Add(time.Duration(i+1)*time.Minute),
			EvtMCPCall, nil, fmt.Sprintf("tool call %d", i)))
	}

	if !indexed(ix, noteID) {
		t.Error("the decision note was evicted while newer routine tool calls survived")
	}
	evicted := 0
	for _, id := range toolIDs {
		if !indexed(ix, id) {
			evicted++
		}
	}
	if evicted == 0 {
		t.Error("nothing was evicted; the cap is not being enforced")
	}
	if got := ix.TotalDocs(); got > MaxIndexDocs {
		t.Errorf("TotalDocs = %d, want <= %d", got, MaxIndexDocs)
	}
}

// Within one tier the old rule still applies: oldest first.
func TestEvictionWithinTierIsOldestFirst(t *testing.T) {
	orig := MaxIndexDocs
	MaxIndexDocs = 5
	defer func() { MaxIndexDocs = orig }()

	ix := NewIndex()
	base := time.Now().Add(-time.Hour)
	var ids []string
	for i := range 8 {
		ids = append(ids, addEvent(ix, base.Add(time.Duration(i)*time.Minute), EvtMCPCall, nil, "same tier"))
	}

	if indexed(ix, ids[0]) {
		t.Error("the oldest event in the tier survived eviction")
	}
	if !indexed(ix, ids[len(ids)-1]) {
		t.Error("the newest event was evicted")
	}
}

// A tag-elevated note must outrank an untagged one, because that is the
// distinction the tag vocabulary exists to make.
func TestEvictionRespectsTagElevation(t *testing.T) {
	orig := MaxIndexDocs
	MaxIndexDocs = 6
	defer func() { MaxIndexDocs = orig }()

	ix := NewIndex()
	base := time.Now().Add(-time.Hour)

	elevated := addEvent(ix, base, EvtNote, []string{"summary"}, "session summary worth keeping")
	plain := addEvent(ix, base.Add(time.Minute), EvtNote, nil, "an untagged note")

	for i := range 10 {
		addEvent(ix, base.Add(time.Duration(i+2)*time.Minute), EvtMCPCall, nil, "filler")
	}

	if !indexed(ix, elevated) {
		t.Error("a note tagged `summary` (P2) was evicted")
	}
	if indexed(ix, plain) {
		t.Log("the untagged note was evicted before the tagged one, as intended")
	}
}

// Removal must actually clean the posting lists, or an evicted document
// keeps scoring as a search hit.
func TestRemoveClearsPostingLists(t *testing.T) {
	ix := NewIndex()
	base := time.Now()
	keep := addEvent(ix, base, EvtNote, nil, "alpha shared token")
	drop := addEvent(ix, base.Add(time.Minute), EvtNote, nil, "beta shared token")

	ix.Remove(drop)

	for _, hit := range ix.SearchBM25("shared token", 10) {
		if hit.ID == drop {
			t.Fatal("a removed document still scores as a BM25 hit")
		}
	}
	for _, hit := range ix.SearchTrigram("shared", 10) {
		if hit.ID == drop {
			t.Fatal("a removed document still scores as a trigram hit")
		}
	}
	if !indexed(ix, keep) {
		t.Error("removing one document dropped another")
	}
	if got := ix.TotalDocs(); got != 1 {
		t.Errorf("TotalDocs = %d, want 1", got)
	}
}

// avgDocLen is maintained arithmetically now; it must still match a fresh
// computation, or BM25 scores drift after every eviction.
func TestAvgDocLenStaysConsistentAcrossRemovals(t *testing.T) {
	ix := NewIndex()
	base := time.Now()
	ids := []string{
		addEvent(ix, base, EvtNote, nil, "one two three four five"),
		addEvent(ix, base.Add(time.Minute), EvtNote, nil, "six seven"),
		addEvent(ix, base.Add(2*time.Minute), EvtNote, nil, "eight nine ten eleven"),
	}
	ix.Remove(ids[1])

	ix.mu.RLock()
	defer ix.mu.RUnlock()
	total := 0
	for _, l := range ix.docLen {
		total += l
	}
	want := float64(total) / float64(len(ix.docLen))
	if diff := ix.avgDocLen - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("avgDocLen = %v, want %v (running sum drifted from the real total)", ix.avgDocLen, want)
	}
}

// A deserialized index has no tier information; eviction must still work and
// must not exceed the cap.
func TestEvictionAfterDeserialize(t *testing.T) {
	orig := MaxIndexDocs
	MaxIndexDocs = 8
	defer func() { MaxIndexDocs = orig }()

	src := NewIndex()
	base := time.Now().Add(-time.Hour)
	for i := range 6 {
		addEvent(src, base.Add(time.Duration(i)*time.Minute), EvtMCPCall, nil, "persisted event")
	}
	blob, err := src.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var loaded Index
	if err := loaded.UnmarshalJSON(blob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for i := range 10 {
		addEvent(&loaded, time.Now().Add(time.Duration(i)*time.Minute), EvtNote, []string{"decision"}, "post-load decision")
	}

	if got := loaded.TotalDocs(); got > MaxIndexDocs {
		t.Errorf("TotalDocs = %d, want <= %d after loading and adding past the cap", got, MaxIndexDocs)
	}
}
