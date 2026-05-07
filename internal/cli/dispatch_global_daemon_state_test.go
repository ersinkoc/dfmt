package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/project"
)

// withIsolatedGlobalDir points DFMT_GLOBAL_DIR + cwd at fresh temp dirs
// so neither the global probe nor the legacy-fallback probe in
// client.DaemonRunning can see a real running daemon. Without this,
// running these tests from the dfmt repo itself (which has its own
// .dfmt/port pointing at the host's live global daemon) would make
// every "no listener" test see listener=true and misclassify as
// Running/Orphan. The cwd reset is critical because DaemonRunning("")
// falls back to filepath.Join("", ".dfmt", "port") = ".dfmt/port"
// at the current working directory.
func withIsolatedGlobalDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)
	t.Chdir(t.TempDir())
	return dir
}

// TestInspectGlobalDaemon_DeadOnFreshDir asserts the baseline: an empty
// ~/.dfmt/ (no port file, no PID file, no listener) classifies as Dead.
// This is the state every fresh host starts in, and the state ensure-
// GlobalDaemon enters its spawn branch from.
func TestInspectGlobalDaemon_DeadOnFreshDir(t *testing.T) {
	withIsolatedGlobalDir(t)

	status, pid := inspectGlobalDaemon()
	if status != globalDaemonDead {
		t.Errorf("status = %v, want globalDaemonDead", status)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}

// TestInspectGlobalDaemon_DeadOnStalePIDFile asserts that a PID file
// pointing at a long-dead process is classified as Dead, not Stuck —
// the host crashed / OS rebooted / daemon was kill -9'd path. The
// listener is also absent, so the verdict is unambiguous.
func TestInspectGlobalDaemon_DeadOnStalePIDFile(t *testing.T) {
	dir := withIsolatedGlobalDir(t)

	// PID 1 is `init` on Linux and SYSTEM-reserved on Windows; on Unix it
	// is technically alive, so use a high "almost certainly dead" PID
	// instead. Picking 999_999 keeps the test stable across platforms.
	pidPath := filepath.Join(dir, project.GlobalPIDFileName)
	if err := os.WriteFile(pidPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, _ := inspectGlobalDaemon()
	if status != globalDaemonDead {
		t.Errorf("status = %v, want globalDaemonDead (stale PID file)", status)
	}
}

// TestInspectGlobalDaemon_StuckOnLivePIDNoListener is the bug-fix case:
// PID file points at our own process (guaranteed alive for the test
// lifetime), no listener bound at the global address. Pre-fix the
// caller would see DaemonRunning=false and try to spawn — which would
// fail with LockError (old daemon still holds the lock) and surface as
// a generic "daemon did not become ready" timeout. Post-fix we classify
// this as Stuck so the operator gets a clear "kill PID X" message.
func TestInspectGlobalDaemon_StuckOnLivePIDNoListener(t *testing.T) {
	dir := withIsolatedGlobalDir(t)

	pidPath := filepath.Join(dir, project.GlobalPIDFileName)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	status, pid := inspectGlobalDaemon()
	if status != globalDaemonStuck {
		t.Errorf("status = %v, want globalDaemonStuck", status)
	}
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
}

// TestCleanupStaleGlobalDaemon_RemovesAll exercises the wipe pass that
// runs before a Dead-state spawn. Each of the four files we lay down
// must be gone after the call; absence of any of them already (the
// realistic crash-mid-shutdown case) must not produce an error.
func TestCleanupStaleGlobalDaemon_RemovesAll(t *testing.T) {
	dir := withIsolatedGlobalDir(t)

	names := []string{
		project.GlobalPIDFileName,
		project.GlobalPortFileName,
		project.GlobalSocketName,
		project.GlobalLockFileName,
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStaleGlobalDaemon()

	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed; stat err: %v", name, err)
		}
	}
}

// TestCleanupStaleGlobalDaemon_IdempotentOnEmptyDir asserts that calling
// cleanup on an already-clean directory is a no-op (no error). This is
// the path taken by every fresh-host first invocation.
func TestCleanupStaleGlobalDaemon_IdempotentOnEmptyDir(t *testing.T) {
	withIsolatedGlobalDir(t)

	// No setup — directory is empty. Should not panic or log.
	cleanupStaleGlobalDaemon()
}

// TestEnsureGlobalDaemon_StuckSurfacesActionableError closes the loop
// from inspectGlobalDaemon back through ensureGlobalDaemon: the Stuck
// state must produce an error containing the offending PID and a
// recovery command, NOT a generic "test binary: not spawning daemon"
// or a 4-second timeout. This is the operator-facing contract — anyone
// who ran `/mcp` against a hung daemon and saw a useless "Connection
// closed" used to land here.
func TestEnsureGlobalDaemon_StuckSurfacesActionableError(t *testing.T) {
	dir := withIsolatedGlobalDir(t)

	pidPath := filepath.Join(dir, project.GlobalPIDFileName)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ensureGlobalDaemon()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	pidStr := fmt.Sprintf("%d", os.Getpid())
	wantSubstrings := []string{"alive", "not responding", pidStr, "dfmt stop"}
	for _, want := range wantSubstrings {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %q", want, msg)
		}
	}
}

// TestEnsureGlobalDaemon_DeadCleansBeforeFallback verifies that when
// inspect classifies as Dead, the cleanup branch runs (stale port/pid
// files are removed) before we hit the test-binary short-circuit. This
// matters because the next real (non-test) call would otherwise spawn
// a daemon whose port file write races with the leftover bytes — the
// daemon writes atomically via safefs, but the dead-state cleanup
// makes the post-spawn ~/.dfmt/ contain only fresh files.
func TestEnsureGlobalDaemon_DeadCleansBeforeFallback(t *testing.T) {
	dir := withIsolatedGlobalDir(t)

	// Plant stale files; no PID, no listener → Dead state.
	stale := []string{
		project.GlobalPIDFileName,
		project.GlobalPortFileName,
		project.GlobalLockFileName,
	}
	for _, name := range stale {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// In the test harness DFMT_DISABLE_AUTOSTART=1 (set by TestMain),
	// so the spawn branch returns the disable-autostart sentinel error.
	// What we actually care about: the cleanup ran before that branch.
	_ = ensureGlobalDaemon()

	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed by Dead-cleanup; stat err: %v", name, err)
		}
	}
}
