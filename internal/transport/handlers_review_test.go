package transport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestStatsCacheReturnsMemoisedResult pins finding #8: Stats() must memoise
// across the dashboard's poll interval. Within statsTTL, a second call
// returns the cached aggregation even when the journal has new events
// since the first call. Outside statsTTL, the next call recomputes.
func TestStatsCacheReturnsMemoisedResult(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(filepath.Join(tmp, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	// Generous TTL so the in-test second call definitely hits the cache.
	prevTTL := statsTTL
	statsTTL = 30 * time.Second
	defer func() { statsTTL = prevTTL }()

	ctx := context.Background()
	mkEvent := func(id string) core.Event {
		return core.Event{
			ID:       id,
			TS:       time.Now(),
			Type:     core.EvtNote,
			Priority: core.PriP3,
			Source:   core.SrcCLI,
		}
	}
	if err := journal.Append(ctx, mkEvent(string(core.NewULID(time.Now())))); err != nil {
		t.Fatalf("seed Append: %v", err)
	}

	first, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("first Stats: %v", err)
	}
	if first.EventsTotal != 1 {
		t.Fatalf("first Stats EventsTotal = %d, want 1", first.EventsTotal)
	}

	// Append a second event directly to the journal — the cache MUST hide
	// it from the next Stats call.
	if err := journal.Append(ctx, mkEvent(string(core.NewULID(time.Now())))); err != nil {
		t.Fatalf("second Append: %v", err)
	}

	second, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("second Stats: %v", err)
	}
	if second.EventsTotal != 1 {
		t.Fatalf("cached Stats EventsTotal = %d, want 1 (cache should hide new event)", second.EventsTotal)
	}

	// Force expiry: now the cache should miss and the new count surface.
	statsTTL = time.Nanosecond
	time.Sleep(2 * time.Millisecond) // walk past the nanosecond TTL
	third, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("third Stats: %v", err)
	}
	if third.EventsTotal != 2 {
		t.Fatalf("post-expiry Stats EventsTotal = %d, want 2", third.EventsTotal)
	}
}

// TestStatsCacheNoCacheBypassReturnsFresh pins the StatsParams.NoCache
// behavior: when set, the handler MUST skip the cache-read path even
// inside the TTL window, so a `dfmt stats` CLI call after a fresh
// journal append sees the new event instead of the 5-second-stale
// memoised count.
//
// Setup mirrors TestStatsCacheReturnsMemoisedResult: same generous
// TTL, same seed-then-append pattern. The contrast is that the second
// call passes NoCache=true and asserts EventsTotal advanced, whereas
// the cache test asserts it did NOT advance.
func TestStatsCacheNoCacheBypassReturnsFresh(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(filepath.Join(tmp, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	prevTTL := statsTTL
	statsTTL = 30 * time.Second
	defer func() { statsTTL = prevTTL }()

	ctx := context.Background()
	mkEvent := func() core.Event {
		return core.Event{
			ID:       string(core.NewULID(time.Now())),
			TS:       time.Now(),
			Type:     core.EvtNote,
			Priority: core.PriP3,
			Source:   core.SrcCLI,
		}
	}
	if err := journal.Append(ctx, mkEvent()); err != nil {
		t.Fatalf("seed Append: %v", err)
	}

	// Prime the cache via a default (cached) call.
	primed, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("primed Stats: %v", err)
	}
	if primed.EventsTotal != 1 {
		t.Fatalf("primed EventsTotal = %d, want 1", primed.EventsTotal)
	}

	// New event lands while we're still inside the TTL window. A cached
	// call would hide it (proven by TestStatsCacheReturnsMemoisedResult);
	// NoCache=true must surface it.
	if err := journal.Append(ctx, mkEvent()); err != nil {
		t.Fatalf("second Append: %v", err)
	}

	fresh, err := h.Stats(ctx, StatsParams{NoCache: true})
	if err != nil {
		t.Fatalf("NoCache Stats: %v", err)
	}
	if fresh.EventsTotal != 2 {
		t.Fatalf("NoCache EventsTotal = %d, want 2 (cache bypass should surface new event)", fresh.EventsTotal)
	}

	// Side-check: NoCache does NOT update the shared cache (TRN-1 fix):
	// a `no_cache: true` call must not poison the cache for another project.
	// The next default call should still return the primed cached value.
	again, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("post-bypass cached Stats: %v", err)
	}
	if again.EventsTotal != 1 {
		t.Fatalf("post-bypass cached EventsTotal = %d, want 1 (NoCache must not update shared cache — TRN-1)", again.EventsTotal)
	}
}

// TestStatsCacheIsKeyedByProject covers TRN-1: in global-daemon mode one
// Handlers serves every project, so the stats cache MUST be keyed by project
// path. Without keying, a stats call for project B within statsTTL of a call
// for project A returns A's numbers. The NoCache store must also be skipped
// so a `no_cache: true` call for one project does not poison the cache for
// another.
func TestStatsCacheIsKeyedByProject(t *testing.T) {
	prevTTL := statsTTL
	statsTTL = 30 * time.Second
	defer func() { statsTTL = prevTTL }()

	tmpA := t.TempDir()
	journalA, err := core.OpenJournal(filepath.Join(tmpA, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal A: %v", err)
	}
	defer journalA.Close()

	tmpB := t.TempDir()
	journalB, err := core.OpenJournal(filepath.Join(tmpB, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal B: %v", err)
	}
	defer journalB.Close()

	// ProjA gets 1 event; projB gets 2 events.
	mkEvent := func() core.Event {
		return core.Event{
			ID:       string(core.NewULID(time.Now())),
			TS:       time.Now(),
			Type:     core.EvtNote,
			Priority: core.PriP3,
			Source:   core.SrcCLI,
		}
	}
	ctx := context.Background()
	if err := journalA.Append(ctx, mkEvent()); err != nil {
		t.Fatalf("Append A: %v", err)
	}
	if err := journalB.Append(ctx, mkEvent()); err != nil {
		t.Fatalf("Append B1: %v", err)
	}
	if err := journalB.Append(ctx, mkEvent()); err != nil {
		t.Fatalf("Append B2: %v", err)
	}

	h := NewHandlers(nil, nil, nil)
	h.SetResourceFetcher(func(projectID string) (Bundle, error) {
		switch projectID {
		case "/projA":
			return Bundle{Journal: journalA, ProjectPath: "/projA"}, nil
		case "/projB":
			return Bundle{Journal: journalB, ProjectPath: "/projB"}, nil
		default:
			return Bundle{}, errNoProject
		}
	})

	ctxA := WithProjectID(ctx, "/projA")
	ctxB := WithProjectID(ctx, "/projB")

	// Prime the cache for project A.
	respA, err := h.Stats(ctxA, StatsParams{})
	if err != nil {
		t.Fatalf("Stats A: %v", err)
	}
	if respA.EventsTotal != 1 {
		t.Fatalf("Stats A EventsTotal = %d, want 1", respA.EventsTotal)
	}

	// Project B must NOT see A's cached result.
	respB, err := h.Stats(ctxB, StatsParams{})
	if err != nil {
		t.Fatalf("Stats B: %v", err)
	}
	if respB.EventsTotal != 2 {
		t.Fatalf("Stats B EventsTotal = %d, want 2 — cache returned project A's result (TRN-1)", respB.EventsTotal)
	}
}

// TestStatsCacheClonesMaps pins the defensive-copy contract: callers
// mutating the returned maps must not corrupt the cache for the next
// caller. Without cloneStatsResponse, two concurrent dashboard calls
// could see each other's writes.
func TestStatsCacheClonesMaps(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(filepath.Join(tmp, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	prevTTL := statsTTL
	statsTTL = 30 * time.Second
	defer func() { statsTTL = prevTTL }()

	ctx := context.Background()
	if err := journal.Append(ctx, core.Event{
		ID: string(core.NewULID(time.Now())), TS: time.Now(),
		Type: core.EvtNote, Priority: core.PriP3, Source: core.SrcCLI,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	first, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("first Stats: %v", err)
	}
	// Mutate the returned map. If the cache is shared, the next call
	// would see the poisoned key.
	first.EventsByType["poisoned"] = 999

	second, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatalf("second Stats: %v", err)
	}
	if _, leaked := second.EventsByType["poisoned"]; leaked {
		t.Fatalf("cache returned a shared map; mutation by first caller leaked into second")
	}
}

func TestStatsCacheCapBoundsProjectEntries(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	now := time.Now()
	for i := 0; i < statsCacheCap; i++ {
		h.statsCache[string(rune('a'+i))] = &statsCacheEntry{resp: &StatsResponse{}, at: now}
	}

	journal, err := core.OpenJournal(filepath.Join(t.TempDir(), "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	if err := journal.Append(context.Background(), core.Event{
		ID: string(core.NewULID(time.Now())), TS: time.Now(),
		Type: core.EvtNote, Priority: core.PriP3, Source: core.SrcCLI,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	h.SetResourceFetcher(func(projectID string) (Bundle, error) {
		return Bundle{Journal: journal, ProjectPath: "/new-project"}, nil
	})

	if _, err := h.Stats(WithProjectID(context.Background(), "/new-project"), StatsParams{}); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(h.statsCache) > statsCacheCap {
		t.Fatalf("stats cache len = %d, want <= %d", len(h.statsCache), statsCacheCap)
	}
}

// TestRecallBoundsBufferedEvents pins finding #7's resolution: Recall
// must not buffer the entire journal into memory. With more events than
// the smallest tier cap, the function still returns successfully and
// produces a non-empty snapshot.
//
// Historical context: this test was written against an earlier stopgap
// that capped a single global buffer at 5000 events and could lose
// high-priority events past that index. The recall handler now uses
// per-tier buckets with FIFO eviction (handlers_recall_tiers_test.go
// covers correctness under that scheme); this test continues to pin
// the fundamental "don't OOM on big journals" invariant.
func TestRecallBoundsBufferedEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bulk-event Recall test in -short mode")
	}
	tmp := t.TempDir()
	journal, err := core.OpenJournal(filepath.Join(tmp, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	// 5050 events — comfortably past the 5000 cap baked into Recall.
	ctx := context.Background()
	const n = 5050
	for i := 0; i < n; i++ {
		ev := core.Event{
			ID:       string(core.NewULID(time.Now())),
			TS:       time.Now(),
			Type:     core.EvtNote,
			Priority: core.PriP3,
			Source:   core.SrcCLI,
		}
		if err := journal.Append(ctx, ev); err != nil {
			t.Fatalf("seed Append %d: %v", i, err)
		}
	}

	// Tight budget so the rendering loop exits early — what we're really
	// testing is that the journal-stream phase doesn't OOM or run
	// unbounded on huge input.
	resp, err := h.Recall(ctx, RecallParams{Budget: 1024})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if resp == nil || resp.Snapshot == "" {
		t.Fatalf("Recall produced empty snapshot")
	}
}
