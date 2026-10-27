package daemon

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
)

func startHealingGlobal(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())
	prev := stateHealInterval
	stateHealInterval = 50 * time.Millisecond
	t.Cleanup(func() { stateHealInterval = prev })
	d, err := NewGlobal(newTestConfig())
	if err != nil {
		t.Fatalf("NewGlobal: %v", err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Stop(context.Background())
		<-d.Done()
	})
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// An older dfmt's stop deleted a successor daemon's rendezvous files,
// leaving it running but undiscoverable (seen twice on one host). The
// daemon now restores them itself.
func TestGlobalDaemonRestoresDeletedStateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open files cannot be removed out from under the daemon on Windows")
	}
	d := startHealingGlobal(t)
	rendezvous := project.GlobalPortPath()
	if !exists(rendezvous) {
		rendezvous = project.GlobalSocketPath()
	}
	for _, p := range []string{project.GlobalPIDPath(), project.GlobalIdentityPath(), project.GlobalLockPath(), rendezvous} {
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove %s: %v", p, err)
		}
	}
	waitFor(t, "state files restored", func() bool {
		return exists(project.GlobalPIDPath()) && exists(project.GlobalIdentityPath()) &&
			exists(project.GlobalLockPath()) && exists(rendezvous)
	})
	if id, ok := ReadIdentity(); !ok || id.PID != os.Getpid() {
		t.Errorf("identity not ours: %+v", id)
	}
	d.lockMu.Lock()
	intact := d.lock.Intact()
	d.lockMu.Unlock()
	if !intact {
		t.Error("lock not re-acquired on the recreated lock file")
	}
	if !d.running.Load() {
		t.Error("daemon stopped while repairing")
	}
}

// If the lock file was replaced and someone else holds the new one, this
// daemon is the duplicate and must step down rather than run beside it.
func TestGlobalDaemonYieldsWhenAnotherHoldsTheLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the lock check is Unix-only (flock)")
	}
	d := startHealingGlobal(t)
	if err := os.Remove(project.GlobalLockPath()); err != nil {
		t.Fatal(err)
	}
	other, err := AcquireGlobalLock() // a second daemon's lock on the new file
	if err != nil {
		t.Fatalf("second lock: %v", err)
	}
	defer func() { _ = other.Release() }()
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate daemon kept running")
	}
}

// A daemon repairs only the directory it started in. Its paths come from the
// environment at call time, so one that outlives a test (or a changed
// DFMT_GLOBAL_DIR) wrote its PID, identity and lock into an unrelated
// directory — seen as TestRunStopWithRunningDaemon failing ~half the time
// under -race -count=5 once self-heal existed.
func TestStateHealStaysInItsOwnDirectory(t *testing.T) {
	d := startHealingGlobal(t)
	other := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", other)
	time.Sleep(300 * time.Millisecond) // several heal ticks at 50 ms
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Errorf("heal wrote into a directory it did not start in: %v", entries)
	}
	if !d.running.Load() {
		t.Error("daemon stopped")
	}
}
