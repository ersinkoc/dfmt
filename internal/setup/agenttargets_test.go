package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func targetAt(t *testing.T, agentID, name, content string) AgentTarget {
	t.Helper()
	target, ok := AgentTargetFor(agentID)
	if !ok {
		t.Fatalf("no target for %s", agentID)
	}
	target.Path = filepath.Join(t.TempDir(), name)
	if content != "" {
		if err := os.WriteFile(target.Path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// Codex config.toml: dfmt owns one marked block, never touches the rest,
// and installing twice leaves exactly one table (a duplicate table is
// invalid TOML and would break Codex's whole config).
func TestAgentTargetCodexTOMLRoundTrip(t *testing.T) {
	user := "model = \"o4\"\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	target := targetAt(t, AgentCodex, "config.toml", user)

	for i := 0; i < 2; i++ {
		if err := target.Install(); err != nil {
			t.Fatalf("Install #%d: %v", i+1, err)
		}
	}
	got := readFile(t, target.Path)
	if n := strings.Count(got, tomlTableHeader); n != 1 {
		t.Fatalf("%d %s tables after two installs:\n%s", n, tomlTableHeader, got)
	}
	if !strings.HasPrefix(got, user) {
		t.Errorf("user content not preserved verbatim:\n%s", got)
	}
	if err := target.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}

	if err := target.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := readFile(t, target.Path); got != user {
		t.Errorf("uninstall did not restore the user's text exactly:\n%q\nwant\n%q", got, user)
	}
}

// A hand-written [mcp_servers.dfmt] wins: adding a second would make the
// file unparseable.
func TestAgentTargetCodexRefusesHandWrittenTable(t *testing.T) {
	target := targetAt(t, AgentCodex, "config.toml", "[mcp_servers.dfmt]\ncommand = \"/opt/dfmt\"\n")
	if err := target.Install(); err == nil {
		t.Fatal("Install succeeded next to a hand-written dfmt table")
	}
	if got := readFile(t, target.Path); strings.Count(got, tomlTableHeader) != 1 {
		t.Errorf("file modified:\n%s", got)
	}
}

func TestAgentTargetCodexTOMLStringEscapes(t *testing.T) {
	if got := tomlString(`C:\Program Files\dfmt "x"\dfmt.exe`); got != `"C:\\Program Files\\dfmt \"x\"\\dfmt.exe"` {
		t.Errorf("tomlString = %s", got)
	}
}

// JSON targets splice one key and keep every other server and setting.
func TestAgentTargetJSONKeepsOtherServers(t *testing.T) {
	target := targetAt(t, AgentZed, "settings.json",
		`{"theme":"One Dark","context_servers":{"other":{"command":"x"}}}`)
	if err := target.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := readFile(t, target.Path)
	for _, want := range []string{`"theme": "One Dark"`, `"other"`, `"dfmt"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s after install:\n%s", want, got)
		}
	}
	if err := target.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	got = readFile(t, target.Path)
	if strings.Contains(got, `"dfmt"`) || !strings.Contains(got, `"other"`) {
		t.Errorf("uninstall removed the wrong entries:\n%s", got)
	}
}

// Zed settings and VS Code's mcp.json allow comments; rewriting one through
// encoding/json would drop them, so dfmt refuses and leaves it alone.
func TestAgentTargetJSONRefusesCommentedFile(t *testing.T) {
	content := "{\n  // my theme\n  \"theme\": \"One Dark\"\n}\n"
	target := targetAt(t, AgentZed, "settings.json", content)
	err := target.Install()
	if err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("Install on a commented file: err=%v, want a refusal naming the manual fix", err)
	}
	if got := readFile(t, target.Path); got != content {
		t.Errorf("commented file was rewritten:\n%s", got)
	}
}

func TestAgentTargetOpenCodeEntryShape(t *testing.T) {
	target := targetAt(t, AgentOpenCode, "opencode.json", "")
	if err := target.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := readFile(t, target.Path)
	for _, want := range []string{`"mcp"`, `"type": "local"`, `"enabled": true`, `"mcp"`} {
		if !strings.Contains(got, want) {
			t.Errorf("opencode entry missing %s:\n%s", want, got)
		}
	}
}

// Verify reads the entry, not the file's existence: a config that still
// exists but lost the dfmt server must fail.
func TestAgentTargetVerifyDetectsMissingEntry(t *testing.T) {
	target := targetAt(t, AgentVSCode, "mcp.json", `{"servers":{"other":{}}}`)
	if err := target.Verify(); err == nil {
		t.Error("Verify passed for a file with no dfmt entry")
	}
}

// Command reads back the launched executable for every format, so doctor
// can tell a stale path from a current one in TOML and YAML configs too.
func TestAgentTargetCommandEveryFormat(t *testing.T) {
	want := ResolveDFMTCommandForEnv(TargetOSUnix)
	for _, id := range []string{AgentCursor, AgentVSCode, AgentCopilotCLI, AgentCodex,
		AgentAntigravity, AgentDevin, AgentZed, AgentOpenCode, AgentKiro, AgentCline} {
		target := targetAt(t, id, "cfg", "")
		if err := target.Install(); err != nil {
			t.Fatalf("%s Install: %v", id, err)
		}
		got, err := target.Command()
		if err != nil || got != want {
			t.Errorf("%s Command = %q, %v; want %q", id, got, err, want)
		}
	}
}

// A file dfmt created holds nothing else after uninstall; it is removed, and
// no `.dfmt.bak` is left behind in either case.
func TestAgentTargetUninstallCleansUp(t *testing.T) {
	created := targetAt(t, AgentCursor, "mcp.json", "")
	if err := created.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := created.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Errorf("dfmt-only file survived uninstall (err=%v)", err)
	}

	shared := targetAt(t, AgentKiro, "mcp.json", `{"theme":"dark"}`)
	if err := shared.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(shared.Path + ".dfmt.bak"); err != nil {
		t.Fatalf("no backup taken of a pre-existing file: %v", err)
	}
	if err := shared.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(shared.Path + ".dfmt.bak"); !os.IsNotExist(err) {
		t.Errorf("backup left behind after uninstall (err=%v)", err)
	}
}
