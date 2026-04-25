package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// captureJournalWarnings swaps the package-level journalWarnf with a recorder
// for the duration of the test and restores it on cleanup.
func captureJournalWarnings(t *testing.T) *[]string {
	t.Helper()
	var warnings []string
	prev := journalWarnf
	journalWarnf = func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { journalWarnf = prev })
	return &warnings
}

// TestStreamSurfacesMalformedLine covers V-9: a corrupted line in the
// journal must be skipped AND the operator must see a warning. Previously
// streamFile silently dropped any line that failed json.Unmarshal, leaving
// no trail for tampering or truncation events.
func TestStreamSurfacesMalformedLine(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	// Write two valid events bookending one malformed line, all newline-
	// terminated so the file reads cleanly into a JSONL stream.
	good1 := Event{
		ID: string(NewULID(time.Now())), TS: time.Now(),
		Type: EvtNote, Priority: PriP3, Source: SrcCLI,
		Data: map[string]any{"n": 1},
	}
	good2 := Event{
		ID: string(NewULID(time.Now().Add(time.Millisecond))), TS: time.Now(),
		Type: EvtNote, Priority: PriP3, Source: SrcCLI,
		Data: map[string]any{"n": 2},
	}
	good1Bytes, _ := json.Marshal(good1)
	good2Bytes, _ := json.Marshal(good2)
	corrupt := []byte(`{"id":"truncated","ts":`) // valid prefix, broken JSON

	var buf bytes.Buffer
	buf.Write(good1Bytes)
	buf.WriteByte('\n')
	buf.Write(corrupt)
	buf.WriteByte('\n')
	buf.Write(good2Bytes)
	buf.WriteByte('\n')

	if err := os.WriteFile(journalPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	warnings := captureJournalWarnings(t)

	j, err := OpenJournal(journalPath, JournalOptions{})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	stream, err := j.Stream(context.Background(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var events []Event
	for e := range stream {
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Errorf("expected 2 valid events through stream, got %d", len(events))
	}

	// Expect at least one warning for the corrupt line. scanLastID (run
	// during OpenJournal) and streamFile (run by Stream) both surface
	// corruption, so both warnings may appear — we only require >=1 that
	// matches the corrupt line's signature.
	if len(*warnings) == 0 {
		t.Fatalf("expected a warning for the malformed line, got none")
	}
	sawCorrupt := false
	for _, w := range *warnings {
		if bytes.Contains([]byte(w), []byte("malformed line")) &&
			bytes.Contains([]byte(w), []byte("truncated")) {
			sawCorrupt = true
			break
		}
	}
	if !sawCorrupt {
		t.Errorf("warning missing or wrong format; got: %v", *warnings)
	}
}

// TestSnippetForWarnTruncates ensures the diagnostic snippet is bounded so a
// huge line doesn't blast stderr.
func TestSnippetForWarnTruncates(t *testing.T) {
	short := []byte(`{"id":"abc"}`)
	if got := snippetForWarn(short); got != string(short) {
		t.Errorf("short input mangled: got %q", got)
	}
	long := bytes.Repeat([]byte("x"), 200)
	got := snippetForWarn(long)
	if len(got) != 83 { // 80 chars + "..."
		t.Errorf("long input length = %d, want 83", len(got))
	}
	if !bytes.HasSuffix([]byte(got), []byte("...")) {
		t.Errorf("long input missing ellipsis: %q", got)
	}
}
