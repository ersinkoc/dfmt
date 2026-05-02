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

	// Mode 0o600 matches the rest of `.dfmt/`. The flock advisory lock
	// is what enforces exclusivity; the file mode just keeps other local
	// users from learning that dfmt is running for the owning user
	// (closes F-G-LOW-1 from the security audit).
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
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

func (e *LockError) Unwrap() error { return nil }

// ProcessExists reports whether a process with the given PID is currently live.
// The actual implementation is platform-specific (see process_unix.go / process_windows.go).
func ProcessExists(pid int) bool {
	return processExistsPlatform(pid)
}
