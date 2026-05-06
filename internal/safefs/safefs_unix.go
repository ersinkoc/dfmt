//go:build !windows
// +build !windows

package safefs

import (
	"os"

	"golang.org/x/sys/unix"
)

// OpenReadNoFollow opens path read-only and refuses to follow a symlink at
// the leaf position. Closes V-09's TOCTOU window: lexical containment checks
// (filepath.EvalSymlinks + Rel) must finish before the open, and a malicious
// process that swaps the leaf for a symlink between the check and the open
// would otherwise cause the open to follow the new symlink. O_NOFOLLOW makes
// the kernel itself refuse the post-swap state, regardless of what the
// pre-check resolved to.
//
// Intermediate path components ARE allowed to be symlinks — the Read tier
// has always tolerated benign within-root symlinks (see EnsureResolvedUnder
// docs); only the final component is gated. Use the writer-side
// CheckNoSymlinks helper if every component must be a regular dir.
func OpenReadNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
}

// WriteFile writes data to path with mode after CheckNoSymlinks(baseDir, path)
// succeeds. Existing regular-file targets are overwritten in place; symlinks
// or non-regular files anywhere along the path are refused.
//
// Uses O_NOFOLLOW so the operating system refuses to follow a symlink at the
// leaf position, closing the TOCTOU window.
//
// V-19: The mode is masked to user-only bits (0o700) before the open call.
// Project-managed paths are documented to be 0o600 across the codebase
// (CLAUDE.md "Local state … all 0o600"); the mask prevents a future caller
// drift (e.g. a copy-paste of the V-02 logging.go bug) from shipping world-
// or group-readable files through this helper.
func WriteFile(baseDir, path string, data []byte, mode os.FileMode) error {
	if err := CheckNoSymlinks(baseDir, path); err != nil {
		return err
	}
	mode &= 0o700
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}
