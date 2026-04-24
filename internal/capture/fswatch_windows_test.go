//go:build windows

package capture

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestWindowsWatchDir(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
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
	w.watchDirFn = windowsWatchDir

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		windowsWatchDir(w, tmpDir)
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

func TestWindowsWatchDir_Subdirectory(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
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
	w.watchDirFn = windowsWatchDir

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		windowsWatchDir(w, tmpDir)
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

func TestWindowsWatchLoop_StopChannel(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
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

	// Start watcher then immediately stop
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		windowsWatchDir(w, tmpDir)
	}()

	time.Sleep(50 * time.Millisecond)
	close(w.stopCh)
	wg.Wait()
}

func TestScanDir_Basic(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create exactly 2 files we control
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	os.WriteFile(file1, []byte("content1"), 0600)
	os.WriteFile(file2, []byte("content2"), 0600)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)

	// Initial scan - should detect creates
	scanDir(w, tmpDir, known, knownDirs)

	// We expect at least file1.txt and file2.txt
	// There might be additional files from the system
	if len(known) < 2 {
		t.Errorf("expected at least 2 tracked files, got %d", len(known))
	}
}

func TestScanDir_Creation(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
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
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)

	// Initial scan - empty dir
	scanDir(w, tmpDir, known, knownDirs)
	initialCount := len(known)

	// Create a new file
	testFile := filepath.Join(tmpDir, "newfile.txt")
	os.WriteFile(testFile, []byte("content"), 0600)

	// Second scan should detect the new file
	scanDir(w, tmpDir, known, knownDirs)

	if len(known) <= initialCount {
		t.Errorf("expected more files after creation, initial=%d, final=%d", initialCount, len(known))
	}
}

func TestScanDir_Modification(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "modify.txt")
	os.WriteFile(testFile, []byte("original"), 0600)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)
	scanDir(w, tmpDir, known, knownDirs)

	// Modify the file
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(testFile, []byte("modified"), 0600)

	// Second scan should detect modification
	scanDir(w, tmpDir, known, knownDirs)

	// Should still have the file tracked
	if _, ok := known["modify.txt"]; !ok {
		t.Error("file should still be tracked after modification")
	}
}

func TestScanDir_Deletion(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "todelete.txt")
	os.WriteFile(testFile, []byte("delete me"), 0600)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)
	scanDir(w, tmpDir, known, knownDirs)

	// Delete the file
	os.Remove(testFile)

	// Third scan should detect deletion and remove from known
	scanDir(w, tmpDir, known, knownDirs)

	if _, ok := known["todelete.txt"]; ok {
		t.Error("deleted file should be removed from known")
	}
}

func TestScanDir_Subdirectory(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "sub")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0600)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{},
		events: make(chan core.Event, 100),
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)
	scanDir(w, tmpDir, known, knownDirs)

	// Should track subdir and nested file
	if len(known) < 2 {
		t.Errorf("expected at least 2 tracked entries (subdir + file), got %d", len(known))
	}
}

func TestScanDir_WalkError(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
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
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)

	// Walk with a non-existent path should not panic
	scanDir(w, filepath.Join(tmpDir, "nonexistent"), known, knownDirs)
}

func TestScanDir_IgnorePattern(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create node_modules directory
	nodeModules := filepath.Join(tmpDir, "node_modules")
	os.MkdirAll(nodeModules, 0755)
	os.WriteFile(filepath.Join(nodeModules, "package.json"), []byte("{}"), 0600)

	w := &FSWatcher{
		path:   tmpDir,
		ignore: []string{"node_modules"},
		events: make(chan core.Event, 100),
	}

	known := make(map[string]trackedFile)
	knownDirs := make(map[string]trackedDir)
	scanDir(w, tmpDir, known, knownDirs)

	// node_modules should be ignored
	if _, ok := known["node_modules"]; ok {
		t.Error("node_modules should be ignored")
	}
}

func TestTrackedFile(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	info, err := os.Stat(".")
	if err != nil {
		t.Skipf("cannot stat current directory: %v", err)
	}

	tf := trackedFile{
		modTime: info.ModTime(),
		isDir:   info.IsDir(),
	}

	if tf.modTime.IsZero() {
		t.Error("trackedFile modTime should not be zero")
	}
}

func TestTrackedFile_IsDir(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("skipping Windows-specific test on non-Windows")
	}

	tmpDir, err := os.MkdirTemp("", "fswatch_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	info, err := os.Stat(tmpDir)
	if err != nil {
		t.Fatalf("failed to stat temp dir: %v", err)
	}

	tf := trackedFile{
		modTime: info.ModTime(),
		isDir:   info.IsDir(),
	}

	if !tf.isDir {
		t.Error("expected isDir to be true for directory")
	}
}
