package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestNewGlobalConstructsWithoutDefaultProject verifies the NewGlobal
// path: daemon comes up with no per-project journal/index/redactor
// pre-loaded, but the resource fetcher is wired and globalMode is set.
// This is the "tek bir daemon" entry point — no project required at
// startup.
func TestNewGlobalConstructsWithoutDefaultProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	cfg := newTestConfig()
	d, err := NewGlobal(cfg)
	if err != nil {
		t.Fatalf("NewGlobal: %v", err)
	}
	if !d.globalMode {
		t.Error("globalMode should be true")
	}
	if d.projectPath != "" {
		t.Errorf("projectPath should be empty in global mode, got %q", d.projectPath)
	}
	if d.journal != nil {
		t.Error("journal should be nil in global mode (per-project, lazy)")
	}
	if d.index != nil {
		t.Error("index should be nil in global mode (per-project, lazy)")
	}
	if d.handlers == nil {
		t.Fatal("handlers must not be nil")
	}
}

// TestNewGlobalServerBindsAtGlobalPaths verifies the listener bind
// paths come from project.GlobalPortPath / GlobalSocketPath rather
// than from any per-project location. Without this, the user-visible
// "stable URL" promise would be broken.
func TestNewGlobalServerBindsAtGlobalPaths(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	cfg := newTestConfig()
	d, err := NewGlobal(cfg)
	if err != nil {
		t.Fatalf("NewGlobal: %v", err)
	}

	hs, ok := d.server.(*transport.HTTPServer)
	if !ok {
		t.Fatalf("d.server type = %T, want *transport.HTTPServer", d.server)
	}

	// On Windows the bind goes to the port file; on Unix it goes to
	// the socket path. Either way the relevant path must sit inside
	// the global override directory.
	want := tmp
	switch runtime.GOOS {
	case "windows":
		got := hs.PortFile()
		if got == "" || filepath.Dir(got) != want {
			t.Errorf("port file %q should sit inside %q", got, want)
		}
	default:
		got := hs.SocketPath()
		if got == "" || filepath.Dir(got) != want {
			t.Errorf("socket path %q should sit inside %q", got, want)
		}
	}
}

// TestNewGlobalStartStopWritesAndCleansPID verifies the lifecycle
// invariants: Start writes ~/.dfmt/daemon.pid and acquires the global
// lock, Stop removes the PID and releases the lock so the next global
// daemon can bind cleanly.
func TestNewGlobalStartStopWritesAndCleansPID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	// Speed up shutdown grace — default is 10s, way too long for a test.
	cfg := newTestConfig()
	cfg.Lifecycle.ShutdownTimeout = "2s"

	d, err := NewGlobal(cfg)
	if err != nil {
		t.Fatalf("NewGlobal: %v", err)
	}

	ctx := context.Background()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	pidPath := filepath.Join(tmp, "daemon.pid")
	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("global PID file not written at %q: %v", pidPath, err)
	}
	lockPath := filepath.Join(tmp, "lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("global lock file not present at %q: %v", lockPath, err)
	}

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("global PID file should be removed after Stop, but stat returned: %v", err)
	}
}

// TestNewGlobalSecondInstanceFailsLockAcquisition proves the singleton
// invariant: only one global daemon can run on a host. The second
// NewGlobal+Start must fail at lock acquisition (not silently bind a
// different ephemeral port).
func TestNewGlobalSecondInstanceFailsLockAcquisition(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	cfg := newTestConfig()
	cfg.Lifecycle.ShutdownTimeout = "2s"

	d1, err := NewGlobal(cfg)
	if err != nil {
		t.Fatalf("first NewGlobal: %v", err)
	}
	if err := d1.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d1.Stop(stopCtx)
	}()

	d2, err := NewGlobal(cfg)
	if err != nil {
		t.Fatalf("second NewGlobal: %v", err)
	}
	if err := d2.Start(context.Background()); err == nil {
		t.Error("second Start should have failed lock acquisition; got nil error")
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d2.Stop(stopCtx)
	}
}

// TestPromoteInProcessBringsUpDaemon proves the v0.6.0 entry point:
// any subcommand can call PromoteInProcess on a daemon-not-running
// miss and end up with a running global daemon owned by the current
// process. No exec.Command spawning, no separate dfmt.exe in tasklist.
func TestPromoteInProcessBringsUpDaemon(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	cfg := newTestConfig()
	cfg.Lifecycle.ShutdownTimeout = "2s"

	d, err := PromoteInProcess(context.Background(), cfg)
	if err != nil {
		t.Fatalf("PromoteInProcess: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(stopCtx)
	}()

	if !d.globalMode {
		t.Error("PromoteInProcess produced non-global daemon")
	}
	if !d.running.Load() {
		t.Error("PromoteInProcess did not Start the daemon (running=false)")
	}
	if d.handlers == nil {
		t.Error("PromoteInProcess returned daemon with nil handlers")
	}
}

// TestPromoteInProcessReturnsLockErrorWhenAnotherDaemonOwns proves
// the fallback contract: when a global daemon already holds the
// host-wide lock, PromoteInProcess returns *LockError so the caller
// can `errors.As` and connect via the client instead of crashing.
func TestPromoteInProcessReturnsLockErrorWhenAnotherDaemonOwns(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	cfg := newTestConfig()
	cfg.Lifecycle.ShutdownTimeout = "2s"

	d1, err := PromoteInProcess(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first PromoteInProcess: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d1.Stop(stopCtx)
	}()

	d2, err := PromoteInProcess(context.Background(), cfg)
	if err == nil {
		t.Fatal("second PromoteInProcess returned nil error; expected *LockError")
	}
	var lerr *LockError
	if !errors.As(err, &lerr) {
		t.Errorf("second PromoteInProcess error: got %v (%T), want *LockError", err, err)
	}
	if d2 != nil {
		t.Error("second PromoteInProcess returned non-nil daemon on lock contention")
	}
}

// TestLoadedProjectsReflectsCacheState pins the dashboard's
// cross-project visibility contract: every project the daemon's
// resource cache has loaded should appear in LoadedProjects(), and
// projects that were never touched should not. The dashboard switcher
// reads this via Handlers.LoadedProjects() in handleAPIAllDaemons.
//
// Path comparison goes through samePath because Resources()
// canonicalizes incoming project IDs (lowercase on Windows / abs on
// Unix), so the strings the daemon hands back may differ from what
// t.TempDir() produced even when they point at the same directory.
func TestLoadedProjectsReflectsCacheState(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	cfg := newTestConfig()
	d, err := NewGlobal(cfg)
	if err != nil {
		t.Fatalf("NewGlobal: %v", err)
	}
	t.Cleanup(func() {
		// Close cached journals so t.TempDir's RemoveAll doesn't
		// trip over an open .dfmt/journal.jsonl on Windows.
		d.closeExtraProjects()
	})

	if got := d.LoadedProjects(); len(got) != 0 {
		t.Errorf("fresh daemon LoadedProjects = %v, want empty", got)
	}

	projA := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projA, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed projA: %v", err)
	}
	if _, err := d.Resources(projA); err != nil {
		t.Fatalf("Resources(projA): %v", err)
	}

	got := d.LoadedProjects()
	if len(got) != 1 || !samePathTest(got[0], projA) {
		t.Errorf("after touching projA, LoadedProjects = %v, want one entry matching %s", got, projA)
	}

	projB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projB, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed projB: %v", err)
	}
	if _, err := d.Resources(projB); err != nil {
		t.Fatalf("Resources(projB): %v", err)
	}

	got = d.LoadedProjects()
	if len(got) != 2 {
		t.Errorf("after touching projB, LoadedProjects len = %d, want 2: %v", len(got), got)
	}
	haveA, haveB := false, false
	for _, p := range got {
		if samePathTest(p, projA) {
			haveA = true
		}
		if samePathTest(p, projB) {
			haveB = true
		}
	}
	if !haveA || !haveB {
		t.Errorf("LoadedProjects missing one of {projA=%s, projB=%s}: %v", projA, projB, got)
	}
}

// samePathTest is the test-local case-insensitive-on-Windows
// equality comparison; cli.samePathCLI does the same job in
// production but lives in a different import graph.
func samePathTest(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
