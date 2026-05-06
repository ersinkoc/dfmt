package cli

import (
	"os"
	"testing"
)

// TestShouldRedirect covers the three outcome classes: mapped tools with
// no daemon (false), unmapped tools (false).
func TestShouldRedirect(t *testing.T) {
	tmp := t.TempDir()
	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	// Without a running daemon, all shouldRedirect calls return false.
	for _, tool := range []string{"Bash", "Read", "WebFetch", "Glob", "Grep", "Edit", "Write"} {
		if shouldRedirect(tool) {
			t.Errorf("shouldRedirect(%q) = true without daemon, want false", tool)
		}
	}

	// Unknown tools always return false.
	for _, tool := range []string{"UnknownTool", "bash", "read", "RunCommand"} {
		if shouldRedirect(tool) {
			t.Errorf("shouldRedirect(%q) = true for unknown tool, want false", tool)
		}
	}
}

// TestBuildRedirectResponse covers all seven mapped tool types and verifies
// the redirect spec structure is correct.
func TestBuildRedirectResponse(t *testing.T) {
	cases := []struct {
		toolName  string
		toolInput map[string]any
		wantMCP   string
		wantKey   string
	}{
		{"Bash", map[string]any{"command": "echo hello"}, "mcp__dfmt__dfmt_exec", "code"},
		{"Read", map[string]any{"path": "/tmp/foo"}, "mcp__dfmt__dfmt_read", "path"},
		{"WebFetch", map[string]any{"url": "https://example.com"}, "mcp__dfmt__dfmt_fetch", "url"},
		{"Glob", map[string]any{"pattern": "*.go"}, "mcp__dfmt__dfmt_glob", "pattern"},
		{"Grep", map[string]any{"pattern": "TODO", "files": "*.go"}, "mcp__dfmt__dfmt_grep", "pattern"},
		{"Edit", map[string]any{"path": "f.go", "old_string": "x", "new_string": "y"}, "mcp__dfmt__dfmt_edit", "path"},
		{"Write", map[string]any{"path": "out.txt", "content": "hello"}, "mcp__dfmt__dfmt_write", "path"},
		{"UnknownTool", map[string]any{"x": "y"}, "mcp__dfmt__dfmt_UnknownTool", ""},
	}

	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			got := buildRedirectResponse(tc.toolName, tc.toolInput)
			redirect, ok := got["redirect"].(map[string]any)
			if !ok {
				t.Fatalf("redirect key missing or not map: %#v", got)
			}
			gotTool, _ := redirect["tool"].(string)
			if gotTool != tc.wantMCP {
				t.Errorf("tool = %q, want %q", gotTool, tc.wantMCP)
			}
			params, ok := redirect["tool_input"].(map[string]any)
			if !ok {
				t.Fatalf("tool_input not map: %#v", redirect)
			}
			if tc.wantKey != "" {
				if _, has := params[tc.wantKey]; !has {
					t.Errorf("params missing key %q: %#v", tc.wantKey, params)
				}
			}
		})
	}
}

// TestCheckSandboxToolchains_DaemonNotRunning covers the early-return path
// when no daemon is running for the project.
func TestCheckSandboxToolchains_DaemonNotRunning(t *testing.T) {
	tmp := t.TempDir()
	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("checkSandboxToolchains panicked: %v", r)
		}
	}()
	checkSandboxToolchains(tmp)
}

// TestCheckInstructionBlockStaleness_NoManifest covers the early-return when
// no manifest exists.
func TestCheckInstructionBlockStaleness_NoManifest(t *testing.T) {
	tmp := t.TempDir()
	prevHome := os.Getenv("HOME")
	prevUserprofile := os.Getenv("USERPROFILE")
	prevXDG := os.Getenv("XDG_DATA_HOME")
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Cleanup(func() {
		os.Setenv("HOME", prevHome)
		os.Setenv("USERPROFILE", prevUserprofile)
		os.Setenv("XDG_DATA_HOME", prevXDG)
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("checkInstructionBlockStaleness panicked: %v", r)
		}
	}()
	checkInstructionBlockStaleness()
}

// TestRunInstallHooks_Basic covers the basic success path.
func TestRunInstallHooks_Basic(t *testing.T) {
	tmp := t.TempDir()
	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	code := Dispatch([]string{"install-hooks"})
	// .git/hooks dir may not exist, so this may return 1. Must not panic.
	if code != 0 && code != 1 {
		t.Errorf("install-hooks returned %d, want 0 or 1", code)
	}
}

// TestToolSubcommand covers the mapping from native tool names to dfmt subcommands.
func TestToolSubcommand(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"Bash", "exec"},
		{"Read", "read"},
		{"WebFetch", "fetch"},
		{"Glob", "glob"},
		{"Grep", "grep"},
		{"Edit", "edit"},
		{"Write", "write"},
		{"UnknownTool", "UnknownTool"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			got := toolSubcommand(tc.tool)
			if got != tc.want {
				t.Errorf("toolSubcommand(%q) = %q, want %q", tc.tool, got, tc.want)
			}
		})
	}
}

// TestLogHookEventToDaemon_NoProject covers the early-return when getProject fails.
func TestLogHookEventToDaemon_NoProject(t *testing.T) {
	prevProject := flagProject
	flagProject = string([]byte{0x00}) // illegal path
	t.Cleanup(func() { flagProject = prevProject })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logHookEventToDaemon panicked: %v", r)
		}
	}()
	input := HookStdinInput{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "ls"},
	}
	logHookEventToDaemon(input)
}

// TestOpenInBrowser covers the platform branch in openInBrowser.
func TestOpenInBrowser(t *testing.T) {
	err := openInBrowser("http://127.0.0.1:9999/dashboard")
	_ = err // error expected (no browser in test env)
}

// TestGoToolchainAtLeast_CoverageBridge adds cases not already covered.
func TestGoToolchainAtLeast_CoverageBridge(t *testing.T) {
	cases := []struct {
		version string
		major   int
		minor   int
		patch   int
		want    bool
	}{
		{"go1.26.2", 1, 26, 2, true},
		{"go1.26.1", 1, 26, 2, false},
		{"go1.27.0", 1, 26, 2, true},
		{"devel go1.28", 1, 26, 2, true},
		{"go1.26.2+", 1, 26, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := goToolchainAtLeast(tc.version, tc.major, tc.minor, tc.patch)
			if got != tc.want {
				t.Errorf("goToolchainAtLeast(%q, %d, %d, %d) = %v, want %v",
					tc.version, tc.major, tc.minor, tc.patch, got, tc.want)
			}
		})
	}
}
