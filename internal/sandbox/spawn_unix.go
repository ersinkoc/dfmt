//go:build !windows

package sandbox

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureChildProc puts the child in its own process group.
//
// Unix has no console-allocation step for child processes (see the Windows
// variant for the failure this guards against there), but it does have the
// orphan problem: `bash -c "a; b"` forks, and killing only the shell leaves
// the grandchild running AND holding the stdout pipe we are reading. Setpgid
// makes the shell a process-group leader so killProcessTree can signal the
// whole tree with one kill(-pgid).
func configureChildProc(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree SIGKILLs the child's entire process group.
//
// The negative-pid form addresses the group, which is why configureChildProc
// must have set Setpgid — without it, -pid would either fail or (if the
// daemon happened to lead a group with that id) signal the wrong processes.
// Falls back to killing the leader alone if the group signal fails, so a
// missing group is still better than no kill at all.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// Already gone — the deadline raced the command finishing.
			return nil
		}
		return cmd.Process.Kill()
	}
	return nil
}
