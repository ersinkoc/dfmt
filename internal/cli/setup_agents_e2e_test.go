package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgentHome makes Claude Code, Cursor and Codex look installed in an
// isolated HOME, with an empty PATH so no real binary is detected. The
// setup/doctor/quickstart paths below used to be covered only on machines
// that happened to have agents installed — CI measured internal/cli three
// points lower than a developer laptop for exactly this reason.
func fakeAgentHome(t *testing.T) (home, proj string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	for _, d := range []string{".claude", ".cursor", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// detectClaudeCode wants a non-empty ~/.claude.
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home, "XDG_DATA_HOME": filepath.Join(root, "xdg"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "APPDATA": filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"), "DFMT_GLOBAL_DIR": filepath.Join(root, "global"),
		"PATH": filepath.Join(root, "emptybin"),
	} {
		t.Setenv(k, v)
	}
	proj = filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev, prevJSON := flagProject, flagJSON
	flagProject, flagJSON = proj, false
	t.Cleanup(func() { flagProject, flagJSON = prev, prevJSON })
	t.Chdir(proj)
	return home, proj
}

func TestSetupConfiguresDetectedAgentsEndToEnd(t *testing.T) {
	home, _ := fakeAgentHome(t)
	run := func(args ...string) (int, string) {
		var code int
		out := captureStdout(t, func() { code = Dispatch(args) })
		return code, out
	}

	if code, out := run("setup", "--dry-run"); code != 0 ||
		!strings.Contains(out, "cursor") || !strings.Contains(out, "codex") {
		t.Fatalf("dry run: code %d out %q", code, out)
	}
	if code, out := run("setup", "--force"); code != 0 {
		t.Fatalf("setup --force: code %d out %q", code, out)
	}
	cursor, _ := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	codex, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(cursor), "dfmt") || !strings.Contains(string(codex), "[mcp_servers.dfmt]") {
		t.Fatalf("agent configs not written:\ncursor=%s\ncodex=%s", cursor, codex)
	}

	_, _ = run("setup", "--verify")
	if _, out := run("doctor"); !strings.Contains(strings.ToLower(out), "cursor") {
		t.Errorf("doctor does not report the configured agents: %q", out)
	}
	_, _ = run("quickstart")

	if code, out := run("setup", "--uninstall"); code != 0 {
		t.Fatalf("uninstall: code %d out %q", code, out)
	}
	cursor, _ = os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	codex, _ = os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if strings.Contains(string(cursor), `"dfmt"`) || strings.Contains(string(codex), "mcp_servers.dfmt") {
		t.Errorf("uninstall left dfmt entries:\ncursor=%s\ncodex=%s", cursor, codex)
	}
	if settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); !strings.Contains(string(settings), "opus") {
		t.Errorf("uninstall damaged the user's Claude settings: %s", settings)
	}
}
