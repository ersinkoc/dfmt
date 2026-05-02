package project

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Discover finds the project root for the given path.
// It walks up looking for .dfmt/ or .git/ directories.
// Honors DFMT_PROJECT env var.
func Discover(path string) (string, error) {
	// Honor DFMT_PROJECT env var
	if envPath := os.Getenv("DFMT_PROJECT"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return filepath.Abs(envPath)
		}
	}

	// Walk up from path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	for {
		// Check for .dfmt directory
		dfmtPath := filepath.Join(absPath, ".dfmt")
		if _, err := os.Stat(dfmtPath); err == nil {
			return absPath, nil
		}

		// Check for .git directory
		gitPath := filepath.Join(absPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return absPath, nil
		}

		// Move to parent
		parent := filepath.Dir(absPath)
		if parent == absPath {
			// Reached root
			break
		}
		absPath = parent
	}

	return "", ErrNoProjectFound
}

var ErrNoProjectFound = &NoProjectError{}

// NoProjectError indicates no project root was found.
type NoProjectError struct{}

func (e *NoProjectError) Error() string {
	return "no DFMT project found (no .dfmt or .git directory in parent tree)"
}

func (e *NoProjectError) Unwrap() error { return nil }

// ID computes the project ID (8 hex chars of SHA-256 of the path).
func ID(projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	return hex.EncodeToString(h[:4])
}

// SocketPath returns the socket path for a project. Long project paths
// (>100 bytes total) cannot fit inside Unix's UNIX_PATH_MAX (108 on Linux,
// 104 on macOS), so we fall back to a hashed name under a per-user runtime
// directory. The runtime dir is owned by the current user with 0700
// permissions — closes F-06 (a same-host attacker on a shared `/tmp` could
// otherwise pre-create the socket file at a predictable hashed path and
// have the daemon clobber it on startup, or impersonate the daemon during
// the bind race).
func SocketPath(projectPath string) string {
	full := filepath.Join(projectPath, ".dfmt", "daemon.sock")
	if len(full) <= 100 {
		return full
	}
	h := sha256.Sum256([]byte(projectPath))
	return filepath.Join(userRuntimeDir(), "dfmt-"+hex.EncodeToString(h[:8])+".sock")
}

// userRuntimeDir returns a per-user directory suitable for transient runtime
// files (sockets, lock files). Resolution order:
//
//  1. $XDG_RUNTIME_DIR (Linux systemd; tmpfs, mode 0700, owned by uid).
//  2. $TMPDIR (macOS sets this to a per-user `/var/folders/.../T`).
//  3. os.TempDir() — used as a base; if it equals "/tmp" (shared) we
//     drop a per-user subdir `dfmt-<uid>` with 0700 perms.
//
// The returned dir is created if missing. Errors creating it are absorbed
// — the caller's net.Listen will surface a clearer error if the path is
// unusable, which is the right point for the operator to see it.
func userRuntimeDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if fi, err := os.Stat(xdg); err == nil && fi.IsDir() {
			return xdg
		}
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		if fi, err := os.Stat(tmp); err == nil && fi.IsDir() {
			return tmp
		}
	}
	base := os.TempDir()
	// On Linux base is typically "/tmp" — shared and world-writable with
	// sticky bit. Drop a per-user subdir there to keep our sockets out of
	// reach of other local users. macOS's per-user TMPDIR is already
	// 0700-owned so the subdir is harmless redundancy.
	dir := filepath.Join(base, "dfmt-"+userTag())
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// userTag returns a short stable identifier for the current user, suitable
// for use in a path component. Prefers numeric uid (compact, ASCII-only)
// then $USER, then a SHA-256 prefix of the home directory as a last resort.
func userTag() string {
	if uid := os.Getuid(); uid >= 0 {
		return hex.EncodeToString([]byte{byte(uid >> 24), byte(uid >> 16), byte(uid >> 8), byte(uid)})
	}
	if u := os.Getenv("USER"); u != "" {
		h := sha256.Sum256([]byte(u))
		return hex.EncodeToString(h[:4])
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		h := sha256.Sum256([]byte(home))
		return hex.EncodeToString(h[:4])
	}
	return "anon"
}
