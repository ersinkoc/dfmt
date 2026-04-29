//go:build windows

package daemon

import "syscall"

const windowsStillActive = 259

// processExistsPlatform reports whether the given PID refers to a live process
// on Windows. Opens a handle with PROCESS_QUERY_INFORMATION and inspects the
// exit code: STILL_ACTIVE means the process is running.
func processExistsPlatform(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil || h == 0 {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == windowsStillActive
}

// geteuid returns -1 on Windows: the POSIX euid concept does not apply,
// so the V-15 root-warn check in Daemon.Start is a no-op on this platform.
// Windows daemons running as SYSTEM should be flagged separately if that
// ever becomes a relevant threat surface.
func geteuid() int { return -1 }
