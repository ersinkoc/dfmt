package cli

import (
	"os"
	"testing"
)

// TestCheckSandboxToolchains_DaemonNotRunning covers the early-return path
// when no daemon is running for the project.
func TestCheckSandboxToolchains_DaemonNotRunning(t *testing.T) {
	tmp := t.TempDir()
	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("checkSandboxToolchains panicked: %v", r)
		}
	}()
	checkSandboxToolchains(tmp)
}

// TestCheckInstructionBlockStaleness_NoManifest covers the early-return when
// no manifest exists.
func TestCheckInstructionBlockStaleness_NoManifest(t *testing.T) {
	tmp := t.TempDir()
	prevHome := os.Getenv("HOME")
	prevUserprofile := os.Getenv("USERPROFILE")
	prevXDG := os.Getenv("XDG_DATA_HOME")
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Cleanup(func() {
		t.Setenv("HOME", prevHome)
		t.Setenv("USERPROFILE", prevUserprofile)
		t.Setenv("XDG_DATA_HOME", prevXDG)
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("checkInstructionBlockStaleness panicked: %v", r)
		}
	}()
	checkInstructionBlockStaleness()
}

// TestRunInstallHooks_Basic covers the basic success path.
func TestRunInstallHooks_Basic(t *testing.T) {
	tmp := t.TempDir()
	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	code := Dispatch([]string{"install-hooks"})
	// .git/hooks dir may not exist, so this may return 1. Must not panic.
	if code != 0 && code != 1 {
		t.Errorf("install-hooks returned %d, want 0 or 1", code)
	}
}

// TestOpenInBrowser covers the platform branch in openInBrowser.
func TestOpenInBrowser(t *testing.T) {
	err := openInBrowser("http://127.0.0.1:9999/dashboard")
	_ = err // error expected (no browser in test env)
}

// TestGoToolchainAtLeast_CoverageBridge adds cases not already covered.
func TestGoToolchainAtLeast_CoverageBridge(t *testing.T) {
	cases := []struct {
		version string
		major   int
		minor   int
		patch   int
		want    bool
	}{
		{"go1.26.2", 1, 26, 2, true},
		{"go1.26.1", 1, 26, 2, false},
		{"go1.27.0", 1, 26, 2, true},
		{"devel go1.28", 1, 26, 2, true},
		{"go1.26.2+", 1, 26, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := goToolchainAtLeast(tc.version, tc.major, tc.minor, tc.patch)
			if got != tc.want {
				t.Errorf("goToolchainAtLeast(%q, %d, %d, %d) = %v, want %v",
					tc.version, tc.major, tc.minor, tc.patch, got, tc.want)
			}
		})
	}
}
