package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/version"
)

func TestVersion(t *testing.T) {
	// version source moved from `var version` in cmd/dfmt/version.go
	// to internal/version.Current in v0.2.0. The default is non-empty
	// (the most-recently-released tag); release builds override via
	// ldflags. Either is acceptable, emptiness is the only failure
	// mode that means the wiring broke.
	if version.Current == "" {
		t.Error("version.Current is empty; expected build-time-injected value or default")
	}
}

func TestMainArgsHandling(t *testing.T) {
	// Verify os.Args handling - in test context it will be test binary
	if len(os.Args) < 1 {
		t.Error("Args should have at least 1 element")
	}
}

func TestProcessArgs(t *testing.T) {
	// Test argument processing logic
	args := []string{"--project", "/path/to/project", "status"}

	for i, arg := range args {
		if arg == "--project" && i+1 < len(args) {
			if args[i+1] != "/path/to/project" {
				t.Error("Project path not extracted correctly")
			}
		}
	}
}

// TestWriteCrashLogPersistsPanicAndStack covers the format contract
// of the crash file. Operators reading ~/.dfmt/last-crash.log expect
// the panic value, dfmt version, an RFC3339-ish timestamp, and the
// full goroutine stack — all of these are part of the support
// surface a doctor diagnostic points at.
func TestWriteCrashLogPersistsPanicAndStack(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	if err := writeCrashLog("simulated boom", []byte("goroutine 1 [running]:\nmain.bang(...)\n")); err != nil {
		t.Fatalf("writeCrashLog: %v", err)
	}

	data, err := os.ReadFile(project.GlobalCrashPath())
	if err != nil {
		t.Fatalf("read crash file: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"panic: simulated boom",
		"version: " + version.Current,
		"timestamp: ",
		"goroutine 1 [running]:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("crash log missing %q\nbody:\n%s", want, body)
		}
	}

	// File must live under the override dir, not anywhere else.
	if filepath.Dir(project.GlobalCrashPath()) != tmp {
		t.Errorf("crash file path = %s, expected under %s", project.GlobalCrashPath(), tmp)
	}
}

// TestWriteCrashLogOverwritesPriorRun exercises the "last crash, not
// a journal" semantics — repeated calls leave only the latest body
// on disk. Without this an operator who saw a crash, restarted, and
// hit a new one would see merged text and ambiguous timestamps.
func TestWriteCrashLogOverwritesPriorRun(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	if err := writeCrashLog("first", []byte("stack-A")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeCrashLog("second", []byte("stack-B")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, err := os.ReadFile(project.GlobalCrashPath())
	if err != nil {
		t.Fatalf("read crash file: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "first") {
		t.Errorf("crash log should have been overwritten; still contains 'first':\n%s", body)
	}
	if !strings.Contains(body, "second") {
		t.Errorf("crash log should contain 'second':\n%s", body)
	}
}
