// Package safefs provides file-write helpers that refuse to follow symlinks
// on any path component.
//
// The threat model is "attacker plants a single symlink in a predictable
// path" — common in malicious npm post-install, untrusted git repos opened
// with hooks, multi-tenant CI, and shared dev hosts. Plain os.WriteFile,
// os.MkdirAll, and friends silently follow symlinks; CheckNoSymlinks closes
// that gap before the write happens.
//
// Residual: a sufficiently capable attacker can race the Lstat → write
// window (TOCTOU). Closing that requires platform-specific file handles
// (O_NOFOLLOW on Unix, FILE_FLAG_OPEN_REPARSE_POINT on Windows) and is out
// of scope for the documented threat model.
package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrSymlinkInPath is returned by CheckNoSymlinks when any component of the
// target path (or the target itself) is a symbolic link.
var ErrSymlinkInPath = errors.New("symlink in path")

// CheckNoSymlinks walks path component-by-component starting from baseDir
// and returns an error if any segment beneath baseDir is a symlink, or if
// path resolves outside baseDir lexically. Both arguments must be absolute.
//
// baseDir itself is treated as the trusted root and is NOT inspected — the
// caller opted into it by passing it. Symlinks at baseDir or above (e.g.
// /var on macOS being a system symlink to /private/var) are accepted.
// Components below baseDir are inspected with Lstat so symlinks are
// detected without being followed. Missing components stop the walk
// successfully — the caller will create them.
//
// The function does NOT create or write anything.
func CheckNoSymlinks(baseDir, path string) error {
	if !filepath.IsAbs(baseDir) {
		return fmt.Errorf("safefs: baseDir not absolute: %s", baseDir)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("safefs: path not absolute: %s", path)
	}

	cleanBase := filepath.Clean(baseDir)
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil {
		return fmt.Errorf("safefs: path outside baseDir: %s", path)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("safefs: path outside baseDir: %s", path)
	}
	if rel == "." {
		// path == baseDir; target writing into baseDir itself is nonsense.
		return fmt.Errorf("safefs: path equals baseDir: %s", path)
	}

	cur := cleanBase
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// Component (and everything below) doesn't exist yet —
				// nothing more to inspect; the eventual write/MkdirAll
				// creates fresh dirs/files.
				return nil
			}
			return fmt.Errorf("safefs: lstat %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkInPath, cur)
		}
		// All non-final segments must be directories.
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("safefs: non-directory in path: %s", cur)
		}
	}
	return nil
}

// ErrPathOutsideRoot is returned by EnsureResolvedUnder when path's symlink-
// resolved location escapes the root.
var ErrPathOutsideRoot = errors.New("path resolves outside root")

// EnsureResolvedUnder is the read-tier containment check: it tolerates
// symlinks AS LONG AS path's fully-resolved target still falls under the
// fully-resolved root. The CheckNoSymlinks helper above is the strict
// "no symlinks anywhere" check used by writes; this one is the looser
// gate appropriate for read-only primitives that walk a directory tree
// and must refuse to follow a symlink leaf out of the project.
//
// Both arguments must be absolute. The returned path is the resolved
// target on success.
//
// Why this exists: filepath.Rel is purely lexical — a symlink leaf inside
// root whose target lives at /etc/passwd computes a relative path that
// looks contained, so the lexical check passes and a subsequent os.ReadFile
// follows the link to the secret. Glob and Grep had this gap; Read closed
// it inline; this helper centralizes the pattern.
func EnsureResolvedUnder(path, root string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("safefs: path not absolute: %s", path)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("safefs: root not absolute: %s", root)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("safefs: evalsymlinks %s: %w", path, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("safefs: evalsymlinks root %s: %w", root, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil {
		return "", fmt.Errorf("safefs: rel %s: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrPathOutsideRoot, path)
	}
	return resolved, nil
}

// WriteFile writes data to path with mode after CheckNoSymlinks(baseDir, path)
// succeeds. Existing regular-file targets are overwritten in place; symlinks
// or non-regular files anywhere along the path are refused.
//
// Note: os.WriteFile uses os.OpenFile(O_WRONLY|O_CREATE|O_TRUNC) which on
// Unix follows symlinks, but the preceding Lstat check ensures the target
// is not a symlink at the moment of inspection. Race-window TOCTOU is
// documented as residual.
func WriteFile(baseDir, path string, data []byte, mode os.FileMode) error {
	if err := CheckNoSymlinks(baseDir, path); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

// WriteFileAtomic writes data to path via a temp file in the same directory
// followed by os.Rename, after CheckNoSymlinks(baseDir, path) succeeds.
//
// rename(2) replaces a pre-existing symlink with the new regular file
// rather than writing through it, so this helper is symlink-safe at the
// target itself even between the Lstat check and the rename. Concurrent
// writers see either the old or new content, never a partial write.
//
// The mode is applied to the temp file via os.Chmod before rename, so the
// final path inherits it atomically. On Windows os.Chmod is best-effort;
// the file is still written.
func WriteFileAtomic(baseDir, path string, data []byte, mode os.FileMode) error {
	if err := CheckNoSymlinks(baseDir, path); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".safefs-*")
	if err != nil {
		return fmt.Errorf("safefs: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	cleanup := func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("safefs: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("safefs: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		closed = true
		_ = os.Remove(tmpPath)
		return fmt.Errorf("safefs: close temp: %w", err)
	}
	closed = true
	// Best-effort chmod — ignored on platforms that don't fully honor it.
	_ = os.Chmod(tmpPath, mode)
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("safefs: rename: %w", err)
	}
	return nil
}
