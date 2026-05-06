package project

import (
	"os"
	"path/filepath"
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
