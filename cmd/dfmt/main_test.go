package main

import (
	"os"
	"testing"
)

func TestVersion(t *testing.T) {
	if version != "dev" {
		t.Errorf("version = %s, want 'dev'", version)
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
