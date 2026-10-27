package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDefaultProjectFSWatchJournalsChanges runs consumeFSWatch for real.
// The tests named after it (TestConsumeFSWatchEvents/Redacted) construct a
// daemon and never Start it, so the consumer had no coverage at all.
func TestDefaultProjectFSWatchJournalsChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		// Watcher latency is platform-specific (macOS CI missed a 5 s
		// window); inotify on Linux is the deterministic case.
		t.Skip("fs watcher timing is only deterministic on Linux")
	}
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

// Stop must not delete a successor's pid file: a daemon started during this
// one's shutdown (an MCP proxy reconnecting) writes its own, and removing it
// left the new daemon running but invisible to `dfmt stop` (observed live).
func TestPidFileNamesUs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.pid")
	if !pidFileNamesUs(p) {
		t.Error("missing pid file should be treated as ours (nothing to protect)")
	}
	_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
	if !pidFileNamesUs(p) {
		t.Error("our own pid not recognized")
	}
	_ = os.WriteFile(p, []byte("1"), 0o600)
	if pidFileNamesUs(p) {
		t.Error("another process's pid treated as ours")
	}
}
