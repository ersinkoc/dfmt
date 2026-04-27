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

// TestRunQuickstartFreshDir covers the happy path: a brand-new directory,
// `dfmt quickstart` should ensure .dfmt/ exists, attempt agent setup
// (DetectWithOverride may legitimately return zero in a test environment
// with no real agents installed), and exit cleanly. Either:
//   - 0 when at least one stub agent is configured, or
//   - 1 when no agents are detected (the empty-detection branch)
//
// Both are valid outcomes; the test asserts the .dfmt directory is the side
// effect we care about regardless.
func TestRunQuickstartFreshDir(t *testing.T) {
	tmp := t.TempDir()

	// Isolate HOME so DetectWithOverride doesn't pick up the developer's
	// real installs and accidentally write to ~/.config or ~/.cursor.
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	code := Dispatch([]string{"quickstart", "-dir", tmp})

	// .dfmt directory must exist regardless of agent-detection outcome.
	if _, err := os.Stat(tmp + "/.dfmt"); err != nil {
		t.Fatalf("quickstart did not create .dfmt: %v", err)
	}

	// Exit code: 0 (configured something) or 1 (no agents). Anything else
	// is a regression — e.g., a panic-recovery turning into 2.
	if code != 0 && code != 1 {
		t.Errorf("quickstart exit code: got %d, want 0 or 1", code)
	}
}

// TestRunQuickstartHelp covers the -h flag — flag.ErrHelp must return 0
// without writing any state (no .dfmt created).
func TestRunQuickstartHelp(t *testing.T) {
	code := runQuickstart([]string{"-h"})
	if code != 0 {
		t.Errorf("quickstart -h: got %d, want 0", code)
	}
}

// TestRunQuickstartBadFlag covers parse-error path: unknown flag must return
// 2 (flag.ContinueOnError convention used elsewhere in this package).
func TestRunQuickstartBadFlag(t *testing.T) {
	code := runQuickstart([]string{"--no-such-flag"})
	if code != 2 {
		t.Errorf("quickstart --no-such-flag: got %d, want 2", code)
	}
}

// Ensure os import stays valid even if tests above don't use it directly.
var _ = os.Getenv
