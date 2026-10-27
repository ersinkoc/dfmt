package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func compressedJournal(t *testing.T, keep, rotations int) (Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := OpenJournal(path, JournalOptions{Durable: true, Compress: true, Keep: keep})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < rotations; i++ {
		if err := j.Append(ctx, Event{ID: fmt.Sprintf("01G%d", i), Type: EvtNote, Project: "p"}); err != nil {
			t.Fatal(err)
		}
		if err := j.Rotate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Append(ctx, Event{ID: "01GZ", Type: EvtNote, Project: "p"}); err != nil {
		t.Fatal(err)
	}
	return j, path
}

// storage.compress_rotated was accepted, defaulted to true and documented,
// while Rotate never compressed anything. Rotated segments are now gzipped
// and read back transparently, cursor and retention included.
func TestJournalCompressesRotatedSegments(t *testing.T) {
	j, path := compressedJournal(t, 0, 3)
	defer j.Close()

	segs, err := journalSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range segs[:len(segs)-1] {
		if !strings.HasSuffix(s, ".jsonl.gz") {
			t.Errorf("rotated segment %s not compressed", filepath.Base(s))
		}
		if _, err := os.Stat(strings.TrimSuffix(s, ".gz")); !os.IsNotExist(err) {
			t.Errorf("uncompressed original of %s left behind", filepath.Base(s))
		}
	}
	if got, want := streamIDs(t, j, ""), []string{"01G0", "01G1", "01G2", "01GZ"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Stream over compressed segments = %v, want %v", got, want)
	}
	if got, want := streamIDs(t, j, "01G1"), []string{"01G2", "01GZ"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Stream from a cursor in a compressed segment = %v, want %v", got, want)
	}
}

// A crash between the .gz rename and the original's removal leaves both;
// the segment must be read once, not twice.
func TestJournalCompressedAndPlainTwinReadOnce(t *testing.T) {
	j, path := compressedJournal(t, 0, 1)
	defer j.Close()
	segs, _ := journalSegments(path)
	gz := segs[0]
	// Recreate the plain twin, as an interrupted compression would leave it.
	plain := strings.TrimSuffix(gz, ".gz")
	ch, _ := j.Stream(context.Background(), "")
	var lines []string
	for e := range ch {
		if e.ID == "01G0" {
			lines = append(lines, e.ID)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("setup: %v", lines)
	}
	src, err := os.ReadFile(path) // any valid journal bytes will do for the twin's existence
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, src, 0o600); err != nil {
		t.Fatal(err)
	}
	segs, _ = journalSegments(path)
	if len(segs) != 2 || segs[0] != plain {
		t.Errorf("segments with a twin = %v, want the plain file once plus the active one", segs)
	}
}

func TestJournalRetentionCountsCompressedSegments(t *testing.T) {
	j, path := compressedJournal(t, 1, 3)
	defer j.Close()
	segs, _ := journalSegments(path)
	if len(segs)-1 != 1 || !strings.HasSuffix(segs[0], ".gz") {
		t.Errorf("segments = %v, want one compressed rotated segment", segs)
	}
	if got, want := streamIDs(t, j, ""), []string{"01G2", "01GZ"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after retention = %v, want %v", got, want)
	}
}
