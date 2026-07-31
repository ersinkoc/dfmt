package transport

import (
	"context"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

// These tests cover the retrieval half of dfmt_remember: a memory that can be
// written but not found again is not memory.
//
// What they pin, all observed on a real journal (193 automatic tool.* events,
// 1 deliberate note):
//   - a search hit said nothing about WHAT it was (Type/Source were declared
//     on the wire struct and never assigned), so the note was
//     indistinguishable from the tool calls around it;
//   - tool.exec/grep/glob hits carried an excerpt of their own type name
//     ("tool.grep") because only data["path"] was consulted;
//   - a query for "timeout" returned six tool events and not the note that
//     discussed timeouts at length — short documents win BM25's length
//     normalization, and the corpus is overwhelmingly short documents.

// rememberSearchFixture wires an in-memory journal + index and records one
// note among a pile of tool events, mirroring the real ratio.
func rememberSearchFixture(t *testing.T) *Handlers {
	t.Helper()
	idx := core.NewIndex()
	h := NewHandlers(idx, &mockJournal{}, nil)

	ctx := context.Background()
	for range 12 {
		if _, err := h.Remember(ctx, RememberParams{
			Type:    "tool.exec",
			Message: "",
			Data:    map[string]any{"code": "go test ./... -timeout 30s"},
		}); err != nil {
			t.Fatalf("seed tool event: %v", err)
		}
	}
	if _, err := h.Remember(ctx, RememberParams{
		Type:    "note",
		Message: "The exec timeout was unenforced: killing the shell left the grandchild holding the stdout pipe, so the read blocked for the command's full duration.",
		Tags:    []string{"finding", "summary"},
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	return h
}

func TestSearchHitsCarryTypeAndSource(t *testing.T) {
	h := rememberSearchFixture(t)

	resp, err := h.Search(context.Background(), SearchParams{Query: "timeout", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("no results for a term present in every seeded event")
	}
	for _, hit := range resp.Results {
		if hit.Type == "" {
			t.Errorf("hit %s has no Type: the field is on the wire struct precisely so a "+
				"note can be told apart from a tool call", hit.ID)
		}
		if hit.Source == "" {
			t.Errorf("hit %s has no Source", hit.ID)
		}
	}
}

// The type filter is what makes a deliberate memory findable in a journal
// dominated by automatic events.
func TestSearchTypeFilterIsolatesNotes(t *testing.T) {
	h := rememberSearchFixture(t)

	unfiltered, err := h.Search(context.Background(), SearchParams{Query: "timeout", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	notes, err := h.Search(context.Background(), SearchParams{Query: "timeout", Limit: 5, Type: "note"})
	if err != nil {
		t.Fatalf("Search with type filter: %v", err)
	}

	if len(notes.Results) == 0 {
		t.Fatal("type filter returned nothing; the note is unreachable")
	}
	for _, hit := range notes.Results {
		if hit.Type != "note" {
			t.Errorf("type filter leaked a %q hit", hit.Type)
		}
	}
	// The point of the filter: the note is not guaranteed to surface without it.
	sawNoteUnfiltered := false
	for _, hit := range unfiltered.Results {
		if hit.Type == "note" {
			sawNoteUnfiltered = true
		}
	}
	t.Logf("note present in unfiltered top-5: %v (filter exists because this is not guaranteed)", sawNoteUnfiltered)
}

// A note's own text must be what its excerpt shows.
func TestSearchExcerptShowsNoteMessage(t *testing.T) {
	h := rememberSearchFixture(t)

	resp, err := h.Search(context.Background(), SearchParams{Query: "grandchild", Limit: 3, Type: "note"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("the note was not found by a word from its own message")
	}
	if !strings.Contains(resp.Results[0].Excerpt, "exec timeout") {
		t.Errorf("excerpt = %q, want the start of the note's message", resp.Results[0].Excerpt)
	}
}

// Regression: a tool event's excerpt was its own type name, which tells the
// agent nothing it did not already have from the Type field.
func TestSearchExcerptShowsToolCommand(t *testing.T) {
	h := rememberSearchFixture(t)

	resp, err := h.Search(context.Background(), SearchParams{Query: "timeout", Limit: 5, Type: "tool.exec"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("no tool.exec hits")
	}
	if !strings.Contains(resp.Results[0].Excerpt, "go test") {
		t.Errorf("excerpt = %q, want the command the event recorded", resp.Results[0].Excerpt)
	}
}
