package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ersinkoc/dfmt/internal/project"
)

// LockFile represents an advisory file lock.
type LockFile struct {
	path string
	file *os.File
}

// AcquireLock acquires an exclusive lock on the per-project daemon.
// Used by legacy single-project daemons; the global daemon (Phase 2)
// uses AcquireGlobalLock instead.
func AcquireLock(projectPath string) (*LockFile, error) {
	lockPath := filepath.Join(projectPath, ".dfmt", "lock")
	return acquireLockAt(lockPath, projectPath)
}

// AcquireGlobalLock acquires the singleton lock for the host-wide
// daemon at ~/.dfmt/lock. Two `dfmt daemon --global` processes cannot
// both hold this lock — the second one fails with LockError and exits
// cleanly so the first daemon's listener stays unique on the loopback
// port / socket.
func AcquireGlobalLock() (*LockFile, error) {
	return acquireLockAt(project.GlobalLockPath(), project.GlobalDir())
}

// acquireLockAt opens lockPath for read/write (creating it 0o600 if
// missing) and tries a non-blocking flock. The ownerLabel is only used
// to seed the LockError message so operators can tell whether they
// collided with a per-project legacy daemon or the global one.
func acquireLockAt(lockPath, ownerLabel string) (*LockFile, error) {
	// Mode 0o600 matches the rest of `.dfmt/`. The flock advisory lock
	// is what enforces exclusivity; the file mode just keeps other local
	// users from learning that dfmt is running for the owning user
	// (closes F-G-LOW-1 from the security audit).
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}

	if err := lockFlock(f, true); err != nil {
		f.Close()
		return nil, &LockError{ownerLabel}
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
