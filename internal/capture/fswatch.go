package capture

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// FSWatcher watches the filesystem for changes.
type FSWatcher struct {
	path        string
	projectPath string
	ignore      []string
	debounceMs  int
	events      chan core.Event
	stopCh      chan struct{}

	// pathsMu guards watchedPaths, which tracks every directory that currently
	// has a live platform-specific watcher. Stop() iterates this slice to wake
	// blocking reads (notably Linux inotify, where close(fd) does NOT unblock a
	// parked unix.Read — the watch goroutine would otherwise leak on shutdown).
	pathsMu      sync.Mutex
	watchedPaths []string

	// Platform-specific watcher function
	watchDirFn func(w *FSWatcher, path string)

	// debounceMap tracks the last emit time per path for non-blocking debounce.
	// Access is guarded by debounceMu.
	debounceMu sync.Mutex
	debounceMap map[string]time.Time
	// cleanupTicker periodically removes stale entries from debounceMap to
	// prevent unbounded growth. Entries older than 10× debounceMs are purged.
	cleanupTicker *time.Ticker
}

// initWatcher is set by platform-specific files via init()
var initWatcher func(w *FSWatcher)

// SetProject sets the project identifier stamped on every emitted Event before its signature is computed. It is optional; when empty, events are emitted with no project attribution.
func (w *FSWatcher) SetProject(p string) { w.projectPath = p }

// NewFSWatcher creates a new filesystem watcher.
func NewFSWatcher(path string, ignore []string, debounceMs int) (*FSWatcher, error) {
	w := &FSWatcher{
		path:       path,
		ignore:     ignore,
		debounceMs: debounceMs,
		events:     make(chan core.Event, 100),
		stopCh:     make(chan struct{}),
		debounceMap: make(map[string]time.Time),
	}
	if debounceMs > 0 {
		w.cleanupTicker = time.NewTicker(time.Duration(debounceMs) * 10 * time.Millisecond)
	}
	if initWatcher != nil {
		initWatcher(w)
	}
	return w, nil
}

// addWatchedPath records a directory that now has a live watcher. Called by
// platform-specific watchDir implementations after a successful add.
func (w *FSWatcher) addWatchedPath(path string) {
	w.pathsMu.Lock()
	w.watchedPaths = append(w.watchedPaths, path)
	w.pathsMu.Unlock()
}

// snapshotWatchedPaths returns a copy of the currently tracked watch paths so
// Stop() can operate without holding the mutex during filesystem writes.
func (w *FSWatcher) snapshotWatchedPaths() []string {
	w.pathsMu.Lock()
	defer w.pathsMu.Unlock()
	out := make([]string, len(w.watchedPaths))
	copy(out, w.watchedPaths)
	return out
}

// Start starts watching the filesystem.
func (w *FSWatcher) Start(ctx context.Context) error {
	// Start the cleanup goroutine for debounce map if debounce is enabled.
	if w.cleanupTicker != nil {
		go w.runDebounceCleanup()
	}
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

// runDebounceCleanup periodically removes stale entries from the debounce map.
// An entry is stale if its last emit time is older than 10× debounceMs.
func (w *FSWatcher) runDebounceCleanup() {
	for {
		select {
		case <-w.stopCh:
			if w.cleanupTicker != nil {
				w.cleanupTicker.Stop()
			}
			return
		case <-w.cleanupTicker.C:
			if w.debounceMs <= 0 {
				continue
			}
			threshold := time.Duration(w.debounceMs*10) * time.Millisecond
			now := time.Now()
			w.debounceMu.Lock()
			for path, lastEmit := range w.debounceMap {
				if now.Sub(lastEmit) > threshold {
					delete(w.debounceMap, path)
				}
			}
			w.debounceMu.Unlock()
		}
	}
}

// Stop stops watching.
//
// On Linux, simply closing stopCh is not enough: watch goroutines are parked
// inside unix.Read(inotify_fd) and close(fd) does NOT unblock that read. To
// guarantee the goroutines observe the stop signal we write a marker file
// inside each watched directory, which generates an inotify event that wakes
// the reader; the loop then sees stopCh is closed and returns.
//
// The marker filename is prefixed with ".dfmt_stop_" so downstream consumers
// that filter dotfiles (or filter this specific prefix) will ignore it. Any
// write errors are intentionally swallowed — Stop() must always return, even
// if a watched directory was removed or became unwritable.
func (w *FSWatcher) Stop(ctx context.Context) error {
	// Closing twice will panic; guard against that but still return nil so
	// repeated Stop calls are safe for callers that defer Stop in tests.
	defer func() { _ = recover() }()
	close(w.stopCh)

	paths := w.snapshotWatchedPaths()
	if len(paths) == 0 {
		return nil
	}
	markerName := ".dfmt_stop_" + strconv.Itoa(os.Getpid())
	for _, dir := range paths {
		markerPath := filepath.Join(dir, markerName)
		// Best-effort: ignore errors (directory removed, read-only, etc.)
		_ = os.WriteFile(markerPath, nil, 0o600)
		// Clean up — we only needed the inotify event, not the file itself.
		_ = os.Remove(markerPath)
	}
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

	// Ignore our own stop-marker files; they exist purely to wake inotify
	// readers during Stop() and are not meaningful filesystem activity.
	if strings.HasPrefix(filepath.Base(path), ".dfmt_stop_") {
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
		w.debounceMu.Lock()
		lastEmit, seen := w.debounceMap[path]
		if seen && time.Since(lastEmit) < time.Duration(w.debounceMs)*time.Millisecond {
			w.debounceMu.Unlock()
			return // still within debounce window
		}
		w.debounceMap[path] = time.Now()
		w.debounceMu.Unlock()
	}

	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  w.projectPath,
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
