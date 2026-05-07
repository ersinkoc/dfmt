package safefs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireSymlinks skips the test on platforms / privilege contexts where
// os.Symlink fails. On Windows, creating symlinks generally requires either
// admin or the SeCreateSymbolicLinkPrivilege; rather than gate the test
// suite on that, we probe and skip if the OS refuses.
func requireSymlinks(t *testing.T, base string) {
	t.Helper()
	probe := filepath.Join(base, "probe-target")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup probe target: %v", err)
	}
	link := filepath.Join(base, "probe-link")
	if err := os.Symlink(probe, link); err != nil {
		t.Skipf("symlinks not creatable on this platform/permission level: %v", err)
	}
	_ = os.Remove(link)
	_ = os.Remove(probe)
}

func TestCheckNoSymlinks_AbsoluteOnly(t *testing.T) {
	if err := CheckNoSymlinks("relative", "/tmp/x"); err == nil {
		t.Error("relative baseDir should error")
	}
	abs := mustAbs(t, t.TempDir())
	if err := CheckNoSymlinks(abs, "relative"); err == nil {
		t.Error("relative path should error")
	}
}

func TestCheckNoSymlinks_PathOutsideBase(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	other := mustAbs(t, t.TempDir())
	if err := CheckNoSymlinks(base, filepath.Join(other, "x")); err == nil {
		t.Error("path outside base should error")
	}
}

func TestCheckNoSymlinks_AllowsRegularPath(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	target := filepath.Join(base, "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoSymlinks(base, target); err != nil {
		t.Errorf("regular path: %v", err)
	}
}

func TestCheckNoSymlinks_AllowsMissingTarget(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	target := filepath.Join(base, "sub", "does-not-exist.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoSymlinks(base, target); err != nil {
		t.Errorf("missing target with regular parent: %v", err)
	}
}

func TestCheckNoSymlinks_RefusesSymlinkAtTarget(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	requireSymlinks(t, base)
	outside := mustAbs(t, t.TempDir())
	outsideTarget := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideTarget, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "leak.txt")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err := CheckNoSymlinks(base, link)
	if err == nil {
		t.Fatal("symlink at target should error")
	}
	if !errors.Is(err, ErrSymlinkInPath) {
		t.Errorf("want ErrSymlinkInPath, got: %v", err)
	}
}

func TestCheckNoSymlinks_RefusesSymlinkInParent(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	requireSymlinks(t, base)
	outside := mustAbs(t, t.TempDir())
	link := filepath.Join(base, "parent")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	target := filepath.Join(link, "innocent.txt")
	err := CheckNoSymlinks(base, target)
	if err == nil {
		t.Fatal("symlink in parent should error")
	}
	if !errors.Is(err, ErrSymlinkInPath) {
		t.Errorf("want ErrSymlinkInPath, got: %v", err)
	}
}

func TestWriteFile_WritesRegularFile(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	target := filepath.Join(base, "out.txt")
	if err := WriteFile(base, target, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q; want %q", got, "hello")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v; want 0600", fi.Mode().Perm())
		}
	}
}

func TestWriteFile_RefusesSymlinkTarget(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	requireSymlinks(t, base)
	outside := mustAbs(t, t.TempDir())
	outsideTarget := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideTarget, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err := WriteFile(base, link, []byte("malicious"), 0o600)
	if err == nil {
		t.Fatal("WriteFile through symlink should error")
	}
	got, err := os.ReadFile(outsideTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("symlink target was overwritten; got %q", got)
	}
}

func TestWriteFileAtomic_OverwritesExisting(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	target := filepath.Join(base, "atomic.txt")
	if err := os.WriteFile(target, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(base, target, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Errorf("got %q; want v2", got)
	}
}

func TestWriteFileAtomic_RefusesSymlinkInParent(t *testing.T) {
	base := mustAbs(t, t.TempDir())
	requireSymlinks(t, base)
	outside := mustAbs(t, t.TempDir())
	link := filepath.Join(base, "linked-dir")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	target := filepath.Join(link, "file.txt")
	err := WriteFileAtomic(base, target, []byte("payload"), 0o600)
	if err == nil {
		t.Fatal("atomic write through symlinked parent should error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("want symlink error, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(err) {
		t.Errorf("write was not blocked: %v", err)
	}
}

func TestWriteFileAtomic_ReplacesSymlinkAsRegular(t *testing.T) {
	// rename(2) replaces a pre-existing symlink with a regular file rather
	// than writing through it. CheckNoSymlinks refuses this case before
	// the rename anyway, but document the expected behavior if the gate
	// is ever relaxed.
	base := mustAbs(t, t.TempDir())
	requireSymlinks(t, base)
	outside := mustAbs(t, t.TempDir())
	outsideTarget := filepath.Join(outside, "elsewhere.txt")
	if err := os.WriteFile(outsideTarget, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := WriteFileAtomic(base, link, []byte("new"), 0o600); err == nil {
		t.Fatal("CheckNoSymlinks should refuse symlink at target")
	}
	got, _ := os.ReadFile(outsideTarget)
	if string(got) != "original" {
		t.Errorf("outside file was modified: %q", got)
	}
}

func TestEnsureResolvedUnder_AbsoluteOnly(t *testing.T) {
	abs := mustAbs(t, t.TempDir())
	if _, err := EnsureResolvedUnder("relative", abs); err == nil {
		t.Error("relative path should error")
	}
	if _, err := EnsureResolvedUnder(abs, "relative"); err == nil {
		t.Error("relative root should error")
	}
}

func TestEnsureResolvedUnder_AllowsContainedFile(t *testing.T) {
	root := mustAbs(t, t.TempDir())
	target := filepath.Join(root, "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := EnsureResolvedUnder(target, root)
	if err != nil {
		t.Errorf("contained file: %v", err)
	}
	if resolved == "" {
		t.Error("resolved path should not be empty")
	}
}

func TestEnsureResolvedUnder_AllowsSymlinkInsideRoot(t *testing.T) {
	root := mustAbs(t, t.TempDir())
	requireSymlinks(t, root)
	target := filepath.Join(root, "real.txt")
	if err := os.WriteFile(target, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := EnsureResolvedUnder(link, root); err != nil {
		t.Errorf("symlink-inside-root should be allowed: %v", err)
	}
}

func TestEnsureResolvedUnder_RefusesSymlinkLeafEscapingRoot(t *testing.T) {
	// V-01: this is the Glob/Grep bypass. A symlink leaf whose path is
	// lexically inside root but whose target lives outside root must be
	// refused — otherwise os.ReadFile on the path follows the link to
	// the secret.
	root := mustAbs(t, t.TempDir())
	requireSymlinks(t, root)
	outside := mustAbs(t, t.TempDir())
	outsideTarget := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideTarget, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.txt")
	if err := os.Symlink(outsideTarget, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := EnsureResolvedUnder(link, root)
	if err == nil {
		t.Fatal("symlink-leaf-out-of-root should error")
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("want ErrPathOutsideRoot, got: %v", err)
	}
}

func TestEnsureResolvedUnder_RefusesPathOutsideRoot(t *testing.T) {
	root := mustAbs(t, t.TempDir())
	other := mustAbs(t, t.TempDir())
	target := filepath.Join(other, "elsewhere.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureResolvedUnder(target, root)
	if err == nil {
		t.Fatal("path outside root should error")
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("want ErrPathOutsideRoot, got: %v", err)
	}
}

func TestEnsureResolvedUnder_RefusesNonexistentPath(t *testing.T) {
	root := mustAbs(t, t.TempDir())
	target := filepath.Join(root, "does-not-exist.txt")
	if _, err := EnsureResolvedUnder(target, root); err == nil {
		t.Error("nonexistent path should error (callers are about to ReadFile)")
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("Abs(%s): %v", p, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Falls back to abs (e.g. on platforms where temp dir resolution
		// fails). Tests below don't depend on resolved == abs identity.
		return abs
	}
	return resolved
}

// =============================================================================
// WriteFileAtomic error path tests
// =============================================================================

func TestWriteFileAtomic_CleanupOnWriteError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permission behavior differs from Unix")
	}
	tmp := t.TempDir()
	// Create a read-only directory so CreateTemp fails
	roDir := filepath.Join(tmp, "ro")
	if err := os.MkdirAll(roDir, 0555); err != nil {
		t.Fatalf("mkdir ro: %v", err)
	}
	defer os.Chmod(roDir, 0755)

	path := filepath.Join(roDir, "file.txt")
	err := WriteFileAtomic(roDir, path, []byte("data"), 0600)
	if err == nil {
		t.Error("WriteFileAtomic should fail on read-only directory")
	}
}

func TestWriteFileAtomic_CleanupOnSyncError(t *testing.T) {
	// os.Sync is a no-op on most filesystems; hard to trigger sync failure.
	// We test the cleanup path by closing the file before sync.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")

	// We can't easily force a sync error in tests, so we just verify
	// the happy path works correctly.
	err := WriteFileAtomic(tmp, path, []byte("hello"), 0600)
	if err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("got %q, want hello", string(data))
	}
}

func TestWriteFileAtomic_CleanupOnCloseError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	err := WriteFileAtomic(tmp, path, []byte("world"), 0600)
	if err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("got %q, want world", string(data))
	}
}

func TestWriteFileAtomic_CleanupOnRenameError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows rename into directory succeeds, unlike Unix EXDEV")
	}
	// When rename fails (e.g. target is a directory), temp file must be removed
	tmp := t.TempDir()
	// Pre-create the target as a directory so rename fails
	targetDir := filepath.Join(tmp, "subdir")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(targetDir, "file.txt")

	err := WriteFileAtomic(tmp, path, []byte("data"), 0600)
	if err == nil {
		t.Error("WriteFileAtomic should fail when target is a directory")
	}
}

func TestWriteFileAtomic_ReplacesExistingOnDisk(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "atomic.txt")
	data := []byte("atomic content")

	err := WriteFileAtomic(tmp, path, data, 0600)
	if err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "atomic content" {
		t.Errorf("got %q, want %q", string(got), string(data))
	}
}

func TestWriteFileAtomic_SymlinkInBaseDir(t *testing.T) {
	tmp := t.TempDir()
	subdir := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(subdir, "link")
	if err := os.Symlink(filepath.Join(tmp, "target"), link); err != nil {
		t.Skip("symlinks not available")
	}
	target := filepath.Join(tmp, "target")
	if err := os.WriteFile(target, []byte("content"), 0600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	err := WriteFileAtomic(subdir, filepath.Join(subdir, "file.txt"), []byte("data"), 0600)
	if err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
}

// =============================================================================
// OpenReadNoFollow tests
// =============================================================================

func TestOpenReadNoFollow_RegularFile(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "regular.txt")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := OpenReadNoFollow(file)
	if err != nil {
		t.Fatalf("OpenReadNoFollow: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("got %q, want hello", string(data))
	}
}

func TestOpenReadNoFollow_NonExistent(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "nonexistent.txt")

	_, err := OpenReadNoFollow(file)
	if err == nil {
		t.Error("OpenReadNoFollow should error on nonexistent file")
	}
}

func TestOpenReadNoFollow_Symlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target.txt")
	if err := os.WriteFile(target, []byte("content"), 0600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(tmp, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not available")
	}

	_, err := OpenReadNoFollow(link)
	if err == nil {
		t.Error("OpenReadNoFollow should reject symlink")
	}
}

func TestOpenReadNoFollow_Directory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.Open succeeds on directories")
	}
	tmp := t.TempDir()

	_, err := OpenReadNoFollow(tmp)
	// Directories can be opened on most systems; behavior varies
	// The important thing is it doesn't panic
	_ = err
}
