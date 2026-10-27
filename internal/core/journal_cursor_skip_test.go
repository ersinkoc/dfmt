package core

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func streamIDs(t *testing.T, j Journal, from string) []string {
	t.Helper()
	ch, err := j.Stream(context.Background(), from)
	if err != nil {
		t.Fatalf("Stream(%q): %v", from, err)
	}
	var ids []string
	for e := range ch {
		if len(e.ID) != 4 { // rotation markers carry minted ids
			continue
		}
		ids = append(ids, e.ID)
	}
	return ids
}

// rotatedJournal writes three segments: [A1 A2] [A3 A4] [A5 (active)].
func rotatedJournal(t *testing.T) (Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := OpenJournal(path, JournalOptions{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, id := range []string{"01A1", "01A2", "01A3", "01A4", "01A5"} {
		if err := j.Append(ctx, Event{ID: id, Type: EvtNote, Project: "p"}); err != nil {
			t.Fatal(err)
		}
		if i == 1 || i == 3 {
			if err := j.Rotate(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	return j, path
}

// TestStreamCursorAcrossSegments pins the cursor semantics the index tail
// relies on, now that Stream skips rotated segments ending before the
// cursor and reads pre-cursor lines by id only.
func TestStreamCursorAcrossSegments(t *testing.T) {
	j, _ := rotatedJournal(t)
	defer j.Close()
	cases := map[string][]string{
		"":     {"01A1", "01A2", "01A3", "01A4", "01A5"},
		"01A1": {"01A2", "01A3", "01A4", "01A5"},
		"01A2": {"01A3", "01A4", "01A5"}, // cursor is the last event of a segment
		"01A3": {"01A4", "01A5"},
		"01A5": nil,
	}
	for from, want := range cases {
		if got := streamIDs(t, j, from); !reflect.DeepEqual(got, want) {
			t.Errorf("Stream(%q) = %v, want %v", from, got, want)
		}
	}
}

// TestStreamCursorFallbackWhenSegmentNameLies: a segment is named for the
// last APPENDED id, and concurrent appends can make that smaller than an
// id inside it. Skipping by name must then fall back to a full pass rather
// than lose the cursor (which would stall the tail forever).
func TestStreamCursorFallbackWhenSegmentNameLies(t *testing.T) {
	j, path := rotatedJournal(t)
	defer j.Close()
	segs, err := journalSegments(path)
	if err != nil || len(segs) != 3 {
		t.Fatalf("segments = %v, %v", segs, err)
	}
	// Rename the [A3 A4] segment as if its last append had been 01A2Z:
	// still sorted after [A1 A2] (chronology intact) but below the cursor
	// 01A3 it contains, so the first pass skips it.
	lying := path + ".01A2Z.jsonl"
	if err := os.Rename(segs[1], lying); err != nil {
		t.Fatal(err)
	}
	if got, want := streamIDs(t, j, "01A3"), []string{"01A4", "01A5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Stream(01A3) with a lying segment name = %v, want %v", got, want)
	}
}

func TestLeadingEventID(t *testing.T) {
	if id, ok := leadingEventID([]byte(`{"id":"01XYZ","ts":"…"}`)); !ok || id != "01XYZ" {
		t.Errorf("leadingEventID = %q, %v", id, ok)
	}
	for _, line := range []string{`{"ts":"x","id":"01XYZ"}`, `{"id":"unterminated`, `[1,2]`} {
		if _, ok := leadingEventID([]byte(line)); ok {
			t.Errorf("leadingEventID(%q) accepted an unexpected shape", line)
		}
	}
}
