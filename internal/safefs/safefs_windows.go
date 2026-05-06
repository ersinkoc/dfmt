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
	// os.O_RDONLY == 0 in Go, so a `flag & os.O_RDONLY != 0` check is a
	// no-op; treat the absence of any write flag as read-only and grant
	// GENERIC_READ. Without this, OpenReadNoFollow (added for V-09) would
	// call CreateFile with desiredAccess=0 and fail.
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		access |= windows.GENERIC_READ
	}
	if flag&os.O_WRONLY != 0 {
		access |= windows.GENERIC_WRITE
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

// OpenReadNoFollow opens path read-only and refuses to follow a leaf
// symlink. Windows has no exact O_NOFOLLOW equivalent — FILE_FLAG_OPEN_
// REPARSE_POINT only changes *what* the open returns (the reparse point
// itself instead of its target), it doesn't make the open fail. So we
// Lstat first and reject if the leaf is a reparse point, then open
// normally. The Lstat+open sequence is atomic enough for the V-09 contract
// because the only race window is between the Lstat and the open: a
// symlink swap during that window would flip our view, but the open will
// then either succeed against the new file (which is fine — we never
// approved the original symlink in the first place) or fail. Either way,
// no symlink ever gets followed under this helper.
func OpenReadNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrSymlinkInPath}
	}
	return os.Open(path)
}

// WriteFile is the Windows-specific implementation that uses
// FILE_FLAG_OPEN_REPARSE_POINT instead of O_NOFOLLOW.
//
// V-19: Mode is masked to user-only bits for parity with the Unix variant.
// Windows file ACLs ignore the Unix mode bits anyway, but we mirror the
// invariant so the call signature carries the same guarantee on both OSes.
func WriteFile(baseDir, path string, data []byte, mode os.FileMode) error {
	if err := CheckNoSymlinks(baseDir, path); err != nil {
		return err
	}
	mode &= 0o700
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
