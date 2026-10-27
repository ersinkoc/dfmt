package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/setup"
)

// Each agent must be configured in the file it actually reads, under the
// key it actually reads. Before 0.9.0 these tests asserted that
// `<dir>/mcp.json` existed for every agent — pinning a file only Cursor
// reads, so seven of nine agents were "configured" without ever seeing dfmt.

func setHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", filepath.Join(tmp, "AppData", "Roaming"))
	return tmp
}

// configureAndRead configures agentID and returns the target file's text.
func configureAndRead(t *testing.T, agentID, wantRel string) string {
	t.Helper()
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: agentID}); err != nil {
		t.Fatalf("configureAgent %s: %v", agentID, err)
	}
	path := filepath.Join(home, filepath.FromSlash(wantRel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: expected config at %s: %v", agentID, path, err)
	}
	target, _ := setup.AgentTargetFor(agentID)
	if target.Path != path {
		t.Errorf("%s: target path %s, want %s", agentID, target.Path, path)
	}
	if err := target.Verify(); err != nil {
		t.Errorf("%s: Verify after configure: %v", agentID, err)
	}
	return string(data)
}

func TestConfigureAgent_Cursor(t *testing.T) {
	if got := configureAndRead(t, "cursor", ".cursor/mcp.json"); !strings.Contains(got, `"mcpServers"`) {
		t.Errorf("cursor config lacks mcpServers:\n%s", got)
	}
}

func TestConfigureAgent_VSCode(t *testing.T) {
	rel := ".config/Code/User/mcp.json"
	switch runtime.GOOS {
	case goosDarwin:
		rel = "Library/Application Support/Code/User/mcp.json"
	case "windows":
		rel = "AppData/Roaming/Code/User/mcp.json"
	}
	got := configureAndRead(t, "vscode", rel)
	if !strings.Contains(got, `"servers"`) || strings.Contains(got, `"mcpServers"`) {
		t.Errorf("vscode config must use the `servers` key:\n%s", got)
	}
}

func TestConfigureAgent_Codex(t *testing.T) {
	got := configureAndRead(t, "codex", ".codex/config.toml")
	if !strings.Contains(got, "[mcp_servers.dfmt]") || !strings.Contains(got, `args = ["mcp"]`) {
		t.Errorf("codex config.toml lacks the dfmt table:\n%s", got)
	}
}

func TestConfigureAgent_CopilotCLI(t *testing.T) {
	got := configureAndRead(t, "copilot-cli", ".copilot/mcp-config.json")
	if !strings.Contains(got, `"mcpServers"`) || !strings.Contains(got, `"tools"`) {
		t.Errorf("copilot cli config lacks mcpServers/tools:\n%s", got)
	}
}

func TestConfigureAgent_Antigravity(t *testing.T) {
	got := configureAndRead(t, "antigravity", ".gemini/config/mcp_config.json")
	if !strings.Contains(got, `"mcpServers"`) {
		t.Errorf("antigravity config lacks mcpServers:\n%s", got)
	}
}

func TestConfigureAgent_Devin(t *testing.T) {
	rel := ".config/devin/mcp_config.json"
	if runtime.GOOS == "windows" {
		rel = "AppData/Roaming/devin/mcp_config.json"
	}
	if got := configureAndRead(t, "devin", rel); !strings.Contains(got, `"mcpServers"`) {
		t.Errorf("devin config lacks mcpServers:\n%s", got)
	}
}

func TestConfigureAgent_Kiro(t *testing.T) {
	if got := configureAndRead(t, "kiro", ".kiro/settings/mcp.json"); !strings.Contains(got, `"mcpServers"`) {
		t.Errorf("kiro config lacks mcpServers:\n%s", got)
	}
}

func TestConfigureAgent_Cline(t *testing.T) {
	user := ".config/Code/User"
	switch runtime.GOOS {
	case goosDarwin:
		user = "Library/Application Support/Code/User"
	case "windows":
		user = "AppData/Roaming/Code/User"
	}
	rel := user + "/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json"
	if got := configureAndRead(t, "cline", rel); !strings.Contains(got, `"mcpServers"`) {
		t.Errorf("cline settings lack mcpServers:\n%s", got)
	}
}

// Retired agents are refused rather than written somewhere nothing reads.
func TestConfigureAgent_RetiredAgentsRejected(t *testing.T) {
	setHome(t)
	for _, id := range []string{"gemini", "windsurf", "continue"} {
		if err := configureAgent(setup.Agent{ID: id}); err == nil {
			t.Errorf("configureAgent(%s) succeeded for a retired agent", id)
		}
	}
}

func TestConfigureAgent_Zed(t *testing.T) {
	rel := ".config/zed/settings.json"
	if runtime.GOOS == "windows" {
		rel = "AppData/Roaming/Zed/settings.json"
	}
	if got := configureAndRead(t, "zed", rel); !strings.Contains(got, `"context_servers"`) {
		t.Errorf("zed settings lacks context_servers:\n%s", got)
	}
}

func TestConfigureAgent_OpenCode(t *testing.T) {
	got := configureAndRead(t, "opencode", ".config/opencode/opencode.json")
	if !strings.Contains(got, `"mcp"`) || !strings.Contains(got, `"local"`) {
		t.Errorf("opencode config lacks a local mcp entry:\n%s", got)
	}
}

// TestConfigureAgent_RemovesLegacyDecoy: a 0.8.0 setup left `<dir>/mcp.json`
// files no agent reads. Re-running setup removes dfmt from them (and the
// file, when dfmt was its only content).
func TestConfigureAgent_RemovesLegacyDecoy(t *testing.T) {
	home := setHome(t)
	legacy := filepath.Join(home, ".codex", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"mcpServers":{"dfmt":{"command":"dfmt"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureAgent(setup.Agent{ID: "codex"}); err != nil {
		t.Fatalf("configureAgent codex: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy %s still present (err=%v)", legacy, err)
	}
}

// TestUninstallAgentConfig_KeepsUserContent: uninstall must strip only the
// dfmt server from an agent's own config — never delete the file, and never
// restore the pre-setup backup over changes the user made since.
func TestUninstallAgentConfig_KeepsUserContent(t *testing.T) {
	home := setHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	path := filepath.Join(home, ".kiro", "settings", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureAgent(setup.Agent{ID: "kiro"}); err != nil {
		t.Fatalf("configureAgent kiro: %v", err)
	}
	// A change the user makes after setup must survive uninstall.
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), `"theme": "dark"`, `"theme": "light"`, 1)
	if edited == string(data) {
		t.Fatalf("fixture edit did not apply:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := runSetupUninstall(); code != 0 {
		t.Fatalf("uninstall = %d", code)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("uninstall deleted the user's settings.json: %v", err)
	}
	if strings.Contains(string(got), "dfmt") {
		t.Errorf("dfmt server still present after uninstall:\n%s", got)
	}
	if !strings.Contains(string(got), `"light"`) {
		t.Errorf("uninstall lost the user's post-setup edit (backup restored?):\n%s", got)
	}
}
