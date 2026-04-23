package cli

import (
	"os"
	"testing"
)

// TestRunStopDaemonNotRunning covers the early-return branch when no daemon
// is alive: no PID file, no socket, nothing to signal.
func TestRunStopDaemonNotRunning(t *testing.T) {
	tmp := t.TempDir()
	prev := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prev })

	code := Dispatch([]string{"stop"})
	if code != 0 {
		t.Errorf("stop on fresh project: got %d, want 0", code)
	}
}

// TestRunListEmptyRegistry exercises runList's "no daemons" branch in both
// default and JSON output modes.
func TestRunListEmptyRegistry(t *testing.T) {
	// Use an isolated fake HOME so the real user registry isn't consulted.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_DATA_HOME", tmp)

	prevJSON := flagJSON
	t.Cleanup(func() { flagJSON = prevJSON })

	flagJSON = false
	if code := Dispatch([]string{"list"}); code != 0 {
		t.Errorf("list (default): got %d, want 0", code)
	}

	flagJSON = true
	if code := Dispatch([]string{"list"}); code != 0 {
		t.Errorf("list (json): got %d, want 0", code)
	}
}

// TestRunStatsBadProject covers the getProject error path in runStats.
func TestRunStatsBadProject(t *testing.T) {
	prev := flagProject
	// Point at a path that cannot be a valid project (null byte is illegal
	// on all OSes we target).
	flagProject = string([]byte{0x00})
	t.Cleanup(func() { flagProject = prev })

	// stats with invalid project should fail (non-zero) but must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = Dispatch([]string{"stats"})
}

// Ensure os import stays valid even if tests above don't use it directly.
var _ = os.Getenv
