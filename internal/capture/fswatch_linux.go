//go:build linux

package capture

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

func init() {
	initWatcher = func(w *FSWatcher) {
		w.watchDirFn = linuxWatchDir
	}
}

func linuxWatchDir(w *FSWatcher, path string) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return
	}

	_, err = unix.InotifyAddWatch(fd, path,
		unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	if err != nil {
		_ = unix.Close(fd)
		return
	}

	// Track the watched path so Stop() can wake the inotify reader by writing
	// a marker file into this directory. close(fd) alone does NOT unblock a
	// goroutine parked in unix.Read on Linux.
	w.addWatchedPath(path)

	w.TrackGoroutine()
	go func() {
		defer w.UntrackGoroutine()
		linuxWatchLoop(w, fd, path)
	}()
}

func linuxWatchLoop(w *FSWatcher, fd int, dirPath string) {
	const eventSize = 16 + unix.PathMax
	buf := make([]byte, eventSize*1024)
	// Ensure the inotify fd is released when the loop exits, regardless of
	// whether Read returned an error or we observed stopCh.
	defer func() { _ = unix.Close(fd) }()

	for {
		n, err := unix.Read(fd, buf)
		if err != nil {
			return
		}

		select {
		case <-w.stopCh:
			return
		default:
		}

		for offset := 0; offset < n; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameLen := int(event.Len)

			var name string
			if nameLen > 0 {
				nameBytes := buf[offset+16 : offset+16+nameLen]
				if len(nameBytes) > 0 && nameBytes[len(nameBytes)-1] == 0 {
					nameBytes = nameBytes[:len(nameBytes)-1]
				}
				name = string(nameBytes)
			}

			fullPath := dirPath
			if name != "" {
				fullPath = filepath.Join(fullPath, name)
			}

			var operation string
			if event.Mask&unix.IN_CREATE != 0 {
				operation = "create"
			} else if event.Mask&unix.IN_DELETE != 0 || event.Mask&unix.IN_MOVED_FROM != 0 {
				operation = "delete"
			} else if event.Mask&unix.IN_MODIFY != 0 || event.Mask&unix.IN_MOVED_TO != 0 {
				operation = "modify"
			} else {
				offset += 16 + int(event.Len)
				continue
			}

			isDir := event.Mask&unix.IN_ISDIR != 0
			w.emitEvent(fullPath, isDir, operation)

			// Register a watcher on any directory created after startup so
			// later edits inside the new subtree aren't silently lost — the
			// parent's watch fires for the mkdir but not for files inside
			// the new dir, which has no watch of its own yet. Mirrors the
			// Windows fix in Round 6. Catch-up walk registers nested dirs
			// and emits creates for files that appeared between mkdir and
			// our InotifyAddWatch.
			if isDir && operation == "create" && !w.shouldIgnore(fullPath) {
				catchUpNewDir(w, fullPath)
			}

			offset += 16 + int(event.Len)
		}
	}
}

// catchUpNewDir registers watchers on every subdirectory of newDir and emits
// create events for any files that already exist there. Best-effort: walk
// errors are swallowed because the subtree may have been deleted again before
// we reach it.
func catchUpNewDir(w *FSWatcher, newDir string) {
	if w.watchDirFn == nil {
		return
	}
	w.watchDirFn(w, newDir)
	_ = filepath.Walk(newDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == newDir {
			return nil
		}
		if w.shouldIgnore(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			w.watchDirFn(w, path)
			w.emitEvent(path, true, "create")
		} else {
			w.emitEvent(path, false, "create")
		}
		return nil
	})
}
