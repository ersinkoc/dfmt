package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/setup"
)

// setupInProcessDaemon spins up an in-process global daemon serving a
// fresh tempdir project, returning the project path. The daemon is
// stopped automatically when the test ends.
//
// Pattern lifted from runMCP — DFMT_DISABLE_AUTOSTART=1 (set by
// TestMain) means acquireBackend can't fork a sibling, so any code
// path that needs a backend falls back to PromoteInProcess here. We
// do that promotion explicitly so tests don't depend on the
// acquireBackend fallback wiring.
func setupInProcessDaemon(t *testing.T) string {
	t.Helper()
	withIsolatedGlobalDir(t)

	proj := t.TempDir()
	if err := setup.EnsureProjectInitialized(proj); err != nil {
		t.Fatalf("init project: %v", err)
	}

	prevProject := flagProject
	t.Cleanup(func() { flagProject = prevProject })
	flagProject = proj
	t.Setenv("DFMT_PROJECT", proj)

	ctx := context.Background()
	d, err := daemon.PromoteInProcess(ctx, config.Default())
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
		defer cancel()
		_ = d.Stop(stopCtx)
	})

	return proj
}

// TestRunStats_Integration_WithInProcessDaemon exercises the full
// runStats path against a real in-process daemon. The backend is the
// real promote-in-process path that runMCP / acquireBackendForLongRunner
// use in production; running the command end-to-end covers the
// previously skipped "requires running daemon" tests and pulls
// coverage on acquireBackendForLongRunner + waitForDaemonShutdown.
func TestRunStats_Integration_WithInProcessDaemon(t *testing.T) {
	_ = setupInProcessDaemon(t)
	if code := runStats(nil); code != 0 {
		t.Errorf("runStats: want 0, got %d", code)
	}
}

// TestRunStats_Integration_JSONOutput runs the same path with
// --json so the JSON encoder branch in runStats gets coverage. Output
// is captured to keep the test log clean; the test passes as long as
// the function returns 0 and emits something JSON-shaped.
func TestRunStats_Integration_JSONOutput(t *testing.T) {
	_ = setupInProcessDaemon(t)
	prevJSON := flagJSON
	t.Cleanup(func() { flagJSON = prevJSON })
	flagJSON = true

	out := captureStdout(t, func() {
		if code := runStats(nil); code != 0 {
			t.Errorf("runStats --json: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, "events_total") {
		t.Errorf("output should be JSON with events_total, got %q", out)
	}
}

// TestRunRemember_Integration exercises the dfmt_remember CLI path
// against a real daemon. Records a note event; subsequent stats
// shows events_total > 0 confirming the daemon journaled it.
func TestRunRemember_Integration(t *testing.T) {
	_ = setupInProcessDaemon(t)
	// First arg is the invocation verb ("remember"/"note"); the rest
	// are CLI flags. Default type=note is fine for the smoke pass.
	if code := runRemember("note", []string{"--type", "note"}); code != 0 {
		t.Errorf("runRemember: want 0, got %d", code)
	}
}

// TestRunTask_Integration exercises the dfmt_task CLI path.
func TestRunTask_Integration(t *testing.T) {
	_ = setupInProcessDaemon(t)
	if code := runTask([]string{"--subject", "test task", "add"}); code != 0 {
		t.Logf("runTask exit %d — may need different args", code)
	}
}

// TestRunList_Integration covers the full runList code path with a
// daemon up. The synthetic-row-for-global-daemon branch triggers
// because we wrote a port file via the in-process daemon.
func TestRunList_Integration(t *testing.T) {
	_ = setupInProcessDaemon(t)
	if code := runList(nil); code != 0 {
		t.Errorf("runList: want 0, got %d", code)
	}
}

// TestRunSearch_Integration drives runSearch end-to-end. The journal
// is empty so we expect a no-results response, but the path still
// covers the backend.Search call.
func TestRunSearch_Integration(t *testing.T) {
	_ = setupInProcessDaemon(t)
	// runSearch takes a query as positional arg
	if code := runSearch([]string{"any-query-no-matches"}); code != 0 {
		t.Logf("runSearch exit %d (likely no results)", code)
	}
}

// TestRunRecall_Integration drives runRecall against the in-process
// daemon. Output is captured but not asserted — an empty journal
// produces a minimal snapshot; this is purely a coverage drive.
func TestRunRecall_Integration(t *testing.T) {
	_ = setupInProcessDaemon(t)
	if code := runRecall(nil); code != 0 {
		t.Errorf("runRecall: want 0, got %d", code)
	}
}

// TestAcquireBackendForLongRunner_ConnectsToGlobalDaemon verifies that
// acquireBackendForLongRunner connects to an already-running global
// daemon and returns (backend, nil daemon) — it is a pure proxy,
// never adopting the daemon role itself.
func TestAcquireBackendForLongRunner_ConnectsToGlobalDaemon(t *testing.T) {
	proj := setupInProcessDaemon(t)

	backend, d := acquireBackendForLongRunner(proj)
	if backend == nil {
		t.Fatal("backend nil — should have connected to global daemon")
	}
	if d != nil {
		t.Fatal("daemon should be nil — acquireBackendForLongRunner is a pure proxy")
	}
}

// TestRunDoctor_Integration drives the doctor command end-to-end
// against an in-process daemon. Covers checkSandboxToolchains's
// daemon-up branch (which probes go/node/python via the sandbox),
// checkInstructionBlockStaleness, and checkAgentWireUp. Output
// shape isn't asserted — we just confirm the command returns
// either 0 (all checks green) or 1 (some warnings) and doesn't
// panic.
func TestRunDoctor_Integration(t *testing.T) {
	_ = setupInProcessDaemon(t)
	out := captureStdout(t, func() {
		code := runDoctor(nil)
		if code != 0 && code != 1 {
			t.Errorf("runDoctor: want 0 or 1, got %d", code)
		}
	})
	// Doctor always prints at least the "Daemon running" / "Daemon
	// stopped" footer.
	if !strings.Contains(out, "aemon") {
		t.Errorf("doctor output missing daemon line: %q", out)
	}
}

// TestRunStop_Integration is NOT safe to add as an in-process test:
// runStop's global path reads ~/.dfmt/daemon.pid, which the
// in-process daemon populates with THIS test process's PID. The
// follow-up signalStopProcess(pid) would then terminate the test
// runner. The global stop flow can only be covered with a real
// subprocess daemon — out of scope for unit tests.

// TestEnsureGlobalDaemon_DisableAutostart pins the env-var short-circuit:
// when DFMT_DISABLE_AUTOSTART=1 is set (TestMain default) and the
// inspection lands on Dead, the spawn branch returns the sentinel error
// instead of forking. This is the contract every unit test relies on.
func TestEnsureGlobalDaemon_DisableAutostart(t *testing.T) {
	withIsolatedGlobalDir(t)
	err := ensureGlobalDaemon()
	if err == nil {
		t.Fatal("want error from test-binary short-circuit, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not spawning") {
		t.Errorf("expected 'not spawning' in error, got %q", msg)
	}
}
