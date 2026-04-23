//go:build linux
// +build linux

package capture

import (
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
		unix.Close(fd)
		return
	}

	go linuxWatchLoop(w, fd, path)
}

func linuxWatchLoop(w *FSWatcher, fd int, dirPath string) {
	const eventSize = 16 + unix.PathMax
	buf := make([]byte, eventSize*1024)

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
				fullPath = fullPath + "/" + name
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

			offset += 16 + int(event.Len)
		}
	}
}
