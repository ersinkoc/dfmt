package transport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestRecall_P1EventPastGlobalCapStillSurfaces is the regression test
// for finding #7. The previous stopgap broke the priority sort whenever
// the journal grew past 5000 events: the cap fired on the first 5000
// stream entries (typically all P3/P4 chatter), and any P1 decision
// further down the stream was silently dropped.
//
// We seed 5050 P3 events followed by a single P1 event with a
// distinctive payload, then verify the P1 lands in the snapshot. Under
// the old global-cap implementation the test would fail — the P1 event
// would never reach the buffer because the read loop short-circuits at
// position 5000.
func TestRecall_P1EventPastGlobalCapStillSurfaces(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bulk-event recall regression in -short mode")
	}
	tmp := t.TempDir()
	journal, err := core.OpenJournal(filepath.Join(tmp, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	ctx := context.Background()
	const noisePrefix = 5050

	// 5050 P3 events to flood past the old global cap.
	for i := 0; i < noisePrefix; i++ {
		ev := core.Event{
			ID:     string(core.NewULID(time.Now())),
			TS:     time.Now(),
			Type:   core.EvtFileEdit, // classifier → PriP3
			Source: core.SrcCLI,
		}
		if err := journal.Append(ctx, ev); err != nil {
			t.Fatalf("noise Append %d: %v", i, err)
		}
	}

	// One P1 decision after the noise. The classifier elevates
	// EvtDecision to PriP1 regardless of the Priority field on the
	// event itself.
	const sentinel = "DECISION_PAST_CAP_MUST_SURVIVE"
	dec := core.Event{
		ID:     string(core.NewULID(time.Now())),
		TS:     time.Now(),
		Type:   core.EvtDecision,
		Source: core.SrcCLI,
		Data:   map[string]any{"message": sentinel},
	}
	if err := journal.Append(ctx, dec); err != nil {
		t.Fatalf("sentinel Append: %v", err)
	}

	// Generous budget so the rendering loop can fit at least the
	// sentinel — the P1 tier is rendered first, so a budget that
	// covers a single P1 line is sufficient.
	resp, err := h.Recall(ctx, RecallParams{Budget: 64 * 1024})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if !strings.Contains(resp.Snapshot, sentinel) {
		t.Errorf("P1 sentinel %q not in snapshot — finding #7 regression\n--- snapshot head ---\n%s",
			sentinel, head(resp.Snapshot, 600))
	}
}

// TestRecall_PerTierCapBoundsMemory confirms the bucket caps are honored
// even under a single-tier flood. We push 7000 P4 events (more than
// p4Cap=500) and verify the snapshot still renders without error and
// without hanging — the FIFO eviction must keep memory bounded as the
// stream proceeds. Verifying exact bucket contents would require
// exposing internal state, so we assert only that the call returns
// successfully and produces output.
func TestRecall_PerTierCapBoundsMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping per-tier-cap test in -short mode")
	}
	tmp := t.TempDir()
	journal, err := core.OpenJournal(filepath.Join(tmp, "journal.jsonl"), core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	ctx := context.Background()
	for i := 0; i < 7000; i++ {
		ev := core.Event{
			ID:     string(core.NewULID(time.Now())),
			TS:     time.Now(),
			Type:   core.EvtMCPCall, // classifier → PriP4
			Source: core.SrcCLI,
		}
		if err := journal.Append(ctx, ev); err != nil {
			t.Fatalf("flood Append %d: %v", i, err)
		}
	}

	resp, err := h.Recall(ctx, RecallParams{Budget: 4096})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if resp == nil || resp.Snapshot == "" {
		t.Fatal("Recall produced empty snapshot")
	}
}

// TestRecall_NewerInTierBeatsOlder pins within-tier ordering: when two
// P1 events exist, the newer one must appear first in the snapshot.
// The previous implementation enforced this via sort.Slice's "TS.After"
// tiebreak; the new per-tier reverse-iteration must preserve the
// property since the journal streams TS-ascending.
func TestRecall_NewerInTierBeatsOlder(t *testing.T) {
	idx := core.NewIndex()
	now := time.Now()
	journal := &mockJournal{events: []core.Event{
		{
			ID:     "older",
			TS:     now.Add(-2 * time.Hour),
			Type:   core.EvtDecision,
			Source: core.SrcCLI,
			Data:   map[string]any{"message": "OLDER_DECISION"},
		},
		{
			ID:     "newer",
			TS:     now,
			Type:   core.EvtDecision,
			Source: core.SrcCLI,
			Data:   map[string]any{"message": "NEWER_DECISION"},
		},
	}}
	h := NewHandlers(idx, journal, nil)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 16 * 1024})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	older := strings.Index(resp.Snapshot, "OLDER_DECISION")
	newer := strings.Index(resp.Snapshot, "NEWER_DECISION")
	if older < 0 || newer < 0 {
		t.Fatalf("missing markers: older=%d newer=%d\nsnapshot=%s", older, newer, resp.Snapshot)
	}
	if newer >= older {
		t.Errorf("newer P1 should appear before older P1; got newer@%d older@%d", newer, older)
	}
}

// TestRecall_TierOrderPreserved pins cross-tier ordering: P1 always
// before P2 before P3 before P4 in the snapshot, regardless of journal
// insertion order. The new per-tier concatenation must produce the
// same priority strata as the old sort.Slice.
func TestRecall_TierOrderPreserved(t *testing.T) {
	idx := core.NewIndex()
	now := time.Now()
	// Insertion order: P4, P3, P2, P1 — opposite of priority order.
	journal := &mockJournal{events: []core.Event{
		{
			ID:     "p4",
			TS:     now.Add(-3 * time.Hour),
			Type:   core.EvtMCPCall,
			Source: core.SrcCLI,
			Data:   map[string]any{"message": "P4_CALL"},
		},
		{
			ID:     "p3",
			TS:     now.Add(-2 * time.Hour),
			Type:   core.EvtFileEdit,
			Source: core.SrcCLI,
			Data:   map[string]any{"message": "P3_EDIT"},
		},
		{
			ID:     "p2",
			TS:     now.Add(-time.Hour),
			Type:   core.EvtGitCommit,
			Source: core.SrcCLI,
			Data:   map[string]any{"message": "P2_COMMIT"},
		},
		{
			ID:     "p1",
			TS:     now,
			Type:   core.EvtDecision,
			Source: core.SrcCLI,
			Data:   map[string]any{"message": "P1_DECISION"},
		},
	}}
	h := NewHandlers(idx, journal, nil)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 16 * 1024})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	positions := map[string]int{
		"P1_DECISION": strings.Index(resp.Snapshot, "P1_DECISION"),
		"P2_COMMIT":   strings.Index(resp.Snapshot, "P2_COMMIT"),
		"P3_EDIT":     strings.Index(resp.Snapshot, "P3_EDIT"),
		"P4_CALL":     strings.Index(resp.Snapshot, "P4_CALL"),
	}
	for k, v := range positions {
		if v < 0 {
			t.Fatalf("marker %q missing from snapshot:\n%s", k, resp.Snapshot)
		}
	}
	want := []string{"P1_DECISION", "P2_COMMIT", "P3_EDIT", "P4_CALL"}
	for i := 1; i < len(want); i++ {
		if positions[want[i-1]] >= positions[want[i]] {
			t.Errorf("tier order broken: %s@%d should precede %s@%d",
				want[i-1], positions[want[i-1]], want[i], positions[want[i]])
		}
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s...(%d more bytes)", s[:n], len(s)-n)
}
