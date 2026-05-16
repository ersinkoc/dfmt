package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/project"
)

// TestRunDashboard_HelpReturnsZero pins flag parsing: `--help` must exit
// cleanly. A regression that propagates flag.ErrHelp as a non-zero exit
// would have a CI that runs `--help` for smoke testing flag the binary
// as broken.
func TestRunDashboard_HelpReturnsZero(t *testing.T) {
	if code := runDashboard([]string{"--help"}); code != 0 {
		t.Errorf("runDashboard --help returned %d, want 0", code)
	}
}

// TestRunDashboard_UnknownFlagReturnsTwo pins flag parsing: an unknown
// flag must exit with code 2 (Go's flag-parse-error convention) so
// scripts wrapping this command can distinguish syntax errors from
// runtime failures (exit 1).
func TestRunDashboard_UnknownFlagReturnsTwo(t *testing.T) {
	if code := runDashboard([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("runDashboard --no-such-flag returned %d, want 2", code)
	}
}

// TestRunDashboard_UnixReportsBrowserUnreachable pins the platform
// gate: on Unix the daemon binds a Unix socket, so a browser cannot
// reach the dashboard. Printing http://127.0.0.1:<port>/dashboard
// would mislead the user into a connection-refused dead end. Exit 1
// keeps scripts from interpreting the no-URL output as success.
//
// Skipped on Windows where the daemon binds TCP loopback and the
// command's happy path requires a running daemon (covered by manual
// smoke test, not unit).
func TestRunDashboard_UnixReportsBrowserUnreachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows path requires a live daemon; manual smoke only")
	}
	tmpDir := t.TempDir()

	prevProject := flagProject
	t.Cleanup(func() { flagProject = prevProject })
	flagProject = tmpDir

	if code := runDashboard(nil); code != 1 {
		t.Errorf("runDashboard on Unix returned %d, want 1 (browser-unreachable hint)", code)
	}
}

// TestRunDashboard_GlobalDaemonURL pins the global-daemon short-circuit:
// when a port file is present under DFMT_GLOBAL_DIR, runDashboard prints
// the http://127.0.0.1:<port>/dashboard URL and exits 0 without
// consulting the legacy per-project registry. This is the v0.6.x
// happy path for Windows hosts.
func TestRunDashboard_GlobalDaemonURL(t *testing.T) {
	dir := withIsolatedGlobalDir(t)
	if err := os.WriteFile(
		filepath.Join(dir, project.GlobalPortFileName),
		[]byte("18765"),
		0o600,
	); err != nil {
		t.Fatalf("write port: %v", err)
	}
	out := captureStdout(t, func() {
		if code := runDashboard(nil); code != 0 {
			t.Errorf("want 0, got %d", code)
		}
	})
	want := "http://127.0.0.1:18765/dashboard"
	if !strings.Contains(out, want) {
		t.Errorf("want %q in output, got %q", want, out)
	}
}

// TestRunDashboard_OpenWithNoBrowserHookSafe asserts that --open is a
// no-op during tests — openInBrowser short-circuits when test.v is
// registered as a flag (Go test runner injects it). Without that
// short-circuit, every CI run would try to launch rundll32 / xdg-open.
func TestRunDashboard_OpenWithNoBrowserHookSafe(t *testing.T) {
	dir := withIsolatedGlobalDir(t)
	if err := os.WriteFile(
		filepath.Join(dir, project.GlobalPortFileName),
		[]byte("18766"),
		0o600,
	); err != nil {
		t.Fatalf("write port: %v", err)
	}
	if code := runDashboard([]string{"--open"}); code != 0 {
		t.Errorf("want 0, got %d", code)
	}
}

// TestOpenInBrowser_TestModeShortCircuit confirms openInBrowser returns
// nil without spawning a process when test.v is registered. Pinning
// this behavior prevents CI pop-ups if someone tightens the gate later.
func TestOpenInBrowser_TestModeShortCircuit(t *testing.T) {
	if err := openInBrowser("http://127.0.0.1:0/dashboard"); err != nil {
		t.Errorf("openInBrowser in test mode: want nil, got %v", err)
	}
}
