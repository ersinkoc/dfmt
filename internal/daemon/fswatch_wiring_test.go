package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew_FSWatcherEnabled_CreatesWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := newTestConfig()
	cfg.Capture.FS.Enabled = true

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Close journal explicitly; we never called Start so Stop is a no-op
	// and Windows will refuse to remove the tempdir while the file is open.
	t.Cleanup(func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	})

	if d.fswatcher == nil {
		t.Error("expected fswatcher to be constructed when Capture.FS.Enabled=true")
	}
}

func TestNew_FSWatcherDisabled_NilWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := newTestConfig()
	cfg.Capture.FS.Enabled = false

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	})

	if d.fswatcher != nil {
		t.Error("expected fswatcher to be nil when Capture.FS.Enabled=false")
	}
}
