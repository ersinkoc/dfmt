//go:build windows

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
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

// killTreeTimeout bounds the taskkill call itself. Terminating a tree is a
// handful of kernel calls; if taskkill has not returned in this window it is
// wedged and the caller is better served by the direct-kill fallback.
const killTreeTimeout = 5 * time.Second

// killProcessTree terminates the child and every descendant it spawned.
//
// Windows has no process groups in the Unix sense: a child spawned by
// bash.exe is an independent process whose only link to its parent is the
// (advisory) parent-pid field. os.Process.Kill maps to TerminateProcess,
// which kills exactly one process — so killing the shell of `bash -c "a; b"`
// left the grandchild running and holding the inherited stdout pipe, which
// is what made every exec deadline unenforceable.
//
// taskkill /T walks that parent-pid tree and /F forces termination. It ships
// with every Windows SKU including Server Core. We resolve it under
// SystemRoot rather than trusting PATH: the daemon inherits the operator's
// PATH, and a writable directory earlier in it would otherwise decide what
// runs here.
//
// A Job Object (KILL_ON_JOB_CLOSE) would be the tighter mechanism, but
// os/exec gives no hook between CreateProcess and the first instruction of
// the child, so the assignment would race the child's own spawns. taskkill
// has no such window because it runs after the fact.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), killTreeTimeout)
	defer cancel()

	taskkill := "taskkill.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		taskkill = filepath.Join(root, "System32", "taskkill.exe")
	}
	kill := exec.CommandContext(ctx, taskkill, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	configureChildProc(kill)
	if err := kill.Run(); err != nil {
		// taskkill exits non-zero when the tree is already gone (the deadline
		// raced the command finishing), and when it is genuinely missing.
		// Neither case is worth surfacing: fall back to killing the leader so
		// the pipe closes either way.
		return cmd.Process.Kill()
	}
	return nil
}
