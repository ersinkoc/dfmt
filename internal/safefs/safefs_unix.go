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
func WriteFile(baseDir, path string, data []byte, mode os.FileMode) error {
	if err := CheckNoSymlinks(baseDir, path); err != nil {
		return err
	}
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
