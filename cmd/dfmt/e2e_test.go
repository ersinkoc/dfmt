//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Unix only: the detached-daemon lifecycle and the signal-0 liveness probe
// below are POSIX.
//
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
	bin := filepath.Join(t.TempDir(), "dfmt")
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go not on PATH: %v", err)
	}
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
	t      *testing.T
	bin    string
	dir    string
	global string
	env    []string
}

func newE2EEnv(t *testing.T) *e2eEnv {
	bin := buildDFMT(t)
	home := t.TempDir()
	e := &e2eEnv{t: t, bin: bin, dir: t.TempDir(), global: filepath.Join(home, ".dfmt")}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "DFMT_") {
			continue
		}
		e.env = append(e.env, kv)
	}
	e.env = append(e.env, "HOME="+home, "DFMT_GLOBAL_DIR="+e.global)
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

// TestE2E_IdleDaemonExits: the idle monitor stopped the daemon but the
// foreground loop waited only for a signal, so the process stayed resident
// forever with no listener — unused daemons never went away. And once that
// was fixed, the process exited mid-Stop (Done closed at Stop's start) and
// left daemon.pid / daemon.json behind.
func TestE2E_IdleDaemonExits(t *testing.T) {
	e := newE2EEnv(t)
	if err := os.MkdirAll(e.global, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "version: 1\nlifecycle:\n    idle_timeout: 2s\n"
	if err := os.WriteFile(filepath.Join(e.global, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustRun("init")
	e.mustRun("note", "keep the daemon busy once")

	raw, err := os.ReadFile(filepath.Join(e.global, "daemon.pid"))
	if err != nil {
		t.Fatalf("no daemon.pid after a daemon-backed command: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", raw, err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("daemon %d still running 15 s after a 2 s idle timeout", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, name := range []string{"daemon.pid", "daemon.json", "port"} {
		if _, err := os.Stat(filepath.Join(e.global, name)); err == nil {
			t.Errorf("%s left behind by an idle-exited daemon", name)
		}
	}
}

// processAlive reports whether pid exists (signal 0 probes without
// delivering anything). Unix only, like the tests that call it.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
