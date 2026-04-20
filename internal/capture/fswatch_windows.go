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

func init() {
	initWatcher = func(w *FSWatcher) {
		w.watchDirFn = windowsWatchDir
	}
}

func windowsWatchDir(w *FSWatcher, path string) {
	go windowsWatchLoop(w, path)
}

func windowsWatchLoop(w *FSWatcher, dirPath string) {
	knownFiles := make(map[string]trackedFile)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			scanDir(w, dirPath, knownFiles)
		}
	}
}

func scanDir(w *FSWatcher, dirPath string, known map[string]trackedFile) {
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
}