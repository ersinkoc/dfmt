package project

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
)

// Phase 2: the global daemon listens at host-scoped paths under
// ~/.dfmt/ instead of one bind per project. These helpers centralize
// the path layout so daemon, client, and doctor agree on where to
// look — there is no per-project equivalent of port/sock/pid/lock for
// a global daemon.

const (
	// GlobalSocketName is the Unix socket file inside ~/.dfmt/.
	GlobalSocketName = "daemon.sock"
	// GlobalPortFileName is the Windows TCP port + token JSON inside ~/.dfmt/.
	GlobalPortFileName = "port"
	// GlobalPIDFileName is the daemon PID file inside ~/.dfmt/.
	GlobalPIDFileName = "daemon.pid"
	// GlobalLockFileName is the singleton-bind advisory lock file inside
	// ~/.dfmt/. Exists only while a global daemon is running; cleaned up
	// on graceful Stop, picked up by the next start.
	GlobalLockFileName = "lock"
	// GlobalCrashLogName captures the most recent panic stack trace plus
	// last-N event tags. Written by the daemon's panic recover handler;
	// read by `dfmt doctor` so operators see why the daemon went down.
	GlobalCrashLogName = "last-crash.log"
)

// GlobalDir returns the directory where the global daemon stores its
// listener / PID / lock files. The directory is created if missing
// (mode 0o700). Errors creating it are absorbed — callers' listener
// bind / file open will surface a more actionable error if the
// directory is unusable.
//
// Resolution: $HOME/.dfmt/ on Unix, %USERPROFILE%/.dfmt/ on Windows.
// Override via $DFMT_GLOBAL_DIR for tests / sandboxed environments.
func GlobalDir() string {
	if env := os.Getenv("DFMT_GLOBAL_DIR"); env != "" {
		_ = os.MkdirAll(env, 0o700)
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fall back to the runtime dir so we never return "" — the listener
		// would otherwise try to bind a relative path and fail with a
		// confusing error. Per-user runtime dir is already 0o700-protected.
		return filepath.Join(userRuntimeDir(), "dfmt-global")
	}
	dir := filepath.Join(home, ".dfmt")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// GlobalSocketPath returns the canonical Unix socket location for the
// global daemon. On Windows this returns the same string as
// GlobalDir()+SocketName for path-symmetry checks, but Windows callers
// should use GlobalPortPath() to find the daemon — Unix sockets are
// not a viable wire format on that OS.
//
// Like the per-project SocketPath, falls back to a hashed name under
// the user runtime dir when the absolute path would exceed UNIX_PATH_MAX
// (108 on Linux, 104 on macOS). Most home directories are short enough
// that the fallback is rare.
func GlobalSocketPath() string {
	full := filepath.Join(GlobalDir(), GlobalSocketName)
	if runtime.GOOS == "windows" || len(full) <= 100 {
		return full
	}
	h := sha256.Sum256([]byte(GlobalDir()))
	return filepath.Join(userRuntimeDir(), "dfmt-global-"+hex.EncodeToString(h[:8])+".sock")
}

// GlobalPortPath returns the path to the Windows TCP port + token JSON
// for the global daemon. The file format mirrors per-project port files
// so existing port-reader code can be reused without changes.
func GlobalPortPath() string {
	return filepath.Join(GlobalDir(), GlobalPortFileName)
}

// GlobalPIDPath returns the path to the global daemon's PID file.
// Used by `dfmt doctor` for liveness checks and by the lock cleanup
// code to detect orphaned locks from crashed daemons.
func GlobalPIDPath() string {
	return filepath.Join(GlobalDir(), GlobalPIDFileName)
}

// GlobalLockPath returns the path to the global singleton-bind lock
// file. Held for the lifetime of a running global daemon; absence of
// the file (or absence of a live owner) is the marker that the next
// `dfmt daemon --global` is free to bind.
func GlobalLockPath() string {
	return filepath.Join(GlobalDir(), GlobalLockFileName)
}

// GlobalCrashPath returns the path to the rolling crash log. Only the
// most recent crash is preserved — operators wanting history should
// archive last-crash.log before restarting.
func GlobalCrashPath() string {
	return filepath.Join(GlobalDir(), GlobalCrashLogName)
}
