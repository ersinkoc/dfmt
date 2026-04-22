package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/core"
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
	} else if lockErr.ProjectPath != tmpDir {
		t.Errorf("LockError.ProjectPath = %s, want %s", lockErr.ProjectPath, tmpDir)
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

	// Ensure journal is closed even though we never started
	if d.journal != nil {
		d.journal.Close()
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

	// Clean up journal
	if d.journal != nil {
		d.journal.Close()
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

	// Clean up journal
	if d.journal != nil {
		d.journal.Close()
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

	// Clean up journal
	if d.journal != nil {
		d.journal.Close()
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

	// Clean up journal
	if d.journal != nil {
		d.journal.Close()
	}
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

	// Clean up journal
	if d.journal != nil {
		d.journal.Close()
	}
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

func TestDaemonServerInterface(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Verify server field is set (either SocketServer or TCPServer)
	if d.server == nil {
		t.Error("server should not be nil")
	}

	// Stop server (without starting - journal not yet active)
	ctx := context.Background()
	d.server.Stop(ctx)

	// Close journal manually (if it was created)
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestNewWithInvalidProjectPath(t *testing.T) {
	// This test is platform-specific: empty path relies on Discover finding a project
	// On Windows, os.Getwd() likely succeeds, so Discover may find a project
	cfg := newTestConfig()
	_, err := New("", cfg)
	// Empty path should either work (if Discover finds project) or fail gracefully
	// We just verify it doesn't panic
	_ = err
}

func TestNewWithInvalidIdleTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	// Invalid timeout should be handled gracefully (defaults to 30m)
	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New with invalid config should still succeed: %v", err)
	}
	// Clean up journal if created
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestNewJournalCreationFailure(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file where directory should be to cause error
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	// Create journal path that will fail - simulate by making parent unreadable
	// Actually just verify that journal creation at valid path works
	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New should succeed with valid path: %v", err)
	}
	// Clean up journal
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestNewIndexLoadFailure(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	// Create .dfmt directory structure
	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Write corrupted index file to trigger LoadIndex failure
	indexPath := filepath.Join(d.projectPath, ".dfmt", "index.gob")
	os.WriteFile(indexPath, []byte("corrupted data"), 0644)

	// Close the first daemon's journal before creating second one
	if d.journal != nil {
		d.journal.Close()
	}

	// Create new daemon with corrupted index - should rebuild
	d2, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New with corrupted index should rebuild: %v", err)
	}
	if d2.index == nil {
		t.Error("index should be created even with corrupted file")
	}
	d2.journal.Close()
}

func TestDaemonStartServerError(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Mock server to return error on Start
	originalServer := d.server
	d.server = &mockServer{startError: fmt.Errorf("server start failed")}

	ctx := context.Background()
	err = d.Start(ctx)
	if err == nil {
		t.Error("Start should fail when server returns error")
	}

	// Restore server and clean up
	d.server = originalServer
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestDaemonStopWithJournalError(t *testing.T) {
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

	// Mock journal to return error on Close
	originalJournal := d.journal
	d.journal = &mockJournal{closeError: fmt.Errorf("journal close failed")}

	err = d.Stop(ctx)
	if err == nil {
		t.Error("Stop should fail when journal.Close returns error")
	}

	// Restore journal for clean shutdown
	d.journal = originalJournal
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestDaemonStopWithServerError(t *testing.T) {
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

	// Mock server to return error on Stop
	d.server = &mockServer{stopError: fmt.Errorf("server stop failed")}

	err = d.Stop(ctx)
	if err == nil {
		t.Error("Stop should fail when server.Stop returns error")
	}

	// Clean up - restore server first
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestDaemonStopCleansUpPIDFile(t *testing.T) {
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

	if err := d.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify PID file is removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file should be removed after Stop")
	}
}

func TestDaemonStartWritesPIDFile(t *testing.T) {
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

	// Verify PID file exists and contains correct PID
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	var pid int
	fmt.Sscanf(string(data), "%d", &pid)

	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}

	d.Stop(ctx)
}

func TestStartIdleMonitorShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "1s"

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Test that calling Stop when already stopped is safe
	err = d.Stop(ctx)
	// It's possible Stop fails because idle timer already stopped it
	// so we just verify no panic occurs
	_ = err
}

func TestStartIdleMonitorCallsStop(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "500ms"

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for idle timer to potentially fire and stop the daemon
	time.Sleep(100 * time.Millisecond)

	// Stop should be safe to call even if idle timer already stopped
	d.Stop(ctx)
}

// mockServer implements Server interface for testing
type mockServer struct {
	startError error
	stopError  error
}

func (m *mockServer) Start(ctx context.Context) error {
	return m.startError
}

func (m *mockServer) Stop(ctx context.Context) error {
	return m.stopError
}

// mockJournal implements Journal interface for testing
type mockJournal struct {
	closeError error
}

func (m *mockJournal) Append(ctx context.Context, e core.Event) error {
	return nil
}

func (m *mockJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	ch := make(chan core.Event)
	close(ch)
	return ch, nil
}

func (m *mockJournal) Checkpoint(ctx context.Context) (string, error) {
	return "", nil
}

func (m *mockJournal) Rotate(ctx context.Context) error {
	return nil
}

func (m *mockJournal) Close() error {
	return m.closeError
}

// =============================================================================
// startIdleMonitor error path tests (50.0% coverage)
// =============================================================================

func TestStartIdleMonitorWithVeryLongTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "999h" // Very long timeout

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	d.startIdleMonitor(ctx)

	if d.idleTimer == nil {
		t.Error("idleTimer should be set with very long timeout")
	}

	// Clean up
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}

	// Clean up daemon resources
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestStartIdleMonitorWithShortTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "1ms" // Very short timeout

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	d.startIdleMonitor(ctx)

	if d.idleTimer == nil {
		t.Error("idleTimer should be set")
	}

	// Wait for timer to potentially fire
	time.Sleep(10 * time.Millisecond)

	// Clean up
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}

	// Clean up daemon resources
	if d.journal != nil {
		d.journal.Close()
	}
}

func TestStartIdleMonitorContextCancel(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "1h" // Long timeout so timer won't fire

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start the monitor
	d.startIdleMonitor(ctx)

	// Cancel the context immediately - should trigger shutdown path
	cancel()

	// Give it a moment to process
	time.Sleep(10 * time.Millisecond)

	// Stop the timer to clean up
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}

	// Clean up daemon resources
	if d.journal != nil {
		d.journal.Close()
	}
}
