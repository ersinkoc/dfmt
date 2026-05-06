package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
)

// TestRunDoctorReportsLastCrashWhenPresent seeds a crash log under
// $DFMT_GLOBAL_DIR and verifies the doctor diagnostic surfaces it
// without flipping the exit code red. The Phase 2 contract: a stale
// crash log is informational, not a doctor failure — operators want
// to see "yes a crash happened" without doctor's overall verdict
// turning into "broken" on every subsequent run forever.
func TestRunDoctorReportsLastCrashWhenPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	// Doctor expects a project to exist. Seed a minimal one.
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Write a crash log directly — recoverAndLogCrash isn't reachable
	// from a test goroutine because it lives in main; we exercise the
	// doctor-side render here.
	crashBody := []byte("timestamp: 2026-05-06T12:00:00Z\npanic: bang\n")
	if err := os.WriteFile(filepath.Join(tmp, "last-crash.log"), crashBody, 0o600); err != nil {
		t.Fatalf("seed crash log: %v", err)
	}

	out := captureStdoutCrashTest(t, func() {
		_ = runDoctor([]string{"--dir", proj})
	})

	if !strings.Contains(out, "Last crash") {
		t.Errorf("doctor output should include 'Last crash' row, got:\n%s", out)
	}
	if !strings.Contains(out, "present") {
		t.Errorf("doctor should describe crash log as 'present', got:\n%s", out)
	}
}

// TestRunDoctorReportsCleanWhenNoCrash is the negative case — a fresh
// install with no last-crash.log should report "(none — clean run)"
// rather than rendering an empty field.
func TestRunDoctorReportsCleanWhenNoCrash(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	out := captureStdoutCrashTest(t, func() {
		_ = runDoctor([]string{"--dir", proj})
	})

	if !strings.Contains(out, "Last crash") {
		t.Errorf("doctor output should include 'Last crash' row, got:\n%s", out)
	}
	if !strings.Contains(out, "clean run") {
		t.Errorf("doctor should report 'clean run' when no crash log, got:\n%s", out)
	}
}

// TestRunStatusJSONIncludesLastCrashWhenPresent verifies the JSON
// shape of `dfmt --json status` carries a structured last_crash
// entry with the path, modified-time, and size — what an operator
// or monitoring agent would want to alert on. Absence keeps the
// payload clean.
func TestRunStatusJSONIncludesLastCrashWhenPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Setenv("DFMT_PROJECT", proj)

	if err := os.WriteFile(filepath.Join(tmp, "last-crash.log"), []byte("body"), 0o600); err != nil {
		t.Fatalf("seed crash log: %v", err)
	}

	prev := flagJSON
	flagJSON = true
	defer func() { flagJSON = prev }()

	out := captureStdoutCrashTest(t, func() {
		_ = runStatus(nil)
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("status output not valid JSON: %v\n%s", err, out)
	}
	lc, ok := got["last_crash"].(map[string]any)
	if !ok {
		t.Fatalf("last_crash field missing or wrong shape: %#v", got["last_crash"])
	}
	if lc["path"] != project.GlobalCrashPath() {
		t.Errorf("last_crash.path = %v, want %s", lc["path"], project.GlobalCrashPath())
	}
	if _, perr := time.Parse(time.RFC3339, lc["modified"].(string)); perr != nil {
		t.Errorf("last_crash.modified = %v, not RFC3339: %v", lc["modified"], perr)
	}
	if size, ok := lc["size"].(float64); !ok || size != 4 {
		t.Errorf("last_crash.size = %v, want 4 (len of 'body')", lc["size"])
	}
}

// TestRunStatusJSONOmitsLastCrashWhenAbsent — the field must not
// appear at all when there's no crash log. JSON shape stability
// matters for any operator dashboard that consumes this output.
func TestRunStatusJSONOmitsLastCrashWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Setenv("DFMT_PROJECT", proj)

	prev := flagJSON
	flagJSON = true
	defer func() { flagJSON = prev }()

	out := captureStdoutCrashTest(t, func() {
		_ = runStatus(nil)
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("status output not valid JSON: %v\n%s", err, out)
	}
	if _, exists := got["last_crash"]; exists {
		t.Errorf("last_crash should be absent when no crash log; got %v", got["last_crash"])
	}
}

// captureStdoutCrashTest is a tiny stdout sink so we can assert on
// runDoctor / runStatus output without coupling tests to a global
// helper that other tests might reuse.
func captureStdoutCrashTest(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	fn()
	_ = w.Close()
	return string(<-done)
}
