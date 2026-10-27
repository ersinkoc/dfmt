package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobIntentSkipsOversizedFiles: the intent scan used os.ReadFile on
// every matched file with no size cap, so `**/*` with an intent read large
// logs and data files whole. It now applies Grep's per-file cap.
func TestGlobIntentSkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxGrepFileBytes) + "\nneedle in the big file\n"
	if err := os.WriteFile(filepath.Join(dir, "big.log"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("needle here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := NewSandbox(dir).Glob(context.Background(), GlobReq{Pattern: "*", Intent: "needle"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	for _, m := range resp.Matches {
		if m.Source == "big.log" {
			t.Errorf("oversized file was read for intent matches: %+v", m)
		}
	}
	if len(resp.Matches) == 0 {
		t.Error("small file's match lost")
	}
}

func TestGlobIntentStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, _ := NewSandbox(dir).Glob(ctx, GlobReq{Pattern: "*", Intent: "needle"})
	if len(resp.Matches) != 0 {
		t.Errorf("canceled glob still scanned files: %+v", resp.Matches)
	}
}
