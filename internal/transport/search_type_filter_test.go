package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestSearchTypeFilterFindsNoteBuriedUnderToolEvents: the type filter ran
// over a bounded over-fetch of the top-K (at most 500 candidates). With more
// short tool events than that matching the query, BM25's length
// normalization ranked them all above a long note, and type:"note" came
// back empty even though the note matched.
func TestSearchTypeFilterFindsNoteBuriedUnderToolEvents(t *testing.T) {
	idx := core.NewIndex()
	base := time.Now()
	for i := 0; i < 800; i++ {
		idx.Add(core.Event{
			ID:   string(core.NewULID(base.Add(time.Duration(i) * time.Millisecond))),
			TS:   base,
			Type: "tool.exec",
			Data: map[string]any{"code": "timeout"},
		})
	}
	// Unrelated traffic keeps "timeout" a minority term (positive IDF), as
	// on a real journal.
	for i := 0; i < 2000; i++ {
		idx.Add(core.Event{
			ID:   string(core.NewULID(base.Add(time.Minute + time.Duration(i)*time.Millisecond))),
			TS:   base,
			Type: "tool.read",
			Data: map[string]any{"path": "internal/other.go"},
		})
	}
	noteID := string(core.NewULID(base.Add(time.Hour)))
	idx.Add(core.Event{
		ID:   noteID,
		TS:   base,
		Type: "note",
		Data: map[string]any{"message": "decided the exec timeout default after measuring " +
			strings.Repeat("several long builds and soak runs across the matrix ", 20)},
	})

	h := NewHandlers(idx, &mockJournal{}, nil)
	resp, err := h.Search(context.Background(), SearchParams{Query: "timeout", Type: "note", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != noteID {
		t.Fatalf("type:note results = %+v, want the one note", resp.Results)
	}

	// Unfiltered ranking is unchanged: the short tool events still lead.
	all, _ := h.Search(context.Background(), SearchParams{Query: "timeout", Limit: 5})
	for _, r := range all.Results {
		if r.ID == noteID {
			t.Errorf("unfiltered top-5 changed: note ranked into it (%+v)", all.Results)
		}
	}
}
