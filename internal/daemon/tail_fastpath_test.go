package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

type countingJournal struct {
	core.Journal
	streams atomic.Int32
}

func (c *countingJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	c.streams.Add(1)
	return c.Journal.Stream(ctx, from)
}

// TestTailOnceSkipsUnchangedJournal: every 3 s tick used to stream the whole
// journal from the top to find its cursor, per loaded project, forever. An
// unchanged file is now one stat; an append is still picked up.
func TestTailOnceSkipsUnchangedJournal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".dfmt", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := core.OpenJournal(path, core.JournalOptions{Path: path, Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	ctx := context.Background()
	if err := real.Append(ctx, core.Event{ID: "01T1", Type: core.EvtNote, Tags: []string{"first-XQ1"}}); err != nil {
		t.Fatal(err)
	}

	j := &countingJournal{Journal: real}
	r := &ProjectResources{ProjectPath: root, Journal: j, Index: core.NewIndex()}

	r.tailOnce(ctx)
	r.tailOnce(ctx)
	r.tailOnce(ctx)
	if n := j.streams.Load(); n != 1 {
		t.Errorf("unchanged journal streamed %d times over 3 ticks, want 1", n)
	}

	if err := real.Append(ctx, core.Event{ID: "01T2", Type: core.EvtNote, Tags: []string{"second-XQ2"}}); err != nil {
		t.Fatal(err)
	}
	r.tailOnce(ctx)
	if n := j.streams.Load(); n != 2 {
		t.Errorf("append not noticed: %d streams, want 2", n)
	}
	if hits := r.Index.SearchTrigram("second-XQ2", 1); len(hits) != 1 {
		t.Error("appended event was not indexed")
	}
}

// TestTailOnceRecoversFromLostCursor: Stream emits only after finding the
// cursor, so a cursor that no longer exists (journal recreated, segment
// deleted) froze the index for the project forever. A changed journal
// with nothing after the cursor now triggers one full re-read.
func TestTailOnceRecoversFromLostCursor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".dfmt", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	j1, err := core.OpenJournal(path, core.JournalOptions{Path: path, Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = j1.Append(ctx, core.Event{ID: "01L1", Type: core.EvtNote, Tags: []string{"before-XQ3"}})
	r := &ProjectResources{ProjectPath: root, Journal: j1, Index: core.NewIndex()}
	r.tailOnce(ctx)
	_ = j1.Close()

	// The journal is recreated: the cursor event 01L1 is gone.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	j2, err := core.OpenJournal(path, core.JournalOptions{Path: path, Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	_ = j2.Append(ctx, core.Event{ID: "01M1", Type: core.EvtNote, Tags: []string{"after-XQ4"}})
	r.Journal = j2

	r.tailOnce(ctx)
	if hits := r.Index.SearchTrigram("after-XQ4", 1); len(hits) != 1 {
		t.Fatal("event in the recreated journal was never indexed: the tail is stuck on a lost cursor")
	}
	if cur := r.lastIndexedID.Load(); cur == nil || *cur != "01M1" {
		t.Errorf("cursor = %v, want 01M1", cur)
	}
}
