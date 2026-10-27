package core

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func rotateN(t *testing.T, keep, rotations int) (Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := OpenJournal(path, JournalOptions{Durable: true, Keep: keep})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < rotations; i++ {
		if err := j.Append(ctx, Event{ID: fmt.Sprintf("01R%d", i), Type: EvtNote, Project: "p"}); err != nil {
			t.Fatal(err)
		}
		if err := j.Rotate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return j, path
}

// TestJournalRetentionKeepsNewestSegments: rotated segments were never
// deleted, so a long-lived project's journal grew without bound. With
// storage.journal_keep_segments = N, each rotation keeps the N newest.
func TestJournalRetentionKeepsNewestSegments(t *testing.T) {
	j, path := rotateN(t, 2, 4)
	defer j.Close()
	segs, err := journalSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(segs) - 1; got != 2 {
		t.Fatalf("rotated segments = %d (%v), want 2", got, segs)
	}
	ids := map[string]bool{}
	for _, id := range streamIDs(t, j, "") {
		ids[id] = true
	}
	if ids["01R0"] || ids["01R1"] || !ids["01R2"] || !ids["01R3"] {
		t.Errorf("surviving events = %v, want only the two newest segments' (01R2, 01R3)", ids)
	}
}

// Keep 0 is the default and must delete nothing.
func TestJournalRetentionZeroKeepsAll(t *testing.T) {
	j, path := rotateN(t, 0, 4)
	defer j.Close()
	segs, _ := journalSegments(path)
	if got := len(segs) - 1; got != 4 {
		t.Errorf("rotated segments = %d, want all 4", got)
	}
}
