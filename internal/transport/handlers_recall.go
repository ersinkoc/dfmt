package transport

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/retrieve"
)

type RecallParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Budget    int    `json:"budget,omitempty"`
	Format    string `json:"format,omitempty"`
}

// RecallResponse is the response for the Recall method.
type RecallResponse struct {
	Snapshot string `json:"snapshot"`
	Format   string `json:"format"`
}

// Recall builds a session snapshot with tier-ordered greedy fill.
func (h *Handlers) Recall(ctx context.Context, params RecallParams) (_ *RecallResponse, err error) {
	defer recordToolCall("recall", ctx, &err, time.Now())
	h.touch()
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Journal == nil {
		return nil, errNoProject
	}
	budget := h.recallBudget(params.Budget)
	format := h.recallFormat(params.Format)

	// Per-tier streaming with FIFO eviction (closes review finding #7).
	//
	// Previous stopgap: read up to recallMaxBufferedEvents=5000 events
	// off the journal stream, then sort the truncated slice by priority.
	// On long-running projects with >5000 events that meant P1
	// decisions past index 5000 were silently dropped — the priority
	// sort had nothing to elevate.
	//
	// New behavior: classify each event as we stream and place it in
	// its tier's bucket. Each bucket has its own cap; on overflow we
	// FIFO-evict the oldest in-bucket event (Recall serves the "most
	// relevant", which for tiers means more recent within-tier).
	// Memory is bounded by the sum of tier caps, independent of
	// journal length. P1 events from any journal position survive as
	// long as the total P1 count fits p1Cap.
	//
	// streamCtx is a child of ctx with its own cancel so the journal's
	// stream goroutine exits cleanly if Recall returns early (e.g.,
	// caller cancellation, downstream render error).
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	stream, err := bundle.Journal.Stream(streamCtx, "")
	if err != nil {
		return nil, fmt.Errorf("stream journal: %w", err)
	}

	const (
		p1Cap = 5000 // decisions/task-done — rare; keep nearly all
		p2Cap = 1000 // commits/errors/elevated notes
		p3Cap = 500  // file edits / audit findings
		p4Cap = 500  // tool calls / unelevated notes
	)
	classifier := core.NewClassifier()
	caps := [4]int{p1Cap, p2Cap, p3Cap, p4Cap}
	var buckets [4][]core.Event

	for e := range stream {
		var idx int
		switch classifier.Classify(e) {
		case core.PriP1:
			idx = 0
		case core.PriP2:
			idx = 1
		case core.PriP3:
			idx = 2
		case core.PriP4:
			idx = 3
		default:
			idx = 3 // unknown priority → P4 bucket so events are still surfaced
		}
		if len(buckets[idx]) >= caps[idx] {
			// In-place FIFO shift. `s = s[1:]` would also drop the
			// front element but slowly grows the backing array on
			// repeated append, and would retain a reference to the
			// dropped Event in the unreachable head slot. copy +
			// overwrite keeps the cap bounded and lets GC collect
			// dropped event payloads.
			copy(buckets[idx], buckets[idx][1:])
			buckets[idx][len(buckets[idx])-1] = e
		} else {
			buckets[idx] = append(buckets[idx], e)
		}
	}

	// Concatenate tiers in priority order. Within each tier the
	// journal streamed events TS-ascending, so reverse-iterate to
	// surface newest-first — matching the previous sort.Slice
	// "TS.After" tiebreak.
	sorted := make([]core.Event, 0, len(buckets[0])+len(buckets[1])+len(buckets[2])+len(buckets[3]))
	for tier := 0; tier < 4; tier++ {
		bucket := buckets[tier]
		for i := len(bucket) - 1; i >= 0; i-- {
			sorted = append(sorted, bucket[i])
		}
	}

	// Greedy fill with budget. Render each candidate line first so we know
	// its exact byte cost, then stop as soon as the budget can't hold the
	// current event — the list is priority-sorted, so a smaller later event
	// sneaking in would violate the tier ordering.
	var used int
	var lines []string
	lines = append(lines, "# Session Snapshot\n")

	for _, e := range sorted {
		// V-01: re-redact on render. The journal-write redactor catches
		// what its patterns matched at the time of write; this second pass
		// covers near-misses, retroactively-added patterns, and operator-
		// supplied custom patterns added after the event was journaled.
		e = h.redactEventForRender(ctx, e)
		var dataStr string
		if e.Data != nil {
			dataStr = formatEventData(e.Data)
		}
		// Compact "MM-DD HH:MM:SS" — full RFC3339 doubled the per-line cost
		// for no benefit since recall is project-scoped, but pure HH:MM:SS
		// (review finding #24) collapsed multi-day sessions ambiguously.
		// Year is implied by the project's lifetime; month-day disambiguates.
		ts := e.TS.Format("01-02 15:04:05")
		actor := ""
		if e.Actor != "" {
			actor = fmt.Sprintf(" @%s", e.Actor)
		}
		tags := ""
		if len(e.Tags) > 0 {
			tags = fmt.Sprintf(" #%s", strings.Join(e.Tags, " #"))
		}
		line := fmt.Sprintf("- [%s] %s%s%s%s", e.Priority, ts, actor, tags, dataStr)
		// +1 for the newline strings.Join will insert between this line and
		// the next; slightly over-counts on the last line but never under.
		lineSize := len(line) + 1

		if used+lineSize > budget {
			break
		}

		lines = append(lines, line)
		used += lineSize
	}

	if len(lines) == 1 {
		lines = append(lines, "_No events in session_")
	}

	snapshot := strings.Join(lines, "\n")

	// format == "md" / default: use the markdown lines already built.
	// For json/xml: re-run greedy fill using retrieve.SnapshotBuilder so
	// path interning (Refs + [rN] tokens) is active on those formats too.
	if format == "json" || format == "xml" {
		// Count how many events from sorted actually made it into lines
		// (the loop above stops when budget is exhausted; we need the same set).
		// Since lines[0] is the "# Session Snapshot" header, selectedCount
		// = len(lines)-1 events from sorted.
		selectedCount := len(lines) - 1
		selected := sorted
		if selectedCount < len(sorted) {
			selected = sorted[:selectedCount]
		}
		if len(selected) == 0 {
			return &RecallResponse{
				Snapshot: "{}",
				Format:   format,
			}, nil
		}
		// V-01: re-redact each event before handing it to the renderer.
		// The markdown path above redacts inline; for json/xml we have to
		// pre-build the redacted slice because SnapshotBuilder reads the
		// fields directly.
		redacted := make([]core.Event, len(selected))
		for i, e := range selected {
			redacted[i] = h.redactEventForRender(ctx, e)
		}
		sb := retrieve.NewSnapshotBuilder(budget)
		snap, _ := sb.Build(redacted)
		if format == "json" {
			snapshot = retrieve.NewJSONRenderer().Render(snap)
		} else {
			snapshot = retrieve.NewXMLRenderer().Render(snap)
		}
	}

	return &RecallResponse{
		Snapshot: snapshot,
		Format:   format,
	}, nil
}

// StatsParams are the parameters for the Stats method.
