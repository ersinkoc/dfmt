package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultProjectFSWatchJournalsChanges runs consumeFSWatch for real.
// The tests named after it (TestConsumeFSWatchEvents/Redacted) construct a
// daemon and never Start it, so the consumer had no coverage at all.
func TestDefaultProjectFSWatchJournalsChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".dfmt"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig()
	cfg.Capture.FS.Enabled = true
	cfg.Capture.FS.DebounceMS = 10
	d, err := New(root, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.fswatcher == nil {
		t.Skip("no fs watcher on this platform")
	}
	ctx := context.Background()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = d.Stop(ctx) }()

	time.Sleep(200 * time.Millisecond) // let the watcher arm
	if err := os.WriteFile(filepath.Join(root, "watched-file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stream, err := d.journal.Stream(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		for e := range stream {
			if strings.HasPrefix(string(e.Type), "file.") {
				for _, v := range e.Data {
					if s, ok := v.(string); ok && strings.Contains(s, "watched-file.txt") {
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("no file.* event for watched-file.txt reached the journal")
}
