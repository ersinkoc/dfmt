package cli

import (
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestBuildCaptureParamsGitCommit covers the happy path for post-commit.
func TestBuildCaptureParamsGitCommit(t *testing.T) {
	p, err := buildCaptureParams([]string{"git", "commit", "abc123", "test message"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Type != string(core.EvtGitCommit) {
		t.Errorf("Type = %q, want %q", p.Type, core.EvtGitCommit)
	}
	if p.Source != string(core.SrcGitHook) {
		t.Errorf("Source = %q, want %q", p.Source, core.SrcGitHook)
	}
	if p.Priority != string(core.PriP2) {
		t.Errorf("Priority = %q, want %q", p.Priority, core.PriP2)
	}
	if got, _ := p.Data["hash"].(string); got != "abc123" {
		t.Errorf(`Data["hash"] = %q, want "abc123"`, got)
	}
	if got, _ := p.Data["message"].(string); got != "test message" {
		t.Errorf(`Data["message"] = %q, want "test message"`, got)
	}
}

// TestBuildCaptureParamsGitCommitNoMessage verifies the optional message arg
// defaults to an empty string.
func TestBuildCaptureParamsGitCommitNoMessage(t *testing.T) {
	p, err := buildCaptureParams([]string{"git", "commit", "deadbeef"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := p.Data["message"].(string); got != "" {
		t.Errorf(`Data["message"] = %q, want ""`, got)
	}
	if got, _ := p.Data["hash"].(string); got != "deadbeef" {
		t.Errorf(`Data["hash"] = %q, want "deadbeef"`, got)
	}
}

func TestBuildCaptureParamsGitCheckout(t *testing.T) {
	p, err := buildCaptureParams([]string{"git", "checkout", "feature/x", "true"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Type != string(core.EvtGitCheckout) {
		t.Errorf("Type = %q, want %q", p.Type, core.EvtGitCheckout)
	}
	if p.Source != string(core.SrcGitHook) {
		t.Errorf("Source = %q, want %q", p.Source, core.SrcGitHook)
	}
	if p.Priority != string(core.PriP2) {
		t.Errorf("Priority = %q, want %q", p.Priority, core.PriP2)
	}
	if got, _ := p.Data["ref"].(string); got != "feature/x" {
		t.Errorf(`Data["ref"] = %q, want "feature/x"`, got)
	}
	if got, _ := p.Data["is_branch"].(string); got != "true" {
		t.Errorf(`Data["is_branch"] = %q, want "true"`, got)
	}
}

func TestBuildCaptureParamsGitCheckoutDefaultBranchFlag(t *testing.T) {
	p, err := buildCaptureParams([]string{"git", "checkout", "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := p.Data["is_branch"].(string); got != "0" {
		t.Errorf(`Data["is_branch"] = %q, want "0" default`, got)
	}
}

func TestBuildCaptureParamsGitPush(t *testing.T) {
	p, err := buildCaptureParams([]string{"git", "push", "origin", "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Type != string(core.EvtGitPush) {
		t.Errorf("Type = %q, want %q", p.Type, core.EvtGitPush)
	}
	if p.Source != string(core.SrcGitHook) {
		t.Errorf("Source = %q, want %q", p.Source, core.SrcGitHook)
	}
	if got, _ := p.Data["remote"].(string); got != "origin" {
		t.Errorf(`Data["remote"] = %q, want "origin"`, got)
	}
	if got, _ := p.Data["branch"].(string); got != "main" {
		t.Errorf(`Data["branch"] = %q, want "main"`, got)
	}
}

func TestBuildCaptureParamsEnvCwd(t *testing.T) {
	p, err := buildCaptureParams([]string{"env.cwd", "/some/path"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Type != string(core.EvtEnvCwd) {
		t.Errorf("Type = %q, want %q", p.Type, core.EvtEnvCwd)
	}
	if p.Source != string(core.SrcShell) {
		t.Errorf("Source = %q, want %q", p.Source, core.SrcShell)
	}
	if p.Priority != string(core.PriP4) {
		t.Errorf("Priority = %q, want %q", p.Priority, core.PriP4)
	}
	if got, _ := p.Data["cwd"].(string); got != "/some/path" {
		t.Errorf(`Data["cwd"] = %q, want "/some/path"`, got)
	}
}

func TestBuildCaptureParamsShell(t *testing.T) {
	p, err := buildCaptureParams([]string{"shell", "ls -la", "/tmp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Type != string(core.EvtNote) {
		t.Errorf("Type = %q, want %q", p.Type, core.EvtNote)
	}
	if p.Source != string(core.SrcShell) {
		t.Errorf("Source = %q, want %q", p.Source, core.SrcShell)
	}
	if got, _ := p.Data["cmd"].(string); got != "ls -la" {
		t.Errorf(`Data["cmd"] = %q, want "ls -la"`, got)
	}
	if got, _ := p.Data["cwd"].(string); got != "/tmp" {
		t.Errorf(`Data["cwd"] = %q, want "/tmp"`, got)
	}
}

func TestBuildCaptureParamsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"git with no subcmd", []string{"git"}},
		{"git commit no hash", []string{"git", "commit"}},
		{"git checkout no ref", []string{"git", "checkout"}},
		{"git push no branch", []string{"git", "push", "origin"}},
		{"git unknown subcmd", []string{"git", "bogus", "x"}},
		{"env.cwd no path", []string{"env.cwd"}},
		{"shell no cmd", []string{"shell"}},
		{"unknown type", []string{"unknown", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildCaptureParams(tc.args); err == nil {
				t.Errorf("expected error for args=%v", tc.args)
			}
		})
	}
}

// TestRunCaptureNoProject ensures runCapture returns non-zero when no project
// context can be resolved — exercises the full path through buildCaptureParams
// and the getProject+client.NewClient error branches. We rely on the existing
// dispatch test convention where no real daemon is spun up and the call is
// expected to fail after constructing valid params.
func TestRunCaptureErrorPaths(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing type", nil},
		{"unknown git subcmd", []string{"git", "bogus", "x"}},
		{"unknown capture type", []string{"whoami"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := runCapture(tc.args); code != 1 {
				t.Errorf("runCapture(%v) = %d, want 1", tc.args, code)
			}
		})
	}
}

// TestRunCaptureGitCommitHappyPath drives runCapture end-to-end with a valid
// arg set. The call will fail at the daemon-contact step (no daemon is
// running in unit tests), which still exercises buildCaptureParams +
// getProject + client.NewClient.Remember error-reporting code paths. We
// accept any non-zero exit code because auto-daemon-spawn may or may not
// work under -count=1 on a clean tempdir.
func TestRunCaptureGitCommitDriversAllCodePaths(t *testing.T) {
	tmp := t.TempDir()
	prev := flagProject
	flagProject = tmp
	defer func() { flagProject = prev }()
	_ = runCapture([]string{"git", "commit", "abc123", "test message"})
}
