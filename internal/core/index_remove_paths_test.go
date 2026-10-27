package core

import (
	"encoding/json"
	"testing"
	"time"
)

func noteEvent(id, msg string) Event {
	return Event{ID: id, TS: time.Now(), Type: EvtNote, Source: SrcMCP, Data: map[string]any{"message": msg}}
}

// A document restored from a persisted index has posting lists but no
// reverse maps; Remove must still purge it (the legacy sweep), or it keeps
// scoring as a hit for a document that is gone.
func TestIndexRemoveRestoredDocumentSweepsPostings(t *testing.T) {
	ix := NewIndex()
	ix.Add(noteEvent("01A", "zebra crossing"))
	ix.Add(noteEvent("01B", "zebra stripes"))
	data, err := json.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewIndex()
	if err := json.Unmarshal(data, restored); err != nil {
		t.Fatal(err)
	}
	restored.Remove("01A")
	for _, h := range restored.SearchBM25("zebra", 10) {
		if h.ID == "01A" {
			t.Fatal("removed document still scores after the legacy sweep")
		}
	}
	if hits := restored.SearchBM25("zebra", 10); len(hits) != 1 || hits[0].ID != "01B" {
		t.Errorf("survivor hits = %+v, want only 01B", hits)
	}
}

// Posting lists are ULID-ordered in practice; out-of-order IDs must fall
// back to a linear scan rather than leave the entry behind, and a removal
// from the middle must keep IDs and TFs parallel.
func TestIndexRemoveOutOfOrderAndMiddle(t *testing.T) {
	ix := NewIndex()
	for _, id := range []string{"01C", "01A", "01B"} { // deliberately unsorted
		ix.Add(noteEvent(id, "walrus tusk"))
	}
	ix.Remove("01A") // middle of the posting list, not where SearchStrings looks
	ix.Remove("01Z") // unknown: a no-op
	got := map[string]bool{}
	for _, h := range ix.SearchBM25("walrus", 10) {
		got[h.ID] = true
	}
	if got["01A"] || !got["01B"] || !got["01C"] || len(got) != 2 {
		t.Errorf("hits after removal = %v, want 01B and 01C", got)
	}
}

func TestIndexMetaAndEventExcerpt(t *testing.T) {
	ix := NewIndex()
	ix.Add(noteEvent("01M", "remember the meta"))
	if m := ix.Meta("01M"); m.Type != string(EvtNote) || m.Source != string(SrcMCP) {
		t.Errorf("Meta = %+v", m)
	}
	if m := ix.Meta("missing"); m != (DocMeta{}) {
		t.Errorf("Meta(missing) = %+v, want zero", m)
	}
	if got := EventExcerpt(noteEvent("x", "a short note")); got != "a short note" {
		t.Errorf("EventExcerpt = %q", got)
	}
}

// At the cap, eviction drains the lowest tier first. Documents restored
// from a persisted index may carry no tier; the cap must still hold (the
// oldest-ID fallback) instead of growing without bound.
func TestIndexEvictionAtCapIncludingRestoredDocs(t *testing.T) {
	prev := MaxIndexDocs
	MaxIndexDocs = 2
	defer func() { MaxIndexDocs = prev }()

	ix := NewIndex()
	ix.Add(noteEvent("01A", "alpha one"))
	ix.Add(noteEvent("01B", "beta two"))
	data, err := json.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewIndex()
	if err := json.Unmarshal(data, restored); err != nil {
		t.Fatal(err)
	}
	restored.Add(noteEvent("01C", "gamma three"))
	if n := restored.TotalDocs(); n != 2 {
		t.Fatalf("TotalDocs = %d, want the cap (2)", n)
	}
	if hits := restored.SearchBM25("alpha", 5); len(hits) != 0 {
		t.Errorf("oldest restored document survived eviction: %+v", hits)
	}
	if hits := restored.SearchBM25("gamma", 5); len(hits) != 1 {
		t.Error("the new document was not indexed")
	}
}
