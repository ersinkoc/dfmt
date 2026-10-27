package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// PersistIndex must fail loudly (and leave no temp file) when the target
// cannot be written — a missing directory, or a path that is a directory.
func TestPersistIndexErrorPaths(t *testing.T) {
	ix := NewIndex()
	ix.Add(noteEvent("01P", "persist me"))

	if err := PersistIndex(ix, filepath.Join(t.TempDir(), "missing", "index.gob"), "01P"); err == nil {
		t.Error("persist into a missing directory succeeded")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "index.gob")
	if err := os.Mkdir(target, 0o700); err != nil { // rename onto a directory fails
		t.Fatal(err)
	}
	if err := PersistIndex(ix, target, "01P"); err == nil {
		t.Error("persist onto a directory succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "index.gob.tmp-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestRebuildIndexFromJournalInto(t *testing.T) {
	if hi, err := RebuildIndexFromJournalInto(context.Background(), nil, nil); hi != "" || err != nil {
		t.Errorf("nil inputs: %q, %v", hi, err)
	}
	j, _ := rotatedJournal(t)
	defer j.Close()
	ix := NewIndex()
	hi, err := RebuildIndexFromJournalInto(context.Background(), j, ix)
	if err != nil || hi < "01A5" {
		t.Errorf("rebuild: hi=%q err=%v", hi, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RebuildIndexFromJournalInto(ctx, j, NewIndex()); err == nil {
		t.Log("canceled rebuild finished before observing cancellation (allowed)")
	}
}
