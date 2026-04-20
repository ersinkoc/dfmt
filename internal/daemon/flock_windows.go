//go:build windows
// +build windows

package daemon

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32     = windows.NewLazySystemDLL("kernel32.dll")
	procLockFileEx  = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	LOCKFILE_EXCLUSIVE_LOCK   = 2
	LOCKFILE_FAIL_IMMEDIATELY = 1
)

func lockFlock(f *os.File, nonblock bool) error {
	var flags uint32 = LOCKFILE_EXCLUSIVE_LOCK
	if nonblock {
		flags |= LOCKFILE_FAIL_IMMEDIATELY
	}

	// Get underlying handle
	handle := syscall.Handle(f.Fd())

	var overlapped syscall.Overlapped
	overlapped.Offset = 0
	overlapped.OffsetHigh = 0

	r1, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if r1 == 0 {
		return err
	}
	return nil
}

func unlockFlock(f *os.File) error {
	handle := syscall.Handle(f.Fd())

	var overlapped syscall.Overlapped
	overlapped.Offset = 0
	overlapped.OffsetHigh = 0

	r1, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if r1 == 0 {
		return err
	}
	return nil
}
