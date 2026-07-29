package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/osutil"
)

// Regression tests for the Git-for-Windows PATH gap.
//
// lookPath deliberately resolves `bash` to Git Bash at
// <root>\usr\bin\bash.exe. That same directory holds the coreutils — ls,
// cat, grep, sed, awk, find, which — and a stock Git for Windows install
// keeps it OUT of the Windows PATH so its POSIX tools don't shadow Windows
// commands. Git Bash re-adds it at terminal startup; the sandbox runs
// `bash.exe -c` with an environment it builds itself and never gets that
// step, so bash launched fine and then could not find any of its own
// utilities: `ls` returned exit 127.
//
// The bug hid for a long time because go/node/python DO live on the Windows
// PATH, so every toolchain smoke test passed while `ls` was broken.

func TestGitForWindowsRoot(t *testing.T) {
	tests := []struct {
		name     string
		exe      string
		wantRoot string
		wantOK   bool
	}{
		{
			name:     "stock layout usr/bin",
			exe:      filepath.Join("C:", "Program Files", "Git", "usr", "bin", "bash.exe"),
			wantRoot: filepath.Join("C:", "Program Files", "Git"),
			wantOK:   true,
		},
		{
			name:     "legacy layout bin",
			exe:      filepath.Join("C:", "Program Files", "Git", "bin", "bash.exe"),
			wantRoot: filepath.Join("C:", "Program Files", "Git"),
			wantOK:   true,
		},
		{
			name:     "x86 install",
			exe:      filepath.Join("C:", "Program Files (x86)", "Git", "usr", "bin", "bash.exe"),
			wantRoot: filepath.Join("C:", "Program Files (x86)", "Git"),
			wantOK:   true,
		},
		{
			// A shell that is not inside a bin/ directory must not have a
			// root inferred — injecting an unrelated directory into the
			// sandbox PATH would be worse than the missing coreutils.
			name:   "not in a bin dir",
			exe:    filepath.Join("C:", "tools", "bash.exe"),
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := gitForWindowsRoot(tc.exe)
			if ok != tc.wantOK {
				t.Fatalf("gitForWindowsRoot(%q) ok = %v, want %v", tc.exe, ok, tc.wantOK)
			}
			if ok && got != tc.wantRoot {
				t.Errorf("gitForWindowsRoot(%q) = %q, want %q", tc.exe, got, tc.wantRoot)
			}
		})
	}
}

// shellCompanionDirs must return only directories that exist, and must
// return them with usr\bin first — the shell's own coreutils take priority.
func TestShellCompanionDirsOrdersAndFiltersByExistence(t *testing.T) {
	if !osutil.IsWindows() {
		t.Skip("Git-for-Windows layout is Windows-only")
	}
	root := t.TempDir()
	usrBin := filepath.Join(root, "usr", "bin")
	mingw := filepath.Join(root, "mingw64", "bin")
	if err := os.MkdirAll(usrBin, 0o700); err != nil {
		t.Fatalf("mkdir usr/bin: %v", err)
	}
	if err := os.MkdirAll(mingw, 0o700); err != nil {
		t.Fatalf("mkdir mingw64/bin: %v", err)
	}
	// usr/local/bin deliberately NOT created — it must be filtered out.

	got := shellCompanionDirs(filepath.Join(usrBin, "bash.exe"))
	want := []string{usrBin, mingw}
	if len(got) != len(want) {
		t.Fatalf("shellCompanionDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shellCompanionDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A shell outside any Git tree must contribute nothing.
func TestShellCompanionDirsIgnoresNonGitShell(t *testing.T) {
	if got := shellCompanionDirs(""); got != nil {
		t.Errorf("shellCompanionDirs(\"\") = %v, want nil", got)
	}
	if !osutil.IsWindows() {
		// On Unix the function is a no-op regardless of input.
		if got := shellCompanionDirs("/usr/bin/bash"); got != nil {
			t.Errorf("shellCompanionDirs on non-Windows = %v, want nil", got)
		}
	}
}

// TestExecFindsPOSIXCoreutils is the end-to-end regression: the exact
// failure the user hit. `ls` must resolve, not exit 127.
func TestExecFindsPOSIXCoreutils(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("regression is specific to the Git-for-Windows PATH layout")
	}
	sb := NewSandbox(t.TempDir())
	if rt, ok := sb.runtimes.Get(langBash); !ok || !rt.Available {
		_ = sb.runtimes.Probe(context.Background())
	}
	rt, ok := sb.runtimes.Get(langBash)
	if !ok || !rt.Available {
		t.Skip("bash not available")
	}
	if _, isGit := gitForWindowsRoot(rt.Executable); !isGit {
		t.Skipf("bash at %q is not a Git-for-Windows install", rt.Executable)
	}

	for _, tool := range []string{"ls", "cat", "sed", "awk"} {
		resp, err := sb.Exec(context.Background(), ExecReq{
			Code:    "command -v " + tool,
			Lang:    langBash,
			Intent:  "locate " + tool,
			Return:  "raw",
			Timeout: 30 * time.Second,
		})
		if err != nil {
			t.Fatalf("Exec(command -v %s): %v", tool, err)
		}
		if resp.Exit != 0 {
			t.Errorf("%s: exit = %d (127 means the shell cannot find its own "+
				"coreutils — Git usr/bin missing from the sandbox PATH); stderr=%q",
				tool, resp.Exit, resp.Stderr)
			continue
		}
		if strings.TrimSpace(resp.RawStdout) == "" {
			t.Errorf("%s: resolved with exit 0 but no path reported", tool)
		}
	}
}

// The fix must not displace toolchains that already resolved from the
// Windows PATH — go/node/python are not in Git's usr/bin, and an ordering
// mistake that shadowed them would trade one broken sandbox for another.
func TestExecStillResolvesWindowsToolchains(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PATH interaction")
	}
	sb := NewSandbox(t.TempDir())
	_ = sb.runtimes.Probe(context.Background())
	if rt, ok := sb.runtimes.Get(langBash); !ok || !rt.Available {
		t.Skip("bash not available")
	}
	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "command -v go",
		Lang:    langBash,
		Intent:  "locate go",
		Return:  "raw",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.Exit != 0 {
		t.Skipf("go not installed on this host (exit %d)", resp.Exit)
	}
	if strings.TrimSpace(resp.RawStdout) == "" {
		t.Error("go resolved with exit 0 but no path reported")
	}
}
