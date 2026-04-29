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

// geteuid returns the effective user ID on Unix. Used by Start to warn the
// operator if the daemon is running as root (V-15).
func geteuid() int { return os.Geteuid() }
