//go:build !windows

package sandbox

import "os/exec"

// configureChildProc is a no-op outside Windows. Unix has no console-
// allocation step for child processes, so a daemon detached via setsid()
// spawns subprocesses exactly the same way a foreground one does. See the
// Windows variant for the failure this guards against there.
func configureChildProc(cmd *exec.Cmd) {}
