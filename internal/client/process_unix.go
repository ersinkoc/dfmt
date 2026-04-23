//go:build !windows

package client

import (
	"os"
	"syscall"
)

// isProcessRunningPlatform reports whether the given PID refers to a live
// process on Unix. Sending signal 0 is the POSIX idiom: it performs the
// permission checks without actually delivering a signal, so a nil return
// means the process exists and we're allowed to signal it.
func isProcessRunningPlatform(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
