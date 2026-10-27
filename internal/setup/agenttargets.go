package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// Agent MCP config targets (SET-1).
//
// Every agent except Claude Code used to be configured by writing
// `{"mcpServers":{"dfmt":…}}` to `<agent dir>/mcp.json`. Only Cursor reads
// that file. Codex reads TOML, VS Code reads a profile-scoped file under a
// `servers` key, Zed reads `context_servers` from its settings, OpenCode
// reads `mcp` with a command array — so setup
// reported "Configured N agent(s)" for agents that never launched dfmt, and
// `setup --verify` confirmed it by statting the file it had just written.
//
// Each target below names the file the agent actually reads, verified
// against the agent's documentation at the time of writing, and how the
// dfmt entry is shaped there. Writes splice one entry into the user's file
// and leave everything else alone; uninstall removes that entry only.

// ConfigFormat selects the writer for an agent's config file.
type ConfigFormat string

const (
	// FormatJSONMap: a JSON object whose Key maps server names to entries.
	FormatJSONMap ConfigFormat = "json-map"
	// FormatTOMLTable: Codex's config.toml, one `[mcp_servers.dfmt]` table.
	FormatTOMLTable ConfigFormat = "toml-table"
)

// clineExtensionID is Cline's VS Code marketplace ID; its MCP settings
// live under the editor's globalStorage for that ID.
const clineExtensionID = "saoudrizwan.claude-dev"

// dfmtServerName is the key (or `name`) the dfmt entry is stored under.
const dfmtServerName = "dfmt"

// AgentTarget is where and how one agent reads its MCP servers.
type AgentTarget struct {
	Agent  string
	Path   string
	Format ConfigFormat
	// Key is the top-level key holding the servers (JSON format).
	Key string
	// entry builds the dfmt entry for this agent from the resolved command.
	entry func(command string) any
}

// AgentTargetFor returns the MCP config target for agentID. ok is false for
// agents that are not configured through a target (Claude Code has its own
// writer) or are unknown.
func AgentTargetFor(agentID string) (AgentTarget, bool) {
	home := HomeDir()
	plain := func(command string) any {
		return map[string]any{"command": command, "args": []any{"mcp"}, "env": map[string]any{}}
	}
	switch agentID {
	case AgentCursor:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcpServers",
			Path: filepath.Join(home, ".cursor", "mcp.json"),
			entry: func(command string) any {
				return map[string]any{"type": "stdio", "command": command, "args": []any{"mcp"}, "env": map[string]any{}}
			}}, true
	case AgentVSCode:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "servers",
			Path: filepath.Join(vscodeUserDir(home), "mcp.json"),
			entry: func(command string) any {
				return map[string]any{"type": "stdio", "command": command, "args": []any{"mcp"}}
			}}, true
	case AgentCopilotCLI:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcpServers",
			Path: filepath.Join(home, ".copilot", "mcp-config.json"),
			entry: func(command string) any {
				return map[string]any{"type": "local", "command": command, "args": []any{"mcp"},
					"env": map[string]any{}, "tools": []any{"*"}}
			}}, true
	case AgentCodex:
		return AgentTarget{Agent: agentID, Format: FormatTOMLTable,
			Path: filepath.Join(home, ".codex", "config.toml")}, true
	case AgentAntigravity:
		// Shared by the Antigravity IDE and the `agy` CLI.
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcpServers",
			Path: filepath.Join(home, ".gemini", "config", "mcp_config.json"), entry: plain}, true
	case AgentDevin:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcpServers",
			Path: filepath.Join(devinConfigDir(home), "mcp_config.json"), entry: plain}, true
	case AgentZed:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "context_servers",
			Path: filepath.Join(zedConfigDir(home), "settings.json"), entry: plain}, true
	case AgentOpenCode:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcp",
			Path: filepath.Join(home, ".config", "opencode", "opencode.json"),
			entry: func(command string) any {
				return map[string]any{"type": "local", "command": []any{command, "mcp"}, "enabled": true}
			}}, true
	case AgentKiro:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcpServers",
			Path: filepath.Join(home, ".kiro", "settings", "mcp.json"), entry: plain}, true
	case AgentCline:
		return AgentTarget{Agent: agentID, Format: FormatJSONMap, Key: "mcpServers",
			Path: filepath.Join(vscodeUserDir(home), "globalStorage", clineExtensionID,
				"settings", "cline_mcp_settings.json"), entry: plain}, true
	}
	return AgentTarget{}, false
}

// LegacyAgentConfigPath is the `<dir>/mcp.json` that dfmt ≤ 0.8.0 wrote for
// agentID, which that agent does not read. "" when there was none or it is
// the agent's real file (Cursor). Devin and Antigravity inherit the decoys
// written for their predecessors, Windsurf and Gemini CLI.
func LegacyAgentConfigPath(agentID string) string {
	home := HomeDir()
	switch agentID {
	case AgentCodex:
		return filepath.Join(home, ".codex", "mcp.json")
	case AgentVSCode:
		return filepath.Join(home, ".vscode", "mcp.json")
	case AgentAntigravity:
		return filepath.Join(home, ".gemini", "mcp.json")
	case AgentDevin:
		return filepath.Join(home, ".windsurf", "mcp.json")
	case AgentZed:
		return filepath.Join(home, ".config", "zed", "mcp.json")
	case AgentOpenCode:
		return filepath.Join(home, ".config", "opencode", "mcp.json")
	}
	return ""
}

// vscodeUserDir is VS Code's default-profile user directory.
func vscodeUserDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(appDataDir(home), "Code", "User")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User")
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "Code", "User")
		}
		return filepath.Join(home, ".config", "Code", "User")
	}
}

// devinConfigDir is Devin's user configuration directory.
func devinConfigDir(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(appDataDir(home), "devin")
	}
	return filepath.Join(home, ".config", "devin")
}

// zedConfigDir is the directory holding Zed's user settings.json.
func zedConfigDir(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(appDataDir(home), "Zed")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && runtime.GOOS != "darwin" {
		return filepath.Join(xdg, "zed")
	}
	return filepath.Join(home, ".config", "zed")
}

func appDataDir(home string) string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}
	return filepath.Join(home, "AppData", "Roaming")
}

// Install writes the dfmt entry into the target file, creating it (and its
// directory) when absent. A pristine copy of an existing file is kept at
// `<path>.dfmt.bak` on the first patch; uninstall does NOT restore it, since
// that would discard whatever the user changed after setup.
func (t AgentTarget) Install() error {
	command := ResolveDFMTCommandForEnv(TargetOSUnix)
	switch t.Format {
	case FormatJSONMap:
		return t.editJSON(func(servers map[string]any) { servers[dfmtServerName] = t.entry(command) })
	case FormatTOMLTable:
		return t.installTOML(command)
	}
	return fmt.Errorf("unknown config format %q", t.Format)
}

// Uninstall removes only the dfmt entry, deleting the file when nothing
// else is left in it. The setup-time backup is dropped afterwards: it was
// never restored (that would discard the user's later edits), so leaving
// it would only litter the agent's config directory. A missing file is a
// no-op.
func (t AgentTarget) Uninstall() error {
	if _, err := os.Stat(t.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var err error
	switch t.Format {
	case FormatJSONMap:
		err = t.editJSON(func(servers map[string]any) { delete(servers, dfmtServerName) })
	case FormatTOMLTable:
		err = t.uninstallTOML()
	default:
		err = fmt.Errorf("unknown config format %q", t.Format)
	}
	if err != nil {
		return err
	}
	if rerr := os.Remove(t.Path + ".dfmt.bak"); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return fmt.Errorf("remove backup: %w", rerr)
	}
	return nil
}

// Verify re-reads the file and reports whether the agent will find dfmt
// there — not merely whether the file exists.
func (t AgentTarget) Verify() error {
	_, err := t.Command()
	return err
}

// Command returns the executable the agent will launch for dfmt, read
// back from the file. An error means the agent will not find dfmt there.
func (t AgentTarget) Command() (string, error) {
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return "", err
	}
	switch t.Format {
	case FormatJSONMap:
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("parse: %w", err)
		}
		servers, _ := cfg[t.Key].(map[string]any)
		entry, ok := servers[dfmtServerName].(map[string]any)
		if !ok {
			return "", fmt.Errorf("no %q entry under %q", dfmtServerName, t.Key)
		}
		switch c := entry["command"].(type) {
		case string:
			if c != "" {
				return c, nil
			}
		case []any: // OpenCode: command and args in one array
			if len(c) > 0 {
				if s, ok := c[0].(string); ok && s != "" {
					return s, nil
				}
			}
		}
		return "", errors.New("dfmt entry has no command")
	case FormatTOMLTable:
		text := string(data)
		start := strings.Index(text, tomlTableHeader)
		if start < 0 {
			return "", fmt.Errorf("no %s table", tomlTableHeader)
		}
		for _, line := range strings.Split(text[start+len(tomlTableHeader):], "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				break
			}
			if rest, ok := strings.CutPrefix(line, "command"); ok {
				rest = strings.TrimSpace(rest)
				if v, ok := strings.CutPrefix(rest, "="); ok {
					if cmd, err := strconv.Unquote(strings.TrimSpace(v)); err == nil && cmd != "" {
						return cmd, nil
					}
				}
			}
		}
		return "", fmt.Errorf("%s has no command", tomlTableHeader)
	}
	return "", fmt.Errorf("unknown config format %q", t.Format)
}

// editJSON applies edit to the servers map under t.Key and writes the file
// back, dropping the key when it ends up empty.
func (t AgentTarget) editJSON(edit func(servers map[string]any)) error {
	cfg, raw, existed, err := readJSONObject(t.Path)
	if err != nil {
		// Zed's settings and VS Code's mcp.json are JSONC; a file with
		// comments cannot be round-tripped through encoding/json without
		// losing them, so refuse rather than rewrite it.
		return fmt.Errorf("%w (if the file has comments, add the dfmt server by hand)", err)
	}
	servers, _ := cfg[t.Key].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	edit(servers)
	if len(servers) == 0 {
		delete(cfg, t.Key)
	} else {
		cfg[t.Key] = servers
	}
	if len(cfg) == 0 && existed {
		// Only reachable on removal: dfmt was the file's sole content.
		if err := os.Remove(t.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", t.Path, err)
		}
		return nil
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", t.Path, err)
	}
	return t.write(append(out, '\n'), raw, existed)
}

func (t AgentTarget) write(out, raw []byte, existed bool) error {
	if existed {
		if err := writeBackupOnce(t.Path, raw); err != nil {
			return err
		}
	}
	dir := filepath.Dir(t.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := safefs.WriteFileAtomic(dir, t.Path, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", t.Path, err)
	}
	return nil
}

// --- Codex: config.toml ---------------------------------------------------
//
// There is no TOML library in the dependency budget, so dfmt owns one
// marker-delimited block holding its table and never parses the rest of the
// file. A table may legally appear anywhere after the root keys, so
// appending at the end is always valid.

const (
	tomlBlockBegin  = "# >>> dfmt (managed by `dfmt setup`; removed by `dfmt setup --uninstall`) >>>"
	tomlBlockEnd    = "# <<< dfmt <<<"
	tomlTableHeader = "[mcp_servers.dfmt]"
)

func (t AgentTarget) installTOML(command string) error {
	raw, err := os.ReadFile(t.Path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", t.Path, err)
	}
	rest := stripTOMLBlock(string(raw))
	// A second [mcp_servers.dfmt] table would make the file invalid TOML
	// and break Codex entirely; one written by hand wins.
	if strings.Contains(rest, tomlTableHeader) {
		return fmt.Errorf("%s already defines %s outside dfmt's managed block; leaving it as is", t.Path, tomlTableHeader)
	}
	block := tomlBlockBegin + "\n" + tomlTableHeader + "\n" +
		"command = " + tomlString(command) + "\n" +
		"args = [\"mcp\"]\n" +
		tomlBlockEnd + "\n"
	out := strings.TrimRight(rest, "\n")
	if out != "" {
		out += "\n\n"
	}
	return t.write([]byte(out+block), raw, existed)
}

func (t AgentTarget) uninstallTOML() error {
	raw, err := os.ReadFile(t.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", t.Path, err)
	}
	rest := stripTOMLBlock(string(raw))
	if rest == string(raw) {
		return nil
	}
	if strings.TrimSpace(rest) == "" {
		if err := os.Remove(t.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", t.Path, err)
		}
		return nil
	}
	return t.write([]byte(strings.TrimRight(rest, "\n")+"\n"), raw, false)
}

// stripTOMLBlock removes dfmt's managed block (markers included).
func stripTOMLBlock(s string) string {
	start := strings.Index(s, tomlBlockBegin)
	if start < 0 {
		return s
	}
	end := strings.Index(s[start:], tomlBlockEnd)
	if end < 0 {
		return s
	}
	end += start + len(tomlBlockEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:start] + s[end:]
}

// tomlString renders s as a TOML basic string. strconv.Quote's escapes are
// a subset of TOML's for valid UTF-8, except \x, which it only emits for
// invalid bytes — never present in a resolved executable path.
func tomlString(s string) string {
	return strconv.Quote(s)
}
