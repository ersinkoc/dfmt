package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestNewFSWatcher(t *testing.T) {
	tmpDir := t.TempDir()

	w, err := NewFSWatcher(tmpDir, []string{"*.tmp"}, 500)
	if err != nil {
		t.Fatalf("NewFSWatcher failed: %v", err)
	}
	if w == nil {
		t.Fatal("NewFSWatcher returned nil")
	}
	if w.path != tmpDir {
		t.Errorf("path = %s, want %s", w.path, tmpDir)
	}
	if w.debounceMs != 500 {
		t.Errorf("debounceMs = %d, want 500", w.debounceMs)
	}
}

func TestFSWatcherShouldIgnore(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{"*.tmp", "node_modules"},
	}

	if w.shouldIgnore("test.tmp") {
		t.Error("shouldIgnore returned true for test.tmp")
	}

	if w.shouldIgnore("main.go") {
		t.Error("shouldIgnore returned true for main.go")
	}
}

func TestFSWatcherShouldIgnoreNested(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{"*.tmp"},
	}

	subDir := filepath.Join(tmpDir, "sub", "deep")
	os.MkdirAll(subDir, 0755)

	tmpFile := filepath.Join(subDir, "test.tmp")
	os.WriteFile(tmpFile, []byte("test"), 0644)

	if !w.shouldIgnore(tmpFile) {
		t.Error("shouldIgnore returned false for nested tmp file")
	}
}

func TestFSWatcherStartStop(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	ctx := context.Background()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := w.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestFSWatcherEvents(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	events := w.Events()
	if events == nil {
		t.Error("Events() returned nil")
	}
}

func TestFSWatcherEmitEventCreate(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	filePath := filepath.Join(tmpDir, "test.txt")

	w.emitEvent(filePath, false, "create")

	select {
	case e := <-w.events:
		if e.Type != core.EvtFileCreate {
			t.Errorf("event type = %s, want %s", e.Type, core.EvtFileCreate)
		}
		if e.Source != core.SrcFSWatch {
			t.Errorf("source = %s, want %s", e.Source, core.SrcFSWatch)
		}
	default:
		t.Error("no event received")
	}
}

func TestFSWatcherEmitEventModify(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:       tmpDir,
		ignore:     []string{},
		events:     make(chan core.Event, 100),
		stopCh:     make(chan struct{}),
		debounceMs: 0,
	}

	filePath := filepath.Join(tmpDir, "test.txt")

	w.emitEvent(filePath, false, "modify")

	select {
	case e := <-w.events:
		if e.Type != core.EvtFileEdit {
			t.Errorf("event type = %s, want %s", e.Type, core.EvtFileEdit)
		}
	default:
		t.Error("no event received")
	}
}

func TestFSWatcherEmitEventDelete(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	filePath := filepath.Join(tmpDir, "test.txt")

	w.emitEvent(filePath, false, "delete")

	select {
	case e := <-w.events:
		if e.Type != core.EvtFileDelete {
			t.Errorf("event type = %s, want %s", e.Type, core.EvtFileDelete)
		}
	default:
		t.Error("no event received")
	}
}

func TestFSWatcherEmitEventCreateDir(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	dirPath := filepath.Join(tmpDir, "newdir")

	w.emitEvent(dirPath, true, "create")

	select {
	case e := <-w.events:
		if e.Type != "directory.create" {
			t.Errorf("event type = %s, want directory.create", e.Type)
		}
	default:
		t.Error("no event received")
	}
}

func TestFSWatcherEmitEventIgnored(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{"*.tmp"},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	filePath := filepath.Join(tmpDir, "test.tmp")

	w.emitEvent(filePath, false, "create")

	select {
	case <-w.events:
		t.Error("event should have been ignored")
	default:
		// Expected - event should be ignored
	}
}

func TestFSWatcherEmitEventUnknown(t *testing.T) {
	tmpDir := t.TempDir()

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	filePath := filepath.Join(tmpDir, "test.txt")

	w.emitEvent(filePath, false, "unknown operation")

	select {
	case <-w.events:
		t.Error("event should not have been emitted for unknown operation")
	default:
		// Expected
	}
}
