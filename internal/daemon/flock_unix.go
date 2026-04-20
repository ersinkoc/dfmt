//go:build !windows
// +build !windows

package daemon

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFlock(f *os.File, nonblock bool) error {
	flags := unix.LOCK_EX
	if nonblock {
		flags |= unix.LOCK_NB
	}
	return unix.Flock(int(f.Fd()), flags)
}

func unlockFlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
