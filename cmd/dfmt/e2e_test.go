package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tests in this file drive the built binary against an isolated home,
// across the process boundary that unit tests cannot cross: a unit test
// never spawns a detached daemon, so it cannot observe what happens when
// none is running — which is exactly when the defects below occurred.

// buildDFMT compiles this package into a temp dir and returns the binary.
func buildDFMT(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the binary; skipped in -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("detached-daemon lifecycle is exercised on Unix only")
	}
	bin := filepath.Join(t.TempDir(), "dfmt")
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	out, err := exec.Command(gobin, "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// e2eEnv runs bin in dir with HOME and DFMT_GLOBAL_DIR pointed at a fresh
// temp tree, so the test neither sees nor disturbs a real daemon. The
// daemon it spawns is stopped on cleanup.
type e2eEnv struct {
	t   *testing.T
	bin string
	dir string
	env []string
}

func newE2EEnv(t *testing.T) *e2eEnv {
	bin := buildDFMT(t)
	home := t.TempDir()
	e := &e2eEnv{t: t, bin: bin, dir: t.TempDir()}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "DFMT_") {
			continue
		}
		e.env = append(e.env, kv)
	}
	e.env = append(e.env, "HOME="+home, "DFMT_GLOBAL_DIR="+filepath.Join(home, ".dfmt"))
	t.Cleanup(func() { _, _ = e.run("stop") })
	return e
}

func (e *e2eEnv) run(args ...string) (string, error) {
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = e.dir
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *e2eEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("dfmt %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// TestE2E_CaptureStartsDaemon: git hooks run `dfmt capture … 2>/dev/null &`.
// capture used a client that never starts a daemon, so after an idle-exit
// every commit, checkout and push was dropped without a trace.
func TestE2E_CaptureStartsDaemon(t *testing.T) {
	e := newE2EEnv(t)
	e.mustRun("init")

	e.mustRun("capture", "git", "commit", "abc123", "e2e capture with no daemon")

	raw, err := os.ReadFile(filepath.Join(e.dir, ".dfmt", "journal.jsonl"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"git.commit"`) {
		t.Fatalf("capture with no daemon running recorded nothing:\n%s", raw)
	}
}

// TestE2E_SearchAfterRestart: after a daemon restart the project index was
// empty until a 3 s tail tick, and was never persisted, so the first
// searches after every restart returned nothing.
func TestE2E_SearchAfterRestart(t *testing.T) {
	e := newE2EEnv(t)
	e.mustRun("init")
	e.mustRun("note", "--tags", "decision", "zebracorn restart sentinel")
	e.mustRun("stop")

	if out := e.mustRun("search", "zebracorn"); !strings.Contains(out, "zebracorn restart sentinel") {
		t.Fatalf("first search after restart missed the note:\n%s", out)
	}
	e.mustRun("stop")
	if _, err := os.Stat(filepath.Join(e.dir, ".dfmt", "index.gob")); err != nil {
		t.Errorf("index not persisted on stop: %v", err)
	}
}
