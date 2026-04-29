package capture

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// watchWG tracks every platform-specific watch goroutine so Stop() can
	// block until they drain. Without this Stop returns before the OS-level
	// watchers exit, which races with subsequent Close() calls on shared
	// resources.
	watchWG sync.WaitGroup

	// Platform-specific watcher function
	watchDirFn func(w *FSWatcher, path string)

	// debounceMap tracks the last emit time per path for non-blocking debounce.
	// Access is guarded by debounceMu.
	debounceMu  sync.Mutex
	debounceMap map[string]time.Time
	// cleanupTicker periodically removes stale entries from debounceMap to
	// prevent unbounded growth. Entries older than 10× debounceMs are purged.
	cleanupTicker *time.Ticker

	// droppedEvents counts events that were dropped because the outgoing
	// channel was full. Consumers can read this via DroppedEvents() to detect
	// fswatch backpressure — silent drops were previously undetectable.
	droppedEvents atomic.Uint64
}

// DroppedEvents returns the number of events that have been dropped due to
// the outgoing channel being full.
func (w *FSWatcher) DroppedEvents() uint64 {
	return w.droppedEvents.Load()
}

// initWatcher is set by platform-specific files via init()
var initWatcher func(w *FSWatcher)

// SetProject sets the project identifier stamped on every emitted Event before its signature is computed. It is optional; when empty, events are emitted with no project attribution.
func (w *FSWatcher) SetProject(p string) { w.projectPath = p }

// NewFSWatcher creates a new filesystem watcher.
func NewFSWatcher(path string, ignore []string, debounceMs int) (*FSWatcher, error) {
	w := &FSWatcher{
		path:        path,
		ignore:      ignore,
		debounceMs:  debounceMs,
		events:      make(chan core.Event, 100),
		stopCh:      make(chan struct{}),
		debounceMap: make(map[string]time.Time),
	}
	if debounceMs > 0 {
		// Cleanup period is 10× the debounce window. Multiplying by
		// time.Millisecond ONCE gives the correct duration — the prior form
		// `time.Duration(debounceMs) * 10 * time.Millisecond` was a nanosecond
		// unit mismatch and produced a bogus tick rate.
		w.cleanupTicker = time.NewTicker(time.Duration(debounceMs*10) * time.Millisecond)
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
		w.watchWG.Add(1)
		go func() {
			defer w.watchWG.Done()
			w.runDebounceCleanup()
		}()
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

// TrackGoroutine registers a platform watch goroutine so Stop can wait for
// it to exit. Each platform's watchDirFn must call this before spawning.
func (w *FSWatcher) TrackGoroutine() {
	w.watchWG.Add(1)
}

// UntrackGoroutine signals completion of a tracked goroutine.
func (w *FSWatcher) UntrackGoroutine() {
	w.watchWG.Done()
}

// runDebounceCleanup periodically removes stale entries from the debounce map.
// An entry is stale if its last emit time is older than 10× debounceMs.
func (w *FSWatcher) runDebounceCleanup() {
	for {
		select {
		case <-w.stopCh:
			// time.Ticker.Stop is idempotent; the prior nil-out write
			// raced with the field's construction-time read in
			// NewFSWatcher under -race. The field has no other reader so
			// dropping the write removes the race without needing a
			// mutex. (Review finding #14.)
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
	// Wait for every registered platform watcher + the debounce cleanup
	// goroutine to exit before returning. Without this the daemon can race
	// past Stop and close shared resources (journal, index) while a
	// platform goroutine is still running.
	w.watchWG.Wait()
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
	// Normalize to forward slashes so user-authored patterns ("node_modules/**")
	// match Windows paths uniformly. filepath.Match's split-on-separator alone
	// doesn't help when the pattern itself contains a separator.
	relSlash := filepath.ToSlash(rel)

	for _, pattern := range w.ignore {
		if matchIgnorePattern(pattern, relSlash) {
			return true
		}
		// Also test each path component individually so a bare-name pattern
		// like "*.log" or "node_modules" matches at any depth — preserves
		// the previous semantics for non-** patterns.
		parts := strings.Split(relSlash, "/")
		for _, part := range parts {
			matched, _ := filepath.Match(pattern, part)
			if matched {
				return true
			}
		}
	}

	return false
}

// matchIgnorePattern handles glob patterns with "**" support, which
// stdlib filepath.Match does NOT. The default ignore list ships with
// patterns like ".dfmt/**", "node_modules/**", and "**/__pycache__"
// that previously matched NOTHING because filepath.Match treated "**"
// as two literal stars. This function fixes that:
//
//   - "prefix/**" matches the prefix itself or anything under it
//   - "**/suffix" matches anything ending in /suffix
//   - "a/**/b"   matches any path with a as ancestor and b as descendant
//   - other patterns delegate to filepath.Match
//
// Without "**" support, enabling fs capture means .dfmt/journal.jsonl
// changes leak straight back into the watcher and the journal grows
// unbounded from its own writes — the canonical "infinite loop" bug.
func matchIgnorePattern(pattern, target string) bool {
	pattern = filepath.ToSlash(pattern)
	// Use stdlib path.Match (slash-only) instead of filepath.Match. On
	// Windows, filepath.Match treats the OS separator (\) as the glob
	// separator and lets `*` cross `/` because `/` is a non-separator
	// from its perspective — that's why `*.swp` was matching `src/foo.swp`
	// on Windows when the test (and the comment in the default ignore
	// list) expected anchored-to-root behavior. path.Match uses `/` on
	// every OS and matches the documented intent of these globs.
	if !strings.Contains(pattern, "**") {
		matched, _ := path.Match(pattern, target)
		return matched
	}

	// Suffix form: "**/x" — matches if path ends with "/x" or equals "x".
	if strings.HasPrefix(pattern, "**/") {
		suffix := pattern[3:]
		if !strings.Contains(suffix, "**") {
			if matched, _ := path.Match(suffix, target); matched {
				return true
			}
			if strings.HasSuffix(target, "/"+suffix) {
				return true
			}
			// Try with each tail of path so "**/x.go" matches a/b/x.go
			parts := strings.Split(target, "/")
			for i := 0; i < len(parts); i++ {
				tail := strings.Join(parts[i:], "/")
				if matched, _ := path.Match(suffix, tail); matched {
					return true
				}
			}
			return false
		}
	}

	// Prefix form: "x/**" — matches "x" itself or anything under "x".
	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3]
		if !strings.Contains(prefix, "**") {
			if target == prefix {
				return true
			}
			return strings.HasPrefix(target, prefix+"/")
		}
	}

	// "a/**/b" form: split on "**" and require each part anchored on a
	// path segment boundary. Works for the common case in our default list
	// without pulling in a full regex compiler.
	parts := strings.Split(pattern, "**")
	if len(parts) == 2 {
		pre, post := strings.TrimSuffix(parts[0], "/"), strings.TrimPrefix(parts[1], "/")
		if !strings.HasPrefix(target, pre) {
			return false
		}
		rest := strings.TrimPrefix(target, pre)
		rest = strings.TrimPrefix(rest, "/")
		// Require post matches some suffix of rest.
		if post == "" {
			return true
		}
		if matched, _ := path.Match(post, rest); matched {
			return true
		}
		segs := strings.Split(rest, "/")
		for i := 0; i < len(segs); i++ {
			tail := strings.Join(segs[i:], "/")
			if matched, _ := path.Match(post, tail); matched {
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
		// Consumer is too slow; record the drop so operators can see fswatch
		// backpressure via DroppedEvents() instead of silently losing signal.
		w.droppedEvents.Add(1)
	}
}
