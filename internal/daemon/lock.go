package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

// LockFile represents an advisory file lock.
type LockFile struct {
	path string
	file *os.File
}

// AcquireLock acquires an exclusive lock on the daemon.
func AcquireLock(projectPath string) (*LockFile, error) {
	lockPath := filepath.Join(projectPath, ".dfmt", "lock")

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}

	// Try to acquire exclusive lock (non-blocking)
	err = lockFlock(f, true)
	if err != nil {
		f.Close()
		return nil, &LockError{projectPath}
	}

	return &LockFile{
		path: lockPath,
		file: f,
	}, nil
}

// Release releases the lock.
func (l *LockFile) Release() error {
	if l.file == nil {
		return nil
	}
	unlockFlock(l.file)
	return l.file.Close()
}

// LockError indicates the daemon is already running.
type LockError struct {
	ProjectPath string
}

func (e *LockError) Error() string {
	return fmt.Sprintf("daemon already running for %s (lock file exists)", e.ProjectPath)
}

// ProcessExists checks if a process with the given PID exists.
func ProcessExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 just checks if process exists
	err = process.Signal(os.Signal(nil))
	return err == nil
}
