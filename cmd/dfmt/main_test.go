package main

import (
	"os"
	"testing"

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
