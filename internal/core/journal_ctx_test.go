package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestJournalAppendHonorsCtx pins finding #19: Append must observe ctx
// cancellation. Pre-fix the ctx parameter was accepted but ignored, so a
// caller that canceled (e.g., daemon shutdown mid-write) had no way to
// abort the append before it landed on disk.
func TestJournalAppendHonorsCtx(t *testing.T) {
	tmp := t.TempDir()
	j, err := OpenJournal(filepath.Join(tmp, "journal.jsonl"), JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel — the very next Append must refuse

	ev := Event{
		ID:       string(NewULID(time.Now())),
		TS:       time.Now(),
		Type:     EvtNote,
		Priority: PriP3,
		Source:   SrcCLI,
	}
	err = j.Append(ctx, ev)
	if err == nil {
		t.Fatal("Append on canceled ctx returned nil; want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Append err = %v, want errors.Is(err, context.Canceled)", err)
	}
}

// TestJournalCheckpointHonorsCtx is the Checkpoint sibling of the Append
// test. The journal's Checkpoint takes the lock; a canceled ctx must
// short-circuit before doing any work.
func TestJournalCheckpointHonorsCtx(t *testing.T) {
	tmp := t.TempDir()
	j, err := OpenJournal(filepath.Join(tmp, "journal.jsonl"), JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = j.Checkpoint(ctx)
	if err == nil {
		t.Fatal("Checkpoint on canceled ctx returned nil; want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Checkpoint err = %v, want errors.Is(err, context.Canceled)", err)
	}
}

// TestJournalAppendNoCancelStillWorks is the negative control: if ctx is
// not canceled, Append returns successfully. Without this, a buggy
// ctx.Err() check that always returns an error would slip through.
func TestJournalAppendNoCancelStillWorks(t *testing.T) {
	tmp := t.TempDir()
	j, err := OpenJournal(filepath.Join(tmp, "journal.jsonl"), JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	ev := Event{
		ID:       string(NewULID(time.Now())),
		TS:       time.Now(),
		Type:     EvtNote,
		Priority: PriP3,
		Source:   SrcCLI,
	}
	if err := j.Append(context.Background(), ev); err != nil {
		t.Fatalf("Append on live ctx: %v", err)
	}

	id, err := j.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("Checkpoint on live ctx: %v", err)
	}
	if id != ev.ID {
		t.Fatalf("Checkpoint ID = %q, want %q", id, ev.ID)
	}
}
