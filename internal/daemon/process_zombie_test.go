//go:build linux

package daemon

import (
	"os/exec"
	"testing"
	"time"
)

// TestProcessExistsIgnoresZombie: kill(pid, 0) succeeds for an exited,
// unreaped child, so a daemon whose long-lived spawner (dfmt mcp) never
// reaped it looked alive forever, and `dfmt stop` reported it "refused to
// stop" after it had already shut down.
func TestProcessExistsIgnoresZombie(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Wait() }()

	// Wait for it to exit without reaping it.
	deadline := time.Now().Add(5 * time.Second)
	for !isZombie(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !isZombie(pid) {
		t.Skip("child did not become a zombie in time")
	}
	if ProcessExists(pid) {
		t.Error("ProcessExists = true for a zombie")
	}
}
