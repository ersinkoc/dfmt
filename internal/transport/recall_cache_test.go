package transport

import (
	"context"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

// The journal-stream count is the cost Recall was paying on every call:
// every segment read, every line unmarshalled, every event's signature
// recomputed. countingJournal (mcp_telemetry_test.go) already counts it.

func recallFixture(t *testing.T) (*Handlers, *countingJournal) {
	t.Helper()
	j := &countingJournal{}
	h := NewHandlers(core.NewIndex(), j, nil)
	for i := range 5 {
		if _, err := h.Remember(context.Background(), RememberParams{
			Type:    "note",
			Message: strings.Repeat("event ", i+1),
			Tags:    []string{"finding"},
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	j.streamCalls = 0 // ignore any streaming done during setup
	return h, j
}

func TestRecallServesFromCacheWhileJournalIsUnchanged(t *testing.T) {
	h, j := recallFixture(t)
	ctx := context.Background()

	first, err := h.Recall(ctx, RecallParams{Budget: 4000})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if j.streamCalls != 1 {
		t.Fatalf("streams = %d after the first recall, want 1", j.streamCalls)
	}

	second, err := h.Recall(ctx, RecallParams{Budget: 4000})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if j.streamCalls != 1 {
		t.Errorf("streams = %d, want 1: an unchanged journal must not be re-streamed", j.streamCalls)
	}
	if first.Snapshot != second.Snapshot {
		t.Error("cached snapshot differs from the freshly built one")
	}
}

// A new event must invalidate: the cursor moves, so the next call rebuilds.
func TestRecallCacheInvalidatesOnNewEvent(t *testing.T) {
	h, j := recallFixture(t)
	ctx := context.Background()

	before, _ := h.Recall(ctx, RecallParams{Budget: 4000})
	if _, err := h.Remember(ctx, RememberParams{
		Type:    "note",
		Message: "a brand new decision that must appear",
		Tags:    []string{"decision"},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	after, err := h.Recall(ctx, RecallParams{Budget: 4000})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if j.streamCalls != 2 {
		t.Errorf("streams = %d, want 2: a new event must invalidate the cache", j.streamCalls)
	}
	if !strings.Contains(after.Snapshot, "brand new decision") {
		t.Error("the new event is missing from the rebuilt snapshot")
	}
	if before.Snapshot == after.Snapshot {
		t.Error("snapshot unchanged after appending an event")
	}
}

// Different budgets and formats are different renderings of the same data
// and must not share a cache slot.
func TestRecallCacheKeyedByBudgetAndFormat(t *testing.T) {
	h, j := recallFixture(t)
	ctx := context.Background()

	wide, _ := h.Recall(ctx, RecallParams{Budget: 4000})
	narrow, _ := h.Recall(ctx, RecallParams{Budget: 200})
	asJSON, _ := h.Recall(ctx, RecallParams{Budget: 4000, Format: "json"})

	if j.streamCalls != 3 {
		t.Errorf("streams = %d, want 3 (one per distinct budget/format)", j.streamCalls)
	}
	if wide.Snapshot == narrow.Snapshot {
		t.Error("a 200-byte budget produced the same snapshot as a 4000-byte one")
	}
	if asJSON.Format != "json" || asJSON.Snapshot == wide.Snapshot {
		t.Error("json format shared the markdown cache entry")
	}

	// Repeats of each still hit.
	_, _ = h.Recall(ctx, RecallParams{Budget: 4000})
	_, _ = h.Recall(ctx, RecallParams{Budget: 200})
	if j.streamCalls != 3 {
		t.Errorf("streams = %d, want 3: repeats must be served from cache", j.streamCalls)
	}
}

// Swapping the redactor must drop cached snapshots: they were rendered
// through the old one and the journal cursor has not moved.
func TestRecallCacheClearedOnRedactorChange(t *testing.T) {
	h, j := recallFixture(t)
	ctx := context.Background()

	_, _ = h.Recall(ctx, RecallParams{Budget: 4000})
	h.SetRedactor(nil)
	_, _ = h.Recall(ctx, RecallParams{Budget: 4000})

	if j.streamCalls != 2 {
		t.Errorf("streams = %d, want 2: a redactor swap must invalidate cached snapshots", j.streamCalls)
	}
}
