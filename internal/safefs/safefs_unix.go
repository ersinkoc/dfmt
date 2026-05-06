//go:build !windows
// +build !windows

package safefs

import (
	"os"

	"golang.org/x/sys/unix"
)

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
