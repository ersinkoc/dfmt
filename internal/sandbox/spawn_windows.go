//go:build windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// winCreateNoWindow is CREATE_NO_WINDOW. The numeric constant is a stable
// part of the Win32 API; we spell it out rather than importing
// x/sys/windows to keep the dependency policy intact (ADR-0004).
const winCreateNoWindow = 0x08000000

// configureChildProc pins the console semantics of every subprocess the
// sandbox spawns.
//
// Why this exists: the global daemon is started detached, with
// DETACHED_PROCESS in its creation flags (see internal/cli/detach_windows.go).
// Win32 documents CREATE_NO_WINDOW as *ignored* when combined with
// DETACHED_PROCESS, so the daemon ends up owning no console at all. When a
// console-less process then spawns a console application (bash.exe, cmd.exe,
// go.exe) without saying anything about consoles, Windows allocates a fresh
// console for the child and starts a conhost.exe to back it. From a fully
// detached process that allocation wedges: the child sits at 0 CPU seconds,
// never runs its command, never writes a byte to the stdout pipe, and the
// daemon's io.ReadAll blocks until the caller's RPC deadline fires.
//
// The symptom was total: with a detached daemon, EVERY dfmt_exec call hung
// forever, while read/glob/grep (pure Go, no subprocess) kept working. Under a
// foreground daemon the same binary returned in ~40 ms, which is why no unit
// test caught it — the test binary always has a console.
//
// CREATE_NO_WINDOW alone (no DETACHED_PROCESS) gives the child a real console
// object with no visible window, which is what console applications expect,
// and the spawn completes regardless of whether the parent has a console.
func configureChildProc(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= winCreateNoWindow
}
