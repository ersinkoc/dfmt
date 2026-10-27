package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/daemon"
)

// toolProject makes an isolated project and global dir with a global daemon
// running in-process, so the CLI commands under test can never reach the
// developer's live daemon.
func toolProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", filepath.Join(root, "global"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "a.go"), []byte("package a\n// needle here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev, prevJSON := flagProject, flagJSON
	flagProject = proj
	// Other tests in this package run Dispatch with --json, which sets the
	// global and never clears it; start from text mode regardless of order.
	flagJSON = false
	t.Cleanup(func() { flagProject, flagJSON = prev, prevJSON })
	t.Chdir(proj)

	// The test binary never spawns a daemon (acquireBackend refuses to
	// fork it), so run a real global daemon in-process for the CLI to
	// connect to.
	d, err := daemon.NewGlobal(config.Default())
	if err != nil {
		t.Fatalf("NewGlobal: %v", err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	return proj
}

// The CLI tool subcommands' success paths had no coverage — every existing
// test asserted the no-daemon failure. These run each command end to end
// through the in-process backend against a real sandbox.
func TestCLIToolRoundTrip(t *testing.T) {
	proj := toolProject(t)

	out := captureStdout(t, func() {
		if code := Dispatch([]string{"glob", "*.go"}); code != 0 {
			t.Errorf("glob returned %d", code)
		}
	})
	if !strings.Contains(out, "a.go") {
		t.Errorf("glob output %q lacks a.go", out)
	}

	out = captureStdout(t, func() {
		if code := Dispatch([]string{"grep", "needle"}); code != 0 {
			t.Errorf("grep returned %d", code)
		}
	})
	if !strings.Contains(out, "needle") {
		t.Errorf("grep output %q lacks the match", out)
	}

	if code := Dispatch([]string{"write", "b.txt", "hello world\n"}); code != 0 {
		t.Fatalf("write returned %d", code)
	}
	if data, _ := os.ReadFile(filepath.Join(proj, "b.txt")); string(data) != "hello world\n" {
		t.Fatalf("write produced %q", data)
	}

	// Explicit truncation, stdin, and ambiguous sources.
	if code := Dispatch([]string{"write", "-content", "", "empty.txt"}); code != 0 {
		t.Errorf("explicit -content \"\" returned %d", code)
	}
	if code := Dispatch([]string{"write", "-content", "x", "b.txt", "y"}); code != 2 {
		t.Errorf("two content sources returned %d, want 2", code)
	}
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("from stdin\n")
	_ = w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	code := Dispatch([]string{"write", "c.txt", "-"})
	os.Stdin = origStdin
	if data, _ := os.ReadFile(filepath.Join(proj, "c.txt")); code != 0 || string(data) != "from stdin\n" {
		t.Errorf("write - : code %d, content %q", code, data)
	}

	if code := Dispatch([]string{"edit", "-old", "world", "b.txt", "there"}); code != 0 {
		t.Errorf("edit returned %d", code)
	}
	if data, _ := os.ReadFile(filepath.Join(proj, "b.txt")); string(data) != "hello there\n" {
		t.Errorf("edit produced %q", data)
	}

	out = captureStdout(t, func() {
		if code := Dispatch([]string{"stats"}); code != 0 {
			t.Errorf("stats returned %d", code)
		}
	})
	if out == "" {
		t.Error("stats printed nothing")
	}

	// JSON mode for the same commands.
	prevJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = prevJSON }()
	out = captureStdout(t, func() { _ = Dispatch([]string{"glob", "*.go"}) })
	if !strings.Contains(out, `"files"`) {
		t.Errorf("glob --json output %q", out)
	}
	out = captureStdout(t, func() { _ = Dispatch([]string{"grep", "needle"}) })
	if !strings.Contains(out, `"matches"`) {
		t.Errorf("grep --json output %q", out)
	}
	out = captureStdout(t, func() { _ = Dispatch([]string{"stats"}) })
	if !strings.Contains(out, `"events_total"`) {
		t.Errorf("stats --json output %q", out)
	}
}

// TestCLIMemoryRoundTrip drives the memory commands end to end: a note
// written with remember must come back through search, recall and tail.
func TestCLIMemoryRoundTrip(t *testing.T) {
	toolProject(t)
	run := func(args ...string) (int, string) {
		var code int
		out := captureStdout(t, func() { code = Dispatch(args) })
		return code, out
	}

	if code, _ := run("remember", "-tags", "decision", "-data", `{"message":"chose the zebra plan"}`,
		"-input-tokens", "10", "-output-tokens", "5", "-cached-tokens", "2", "-model", "m"); code != 0 {
		t.Fatalf("remember returned %d", code)
	}
	if code, out := run("search", "-type", "note", "zebra"); code != 0 || !strings.Contains(out, "zebra") {
		t.Errorf("search: code %d out %q", code, out)
	}
	if code, out := run("recall", "-budget", "8192"); code != 0 || !strings.Contains(out, "zebra") {
		t.Errorf("recall md: code %d out %q", code, out)
	}
	for _, f := range []string{"json", "xml"} {
		if code, out := run("recall", "-format", f); code != 0 || !strings.Contains(out, "zebra") {
			t.Errorf("recall %s: code %d", f, code)
		}
	}
	// tail used to print "[p3] <time> #decision" for this note: no type, no
	// text, and the stored priority instead of the classified one.
	if code, out := run("tail", "-limit", "5"); code != 0 || !strings.Contains(out, "zebra") ||
		!strings.Contains(out, "[p2]") || !strings.Contains(out, " note ") {
		t.Errorf("tail: code %d out %q", code, out)
	}
	if code, out := run("stats"); code != 0 || out == "" {
		t.Errorf("stats: code %d", code)
	}
	if code, _ := run("task", "write the docs"); code != 0 {
		t.Errorf("task returned %d", code)
	}
	if code, out := run("read", "a.go"); code != 0 || !strings.Contains(out, "needle") {
		t.Errorf("read: code %d out %q", code, out)
	}
	if code, out := run("exec", "-intent", "listing", "ls"); code != 0 || !strings.Contains(out, "a.go") {
		t.Errorf("exec: code %d out %q", code, out)
	}

	prevJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = prevJSON }()
	for _, args := range [][]string{{"search", "zebra"}, {"read", "a.go"}, {"exec", "ls"}, {"tail", "-limit", "1"}} {
		if code, out := run(args...); code != 0 || !strings.Contains(out, "{") {
			t.Errorf("%v --json: code %d out %q", args, code, out)
		}
	}
}

// TestCLIOpsAgainstLiveDaemon covers the operator commands' connected
// paths — status, list, doctor, dashboard, config — which existing tests
// only reached with no daemon running.
func TestCLIOpsAgainstLiveDaemon(t *testing.T) {
	toolProject(t)
	run := func(args ...string) (int, string) {
		var code int
		out := captureStdout(t, func() { code = Dispatch(args) })
		return code, out
	}
	if code, out := run("status"); code != 0 || !strings.Contains(out, "running") {
		t.Errorf("status: code %d out %q", code, out)
	}
	if code, out := run("list"); code != 0 || out == "" {
		t.Errorf("list: code %d out %q", code, out)
	}
	if code, out := run("dashboard"); code != 0 || !strings.Contains(out, "http") {
		t.Errorf("dashboard: code %d out %q", code, out)
	}
	if _, out := run("doctor"); !strings.Contains(strings.ToLower(out), "daemon") {
		t.Errorf("doctor output lacks the daemon row: %q", out)
	}
	if code, _ := run("config", "set", "storage.journal_keep_segments", "3"); code != 0 {
		t.Errorf("config set returned %d", code)
	}
	if code, out := run("config", "get", "storage.journal_keep_segments"); code != 0 || !strings.Contains(out, "3") {
		t.Errorf("config get: code %d out %q", code, out)
	}
	if code, _ := run("config", "set", "storage.journal_keep_segments", "-1"); code == 0 {
		t.Error("config set accepted a negative keep count")
	}

	prevJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = prevJSON }()
	for _, args := range [][]string{{"status"}, {"list"}} {
		if code, out := run(args...); code != 0 || !strings.Contains(out, "{") {
			t.Errorf("%v --json: code %d out %q", args, code, out)
		}
	}
}

// dialBackend("") is the unbound-session path (ADR-0029): it must reach the
// global daemon without auto-initializing any directory.
func TestDialBackendBoundAndUnbound(t *testing.T) {
	proj := toolProject(t)
	if dialBackend("") == nil {
		t.Error("unbound dial found no backend with a daemon running")
	}
	if dialBackend(proj) == nil {
		t.Error("bound dial found no backend")
	}
}

// A fetch the sandbox refuses (loopback is never reachable) must fail with
// a non-zero exit, through the real daemon path; plus the flag errors.
func TestCLIFetchRefusalAndArgs(t *testing.T) {
	toolProject(t)
	if code := Dispatch([]string{"fetch"}); code != 1 {
		t.Errorf("fetch without URL returned %d, want 1", code)
	}
	if code := Dispatch([]string{"fetch", "--bogus"}); code != 2 {
		t.Errorf("fetch with a bad flag returned %d, want 2", code)
	}
	if code := Dispatch([]string{"fetch", "-intent", "x", "-method", "POST", "-body", "{}", "-timeout", "2",
		"http://127.0.0.1:1/"}); code == 0 {
		t.Error("fetch to loopback succeeded; the sandbox must refuse it")
	}
}

func TestToolchainCandidateDirs(t *testing.T) {
	dirs := toolchainCandidateDirs()
	if len(dirs) == 0 {
		t.Fatal("no candidate toolchain directories")
	}
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			t.Errorf("candidate %q is not absolute", d)
		}
	}
}

// With no daemon reachable (the test binary never spawns one), dashboard
// falls back to the legacy registry and, for a Unix-socket project, explains
// why there is no browser URL instead of printing a dead one.
func TestCLIDashboardWithoutDaemon(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", filepath.Join(root, "global"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev, prevJSON := flagProject, flagJSON
	flagProject, flagJSON = proj, false
	t.Cleanup(func() { flagProject, flagJSON = prev, prevJSON })

	// The legacy registry is process-wide and other tests leave rows in it
	// (on Windows it is not under the dirs isolated above), so assert only
	// about THIS project: no URL may be offered for it.
	out := captureStdout(t, func() { _ = Dispatch([]string{"dashboard"}) })
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "http") && strings.Contains(line, proj) {
			t.Errorf("dashboard offered a URL for a project with no daemon: %q", line)
		}
	}
	if lookupDashboardURL(proj) != "" {
		t.Error("lookupDashboardURL found a URL with no daemon")
	}
}
