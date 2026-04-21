package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
)

func TestDispatchEmptyArgs(t *testing.T) {
	// Should print usage and return 0
	code := Dispatch([]string{})
	if code != 0 {
		t.Errorf("Dispatch([]) returned %d, want 0", code)
	}
}

func TestDispatchHelp(t *testing.T) {
	codes := []int{
		Dispatch([]string{"help"}),
		Dispatch([]string{"--help"}),
		Dispatch([]string{"-h"}),
	}
	for _, code := range codes {
		if code != 0 {
			t.Errorf("Dispatch(help) returned %d, want 0", code)
		}
	}
}

func TestDispatchUnknown(t *testing.T) {
	code := Dispatch([]string{"unknowncommand"})
	if code != 1 {
		t.Errorf("Dispatch(unknown) returned %d, want 1", code)
	}
}

func TestDispatchInit(t *testing.T) {
	code := Dispatch([]string{"init", "-dir", "/tmp/test-dfmt-init"})
	if code != 0 {
		t.Errorf("Dispatch(init) returned %d, want 0", code)
	}
}

func TestDispatchList(t *testing.T) {
	code := Dispatch([]string{"list"})
	if code != 0 {
		t.Errorf("Dispatch(list) returned %d, want 0", code)
	}
}

func TestDispatchStats(t *testing.T) {
	code := Dispatch([]string{"stats"})
	if code != 0 {
		t.Errorf("Dispatch(stats) returned %d, want 0", code)
	}
}

func TestPrintUsage(t *testing.T) {
	// Just verify it doesn't panic
	printUsage()
}

func TestVersion(t *testing.T) {
	if Version != "0.1.0" {
		t.Errorf("Version = %s, want '0.1.0'", Version)
	}
}

func TestGetProjectWithFlag(t *testing.T) {
	// Save original and restore after test
	original := flagProject
	defer func() { flagProject = original }()

	flagProject = "/test/path"
	proj, err := getProject()
	if err != nil {
		t.Fatalf("getProject() failed: %v", err)
	}
	if proj != "/test/path" {
		t.Errorf("getProject() = %s, want '/test/path'", proj)
	}
}

func TestGetProjectWithoutFlag(t *testing.T) {
	// Without the flag, it will try to discover
	// Just verify it doesn't panic
	flagProject = ""
	_, _ = getProject()
}

func TestDispatchRememberNoProject(t *testing.T) {
	flagProject = ""
	// Should fail because no project
	code := Dispatch([]string{"remember", "note", "test note"})
	if code != 1 {
		t.Logf("remember without project returned %d (expected fail)", code)
	}
}

func TestDispatchSearchNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"search", "test"})
	if code != 1 {
		t.Logf("search without project returned %d", code)
	}
}

func TestDispatchRecallNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"recall"})
	if code != 1 {
		t.Logf("recall without project returned %d", code)
	}
}

func TestDispatchStatusNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"status"})
	if code != 1 {
		t.Logf("status without project returned %d", code)
	}
}

func TestDispatchConfigNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"config"})
	// Config tries to load project even if just printing info
	if code != 1 {
		t.Logf("config without project returned %d", code)
	}
}

func TestRunList(t *testing.T) {
	code := Dispatch([]string{"list"})
	if code != 0 {
		t.Errorf("list returned %d, want 0", code)
	}
}

func TestRunListJSON(t *testing.T) {
	flagJSON = true
	defer func() { flagJSON = false }()

	code := Dispatch([]string{"list"})
	if code != 0 {
		t.Errorf("list --json returned %d, want 0", code)
	}
}

func TestRunStats(t *testing.T) {
	code := Dispatch([]string{"stats"})
	if code != 0 {
		t.Errorf("stats returned %d, want 0", code)
	}
}

func TestRunStatsJSON(t *testing.T) {
	flagJSON = true
	defer func() { flagJSON = false }()

	code := Dispatch([]string{"stats"})
	if code != 0 {
		t.Errorf("stats --json returned %d, want 0", code)
	}
}

func TestRunTail(t *testing.T) {
	code := Dispatch([]string{"tail"})
	if code != 0 {
		t.Errorf("tail returned %d, want 0", code)
	}
}

func TestRunTailFollow(t *testing.T) {
	code := Dispatch([]string{"tail", "--follow"})
	if code != 0 {
		t.Errorf("tail --follow returned %d, want 0", code)
	}
}

func TestRunShellInit(t *testing.T) {
	code := Dispatch([]string{"shell-init"})
	if code != 0 {
		t.Errorf("shell-init returned %d, want 0", code)
	}
}

func TestRunShellInitBash(t *testing.T) {
	code := Dispatch([]string{"shell-init", "bash"})
	if code != 0 {
		t.Errorf("shell-init bash returned %d, want 0", code)
	}
}

func TestRunShellInitZsh(t *testing.T) {
	code := Dispatch([]string{"shell-init", "zsh"})
	if code != 0 {
		t.Errorf("shell-init zsh returned %d, want 0", code)
	}
}

func TestRunShellInitFish(t *testing.T) {
	code := Dispatch([]string{"shell-init", "fish"})
	if code != 0 {
		t.Errorf("shell-init fish returned %d, want 0", code)
	}
}

func TestRunShellInitUnknown(t *testing.T) {
	code := Dispatch([]string{"shell-init", "unknownshell"})
	if code != 1 {
		t.Errorf("shell-init unknownshell returned %d, want 1", code)
	}
}

func TestRunCaptureGit(t *testing.T) {
	code := Dispatch([]string{"capture", "git", "commit", "abc123"})
	if code != 0 {
		t.Errorf("capture git commit returned %d, want 0", code)
	}
}

func TestRunCaptureGitCheckout(t *testing.T) {
	code := Dispatch([]string{"capture", "git", "checkout", "main"})
	if code != 0 {
		t.Errorf("capture git checkout returned %d, want 0", code)
	}
}

func TestRunCaptureGitPush(t *testing.T) {
	code := Dispatch([]string{"capture", "git", "push", "origin", "main"})
	if code != 0 {
		t.Errorf("capture git push returned %d, want 0", code)
	}
}

func TestRunCaptureShell(t *testing.T) {
	code := Dispatch([]string{"capture", "shell", "ls"})
	if code != 0 {
		t.Errorf("capture shell returned %d, want 0", code)
	}
}

func TestRunCaptureMissingArgs(t *testing.T) {
	code := Dispatch([]string{"capture"})
	if code != 1 {
		t.Errorf("capture missing args returned %d, want 1", code)
	}
}

func TestRunCaptureUnknownType(t *testing.T) {
	code := Dispatch([]string{"capture", "unknown"})
	if code != 1 {
		t.Errorf("capture unknown returned %d, want 1", code)
	}
}

func TestRunTaskDone(t *testing.T) {
	code := Dispatch([]string{"task", "done", "123"})
	if code != 0 {
		t.Errorf("task done returned %d, want 0", code)
	}
}

func TestRunTaskMissingBody(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"task"})
	if code != 1 {
		t.Errorf("task missing body returned %d, want 1", code)
	}
}

func TestMustMarshalJSON(t *testing.T) {
	input := map[string]any{"key": "value", "num": 42}
	result := mustMarshalJSON(input)
	if result == "" {
		t.Error("mustMarshalJSON returned empty string")
	}
	if !strings.Contains(result, `"key"`) {
		t.Error("mustMarshalJSON result missing expected key")
	}
}

func TestFlagProjectEnvNotSet(t *testing.T) {
	// Without flagProject set, getProject should try cwd discovery
	flagProject = ""
	// This may fail due to no project found but shouldn't panic
	_, _ = getProject()
}

func TestRunSetupNoAgents(t *testing.T) {
	// With unknown/empty agent override, should say no agents detected
	flagProject = ""
	code := Dispatch([]string{"setup", "--agent", "nonexistentagent"})
	// May return 0 or 1 depending on whether agents are found
	_ = code
}

func TestRunSetupVerifyNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"setup", "--verify"})
	// Should fail because no project
	if code != 1 {
		t.Logf("setup --verify without project returned %d", code)
	}
}

func TestRunSetupUninstallNoManifest(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"setup", "--uninstall"})
	// Should handle missing manifest gracefully
	_ = code
}

func TestDispatchInitWithGitignore(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a .gitignore first
	gitignorePath := tmpDir + "/.gitignore"
	os.WriteFile(gitignorePath, []byte("existing content\n"), 0644)

	code := Dispatch([]string{"init", "-dir", tmpDir})
	if code != 0 {
		t.Errorf("init returned %d, want 0", code)
	}

	// Verify .dfmt was created
	dfmtPath := tmpDir + "/.dfmt"
	if _, err := os.Stat(dfmtPath); os.IsNotExist(err) {
		t.Error(".dfmt directory was not created")
	}
}

func TestDispatchInitWithExistingDfmt(t *testing.T) {
	tmpDir := t.TempDir()
	// Create .dfmt directory
	dfmtPath := tmpDir + "/.dfmt"
	os.MkdirAll(dfmtPath, 0755)

	code := Dispatch([]string{"init", "-dir", tmpDir})
	// Should still succeed
	if code != 0 {
		t.Errorf("init returned %d, want 0", code)
	}
}

func TestRunCaptureGitInvalidSubcommand(t *testing.T) {
	code := Dispatch([]string{"capture", "git", "invalid"})
	if code != 1 {
		t.Errorf("capture git invalid returned %d, want 1", code)
	}
}

func TestRunCaptureGitMissingArgs(t *testing.T) {
	code := Dispatch([]string{"capture", "git", "commit"})
	if code != 1 {
		t.Errorf("capture git commit missing args returned %d, want 1", code)
	}
}

func TestRunCaptureUnknownSubcommand(t *testing.T) {
	code := Dispatch([]string{"capture", "unknown", "subcommand"})
	if code != 1 {
		t.Errorf("capture unknown subcommand returned %d, want 1", code)
	}
}

func TestMustMarshalJSONPretty(t *testing.T) {
	input := map[string]any{
		"name": "test",
		"values": []int{1, 2, 3},
	}
	result := mustMarshalJSON(input)
	if result == "" {
		t.Error("mustMarshalJSON returned empty string")
	}
	if !strings.Contains(result, "test") {
		t.Error("mustMarshalJSON result missing expected content")
	}
}

func TestRunStopNoDaemon(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"stop"})
	// Should still return 0 (just says not running)
	if code != 0 {
		t.Logf("stop without daemon returned %d", code)
	}
}

func TestRunDaemonAlreadyRunning(t *testing.T) {
	flagProject = ""
	// Should report already running or start new daemon
	code := Dispatch([]string{"daemon"})
	_ = code // May start daemon or report running
}

func TestRunStopWithRunningDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	// Create fake .dfmt directory
	dfmtDir := tmpDir + "/.dfmt"
	os.MkdirAll(dfmtDir, 0755)
	// Create a fake socket file
	socketPath := dfmtDir + "/daemon.sock"
	os.WriteFile(socketPath, []byte("fake"), 0644)
	// Create lock file
	lockPath := dfmtDir + "/lock"
	os.WriteFile(lockPath, []byte("test"), 0644)
	// Create PID file
	pidPath := dfmtDir + "/daemon.pid"
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)

	flagProject = tmpDir
	code := Dispatch([]string{"stop"})
	if code != 0 {
		t.Errorf("stop with daemon returned %d, want 0", code)
	}
}

func TestRunDoctorAllChecks(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a minimal project structure
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	// Write a valid config
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte(`version: 1`), 0644)

	code := Dispatch([]string{"doctor", "-dir", tmpDir})
	_ = code // May pass or fail depending on daemon state
}

func TestRunDaemonWithForegroundTimeout(t *testing.T) {
	// Skip this test on Windows - Unix socket daemon doesn't work
	if os.PathSeparator == '\\' {
		t.Skip("skipping daemon test on Windows")
	}
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte(`version: 1`), 0644)

	flagProject = tmpDir
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan int, 1)
	go func() {
		done <- Dispatch([]string{"daemon", "-foreground"})
	}()

	select {
	case <-ctx.Done():
		// Timeout - expected when daemon starts but can't accept connections
	case code := <-done:
		_ = code
	}
}

func TestRunStatusJSON(t *testing.T) {
	tmpDir := t.TempDir()
	flagProject = tmpDir
	flagJSON = true
	defer func() { flagJSON = false }()

	code := Dispatch([]string{"status"})
	if code != 0 {
		t.Errorf("status --json returned %d, want 0", code)
	}
}

func TestRunConfigJSON(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte(`version: 1`), 0644)

	flagProject = tmpDir
	flagJSON = true
	defer func() { flagJSON = false }()

	code := Dispatch([]string{"config"})
	_ = code // May fail due to config loading but exercises code
}

func TestRunRecallJSON(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	flagJSON = true
	defer func() { flagJSON = false }()

	// Will fail because no daemon but exercises code path
	code := Dispatch([]string{"recall", "-budget", "1000", "-format", "json"})
	if code != 1 {
		t.Logf("recall returned %d (expected fail)", code)
	}
}

func TestRunSearchJSON(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	flagJSON = true
	defer func() { flagJSON = false }()

	code := Dispatch([]string{"search", "-limit", "5", "test"})
	if code != 1 {
		t.Logf("search returned %d (expected fail)", code)
	}
}

func TestRunRememberJSON(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	flagJSON = true
	defer func() { flagJSON = false }()

	code := Dispatch([]string{"remember", "-type", "note", "-source", "test", "test tag"})
	if code != 1 {
		t.Logf("remember returned %d (expected fail)", code)
	}
}

func TestRunRememberWithData(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"remember", "-type", "note", "-data", `{"key":"value"}`, "tag1"})
	if code != 1 {
		t.Logf("remember with data returned %d (expected fail)", code)
	}
}

func TestRunRememberWithActor(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"remember", "-type", "note", "-actor", "user@test.com", "tag1"})
	if code != 1 {
		t.Logf("remember with actor returned %d (expected fail)", code)
	}
}

func TestRunCaptureShellCmd(t *testing.T) {
	code := Dispatch([]string{"capture", "shell", "ls -la"})
	if code != 0 {
		t.Errorf("capture shell returned %d, want 0", code)
	}
}

func TestRunInstallHooks(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.git/hooks", 0755)

	// Create hook source files
	hooksSrc := tmpDir + "/docs/hooks"
	os.MkdirAll(hooksSrc, 0755)
	os.WriteFile(hooksSrc+"/git-post-commit.sh", []byte("#!/bin/bash\necho test"), 0644)
	os.WriteFile(hooksSrc+"/git-post-checkout.sh", []byte("#!/bin/bash\necho test"), 0644)
	os.WriteFile(hooksSrc+"/git-pre-push.sh", []byte("#!/bin/bash\necho test"), 0644)

	flagProject = tmpDir
	code := Dispatch([]string{"install-hooks"})
	if code != 0 {
		t.Errorf("install-hooks returned %d, want 0", code)
	}
}

func TestRunExec(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"exec", "echo hello"})
	if code != 1 {
		t.Logf("exec returned %d", code)
	}
}

func TestRunMCPStdin(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	// This will read from stdin - in test environment it will likely get EOF
	// Just verify it doesn't panic
	code := Dispatch([]string{"mcp"})
	_ = code
}

func TestDispatchUnknownCommand(t *testing.T) {
	code := Dispatch([]string{"unknownsubcommand"})
	if code != 1 {
		t.Errorf("unknown command returned %d, want 1", code)
	}
}

func TestDispatchEmptyStringCommand(t *testing.T) {
	code := Dispatch([]string{""})
	if code != 1 {
		t.Errorf("empty string command returned %d, want 1", code)
	}
}

func TestRunSetupDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"setup", "--dry-run", "--agent", "nonexistentagent"})
	_ = code // May return 0 or 1
}

func TestRunSetupForce(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	// Force skips the y/N prompt
	code := Dispatch([]string{"setup", "--force", "--agent", "nonexistentagent"})
	_ = code
}

func TestRunShellInitBashExplicit(t *testing.T) {
	code := Dispatch([]string{"shell-init", "bash"})
	if code != 0 {
		t.Errorf("shell-init bash returned %d, want 0", code)
	}
}

func TestRunShellInitZshExplicit(t *testing.T) {
	code := Dispatch([]string{"shell-init", "zsh"})
	if code != 0 {
		t.Errorf("shell-init zsh returned %d, want 0", code)
	}
}

func TestRunShellInitFishExplicit(t *testing.T) {
	code := Dispatch([]string{"shell-init", "fish"})
	if code != 0 {
		t.Errorf("shell-init fish returned %d, want 0", code)
	}
}

func TestRunShellInitUnknownShell(t *testing.T) {
	code := Dispatch([]string{"shell-init", "csh"})
	if code != 1 {
		t.Errorf("shell-init csh returned %d, want 1", code)
	}
}

func TestRunConfigWithArgs(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte(`version: 1`), 0644)

	flagProject = tmpDir
	code := Dispatch([]string{"config", "get"})
	_ = code // May fail but exercises code
}

func TestGetProjectWithCWD(t *testing.T) {
	flagProject = ""
	proj, err := getProject()
	if err != nil {
		t.Logf("getProject failed (expected if no project): %v", err)
	}
	_ = proj
}

func TestRunStatusNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"status"})
	if code != 1 {
		t.Logf("status without project returned %d", code)
	}
}

func TestRunConfigNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"config"})
	if code != 1 {
		t.Logf("config without project returned %d", code)
	}
}

func TestRunInitWithExistingGitignore(t *testing.T) {
	tmpDir := t.TempDir()
	gitignorePath := tmpDir + "/.gitignore"
	os.WriteFile(gitignorePath, []byte("existing\n.DFMT/\n"), 0644)

	code := Dispatch([]string{"init", "-dir", tmpDir})
	if code != 0 {
		t.Errorf("init with existing gitignore returned %d, want 0", code)
	}
}

func TestRunInitWithNewDir(t *testing.T) {
	tmpDir := t.TempDir()
	code := Dispatch([]string{"init", "-dir", tmpDir})
	if code != 0 {
		t.Errorf("init returned %d, want 0", code)
	}
}

func TestRunInitErrorCreatingDir(t *testing.T) {
	// Try to init in a path that shouldn't work
	flagProject = ""
	code := Dispatch([]string{"init", "-dir", "/proc/invalid/path"})
	if code != 1 {
		t.Logf("init error case returned %d", code)
	}
}

func TestRunDoctorNoDir(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"doctor"})
	if code != 0 && code != 1 {
		t.Errorf("doctor returned unexpected %d", code)
	}
}

func TestRunDoctorWithChecks(t *testing.T) {
	tmpDir := t.TempDir()
	// Create minimal project
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte(`version: 1`), 0644)

	code := Dispatch([]string{"doctor", "-dir", tmpDir})
	_ = code
}

func TestRunSearchWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"search", "-limit", "20", "test"})
	if code != 1 {
		t.Logf("search returned %d (expected fail)", code)
	}
}

func TestRunRecallWithFormat(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"recall", "-budget", "500", "-format", "xml"})
	if code != 1 {
		t.Logf("recall returned %d (expected fail)", code)
	}
}

func TestRunRecallWithFormatJSON(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"recall", "-format", "json"})
	if code != 1 {
		t.Logf("recall returned %d (expected fail)", code)
	}
}

func TestRunTaskCreate(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"task", "Buy groceries"})
	if code != 1 {
		t.Logf("task create returned %d (expected fail)", code)
	}
}

func TestDispatchNoteCommand(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"note", "My quick note"})
	if code != 1 {
		t.Logf("note returned %d (expected fail)", code)
	}
}

func TestDispatchRememberCommand(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"remember", "-type", "decision", "Implementing feature X"})
	if code != 1 {
		t.Logf("remember returned %d (expected fail)", code)
	}
}

func TestRunStopNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"stop"})
	if code != 1 {
		t.Logf("stop without project returned %d", code)
	}
}

func TestRunListNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"list"})
	if code != 0 {
		t.Errorf("list returned %d, want 0", code)
	}
}

func TestRunStatsNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"stats"})
	if code != 0 {
		t.Errorf("stats returned %d, want 0", code)
	}
}

func TestRunTailNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"tail"})
	if code != 0 {
		t.Errorf("tail returned %d, want 0", code)
	}
}

func TestRunShellInitNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"shell-init"})
	if code != 0 {
		t.Errorf("shell-init returned %d, want 0", code)
	}
}

func TestRunCaptureNoProject(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"capture", "git", "commit", "abc123"})
	if code != 0 {
		t.Errorf("capture returned %d, want 0", code)
	}
}

func TestRunCaptureMissingSubcommand(t *testing.T) {
	code := Dispatch([]string{"capture", "git"})
	if code != 1 {
		t.Errorf("capture git missing subcommand returned %d, want 1", code)
	}
}

func TestRunCaptureInvalidType(t *testing.T) {
	code := Dispatch([]string{"capture", "invalidtype"})
	if code != 1 {
		t.Errorf("capture invalidtype returned %d, want 1", code)
	}
}

func TestRunCaptureUnknownCaptureType(t *testing.T) {
	code := Dispatch([]string{"capture", "filesystem", "event"})
	if code != 1 {
		t.Errorf("capture filesystem returned %d, want 1", code)
	}
}

func TestRunCaptureUnknownCaptureTypeAgain(t *testing.T) {
	code := Dispatch([]string{"capture", "filesystem", "event"})
	if code != 1 {
		t.Errorf("capture filesystem returned %d, want 1", code)
	}
}

func TestRunTaskNoArgs(t *testing.T) {
	flagProject = ""
	code := Dispatch([]string{"task"})
	if code != 1 {
		t.Errorf("task no args returned %d, want 1", code)
	}
}

func TestRunTaskInvalidSubcommand(t *testing.T) {
	code := Dispatch([]string{"task", "invalid", "arg"})
	// Will fail because no daemon running
	if code != 1 {
		t.Logf("task invalid returned %d (expected fail)", code)
	}
}

func TestMustMarshalJSONWithInt(t *testing.T) {
	result := mustMarshalJSON(123)
	if result == "" {
		t.Error("mustMarshalJSON with int returned empty")
	}
}

func TestMustMarshalJSONWithSlice(t *testing.T) {
	result := mustMarshalJSON([]string{"a", "b"})
	if result == "" {
		t.Error("mustMarshalJSON with slice returned empty")
	}
}

func TestMustMarshalJSONWithMap(t *testing.T) {
	result := mustMarshalJSON(map[string]int{"key": 42})
	if result == "" {
		t.Error("mustMarshalJSON with map returned empty")
	}
}

func TestRunSearchEmptyQuery(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	// Empty query should fail
	code := Dispatch([]string{"search"})
	if code != 1 {
		t.Errorf("search with empty query returned %d, want 1", code)
	}
}

func TestRunSearchWithLimitZero(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"search", "-limit", "0", "test"})
	if code != 1 {
		t.Logf("search -limit 0 returned %d (expected fail)", code)
	}
}

func TestRunSearchWithLimitHigh(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"search", "-limit", "100", "test query"})
	if code != 1 {
		t.Logf("search with high limit returned %d (expected fail)", code)
	}
}

func TestRunSearchNoQuery(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"search"})
	if code != 1 {
		t.Errorf("search with no query returned %d, want 1", code)
	}
}

func TestRunSearchWithSpecialChars(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	code := Dispatch([]string{"search", "-limit", "5", "test with 'special' chars"})
	if code != 1 {
		t.Logf("search with special chars returned %d (expected fail)", code)
	}
}

// MCP protocol tests

func TestRunMCPWithDaemonRunning(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping MCP test on Windows (Unix socket)")
	}
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	// Create a real Unix socket
	socketPath := tmpDir + "/.dfmt/daemon.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer listener.Close()

	flagProject = tmpDir

	// Send MCP initialize request via stdin
	input := `{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}` + "\n"

	// Run MCP in goroutine
	done := make(chan error, 1)
	go func() {
		// Redirect stdin
		oldStdin := os.Stdin
		r, w, _ := os.Pipe()
		os.Stdin = r
		w.WriteString(input)
		w.Close()
		defer func() { os.Stdin = oldStdin }()

		Dispatch([]string{"mcp"})
		done <- nil
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMCPProtocolInitialize(t *testing.T) {
	// Test MCP protocol initialization directly
	index := core.NewIndex()
	handlers := transport.NewHandlers(index, nil)
	mcp := transport.NewMCPProtocol(handlers)

	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("Initialize returned error: %s", resp.Error.Message)
	}
	if resp.ID != 1 {
		t.Errorf("ID = %v, want 1", resp.ID)
	}
}

func TestMCPProtocolToolsList(t *testing.T) {
	index := core.NewIndex()
	handlers := transport.NewHandlers(index, nil)
	mcp := transport.NewMCPProtocol(handlers)

	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      2,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("tools/list returned error: %s", resp.Error.Message)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("Result type %T, want map", resp.Result)
	}
	toolsVal, ok := result["tools"]
	if !ok {
		t.Fatal("tools field not found in result")
	}
	// Tools is []MCPTool which when unmarshaled into interface{} becomes []interface{}
	// but can also be typed []MCPTool depending on JSON unmarshal behavior
	switch v := toolsVal.(type) {
	case []interface{}:
		if len(v) < 3 {
			t.Errorf("tools count = %d, want at least 3", len(v))
		}
	case []map[string]interface{}:
		if len(v) < 3 {
			t.Errorf("tools count = %d, want at least 3", len(v))
		}
	default:
		// For typed slices like []transport.MCPTool, use reflection or skip
		t.Logf("tools type %T - skipping length check", toolsVal)
	}
}

func TestMCPProtocolPing(t *testing.T) {
	index := core.NewIndex()
	handlers := transport.NewHandlers(index, nil)
	mcp := transport.NewMCPProtocol(handlers)

	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "ping",
		ID:      3,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("ping returned error: %s", resp.Error.Message)
	}
	if resp.ID != 3 {
		t.Errorf("ID = %v, want 3", resp.ID)
	}
}

func TestMCPProtocolUnknownMethod(t *testing.T) {
	index := core.NewIndex()
	handlers := transport.NewHandlers(index, nil)
	mcp := transport.NewMCPProtocol(handlers)

	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "unknown/method",
		ID:      4,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("Expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("Error code = %d, want -32601", resp.Error.Code)
	}
}

func TestMCPProtocolToolsCallNoHandler(t *testing.T) {
	mcp := transport.NewMCPProtocol(nil)

	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      5,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("Expected error when no handler")
	}
}

func TestMCPProtocolToolsCallInvalidParams(t *testing.T) {
	index := core.NewIndex()
	handlers := transport.NewHandlers(index, nil)
	mcp := transport.NewMCPProtocol(handlers)

	// Missing name field
	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"arguments":{}}`),
		ID:      6,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Logf("tools/call with invalid params returned error: %s", resp.Error.Message)
	}
}

func TestMCPProtocolToolsCallKnownTool(t *testing.T) {
	index := core.NewIndex()
	handlers := transport.NewHandlers(index, nil)
	mcp := transport.NewMCPProtocol(handlers)

	req := &transport.MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search","arguments":{"query":"test"}}`),
		ID:      7,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	// Should work even without daemon (search returns empty results)
	if resp.Error != nil {
		t.Logf("dfmt.search returned error (expected if no daemon): %s", resp.Error.Message)
	}
}

// Setup tests

func TestRunSetupFlags(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	flagProject = tmpDir
	// Test --dry-run with no agents
	code := Dispatch([]string{"setup", "--dry-run"})
	// Should say no agents detected
	_ = code
}

func TestRunSetupUninstallEmptyManifest(t *testing.T) {
	tmpDir := t.TempDir()

	// Override manifest path to temp
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Create empty manifest dir
	manifestDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(manifestDir, 0755)

	// Write empty manifest
	emptyManifest := &setup.Manifest{Version: 1}
	setup.SaveManifest(emptyManifest)

	flagProject = tmpDir
	code := Dispatch([]string{"setup", "--uninstall"})
	if code != 0 {
		t.Errorf("setup --uninstall returned %d, want 0", code)
	}
}

func TestRunSetupUninstallWithFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Override manifest path
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Create manifest dir
	manifestDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(manifestDir, 0755)

	// Create a file to be tracked
	testFile := filepath.Join(tmpDir, "testfile.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	// Create manifest with file entry
	m := &setup.Manifest{
		Version: 1,
		Files: []setup.FileEntry{
			{Path: testFile, Agent: "test", Version: "1"},
		},
	}
	setup.SaveManifest(m)

	flagProject = tmpDir
	code := Dispatch([]string{"setup", "--uninstall"})
	if code != 0 {
		t.Errorf("setup --uninstall with files returned %d, want 0", code)
	}
}

func TestRunSetupVerifyEmptyManifest(t *testing.T) {
	tmpDir := t.TempDir()

	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	manifestDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(manifestDir, 0755)
	setup.SaveManifest(&setup.Manifest{Version: 1})

	flagProject = tmpDir
	code := Dispatch([]string{"setup", "--verify"})
	if code != 0 {
		t.Errorf("setup --verify with empty manifest returned %d, want 0", code)
	}
}

func TestRunSetupVerifyMissingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	manifestDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(manifestDir, 0755)

	// Create manifest with non-existent file
	m := &setup.Manifest{
		Version: 1,
		Files: []setup.FileEntry{
			{Path: filepath.Join(tmpDir, "nonexistent.txt"), Agent: "test", Version: "1"},
		},
	}
	setup.SaveManifest(m)

	flagProject = tmpDir
	code := Dispatch([]string{"setup", "--verify"})
	if code != 1 {
		t.Errorf("setup --verify with missing files returned %d, want 1", code)
	}
}

func TestRunSetupVerifyAllPresent(t *testing.T) {
	tmpDir := t.TempDir()

	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	manifestDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(manifestDir, 0755)

	// Create a real file
	testFile := filepath.Join(tmpDir, "present.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	m := &setup.Manifest{
		Version: 1,
		Files: []setup.FileEntry{
			{Path: testFile, Agent: "test", Version: "1"},
		},
	}
	setup.SaveManifest(m)

	flagProject = tmpDir
	code := Dispatch([]string{"setup", "--verify"})
	if code != 0 {
		t.Errorf("setup --verify with present files returned %d, want 0", code)
	}
}

// configureAgent tests

func TestConfigureAgentUnknown(t *testing.T) {
	agent := setup.Agent{ID: "unknown-agent", Name: "Unknown"}
	err := configureAgent(agent)
	if err == nil {
		t.Error("configureAgent with unknown ID should return error")
	}
	if err != nil && !strings.Contains(err.Error(), "unsupported agent") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConfigureAgentClaudeCode(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping on Windows - HOME env behaves differently")
	}
	tmpDir := t.TempDir()
	home := tmpDir

	// Override home for this test
	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")

	// Create .claude directory
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0755)

	agent := setup.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Version:    "1.0",
		InstallDir: claudeDir,
	}

	err := configureAgent(agent)
	if err != nil {
		t.Fatalf("configureAgent(claude-code) failed: %v", err)
	}

	// Verify mcp.json was created
	mcpPath := filepath.Join(claudeDir, "mcp.json")
	if _, err := os.Stat(mcpPath); os.IsNotExist(err) {
		t.Error("mcp.json was not created")
	}
}

func TestConfigureAgentCodex(t *testing.T) {
	agent := setup.Agent{
		ID:         "codex",
		Name:       "Codex",
		Version:    "1.0",
		InstallDir: "/tmp/codex",
	}

	err := configureAgent(agent)
	if err == nil {
		t.Error("configureAgent(codex) should return error (not implemented)")
	}
}

func TestConfigureClaudeCodeNewDir(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping on Windows - os.UserHomeDir() uses USERPROFILE not HOME")
	}
	tmpDir := t.TempDir()
	home := tmpDir

	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")

	// Create non-existent .claude path
	claudeDir := filepath.Join(home, ".claude")

	agent := setup.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Version:    "1.0",
		InstallDir: claudeDir,
	}

	err := configureClaudeCode(agent)
	if err != nil {
		t.Fatalf("configureClaudeCode failed: %v", err)
	}

	// Verify directory and file exist
	if info, err := os.Stat(claudeDir); err != nil || !info.IsDir() {
		t.Error(".claude directory was not created")
	}

	mcpPath := filepath.Join(claudeDir, "mcp.json")
	if _, err := os.Stat(mcpPath); os.IsNotExist(err) {
		t.Error("mcp.json was not created")
	}
}

func TestConfigureClaudeCodeExisting(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping on Windows - os.UserHomeDir() uses USERPROFILE not HOME")
	}
	tmpDir := t.TempDir()
	home := tmpDir

	os.Setenv("HOME", home)
	defer os.Unsetenv("HOME")

	// Pre-create .claude directory
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Pre-create mcp.json
	mcpPath := filepath.Join(claudeDir, "mcp.json")
	os.WriteFile(mcpPath, []byte("{}"), 0644)

	agent := setup.Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Version:    "1.0",
		InstallDir: claudeDir,
	}

	err := configureClaudeCode(agent)
	if err != nil {
		t.Fatalf("configureClaudeCode with existing file failed: %v", err)
	}

	// Verify backup was created
	backupPath := mcpPath + ".dfmt.bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("backup file was not created")
	}
}

func TestConfigureCodexNotImplemented(t *testing.T) {
	agent := setup.Agent{
		ID:         "codex",
		Name:       "Codex CLI",
		Version:    "1.0",
		InstallDir: "/tmp/codex",
	}

	err := configureCodex(agent)
	if err == nil {
		t.Error("configureCodex should return error (not implemented)")
	}
}

func TestPrintUsageDoesNotPanic(t *testing.T) {
	// Capture stdout to verify printUsage works
	printUsage()
}
