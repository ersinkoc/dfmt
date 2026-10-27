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
	prev := flagProject
	flagProject = proj
	t.Cleanup(func() { flagProject = prev })
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
