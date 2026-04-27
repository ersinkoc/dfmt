package cli

import (
	"runtime"
	"testing"
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
