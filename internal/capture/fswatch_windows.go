//go:build windows

package capture

import (
	"os"
	"path/filepath"
	"time"
)

type trackedFile struct {
	modTime time.Time
	isDir   bool
}

// trackedDir holds the modification time of the directory itself (not its contents).
// We only walk a directory if its own modtime has changed since last scan.
type trackedDir struct {
	dirModTime time.Time
}

func init() {
	initWatcher = func(w *FSWatcher) {
		w.watchDirFn = windowsWatchDir
	}
}

func windowsWatchDir(w *FSWatcher, path string) {
	w.TrackGoroutine()
	go func() {
		defer w.UntrackGoroutine()
		windowsWatchLoop(w, path)
	}()
}

func windowsWatchLoop(w *FSWatcher, dirPath string) {
	knownFiles := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			// Only walk if the directory's own modtime has changed, indicating
			// potential file activity. This avoids O(tree_walk) every 2s when
			// the tree is static.
			if shouldWalkDir(dirPath, knownDirs) {
				scanDir(w, dirPath, knownFiles, knownDirs)
			}
		}
	}
}

// shouldWalkDir checks if the directory's own modification time has changed
// since the last scan. Directory modtime changes when files are added, removed,
// or renamed inside it. Use dirPath as the key — the prior implementation
// called filepath.Rel(dirPath, dirPath) which always returns "." and made the
// map effectively single-entry, dropping mod-time tracking for sibling dirs.
func shouldWalkDir(dirPath string, knownDirs map[string]trackedDir) bool {
	info, err := os.Stat(dirPath)
	if err != nil {
		return false
	}
	prev, ok := knownDirs[dirPath]
	if !ok {
		knownDirs[dirPath] = trackedDir{dirModTime: info.ModTime()}
		return true
	}
	if !prev.dirModTime.Equal(info.ModTime()) {
		knownDirs[dirPath] = trackedDir{dirModTime: info.ModTime()}
		return true
	}
	return false
}

// maxTrackedEntries caps knownFiles/knownDirs so a hostile or deep tree
// cannot grow the tracking maps without bound. When exceeded we drop half the
// entries at random — the next walk will re-populate anything still present.
const maxTrackedEntries = 100_000

func scanDir(w *FSWatcher, dirPath string, known map[string]trackedFile, knownDirs map[string]trackedDir) {
	filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if w.shouldIgnore(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		rel, _ := filepath.Rel(dirPath, path)
		tf := trackedFile{modTime: info.ModTime(), isDir: info.IsDir()}

		if prev, ok := known[rel]; ok {
			if !prev.modTime.Equal(info.ModTime()) {
				w.emitEvent(path, info.IsDir(), "modify")
				known[rel] = tf
			}
		} else {
			w.emitEvent(path, info.IsDir(), "create")
			known[rel] = tf
		}

		return nil
	})

	// Check for deletions - files that existed before but not now
	for rel := range known {
		fullPath := filepath.Join(dirPath, rel)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			w.emitEvent(fullPath, false, "delete")
			delete(known, rel)
		}
	}

	// Evict knownDirs entries whose directory no longer exists. Without this
	// every rename/delete leaves a zombie modtime in the map.
	for dp := range knownDirs {
		if _, err := os.Stat(dp); os.IsNotExist(err) {
			delete(knownDirs, dp)
		}
	}

	// Hard cap as a final safety net against a tree that keeps growing even
	// after zombie eviction.
	pruneOverflow(known, maxTrackedEntries)
	pruneOverflowDirs(knownDirs, maxTrackedEntries)
}

func pruneOverflow(m map[string]trackedFile, cap int) {
	if len(m) <= cap {
		return
	}
	drop := len(m) - cap/2
	for k := range m {
		if drop <= 0 {
			break
		}
		delete(m, k)
		drop--
	}
}

func pruneOverflowDirs(m map[string]trackedDir, cap int) {
	if len(m) <= cap {
		return
	}
	drop := len(m) - cap/2
	for k := range m {
		if drop <= 0 {
			break
		}
		delete(m, k)
		drop--
	}
}
