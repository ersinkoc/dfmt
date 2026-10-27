//go:build !windows

package daemon

import (
	"bytes"
	"os"
	"strconv"
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
	if p.Signal(syscall.Signal(0)) != nil {
		return false
	}
	// kill(pid, 0) succeeds for a zombie: an exited process its parent has
	// not reaped yet. A daemon spawned by a long-lived `dfmt mcp` becomes
	// one when it exits, and counting it as alive made `dfmt stop` report
	// "refused to stop" — and skip the restart — for a daemon that had
	// already shut down cleanly. Linux exposes the state; elsewhere the
	// signal probe is all there is.
	return !isZombie(pid)
}

// isZombie reports whether /proc says pid has exited but not been reaped.
// False when /proc is unavailable (non-Linux), i.e. no claim is made.
func isZombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// Format: "pid (comm) S ..." — comm may contain spaces or parens, so
	// the state is the first field after the LAST ')'.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		return false
	}
	return data[i+2] == 'Z'
}

// geteuid returns the effective user ID on Unix. Used by Start to warn the
// operator if the daemon is running as root (V-15).
func geteuid() int { return os.Geteuid() }
