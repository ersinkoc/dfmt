package capture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// FSWatcher watches the filesystem for changes.
type FSWatcher struct {
	path       string
	ignore     []string
	debounceMs int
	events     chan core.Event
	stopCh     chan struct{}

	// Platform-specific watcher function
	watchDirFn func(w *FSWatcher, path string)
}

// initWatcher is set by platform-specific files via init()
var initWatcher func(w *FSWatcher)

// NewFSWatcher creates a new filesystem watcher.
func NewFSWatcher(path string, ignore []string, debounceMs int) (*FSWatcher, error) {
	w := &FSWatcher{
		path:       path,
		ignore:     ignore,
		debounceMs: debounceMs,
		events:     make(chan core.Event, 100),
		stopCh:     make(chan struct{}),
	}
	if initWatcher != nil {
		initWatcher(w)
	}
	return w, nil
}

// Start starts watching the filesystem.
func (w *FSWatcher) Start(ctx context.Context) error {
	return filepath.Walk(w.path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			if w.shouldIgnore(path) {
				return filepath.SkipDir
			}
			if w.watchDirFn != nil {
				w.watchDirFn(w, path)
			}
		}

		return nil
	})
}

// Stop stops watching.
func (w *FSWatcher) Stop(ctx context.Context) error {
	close(w.stopCh)
	return nil
}

// Events returns the event channel.
func (w *FSWatcher) Events() <-chan core.Event {
	return w.events
}

func (w *FSWatcher) shouldIgnore(path string) bool {
	rel, err := filepath.Rel(w.path, path)
	if err != nil {
		return false
	}

	for _, pattern := range w.ignore {
		matched, _ := filepath.Match(pattern, rel)
		if matched {
			return true
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			matched, _ = filepath.Match(pattern, part)
			if matched {
				return true
			}
		}
	}

	return false
}

// emitEvent sends a filesystem event to the channel.
func (w *FSWatcher) emitEvent(path string, isDir bool, operation string) {
	if w.shouldIgnore(path) {
		return
	}

	var eventType core.EventType
	switch operation {
	case "create":
		if isDir {
			eventType = "directory.create"
		} else {
			eventType = core.EvtFileCreate
		}
	case "modify":
		eventType = core.EvtFileEdit
	case "delete":
		eventType = core.EvtFileDelete
	default:
		return
	}

	if w.debounceMs > 0 {
		time.Sleep(time.Duration(w.debounceMs) * time.Millisecond)
	}

	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     eventType,
		Priority: core.PriP3,
		Source:   core.SrcFSWatch,
		Data: map[string]any{
			"path": path,
		},
	}
	e.Sig = e.ComputeSig()

	select {
	case w.events <- e:
	default:
	}
}
