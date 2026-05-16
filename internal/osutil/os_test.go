package osutil

import (
	"runtime"
	"testing"
)

// TestIsWindows pins the helper's contract against runtime.GOOS. The
// function is a one-liner; the test exists so a hypothetical future
// rework (e.g., honoring an env var override for cross-OS test
// fixtures) keeps the production-mode contract intact.
func TestIsWindows(t *testing.T) {
	if got, want := IsWindows(), runtime.GOOS == Windows; got != want {
		t.Errorf("IsWindows() = %v, want %v (GOOS=%q)", got, want, runtime.GOOS)
	}
}

// TestSamePath_ByteEqual: identical strings always match regardless
// of platform.
func TestSamePath_ByteEqual(t *testing.T) {
	if !SamePath("/a/b/c", "/a/b/c") {
		t.Error("identical: want true")
	}
	if !SamePath("", "") {
		t.Error("empty/empty: want true")
	}
	if SamePath("a", "b") {
		t.Error("a vs b: want false")
	}
}

// TestSamePath_CaseSensitivity: the only platform-dependent branch.
// On Windows mixed case matches; elsewhere it doesn't.
func TestSamePath_CaseSensitivity(t *testing.T) {
	got := SamePath("/A/B/c", "/a/b/c")
	if IsWindows() && !got {
		t.Error("windows: mixed case should match")
	}
	if !IsWindows() && got {
		t.Error("posix: mixed case should not match")
	}
}

// TestSamePath_NoCleaning: SamePath does NOT apply filepath.Clean —
// trailing slashes and "./" components produce false. Callers that
// want normalization use SameCleanPath.
func TestSamePath_NoCleaning(t *testing.T) {
	if SamePath("/a/b/c/", "/a/b/c") && !IsWindows() {
		t.Error("trailing slash should NOT match without Clean")
	}
}

// TestSameCleanPath_NormalizesTrailingSlash: with Clean applied,
// trailing slashes are equivalent.
func TestSameCleanPath_NormalizesTrailingSlash(t *testing.T) {
	if !SameCleanPath("/a/b/c/", "/a/b/c") {
		t.Error("trailing slash should match after Clean")
	}
}

// TestSameCleanPath_NormalizesDotSegments: "/." and similar lexical
// noise is collapsed by Clean.
func TestSameCleanPath_NormalizesDotSegments(t *testing.T) {
	if !SameCleanPath("/a/./b/c", "/a/b/c") {
		t.Error("dot segment should match after Clean")
	}
	if !SameCleanPath("/a/b/x/../c", "/a/b/c") {
		t.Error("parent segment should match after Clean")
	}
}
