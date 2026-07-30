package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The tests in this file guard the exec deadline. The regression they exist
// for: req.Timeout was enforced against the shell only, so a command that
// forked (which `bash -c "a; b"` always does) left a descendant holding the
// inherited stdout pipe. The deadline fired on schedule, killed the shell,
// and then io.ReadAll blocked on that descendant's copy of the pipe for the
// command's full natural duration.
//
// Measured before the fix, with Timeout=3s:
//
//	code="sleep 20"                       elapsed=20.7s exit=1 timed_out=false stdout=""
//	code="echo START; sleep 20; echo END" elapsed=20.1s exit=1 timed_out=false stdout="START\n"
//
// Every symptom above is asserted against below: the call must return near
// the deadline, say that it timed out, and keep whatever the command printed
// before it was killed.

// execTimeoutBudget is how long past the deadline the call may take before
// the test calls it a failure. Generous enough for a loaded CI box to spawn
// a shell, run taskkill/kill, and drain the pipes (the kill grace period
// alone is execWaitDelay), tight enough that "waited for the command to
// finish on its own" — the actual bug — cannot pass.
const execTimeoutBudget = 6 * time.Second

func TestExecEnforcesTimeoutOnForkingCommand(t *testing.T) {
	sb := execTestSandbox(t)

	const deadline = 2 * time.Second
	start := time.Now()
	resp, err := sb.Exec(context.Background(), ExecReq{
		// Compound command: the shell forks, so `sleep` is a grandchild of
		// the process we spawn. This is the shape that hung.
		Code:    "echo STARTED; sleep 30; echo NEVER",
		Return:  "raw",
		Timeout: deadline,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if elapsed > deadline+execTimeoutBudget {
		t.Errorf("Exec returned after %s, want <= %s: the deadline did not stop the command",
			elapsed.Round(100*time.Millisecond), deadline+execTimeoutBudget)
	}
	if !resp.TimedOut {
		t.Error("TimedOut = false, want true: a killed command must be reported as timed out")
	}
	if !strings.Contains(resp.Stderr, "timed out") {
		t.Errorf("Stderr = %q, want it to mention the timeout", resp.Stderr)
	}
	// Output produced before the deadline is the most useful thing the agent
	// gets from a hung command; dropping it (as the pre-fix error path did)
	// leaves nothing to diagnose with.
	if !strings.Contains(resp.RawStdout, "STARTED") {
		t.Errorf("RawStdout = %q, want the output printed before the deadline", resp.RawStdout)
	}
	if strings.Contains(resp.RawStdout, "NEVER") {
		t.Errorf("RawStdout = %q, contains output from after the deadline", resp.RawStdout)
	}
}

// TestExecTimeoutKillsDescendants asserts the tree is actually dead, not just
// detached from our pipes. A descendant that survives keeps running the work
// the agent asked to stop — and on the next call it competes for the same
// files and ports.
func TestExecTimeoutKillsDescendants(t *testing.T) {
	sb := execTestSandbox(t)

	dir := t.TempDir()
	sb = NewSandboxWithPolicyAndRuntimes(dir, sb.policy, sb.runtimes)

	// The descendant writes a marker file after a delay that comfortably
	// outlives the deadline. If the kill worked, the marker never appears.
	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "(sleep 4; echo alive > survivor.txt) & echo SPAWNED; wait",
		Return:  "raw",
		Timeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !resp.TimedOut {
		t.Fatalf("TimedOut = false, want true (exit=%d stdout=%q)", resp.Exit, resp.RawStdout)
	}

	// Wait past the descendant's own delay before checking.
	time.Sleep(6 * time.Second)
	if _, err := sb.Read(context.Background(), ReadReq{Path: "survivor.txt"}); err == nil {
		t.Error("survivor.txt exists: a descendant outlived the timeout kill")
	}
}

// TestExecUnderDeadlineIsNotReportedAsTimedOut pins the other direction —
// a command that finishes inside its budget must not be flagged, and must
// keep its real exit code.
func TestExecUnderDeadlineIsNotReportedAsTimedOut(t *testing.T) {
	sb := execTestSandbox(t)

	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "echo quick; exit 3",
		Return:  "raw",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.TimedOut {
		t.Error("TimedOut = true for a command that finished well inside its budget")
	}
	if resp.Exit != 3 {
		t.Errorf("Exit = %d, want 3: the command's own status must survive", resp.Exit)
	}
	if !strings.Contains(resp.RawStdout, "quick") {
		t.Errorf("RawStdout = %q, want the command output", resp.RawStdout)
	}
}
