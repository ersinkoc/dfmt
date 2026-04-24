//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// processExistsPlatform reports whether the given PID refers to a live process
// on Unix. Signal 0 is the POSIX idiom: it performs permission checks without
// delivering a signal.
func processExistsPlatform(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
