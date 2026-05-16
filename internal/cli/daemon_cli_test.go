package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever fn printed. Used to assert the JSON shape of runHook output
// without coupling tests to an external runner.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// stuffStdin replaces os.Stdin with a pipe whose write end gets payload.
// Restores the original on cleanup.
func stuffStdin(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write([]byte(payload))
		_ = w.Close()
	}()
}

// TestRunHook_NoSubcommand: missing positional args ("dfmt hook"
// alone) returns 0 with the default no-block JSON. Hook shells call
// dfmt with the wrong shape on day-one; the binary must not crash.
func TestRunHook_NoSubcommand(t *testing.T) {
	out := captureStdout(t, func() {
		if code := runHook([]string{"claude-code"}); code != 0 {
			t.Errorf("code: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"block":false`) {
		t.Errorf("output: want block:false, got %q", out)
	}
}

// TestRunHook_NonPretooluseSubcommand: "dfmt hook claude-code other"
// also returns the default no-block response — only "pretooluse" gets
// the redirect logic.
func TestRunHook_NonPretooluseSubcommand(t *testing.T) {
	out := captureStdout(t, func() {
		if code := runHook([]string{"claude-code", "posttooluse"}); code != 0 {
			t.Errorf("code: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"block":false`) {
		t.Errorf("output: want block:false, got %q", out)
	}
}

// TestRunHook_BadStdinJSON: a malformed stdin payload produces the
// default no-block response — never an error to the caller because
// hooks must be tolerant of upstream weirdness.
func TestRunHook_BadStdinJSON(t *testing.T) {
	stuffStdin(t, `{not-json`)
	out := captureStdout(t, func() {
		if code := runHook([]string{"claude-code", "pretooluse"}); code != 0 {
			t.Errorf("code: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"block":false`) {
		t.Errorf("output: want block:false, got %q", out)
	}
}

// TestRunHook_EmptyToolName: a parseable payload with an empty tool
// name also takes the no-block path — shouldRedirect's switch never
// matches "".
func TestRunHook_EmptyToolName(t *testing.T) {
	stuffStdin(t, `{"tool_name":"","tool_input":{}}`)
	out := captureStdout(t, func() {
		if code := runHook([]string{"claude-code", "pretooluse"}); code != 0 {
			t.Errorf("code: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"block":false`) {
		t.Errorf("output: want block:false, got %q", out)
	}
}

// TestRunHook_UnknownToolName: a tool name outside the redirect switch
// (e.g., "TodoWrite") goes through to the no-redirect branch. The
// daemon-check is irrelevant when the tool name isn't in the switch.
func TestRunHook_UnknownToolName(t *testing.T) {
	stuffStdin(t, `{"tool_name":"TodoWrite","tool_input":{}}`)
	out := captureStdout(t, func() {
		if code := runHook([]string{"claude-code", "pretooluse"}); code != 0 {
			t.Errorf("code: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"block":false`) {
		t.Errorf("output: want block:false, got %q", out)
	}
}

// TestRunHook_KnownToolNoDfmtProject: a redirectable tool (Bash) with
// stdin but in a cwd that has no .dfmt/ — shouldRedirect's
// DaemonRunning probe returns false, so the no-block path is taken.
// This is the most common runtime state for projects that haven't yet
// run `dfmt init`.
func TestRunHook_KnownToolNoDfmtProject(t *testing.T) {
	t.Chdir(t.TempDir())
	stuffStdin(t, `{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
	out := captureStdout(t, func() {
		if code := runHook([]string{"claude-code", "pretooluse"}); code != 0 {
			t.Errorf("code: want 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"block":false`) {
		t.Errorf("output: want block:false, got %q", out)
	}
}

// TestRunStop_HelpFlag covers the --help short-circuit.
func TestRunStop_HelpFlag(t *testing.T) {
	if code := runStop([]string{"--help"}); code != 0 {
		t.Errorf("want 0, got %d", code)
	}
}

// TestRunStop_UnknownFlag covers the flag-parse error path.
func TestRunStop_UnknownFlag(t *testing.T) {
	if code := runStop([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

// TestRunStop_PositionalArgs covers the explicit rejection of
// positional args. Stop shouldn't take any — the rejection short-
// circuits before we touch any state.
func TestRunStop_PositionalArgs(t *testing.T) {
	if code := runStop([]string{"foo"}); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

// TestRunStop_NoDaemonRunning exercises the legacy per-project branch
// when there's no daemon at all. Result: "Daemon not running" + exit 0
// (idempotent — stop is supposed to be safe to call multiple times).
func TestRunStop_NoDaemonRunning(t *testing.T) {
	withIsolatedGlobalDir(t) // empties global, isolates cwd
	out := captureStdout(t, func() {
		if code := runStop(nil); code != 0 {
			t.Errorf("want 0, got %d", code)
		}
	})
	if !strings.Contains(out, "not running") {
		t.Errorf("expected 'not running' in output, got %q", out)
	}
}

// TestStopGlobalDaemon_MissingPIDFile: when globalDashboardURL claims a
// daemon is reachable but ~/.dfmt/daemon.pid is gone, stopGlobalDaemon
// surfaces an actionable error rather than pretending it stopped.
// We exercise this by writing a port file (gets globalDashboardURL to
// return non-empty) without a corresponding PID file.
func TestStopGlobalDaemon_MissingPIDFile(t *testing.T) {
	dir := withIsolatedGlobalDir(t)
	// No PID file written → readGlobalDaemonPID returns 0 → error branch.
	if err := os.WriteFile(
		filepath.Join(dir, project.GlobalPortFileName),
		[]byte("12345\n"),
		0o600,
	); err != nil {
		t.Fatalf("write port: %v", err)
	}
	if code := stopGlobalDaemon(); code != 1 {
		t.Errorf("want 1 (error code), got %d", code)
	}
}

// TestRunDaemon_HelpFlag covers --help.
func TestRunDaemon_HelpFlag(t *testing.T) {
	if code := runDaemon([]string{"--help"}); code != 0 {
		t.Errorf("want 0, got %d", code)
	}
}

// TestRunDaemon_UnknownFlag covers the flag-parse error path.
func TestRunDaemon_UnknownFlag(t *testing.T) {
	if code := runDaemon([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("want 2, got %d", code)
	}
}

// TestRunDaemon_AlreadyStuck: a PID file pointing at this test
// process (alive but obviously not listening as a daemon) makes
// inspectGlobalDaemon classify as Stuck. runDaemon must report the
// actionable error and return 1 instead of spawning a sibling.
func TestRunDaemon_AlreadyStuck(t *testing.T) {
	dir := withIsolatedGlobalDir(t)
	if err := os.WriteFile(
		filepath.Join(dir, project.GlobalPIDFileName),
		[]byte("12345"+"\n"),
		0o600,
	); err != nil {
		// note: using a strconv would clash with this test file's imports;
		// the test process's PID is captured via os.Getpid below.
		t.Fatalf("setup: %v", err)
	}
	// Overwrite with this process's PID so inspectGlobalDaemon's
	// liveness probe returns alive.
	pidPath := filepath.Join(dir, project.GlobalPIDFileName)
	if err := os.WriteFile(pidPath, []byte(itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if code := runDaemon(nil); code != 1 {
		t.Errorf("want 1 (stuck error), got %d", code)
	}
}

// TestSamePathCLI_PlatformAwareCompare: byte-equal on POSIX,
// case-insensitive on Windows. The helper deliberately does NOT
// normalize separators or trailing slashes — callers pass already-
// cleaned paths.
func TestSamePathCLI_PlatformAwareCompare(t *testing.T) {
	if !samePathCLI("/a/b/c", "/a/b/c") {
		t.Error("identical paths: want true")
	}
	if samePathCLI("/a/b", "/a/c") {
		t.Error("different paths: want false")
	}
	if !samePathCLI("", "") {
		t.Error("empty/empty: want true")
	}
	// Case sensitivity is platform-specific; the helper inherits
	// runtime.GOOS.
	gotMixed := samePathCLI("/A/B/c", "/a/b/c")
	if runtime.GOOS == "windows" && !gotMixed {
		t.Error("windows: mixed case should match")
	}
	if runtime.GOOS != "windows" && gotMixed {
		t.Error("posix: mixed case should not match")
	}
}

// TestReadPIDFile_NotExist: a missing path returns 0 — callers treat
// "no file" and "bad PID" identically (both mean "no actionable PID").
func TestReadPIDFile_NotExist(t *testing.T) {
	dir := t.TempDir()
	if pid := readPIDFile(filepath.Join(dir, "no-such-pid")); pid != 0 {
		t.Errorf("missing file: want 0, got %d", pid)
	}
}

// TestReadPIDFile_Valid: a well-formed PID file returns the integer.
func TestReadPIDFile_Valid(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	if err := os.WriteFile(pidPath, []byte("12345\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if pid := readPIDFile(pidPath); pid != 12345 {
		t.Errorf("want 12345, got %d", pid)
	}
}

// TestReadPIDFile_Malformed: Sscanf returns zero on a non-numeric body
// — same shape as the no-file path, callers can't distinguish.
func TestReadPIDFile_Malformed(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if pid := readPIDFile(pidPath); pid != 0 {
		t.Errorf("want 0 for malformed, got %d", pid)
	}
}

// TestReadPIDFile_Empty: an empty file also yields 0.
func TestReadPIDFile_Empty(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	if err := os.WriteFile(pidPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if pid := readPIDFile(pidPath); pid != 0 {
		t.Errorf("want 0 for empty, got %d", pid)
	}
}

// TestWaitForExit_TimeoutOnLiveDaemonStub: when client.DaemonRunning
// keeps returning true (our isolated dir has no daemon but a real
// daemon may be reachable through some other channel), the function
// must return on timeout rather than hang. We use a short timeout so
// the test stays fast; the actual elapsed time is bounded by the poll
// cadence (100ms) and the timeout value.
func TestWaitForExit_TimeoutOnLiveDaemonStub(t *testing.T) {
	dir := withIsolatedGlobalDir(t)
	start := time.Now()
	waitForExit(0, 200*time.Millisecond, dir)
	elapsed := time.Since(start)
	// Either the daemon was never running (returns immediately) or the
	// timeout caps the wait. Both are valid; just confirm we didn't
	// hang forever.
	if elapsed > 2*time.Second {
		t.Errorf("waitForExit took too long: %v", elapsed)
	}
}

// TestWaitForGlobalExit_TimeoutOnLivePID: passes this test process's
// PID — which IS alive — so the function polls until the deadline. A
// 200ms timeout caps the wait so the test stays fast.
func TestWaitForGlobalExit_TimeoutOnLivePID(t *testing.T) {
	start := time.Now()
	waitForGlobalExit(os.Getpid(), 200*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Errorf("waitForGlobalExit returned too early: %v (live PID should poll)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitForGlobalExit hung: %v", elapsed)
	}
}

// TestWaitForGlobalExit_ImmediateOnDeadPID: a PID that's already
// unreachable (we use a deliberately invalid PID) returns immediately
// without waiting the full timeout.
func TestWaitForGlobalExit_ImmediateOnDeadPID(t *testing.T) {
	start := time.Now()
	// PID 0 is reserved on every supported OS — isProcessRunning treats
	// it as "not running" rather than racing against an actual process.
	waitForGlobalExit(0, 5*time.Second)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("waitForGlobalExit on PID 0: want immediate, took %v", elapsed)
	}
}

// TestStripGlobalFlags_NoOpWhenAbsent: input without any global flag
// returns the slice unchanged — the function short-circuits on the
// first pass to avoid an unnecessary allocation.
func TestStripGlobalFlags_NoOpWhenAbsent(t *testing.T) {
	in := []string{"sub", "--other", "value", "arg"}
	out := stripGlobalFlags(in)
	if len(out) != len(in) {
		t.Errorf("len differs: want %d, got %d", len(in), len(out))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("out[%d]: want %q, got %q", i, in[i], out[i])
		}
	}
}

// TestStripGlobalFlags_RemovesJSON: --json (and -json) are pulled out
// and flagJSON is set. The remaining args go to the subcommand.
func TestStripGlobalFlags_RemovesJSON(t *testing.T) {
	prevJSON := flagJSON
	t.Cleanup(func() { flagJSON = prevJSON })
	flagJSON = false

	out := stripGlobalFlags([]string{"--json", "sub", "arg"})
	if !flagJSON {
		t.Error("flagJSON should be set")
	}
	want := []string{"sub", "arg"}
	if len(out) != len(want) || out[0] != want[0] || out[1] != want[1] {
		t.Errorf("out: want %v, got %v", want, out)
	}
}

// TestStripGlobalFlags_RemovesProjectWithValue: --project takes the
// next positional and stores it on flagProject + DFMT_PROJECT.
func TestStripGlobalFlags_RemovesProjectWithValue(t *testing.T) {
	prevProject := flagProject
	prevEnv, hadEnv := os.LookupEnv("DFMT_PROJECT")
	t.Cleanup(func() {
		flagProject = prevProject
		if hadEnv {
			os.Setenv("DFMT_PROJECT", prevEnv)
		} else {
			os.Unsetenv("DFMT_PROJECT")
		}
	})
	flagProject = ""
	os.Unsetenv("DFMT_PROJECT")

	out := stripGlobalFlags([]string{"--project", "/tmp/foo", "sub"})
	if flagProject != "/tmp/foo" {
		t.Errorf("flagProject: want /tmp/foo, got %q", flagProject)
	}
	if env := os.Getenv("DFMT_PROJECT"); env != "/tmp/foo" {
		t.Errorf("DFMT_PROJECT env: want /tmp/foo, got %q", env)
	}
	if len(out) != 1 || out[0] != "sub" {
		t.Errorf("out: want [sub], got %v", out)
	}
}

// TestStripGlobalFlags_ProjectDanglingNoValue: when --project is the
// last token (no value to consume), the function leaves flagProject
// unchanged and silently drops the flag. This is a defensive case
// for malformed argv — we'd rather lose the flag than crash.
func TestStripGlobalFlags_ProjectDanglingNoValue(t *testing.T) {
	prevProject := flagProject
	t.Cleanup(func() { flagProject = prevProject })
	flagProject = "before"

	_ = stripGlobalFlags([]string{"--project"})
	if flagProject != "before" {
		t.Errorf("flagProject mutated: want 'before', got %q", flagProject)
	}
}

// TestIsTestBinary_ReturnsTrue confirms the test binary detection
// works in this test context — the function is used by acquireBackend
// to gate the in-process promotion fallback, and if it ever returns
// false from tests we'd start spawning sibling daemons.
func TestIsTestBinary_ReturnsTrue(t *testing.T) {
	if !isTestBinary() {
		t.Error("isTestBinary should be true under go test")
	}
}

// itoa avoids pulling strconv into the test file just for one digit-
// to-string. fmt.Sprint would also work but allocates a buffer; we
// have at most a few-digit number so manual is fine.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
