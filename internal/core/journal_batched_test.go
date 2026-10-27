package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Batched durability syncs from a background loop; appends must be readable
// and Close must stop the loop cleanly.
func TestJournalBatchedModeSyncsAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := OpenJournal(path, JournalOptions{BatchMS: 5})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"01S1", "01S2"} {
		if err := j.Append(context.Background(), Event{ID: id, Type: EvtNote, Project: "p"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	time.Sleep(30 * time.Millisecond) // a few sync ticks
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	j2, err := OpenJournal(path, JournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	if got := streamIDs(t, j2, ""); len(got) != 2 {
		t.Errorf("events after batched writes = %v, want 2", got)
	}
}
