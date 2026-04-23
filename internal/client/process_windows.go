//go:build windows

package client

import "syscall"

// windowsStillActive is the exit code reported by GetExitCodeProcess while the
// process is still running (STILL_ACTIVE macro in the Windows SDK).
const windowsStillActive = 259

// isProcessRunningPlatform reports whether the given PID refers to a live
// process on Windows. We open a handle with PROCESS_QUERY_INFORMATION and
// read the exit code: STILL_ACTIVE means the process hasn't terminated.
func isProcessRunningPlatform(pid int) bool {
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
