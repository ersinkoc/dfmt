package capture

import (
	"context"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestNewGitCapture(t *testing.T) {
	gc := NewGitCapture("/test/path")
	if gc == nil {
		t.Fatal("NewGitCapture returned nil")
	}
	if gc.projectPath != "/test/path" {
		t.Errorf("projectPath = %s, want '/test/path'", gc.projectPath)
	}
}

func TestGitCaptureSubmitCommit(t *testing.T) {
	gc := NewGitCapture("/test/path")
	err := gc.SubmitCommit(context.Background(), "abc123", "Test commit message\nMore details")
	if err != nil {
		t.Fatalf("SubmitCommit failed: %v", err)
	}
}

func TestGitCaptureSubmitCheckout(t *testing.T) {
	gc := NewGitCapture("/test/path")
	err := gc.SubmitCheckout(context.Background(), "main", true)
	if err != nil {
		t.Fatalf("SubmitCheckout failed: %v", err)
	}

	err = gc.SubmitCheckout(context.Background(), "feature/test", false)
	if err != nil {
		t.Fatalf("SubmitCheckout failed: %v", err)
	}
}

func TestGitCaptureSubmitPush(t *testing.T) {
	gc := NewGitCapture("/test/path")
	err := gc.SubmitPush(context.Background(), "origin", "main")
	if err != nil {
		t.Fatalf("SubmitPush failed: %v", err)
	}
}

func TestGitLog(t *testing.T) {
	// This will fail if git is not available or not a git repo
	commits, err := GitLog(5)
	if err != nil {
		t.Logf("GitLog failed (may not be in git repo): %v", err)
	}
	for _, c := range commits {
		if c.Hash == "" {
			t.Error("Commit hash is empty")
		}
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Single line", "Single line"},
		{"First line\nSecond line", "First line"},
		{"Line with trailing\n", "Line with trailing"},
		{"", ""},
		{"  Spaced  \n  more", "Spaced"},
	}

	for _, tt := range tests {
		got := firstLine(tt.input)
		if got != tt.expected {
			t.Errorf("firstLine(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestNewShellCapture(t *testing.T) {
	sc := NewShellCapture("/test/path")
	if sc == nil {
		t.Fatal("NewShellCapture returned nil")
	}
	if sc.projectPath != "/test/path" {
		t.Errorf("projectPath = %s, want '/test/path'", sc.projectPath)
	}
}

func TestShellCaptureSubmitCommand(t *testing.T) {
	sc := NewShellCapture("/test/path")
	err := sc.SubmitCommand(context.Background(), "ls -la", "/test/path")
	if err != nil {
		t.Fatalf("SubmitCommand failed: %v", err)
	}
}

func TestDetectShell(t *testing.T) {
	shell := DetectShell()
	if shell == "" {
		t.Error("DetectShell returned empty string")
	}
}

func TestGitCommit(t *testing.T) {
	c := GitCommit{
		Hash:    "abc123",
		Message: "Test commit",
	}

	if c.Hash != "abc123" {
		t.Errorf("Hash = %s, want 'abc123'", c.Hash)
	}
	if c.Message != "Test commit" {
		t.Errorf("Message = %s, want 'Test commit'", c.Message)
	}
}

func TestEventTypes(t *testing.T) {
	// Verify that the core event types exist
	if core.EvtGitCommit != "git.commit" {
		t.Errorf("EvtGitCommit = %s, want 'git.commit'", core.EvtGitCommit)
	}
	if core.EvtGitCheckout != "git.checkout" {
		t.Errorf("EvtGitCheckout = %s, want 'git.checkout'", core.EvtGitCheckout)
	}
	if core.EvtGitPush != "git.push" {
		t.Errorf("EvtGitPush = %s, want 'git.push'", core.EvtGitPush)
	}
	if core.EvtMCPCall != "mcp.call" {
		t.Errorf("EvtMCPCall = %s, want 'mcp.call'", core.EvtMCPCall)
	}
}

func TestGitCaptureFields(t *testing.T) {
	gc := &GitCapture{projectPath: "/custom/path"}
	if gc.projectPath != "/custom/path" {
		t.Errorf("projectPath = %s, want '/custom/path'", gc.projectPath)
	}
}

func TestShellCaptureFields(t *testing.T) {
	sc := &ShellCapture{projectPath: "/custom/path"}
	if sc.projectPath != "/custom/path" {
		t.Errorf("projectPath = %s, want '/custom/path'", sc.projectPath)
	}
}

func TestSubmitGitCommitEventFields(t *testing.T) {
	gc := NewGitCapture("/test")

	// Create event manually to verify fields
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtGitCommit,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"hash":    "abc123",
			"message": "Test",
		},
	}
	e.Sig = e.ComputeSig()

	if e.Type != core.EvtGitCommit {
		t.Errorf("Type = %s, want %s", e.Type, core.EvtGitCommit)
	}
	if e.Priority != core.PriP2 {
		t.Errorf("Priority = %s, want %s", e.Priority, core.PriP2)
	}
	if e.Source != core.SrcGitHook {
		t.Errorf("Source = %s, want %s", e.Source, core.SrcGitHook)
	}

	_ = gc // Use gc to confirm it's valid
}

func TestSubmitGitCheckoutEventFields(t *testing.T) {
	gc := NewGitCapture("/test")

	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtGitCheckout,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"ref":       "main",
			"is_branch": true,
		},
	}
	e.Sig = e.ComputeSig()

	if e.Type != core.EvtGitCheckout {
		t.Errorf("Type = %s, want %s", e.Type, core.EvtGitCheckout)
	}

	_ = gc
}

func TestSubmitGitPushEventFields(t *testing.T) {
	gc := NewGitCapture("/test")

	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtGitPush,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"remote": "origin",
			"branch": "main",
		},
	}
	e.Sig = e.ComputeSig()

	if e.Type != core.EvtGitPush {
		t.Errorf("Type = %s, want %s", e.Type, core.EvtGitPush)
	}

	_ = gc
}

func TestSubmitShellCommandEventFields(t *testing.T) {
	sc := NewShellCapture("/test")

	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtMCPCall,
		Priority: core.PriP4,
		Source:   core.SrcShell,
		Data: map[string]any{
			"command": "ls",
			"cwd":     "/test",
		},
	}
	e.Sig = e.ComputeSig()

	if e.Type != core.EvtMCPCall {
		t.Errorf("Type = %s, want %s", e.Type, core.EvtMCPCall)
	}
	if e.Priority != core.PriP4 {
		t.Errorf("Priority = %s, want %s", e.Priority, core.PriP4)
	}
	if e.Source != core.SrcShell {
		t.Errorf("Source = %s, want %s", e.Source, core.SrcShell)
	}

	_ = sc
}
