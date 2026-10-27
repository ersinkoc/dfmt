package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEventSignatureIsStable pins ComputeSig for a fixed event. Stream
// skips any line whose signature does not validate, so changing the
// signature algorithm (or the fields it covers, or their encoding) would
// make every event in every existing journal silently disappear from
// recall, search and replay. Changing this value requires a migration.
func TestEventSignatureIsStable(t *testing.T) {
	e := Event{ID: "01JFIXTURE0000000000000000", TS: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Project: "/p",
		Type: EvtNote, Priority: PriP3, Source: SrcMCP, Actor: "agent",
		Data: map[string]any{"message": "pinned", "n": float64(3)}, Refs: []string{"r1"}, Tags: []string{"decision"}}
	if got, want := e.ComputeSig(), "ea228639e6274f5e"; got != want {
		t.Fatalf("ComputeSig = %s, pinned %s — existing journals would no longer validate", got, want)
	}
}

// TestJournalReadsAPinnedLine replays a journal line in today's on-disk
// shape, written by hand rather than by this binary's Append, so a change
// to the event encoding or the reader breaks here instead of in a user's
// old journal.
func TestJournalReadsAPinnedLine(t *testing.T) {
	line := `{"id":"01JFIXTURE0000000000000000","ts":"2026-01-02T03:04:05Z","project":"/p","type":"note",` +
		`"priority":"p3","source":"mcp","actor":"agent","data":{"message":"pinned","n":3},` +
		`"refs":["r1"],"tags":["decision"],"sig":"ea228639e6274f5e"}` + "\n"
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(path, JournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ch, err := j.Stream(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 1 || got[0].ID != "01JFIXTURE0000000000000000" || got[0].Data["message"] != "pinned" {
		t.Fatalf("pinned line read back as %+v", got)
	}
}
