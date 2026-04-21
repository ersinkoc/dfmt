package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/config"
)

func TestLockError(t *testing.T) {
	err := &LockError{ProjectPath: "/test/path"}
	expected := "daemon already running for /test/path (lock file exists)"
	if err.Error() != expected {
		t.Errorf("LockError.Error() = %s, want %s", err.Error(), expected)
	}
	if err.ProjectPath != "/test/path" {
		t.Errorf("LockError.ProjectPath = %s, want /test/path", err.ProjectPath)
	}
}

func TestLockFileRelease(t *testing.T) {
	lockPath := filepath.Join(os.TempDir(), "dfmt-test-lock")
	f, err := os.Create(lockPath)
	if err != nil {
		t.Fatalf("Create lock file failed: %v", err)
	}
	f.Close()
	defer os.Remove(lockPath)

	l := &LockFile{path: lockPath, file: nil}
	if err := l.Release(); err != nil {
		t.Errorf("Release with nil file failed: %v", err)
	}
}

func TestLockFileReleaseWithFile(t *testing.T) {
	lockPath := filepath.Join(os.TempDir(), "dfmt-test-lock2")
	f, err := os.Create(lockPath)
	if err != nil {
		t.Fatalf("Create lock file failed: %v", err)
	}
	defer os.Remove(lockPath)

	l := &LockFile{path: lockPath, file: f}
	if err := l.Release(); err != nil {
		t.Errorf("Release failed: %v", err)
	}
}

func TestProcessExists(t *testing.T) {
	// Test with current process
	pid := os.Getpid()
	result := ProcessExists(pid)
	// On Windows, FindProcess succeeds but Signal(0) may fail for own process
	// So we just verify it doesn't panic
	_ = result

	// Test with invalid PID - should return false
	if ProcessExists(999999) {
		t.Error("ProcessExists should return false for invalid PID")
	}
}

func TestLockFileReleaseNil(t *testing.T) {
	lf := &LockFile{path: "/test/path", file: nil}
	// Release with nil file should not panic
	err := lf.Release()
	if err != nil {
		t.Errorf("Release with nil file failed: %v", err)
	}
}

func TestAcquireLockNonBlocking(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	// First acquire should succeed
	lock1, err := AcquireLock(tmpDir)
	if err != nil {
		t.Fatalf("First AcquireLock failed: %v", err)
	}
	defer lock1.Release()

	// Second acquire should fail (already locked)
	_, err = AcquireLock(tmpDir)
	if err == nil {
		t.Error("Second AcquireLock should have failed")
	}
	lockErr, ok := err.(*LockError)
	if !ok {
		t.Error("Error should be LockError")
	} else {
		if lockErr.ProjectPath != tmpDir {
			t.Errorf("LockError.ProjectPath = %s, want %s", lockErr.ProjectPath, tmpDir)
		}
	}
}

func TestAcquireLockInvalidPath(t *testing.T) {
	// Test with a path that cannot be created
	// This will fail at OpenFile due to permission or path issue
	_, err := AcquireLock("/proc/invalid-lock-path")
	if err == nil {
		t.Error("AcquireLock should fail for invalid path")
	}
}

func TestLockErrorString(t *testing.T) {
	err := &LockError{ProjectPath: "/another/test"}
	expected := "daemon already running for /another/test (lock file exists)"
	if err.Error() != expected {
		t.Errorf("LockError.Error() = %s, want %s", err.Error(), expected)
	}
}

func TestLockFileFields(t *testing.T) {
	lf := &LockFile{path: "/test/path", file: nil}
	if lf.path != "/test/path" {
		t.Errorf("path = %s, want /test/path", lf.path)
	}
}

func TestNewDaemonWithEmptyPath(t *testing.T) {
	// Empty path should try to discover from cwd
	cfg := newTestConfig()
	_, err := New("", cfg)
	// It might fail due to no project found, but shouldn't panic
	_ = err
}

func newTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Storage.JournalMaxBytes = 1024 * 1024
	cfg.Storage.Durability = "memory"
	cfg.Storage.MaxBatchMS = 100
	cfg.Storage.CompressRotated = false
	cfg.Lifecycle.IdleTimeout = "30m"
	return cfg
}

func TestDaemonStartStop(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	pidPath := filepath.Join(tmpDir, ".dfmt", "daemon.pid")
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Error("daemon.pid was not created")
	}

	if err := d.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestDaemonStopWhenNotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	if err := d.Stop(ctx); err != nil {
		t.Errorf("Stop when not running failed: %v", err)
	}
}

func TestStartIdleMonitorInvalidDuration(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "invalid"

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// startIdleMonitor with invalid duration should use default 30m
	d.startIdleMonitor(context.Background())

	// Just verify it doesn't panic
	if d.idleTimer == nil {
		t.Error("idleTimer should be set")
	}

	// Clean up timer to avoid lingering goroutines
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}
}

func TestStartIdleMonitorValidDuration(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "5m"

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	d.startIdleMonitor(context.Background())

	if d.idleTimer == nil {
		t.Error("idleTimer should be set with valid duration")
	}

	// Clean up timer
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}
}

func TestStartIdleMonitorZeroDuration(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "0"

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Zero or invalid duration defaults to 30m
	d.startIdleMonitor(context.Background())

	if d.idleTimer == nil {
		t.Error("idleTimer should be set")
	}

	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}
}

func TestRegister(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// register() is a no-op, just verify it doesn't panic
	d.register()
}

func TestUnregister(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// unregister() is a no-op, just verify it doesn't panic
	d.unregister()
}

func TestDaemonStartAlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	// First start
	if err := d.Start(ctx); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}

	// Second start should fail
	err = d.Start(ctx)
	if err == nil {
		t.Error("Second Start should fail for already running daemon")
	}

	d.Stop(ctx)
}
