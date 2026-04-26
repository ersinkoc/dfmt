//go:build windows

package client

import (
	"errors"
	"syscall"
)

// windowsStillActive is the exit code reported by GetExitCodeProcess while the
// process is still running (STILL_ACTIVE macro in the Windows SDK).
const windowsStillActive = 259

// errAccessDenied corresponds to ERROR_ACCESS_DENIED (5). Returned by
// OpenProcess when the caller lacks the requested rights — typically because
// the daemon runs as a different user (admin/SYSTEM) than the CLI invocation.
const errAccessDenied syscall.Errno = 5

// isProcessRunningPlatform reports whether the given PID refers to a live
// process on Windows. We open a handle with PROCESS_QUERY_INFORMATION and
// read the exit code: STILL_ACTIVE means the process hasn't terminated.
//
// Failure modes deserve different verdicts:
//   - ERROR_ACCESS_DENIED → the process EXISTS but we can't query it (daemon
//     under a different user or token). Returning false here would cause
//     cleanupStaleDaemon to delete a live daemon's PID file. Treat as alive.
//   - Any other OpenProcess error → process truly gone (or never existed).
func isProcessRunningPlatform(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == errAccessDenied {
			return true
		}
		return false
	}
	if h == 0 {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == windowsStillActive
}
