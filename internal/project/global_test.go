package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGlobalDirRespectsEnvOverride verifies DFMT_GLOBAL_DIR redirects
// the global path resolver. Used by tests + sandboxed environments
// (snap, flatpak) that can't write to $HOME/.dfmt directly.
func TestGlobalDirRespectsEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	if got := GlobalDir(); got != tmp {
		t.Errorf("GlobalDir(): got %q, want %q", got, tmp)
	}
	// Sub-paths should sit inside the override.
	for _, p := range []string{GlobalPortPath(), GlobalPIDPath(), GlobalLockPath(), GlobalCrashPath()} {
		if !strings.HasPrefix(p, tmp) {
			t.Errorf("path %q does not start with override %q", p, tmp)
		}
	}
}

// TestGlobalDirCreatesDirectory ensures GlobalDir is a side-effecting
// helper: even if the override doesn't exist yet, it gets created with
// 0o700 perms so the daemon can immediately drop port/lock files into it.
func TestGlobalDirCreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "fresh")
	t.Setenv("DFMT_GLOBAL_DIR", target)

	if got := GlobalDir(); got != target {
		t.Errorf("GlobalDir(): got %q, want %q", got, target)
	}
	if fi, err := os.Stat(target); err != nil {
		t.Errorf("target not created: %v", err)
	} else if !fi.IsDir() {
		t.Errorf("target is not a directory")
	}
}

// TestGlobalCrashPathStable verifies that two calls return the same
// path. Doctor reads it on every invocation; if the helper produced a
// timestamp-suffixed name we'd lose the most-recent-crash semantics.
func TestGlobalCrashPathStable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	if a, b := GlobalCrashPath(), GlobalCrashPath(); a != b {
		t.Errorf("GlobalCrashPath instability: %q vs %q", a, b)
	}
}

// TestGlobalSocketPathShortPath covers the normal (non-fallback) branch:
// when GlobalDir() produces a path under 100 chars, the socket is placed
// directly inside it rather than under the runtime dir.
func TestGlobalSocketPathShortPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	got := GlobalSocketPath()
	// On all platforms with a short temp dir, this should be a direct join
	// without the sha256-fallback branch.
	want := filepath.Join(tmp, GlobalSocketName)
	if got != want {
		t.Errorf("GlobalSocketPath short path: got %q, want %q", got, want)
	}
}

// TestGlobalSocketPathConsistency verifies two calls produce identical paths
// and that the path is stable across invocations (no timestamp suffix).
func TestGlobalSocketPathConsistency(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	a := GlobalSocketPath()
	b := GlobalSocketPath()
	if a != b {
		t.Errorf("GlobalSocketPath instability: %q vs %q", a, b)
	}
}

// TestGlobalSocketPathFallbackOnLongHome documents the sha256-fallback
// behavior when HOME/.dfmt exceeds 100 chars. We simulate a long path
// by injecting a long directory name via DFMT_GLOBAL_DIR. On Windows
// the fallback branch is never hit so we skip it.
func TestGlobalSocketPathFallbackOnLongHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use Unix socket fallback")
	}
	// Create a path > 100 chars by padding the dir name
	longDir := strings.Repeat("x", 120)
	tmp := t.TempDir()
	longPath := filepath.Join(tmp, longDir)
	t.Setenv("DFMT_GLOBAL_DIR", longPath)
	got := GlobalSocketPath()
	// Should contain the sha256-hashed fallback name, not the direct join
	if len(got) <= len(longPath)+len(GlobalSocketName)+5 {
		t.Errorf("GlobalSocketPath fallback: got %q, expected hashed fallback", got)
	}
}

// TestGlobalSocketPathOnWindows verifies that on Windows the function
// returns the direct join even when the path is short (no Unix socket
// semantics needed on Windows).
func TestGlobalSocketPathOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	got := GlobalSocketPath()
	want := filepath.Join(tmp, GlobalSocketName)
	if got != want {
		t.Errorf("Windows GlobalSocketPath: got %q, want %q", got, want)
	}
}

// TestGlobalSocketPathOnUnix verifies Unix behavior: should use direct path
// when under 100 chars, fallback otherwise.
func TestGlobalSocketPathOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	got := GlobalSocketPath()
	want := filepath.Join(tmp, GlobalSocketName)
	if got != want {
		t.Errorf("Unix GlobalSocketPath: got %q, want %q", got, want)
	}
}
