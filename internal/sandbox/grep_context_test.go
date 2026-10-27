package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestGrepContextLines: GrepReq.Context was advertised in the schema and
// decoded, but nothing read it — callers got bare lines.
func TestGrepContextLines(t *testing.T) {
	dir := t.TempDir()
	body := "one\ntwo\nNEEDLE\nfour\nfive\n"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := NewSandbox(dir)
	resp, err := sb.Grep(context.Background(), GrepReq{Pattern: "NEEDLE", Context: 2})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Fatalf("matches = %+v, want 1", resp.Matches)
	}
	m := resp.Matches[0]
	if !reflect.DeepEqual(m.Before, []string{"one", "two"}) || !reflect.DeepEqual(m.After, []string{"four", "five"}) {
		t.Errorf("Before=%q After=%q, want [one two] / [four five]", m.Before, m.After)
	}

	// Clamped at the file edges, and the empty string after the final
	// newline is not a line.
	resp, _ = sb.Grep(context.Background(), GrepReq{Pattern: "five", Context: 3})
	if got := resp.Matches[0].After; got != nil {
		t.Errorf("After at EOF = %q, want none", got)
	}

	// No context requested → no context fields.
	resp, _ = sb.Grep(context.Background(), GrepReq{Pattern: "NEEDLE"})
	if resp.Matches[0].Before != nil || resp.Matches[0].After != nil {
		t.Errorf("context returned without being asked: %+v", resp.Matches[0])
	}
}

// TestGrepReportsMatchCap: a count equal to the cap read like the whole
// answer while later files were never scanned.
func TestGrepReportsMatchCap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Repeat("hit\n", 150)), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "hit"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(resp.Summary, "cap") {
		t.Errorf("Summary = %q, want the cap named", resp.Summary)
	}
}

func TestGrepHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSandbox(dir).Grep(ctx, GrepReq{Pattern: "hit"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Grep on a canceled context = %v, want context.Canceled", err)
	}
}
