//go:build windows
// +build windows

package safefs

import (
	"os"

	"golang.org/x/sys/windows"
)

// openFileNoFollow opens a file with the reparse-point flag so that the
// operating system refuses to follow a symlink at the leaf position.
// windows.OpenFile does not expose O_NOFOLLOW; we use the syscall directly.
func openFileNoFollow(path string, flag int, mode os.FileMode) (*os.File, error) {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var access uint32
	if flag&os.O_WRONLY != 0 {
		access |= windows.GENERIC_WRITE
	}
	if flag&os.O_RDONLY != 0 {
		access |= windows.GENERIC_READ
	}
	if flag&os.O_RDWR != 0 {
		access |= windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	var creat uint32
	if flag&os.O_CREATE != 0 {
		if flag&os.O_TRUNC != 0 {
			creat = windows.CREATE_ALWAYS
		} else {
			creat = windows.OPEN_ALWAYS
		}
	} else {
		creat = windows.OPEN_EXISTING
	}
	h, err := windows.CreateFile(
		pathp,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		creat,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// WriteFile is the Windows-specific implementation that uses
// FILE_FLAG_OPEN_REPARSE_POINT instead of O_NOFOLLOW.
func WriteFile(baseDir, path string, data []byte, mode os.FileMode) error {
	if err := CheckNoSymlinks(baseDir, path); err != nil {
		return err
	}
	f, err := openFileNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}
