package core

import (
	"testing"
	"time"
)

func TestSearchTypeRestrictsBeforeTopK(t *testing.T) {
	ix := NewIndex()
	base := time.Now()
	ix.Add(Event{ID: string(NewULID(base)), TS: base, Type: "tool.exec",
		Data: map[string]any{"code": "AUDIT_MARKER marker run"}})
	noteID := string(NewULID(base.Add(time.Second)))
	ix.Add(Event{ID: noteID, TS: base, Type: "note",
		Data: map[string]any{"message": "AUDIT_MARKER marker decision recorded here"}})
	ix.Add(Event{ID: string(NewULID(base.Add(2 * time.Second))), TS: base, Type: "tool.read",
		Data: map[string]any{"path": "unrelated.go"}})

	for name, hits := range map[string][]ScoredHit{
		"bm25":    ix.SearchBM25Type("marker", 1, "note"),
		"trigram": ix.SearchTrigramType("AUDIT_MARKER", 1, "note"),
	} {
		if len(hits) != 1 || hits[0].ID != noteID {
			t.Errorf("%s type:note = %+v, want only the note", name, hits)
		}
	}
	if hits := ix.SearchBM25Type("marker", 5, ""); len(hits) != 2 {
		t.Errorf("unfiltered = %d hits, want 2", len(hits))
	}
	if hits := ix.SearchTrigramType("AUDIT_MARKER", 5, "missing"); len(hits) != 0 {
		t.Errorf("unknown type returned %+v", hits)
	}
}
