//go:build linux

package capture

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"golang.org/x/sys/unix"
)

func TestLinuxWatchDir(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	// Create a temp directory to watch
	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}
	w.watchDirFn = linuxWatchDir

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchDir(w, tmpDir)
	}()

	// Wait for watcher to start
	time.Sleep(100 * time.Millisecond)

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFile, []byte("hello"), 0600)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Wait for event to be captured
	select {
	case event := <-w.events:
		t.Logf("Captured event: %s", event.Type)
	case <-time.After(3 * time.Second):
	}

	// Stop watcher
	close(w.stopCh)
	wg.Wait()
}

func TestLinuxWatchDir_Subdirectory(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}
	w.watchDirFn = linuxWatchDir

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchDir(w, tmpDir)
	}()

	time.Sleep(100 * time.Millisecond)

	// Create subdirectory and file
	subDir := filepath.Join(tmpDir, "subdir")
	err = os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	testFile := filepath.Join(subDir, "nested.txt")
	err = os.WriteFile(testFile, []byte("nested"), 0600)
	if err != nil {
		t.Fatalf("failed to write nested file: %v", err)
	}

	select {
	case event := <-w.events:
		t.Logf("Captured event: %s", event.Type)
	case <-time.After(3 * time.Second):
	}

	close(w.stopCh)
	wg.Wait()
}

func TestLinuxWatchLoop_CreateEvent(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create inotify instance
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Skipf("inotify not available: %v", err)
	}
	defer unix.Close(fd)

	wd, err := unix.InotifyAddWatch(fd, tmpDir,
		unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	if err != nil {
		t.Fatalf("inotify add watch failed: %v", err)
	}
	defer unix.InotifyRmWatch(fd, uint32(wd))

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchLoop(w, fd, tmpDir)
	}()

	time.Sleep(50 * time.Millisecond)

	// Trigger IN_CREATE event
	testFile := filepath.Join(tmpDir, "create_test.txt")
	err = os.WriteFile(testFile, []byte("content"), 0600)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	select {
	case event := <-w.events:
		t.Logf("Captured create event: %s", event.Type)
	case <-time.After(2 * time.Second):
		t.Log("timeout waiting for create event")
	}

	close(w.stopCh)
	_ = os.WriteFile(filepath.Join(tmpDir, ".stop_create"), nil, 0600)
	wg.Wait()
}

func TestLinuxWatchLoop_ModifyEvent(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Skipf("inotify not available: %v", err)
	}
	defer unix.Close(fd)

	wd, err := unix.InotifyAddWatch(fd, tmpDir,
		unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	if err != nil {
		t.Fatalf("inotify add watch failed: %v", err)
	}
	defer unix.InotifyRmWatch(fd, uint32(wd))

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchLoop(w, fd, tmpDir)
	}()

	time.Sleep(50 * time.Millisecond)

	// Create and modify file
	testFile := filepath.Join(tmpDir, "modify_test.txt")
	os.WriteFile(testFile, []byte("v1"), 0600)
	time.Sleep(50 * time.Millisecond)

	os.WriteFile(testFile, []byte("v2"), 0600)

	select {
	case event := <-w.events:
		t.Logf("Captured modify event: %s", event.Type)
	case <-time.After(2 * time.Second):
		t.Log("timeout waiting for modify event")
	}

	close(w.stopCh)
	_ = os.WriteFile(filepath.Join(tmpDir, ".stop_modify"), nil, 0600)
	wg.Wait()
}

func TestLinuxWatchLoop_DeleteEvent(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Skipf("inotify not available: %v", err)
	}
	defer unix.Close(fd)

	wd, err := unix.InotifyAddWatch(fd, tmpDir,
		unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	if err != nil {
		t.Fatalf("inotify add watch failed: %v", err)
	}
	defer unix.InotifyRmWatch(fd, uint32(wd))

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchLoop(w, fd, tmpDir)
	}()

	time.Sleep(50 * time.Millisecond)

	// Create and delete file
	testFile := filepath.Join(tmpDir, "delete_test.txt")
	os.WriteFile(testFile, []byte("delete me"), 0600)
	time.Sleep(50 * time.Millisecond)

	os.Remove(testFile)

	select {
	case event := <-w.events:
		t.Logf("Captured delete event: %s", event.Type)
	case <-time.After(2 * time.Second):
		t.Log("timeout waiting for delete event")
	}

	close(w.stopCh)
	_ = os.WriteFile(filepath.Join(tmpDir, ".stop_delete"), nil, 0600)
	wg.Wait()
}

func TestLinuxWatchDir_InvalidPath(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	w := &FSWatcher{
		path:   tmpDir(),
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}
	w.watchDirFn = linuxWatchDir

	// Watch non-existent path - should not panic
	linuxWatchDir(w, filepath.Join(os.TempDir(), "nonexistent_path_12345"))
}

func TestLinuxWatchLoop_EmptyName(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Skipf("inotify not available: %v", err)
	}
	defer unix.Close(fd)

	wd, err := unix.InotifyAddWatch(fd, tmpDir,
		unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO)
	if err != nil {
		t.Fatalf("inotify add watch failed: %v", err)
	}
	defer unix.InotifyRmWatch(fd, uint32(wd))

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchLoop(w, fd, tmpDir)
	}()

	time.Sleep(50 * time.Millisecond)

	// Create directory (events on directory itself may have empty name)
	subDir := filepath.Join(tmpDir, "subdir")
	err = os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	close(w.stopCh)
	_ = os.WriteFile(filepath.Join(tmpDir, ".stop_empty"), nil, 0600)
	wg.Wait()
}

func TestLinuxWatchLoop_ReadError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	// Create a pipe to get a valid fd that will cause read errors
	r, w, err := os.Pipe()
	if err != nil {
		t.Skipf("pipe not available: %v", err)
	}
	defer r.Close()
	defer w.Close()

	wr := &FSWatcher{
		path:   tmpDir(),
		ignore: []string{},
		events: make(chan core.Event, 100),
		stopCh: make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		linuxWatchLoop(wr, int(w.Fd()), "/tmp")
	}()

	// Write some garbage to cause read error
	w.Write([]byte("garbage"))

	time.Sleep(50 * time.Millisecond)
	close(wr.stopCh)
	wg.Wait()
}

func tmpDir() string {
	d, _ := os.MkdirTemp("", "fswatch_test")
	os.RemoveAll(d)
	return d
}

// TestFSWatcherStopUnblocks verifies that Stop() actually terminates all
// linux watch goroutines rather than leaving them parked inside unix.Read.
// Previously Stop() only closed stopCh; the read never returned and the
// goroutine leaked. Stop() now writes a marker file into each watched
// directory to wake the reader.
func TestFSWatcherStopUnblocks(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Linux-specific test on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_stop_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Also create a nested subdir so Start() walks and registers two separate
	// inotify fds. Both watch goroutines must unblock on Stop().
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	w, err := NewFSWatcher(tmpDir, []string{}, 0)
	if err != nil {
		t.Fatalf("NewFSWatcher failed: %v", err)
	}

	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give the watch goroutines a moment to park inside unix.Read.
	time.Sleep(100 * time.Millisecond)

	// Drain events in the background so a full channel does not block
	// emitEvent and keep a goroutine alive past Stop().
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case <-w.events:
			case <-time.After(3 * time.Second):
				return
			}
		}
	}()

	// Stop() must return promptly AND the watch goroutines must exit.
	// We enforce both by putting Stop in its own goroutine and waiting with
	// a timeout. If Stop hangs, we fail loudly rather than time-out the test.
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- w.Stop(ctx)
	}()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s — watch goroutines likely leaked")
	}
}

// TestFSWatcherStopNoWatches verifies Stop() is safe when no watchers were
// ever registered (e.g. Start was never called or the path did not exist).
func TestFSWatcherStopNoWatches(t *testing.T) {
	w, err := NewFSWatcher(os.TempDir(), []string{}, 0)
	if err != nil {
		t.Fatalf("NewFSWatcher failed: %v", err)
	}
	ctx := context.Background()

	stopDone := make(chan error, 1)
	go func() { stopDone <- w.Stop(ctx) }()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Stop hung with no registered watches")
	}
}
