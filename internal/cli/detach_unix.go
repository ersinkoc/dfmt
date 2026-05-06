//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detachSysProcAttr configures cmd so the child survives the parent's
// exit. On Unix this means a fresh session (Setsid) so the child is
// not killed by SIGHUP when the controlling terminal dies and is not
// reaped by the parent's process group.
func detachSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
