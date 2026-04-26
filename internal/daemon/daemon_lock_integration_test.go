package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDaemonStart_AcquiresLockOnSecondInstance locks in the singleton
// invariant: two Daemon instances pointed at the same project path can
// never both be Start()ed. Before AcquireLock was wired into Start(), the
// lock file was dead code and on Windows two daemons could happily coexist
// (each binding 127.0.0.1:0 to a different ephemeral port and overwriting
// each other's port file). This test makes that the regression test.
func TestDaemonStart_AcquiresLockOnSecondInstance(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create .dfmt so AcquireLock can open the lock file.
	if err := os.MkdirAll(filepath.Join(tmpDir, ".dfmt"), 0o700); err != nil {
		t.Fatalf("mkdir .dfmt: %v", err)
	}

	cfg := newTestConfig()

	d1, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("first New failed: %v", err)
	}
	if err := d1.Start(context.Background()); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	defer d1.Stop(context.Background())

	// Second daemon for same path must fail at Start (lock contention) on
	// every platform — the underlying flock/LockFileEx is non-blocking and
	// the second Start propagates the error without binding any listener.
	//
	// Note: New() may or may not fail on Unix (socket bind is in New() and
	// would trigger EADDRINUSE because d1's socket is bound). We only assert
	// on Start() to keep the test platform-portable.
	d2, err := New(tmpDir, cfg)
	if err != nil {
		// Expected on Unix where socket bind happens in New(); fail-fast.
		t.Logf("second New failed as expected (Unix socket bind): %v", err)
		return
	}

	startErr := d2.Start(context.Background())
	if startErr == nil {
		// Second Start succeeded — singleton broken. Clean up before failing.
		_ = d2.Stop(context.Background())
		t.Fatal("second daemon Start() succeeded; expected lock contention error")
	}
	t.Logf("second Start correctly rejected: %v", startErr)
}

// TestDaemonStop_ReleasesLockForNextStart ensures Stop releases the
// singleton lock so a follow-up start (after a crash, restart, etc.)
// can acquire it cleanly. Without this, a clean shutdown would
// effectively brick the project until the OS reclaimed the file lock.
func TestDaemonStop_ReleasesLockForNextStart(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".dfmt"), 0o700); err != nil {
		t.Fatalf("mkdir .dfmt: %v", err)
	}

	cfg := newTestConfig()

	// First lifecycle: New → Start → Stop.
	d1, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := d1.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := d1.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}

	// Second lifecycle on same path must succeed; a stale lock would block.
	d2, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("second New (should succeed after first Stop): %v", err)
	}
	if err := d2.Start(context.Background()); err != nil {
		t.Fatalf("second Start (should succeed after first Stop): %v", err)
	}
	if err := d2.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
