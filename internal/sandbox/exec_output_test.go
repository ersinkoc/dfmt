package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The tests in this file assert that Exec actually returns the subprocess's
// OUTPUT, not merely a plausible exit code.
//
// Both regressions they guard against shipped and went unnoticed because the
// pre-existing TestSandboxExec asserts `resp.Exit == 0` and nothing else — an
// Exec that returns a correct exit code with empty stdout and empty stderr
// passes it:
//
//   1. On Windows, a daemon started with DETACHED_PROCESS owns no console.
//      Spawning a console application from it without CREATE_NO_WINDOW wedges
//      the child at 0 CPU seconds — it never runs its command and never writes
//      a byte. See spawn_windows.go. Every dfmt_exec through a detached (i.e.
//      production) daemon returned nothing.
//   2. execImpl set cmd.Stderr to a capped buffer but never assigned
//      ExecResp.Stderr, so every compiler error, stack trace, and test-failure
//      banner arrived as an empty body with a bare non-zero exit code.
//
// Neither reproduces under `go test` alone — the test binary always has a
// console, and (2) is invisible unless the assertion looks at Stderr. These
// tests look at the bytes.

// execTestSandbox builds a sandbox for the given working directory, skipping
// the test when no POSIX shell is available (bare Windows without Git Bash).
func execTestSandbox(t *testing.T) *SandboxImpl {
	t.Helper()
	sb := NewSandbox(t.TempDir())
	lang := DetectShell()
	if rt, ok := sb.runtimes.Get(lang); !ok || !rt.Available {
		_ = sb.runtimes.Probe(context.Background())
	}
	if rt, ok := sb.runtimes.Get(lang); !ok || !rt.Available {
		t.Skipf("no shell runtime available (%s)", lang)
	}
	return sb
}

// TestExecReturnsStdout is the regression test for the detached-daemon spawn
// wedge: a successful command must come back with its stdout populated, not
// just exit 0.
func TestExecReturnsStdout(t *testing.T) {
	sb := execTestSandbox(t)

	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "echo hello-dfmt",
		Intent:  "greeting",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.Exit != 0 {
		t.Fatalf("Exit = %d, want 0 (stderr: %q)", resp.Exit, resp.Stderr)
	}
	// RawStdout is the pre-filter capture and is what the content store
	// stashes; assert on it so an over-eager return policy cannot mask a
	// genuinely empty subprocess read.
	if !strings.Contains(resp.RawStdout, "hello-dfmt") {
		t.Errorf("RawStdout = %q, want it to contain %q", resp.RawStdout, "hello-dfmt")
	}
	if resp.RawStdout == "" {
		t.Error("RawStdout is empty: subprocess produced no output at all " +
			"(detached-console spawn wedge, see spawn_windows.go)")
	}
}

// TestExecReturnsStderr is the regression test for the unassigned
// ExecResp.Stderr field. A failing command must explain itself.
func TestExecReturnsStderr(t *testing.T) {
	sb := execTestSandbox(t)

	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "echo diagnostic-detail 1>&2; exit 3",
		Intent:  "failure reason",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.Exit != 3 {
		t.Errorf("Exit = %d, want 3", resp.Exit)
	}
	if !strings.Contains(resp.Stderr, "diagnostic-detail") {
		t.Errorf("Stderr = %q, want it to contain %q — a non-zero exit with no "+
			"stderr leaves the agent nothing to act on", resp.Stderr, "diagnostic-detail")
	}
}

// TestExecStderrDoesNotLeakIntoStdout guards the channel separation: a
// command that writes only to stderr must not have those bytes appear as
// stdout, or intent matching would score diagnostics as content.
func TestExecStderrDoesNotLeakIntoStdout(t *testing.T) {
	sb := execTestSandbox(t)

	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "echo only-on-stderr 1>&2",
		Intent:  "check channels",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if strings.Contains(resp.RawStdout, "only-on-stderr") {
		t.Errorf("RawStdout = %q, want stderr bytes kept out of stdout", resp.RawStdout)
	}
	if !strings.Contains(resp.Stderr, "only-on-stderr") {
		t.Errorf("Stderr = %q, want it to contain %q", resp.Stderr, "only-on-stderr")
	}
}

// TestCappedBufferAbsorbsBeyondLimit pins the cappedBuffer contract that
// execImpl depends on: retain up to limit, report every write fully
// consumed, and flag truncation.
//
// The "report fully consumed" half matters — returning a short write would
// make exec.Cmd's copier abort with ErrShortWrite and turn a chatty-but-
// successful command into a spurious Exec failure.
func TestCappedBufferAbsorbsBeyondLimit(t *testing.T) {
	c := &cappedBuffer{limit: 8}

	n, err := c.Write([]byte("1234"))
	if err != nil || n != 4 {
		t.Fatalf("Write(4) = (%d, %v), want (4, nil)", n, err)
	}
	if c.Truncated() {
		t.Error("Truncated() = true before the limit was reached")
	}

	// Straddles the cap: 4 retained, 8 dropped, all 12 reported written.
	n, err = c.Write([]byte("567890ab cdef"[:12]))
	if err != nil || n != 12 {
		t.Fatalf("Write(12) = (%d, %v), want (12, nil)", n, err)
	}
	if got := c.Len(); got != 8 {
		t.Errorf("Len() = %d, want 8 (the limit)", got)
	}
	if got := c.String(); got != "12345678" {
		t.Errorf("String() = %q, want %q", got, "12345678")
	}
	if !c.Truncated() {
		t.Error("Truncated() = false after writing past the limit")
	}

	// Writes once already full are still reported as consumed.
	n, err = c.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Fatalf("Write past full = (%d, %v), want (4, nil)", n, err)
	}
	if got := c.Len(); got != 8 {
		t.Errorf("Len() = %d after write past full, want 8", got)
	}
}
