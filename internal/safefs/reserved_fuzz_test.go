package safefs

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzCheckNoSymlinks_AbsPathContract enforces the safefs invariant that
// CheckNoSymlinks rejects non-absolute paths up front and never panics on
// arbitrary input. The path-walk itself only runs when both arguments
// are absolute and inside the same root; this fuzz exercises the
// validation layer.
//
// Symlink-traversal correctness with real filesystem state is covered
// by safefs_test.go — fuzzing it would require materializing symlinks
// per-input, which is outside the harness scope. The narrower invariant
// here ("no panic, abs check holds") is what surfaces parser bugs.
func FuzzCheckNoSymlinks_AbsPathContract(f *testing.F) {
	seeds := [][2]string{
		{"/tmp", "/tmp/foo"},
		{"C:\\base", "C:\\base\\sub"},
		{"", ""},
		{"/", "/etc/passwd"},
		{"relative", "/abs/path"},
		{"/abs", "relative"},
		{"/a", "/a"},
		{"/a", "/a/.."},
		{"/a", "/a/../../etc"},
		{"\x00", "\x00"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, baseDir, path string) {
		err := CheckNoSymlinks(baseDir, path)
		// Either argument non-absolute MUST produce an error; this is
		// the documented contract that callers (WriteFile / WriteFileAtomic)
		// rely on as a precondition for the actual symlink walk.
		if !filepath.IsAbs(baseDir) || !filepath.IsAbs(path) {
			if err == nil {
				t.Fatalf("CheckNoSymlinks(%q, %q) returned nil for non-absolute input", baseDir, path)
			}
			return
		}
		// Lexical containment: a fully-resolved cleanPath outside cleanBase
		// MUST refuse, regardless of what's actually on disk. This catches
		// `../../../etc/passwd`-shaped escapes the walk would otherwise
		// process one component at a time.
		cleanBase := filepath.Clean(baseDir)
		cleanPath := filepath.Clean(path)
		rel, relErr := filepath.Rel(cleanBase, cleanPath)
		if relErr == nil {
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if err == nil {
					t.Fatalf("CheckNoSymlinks(%q, %q): path escapes baseDir but no error", baseDir, path)
				}
			}
		}
	})
}

// FuzzCheckNoReservedNames_NoFalseNegative wraps the reserved-name guard
// in a fuzzer so combinations of separators, drive letters, case, and
// trailing-colon variants don't slip through. The unit tests cover a
// fixed set; this catches edge cases the test author didn't think of.
func FuzzCheckNoReservedNames_NoFalseNegative(f *testing.F) {
	seeds := []string{
		"NUL",
		"NUL:",
		"NUL.txt",
		"foo/NUL/bar",
		"C:\\NUL\\baz",
		"D:/CON/x",
		"   nul   ",
		"COM9.log",
		"LPT1:",
		"normal/path/file.txt",
		"",
		"...",
		"\\\\?\\C:\\NUL",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		err := CheckNoReservedNames(path)
		// If a path component exactly matches a reserved name (after the
		// canonical strip the function performs internally), we MUST
		// flag it. We re-run the canonical detection here on each
		// non-empty component to assert the guard didn't miss it.
		// Mirror the function's separator normalization: ToSlash is a
		// no-op on non-Windows hosts, so explicitly replace backslashes
		// too — the production code does the same so paths copied from
		// a Windows tool to a Linux host (NTFS share, drvfs) get
		// component-split correctly.
		s := strings.ReplaceAll(filepath.ToSlash(path), "\\", "/")
		// Strip leading drive-letter prefix to match the function's
		// behavior; otherwise "C:" would itself be inspected.
		if len(s) >= 2 && s[1] == ':' {
			s = s[2:]
			s = strings.TrimPrefix(s, "/")
		}
		expectedReserved := false
		for _, comp := range strings.Split(s, "/") {
			if comp == "" || comp == "." || comp == ".." {
				continue
			}
			if IsWindowsReservedComponent(comp) {
				expectedReserved = true
				break
			}
		}
		if expectedReserved && !errors.Is(err, ErrReservedName) {
			t.Fatalf("CheckNoReservedNames(%q): expected ErrReservedName, got %v", path, err)
		}
		if !expectedReserved && err != nil {
			t.Fatalf("CheckNoReservedNames(%q): unexpected error %v", path, err)
		}
	})
}
