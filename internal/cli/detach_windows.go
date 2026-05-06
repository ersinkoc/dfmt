//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// Windows process-creation flags. We do not import x/sys/windows to
// keep the dependency policy intact (stdlib + x/sys/unix only); the
// numeric constants are stable parts of the Win32 API.
const (
	winDetachedProcess     = 0x00000008
	winCreateNoWindow      = 0x08000000
	winCreateNewProcessGrp = 0x00000200
)

// detachSysProcAttr configures cmd so the child survives the parent's
// exit and is fully detached from the parent's console. On Windows the
// child gets its own process group and no console window — the only
// way the foreground `dfmt` invocation can exit promptly while the
// daemon role keeps serving.
func detachSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: winDetachedProcess | winCreateNoWindow | winCreateNewProcessGrp,
		HideWindow:    true,
	}
}
