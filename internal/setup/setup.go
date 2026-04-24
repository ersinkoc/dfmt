package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

// HomeDir returns the user's home directory, respecting HOME env var for testability.
// On Windows, $HOME is ignored unless it's a native absolute path — git-bash/MSYS
// set it to Unix-style forms like "/c/Users/foo" which filepath.Join cannot use.
func HomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		if runtime.GOOS != "windows" || filepath.IsAbs(home) {
			return home
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// Agent represents a detected AI coding agent.
type Agent struct {
	ID         string // "claude-code", "codex", etc.
	Name       string // Display name
	Version    string
	InstallDir string
	Detected   bool
	Confidence float64 // 0.0-1.0
}

// Manifest tracks files written by dfmt setup.
type Manifest struct {
	Version   int          `yaml:"version"`
	Timestamp string       `yaml:"timestamp"`
	Agents    []AgentEntry `yaml:"agents"`
	Files     []FileEntry  `yaml:"files"`
}

// AgentEntry records agent setup state.
type AgentEntry struct {
	AgentID    string `yaml:"agent_id"`
	Configured bool   `yaml:"configured"`
	ConfigDir  string `yaml:"config_dir"`
}

// FileEntry records a file written by setup.
type FileEntry struct {
	Path    string `yaml:"path"`
	Agent   string `yaml:"agent"`
	Version string `yaml:"version"`
}

// RecordAgent bumps the manifest timestamp and upserts an AgentEntry
// marking the given agent as configured.
func (m *Manifest) RecordAgent(agentID, configDir string) {
	if m.Version == 0 {
		m.Version = 1
	}
	m.Timestamp = time.Now().UTC().Format(time.RFC3339)
	for i, a := range m.Agents {
		if a.AgentID == agentID {
			m.Agents[i].Configured = true
			m.Agents[i].ConfigDir = configDir
			return
		}
	}
	m.Agents = append(m.Agents, AgentEntry{
		AgentID:    agentID,
		Configured: true,
		ConfigDir:  configDir,
	})
}

// Detect runs filesystem probes to find installed agents.
func Detect() []Agent {
	agents := []Agent{}

	if a := detectClaudeCode(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectCursor(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectVSCode(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectCodex(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectGemini(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectWindsurf(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectZed(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectContinue(); a != nil {
		agents = append(agents, *a)
	}
	if a := detectOpenCode(); a != nil {
		agents = append(agents, *a)
	}

	return agents
}

// DetectWithOverride filters detection to specific agent IDs.
func DetectWithOverride(override []string) []Agent {
	all := Detect()
	if len(override) == 0 {
		return all
	}

	var result []Agent
	for _, id := range override {
		for _, a := range all {
			if a.ID == id {
				result = append(result, a)
				break
			}
		}
	}
	return result
}

// ManifestPath returns the path to the setup manifest.
func ManifestPath() string {
	xdgDataHome := os.Getenv("XDG_DATA_HOME")
	if xdgDataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		xdgDataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdgDataHome, "dfmt", "setup-manifest.json")
}

// LoadManifest loads the setup manifest.
func LoadManifest() (*Manifest, error) {
	path := ManifestPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{Version: 1}, nil
		}
		return nil, err
	}

	var m Manifest
	// Try JSON first, fall back to YAML
	if err := json.Unmarshal(data, &m); err != nil {
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// SaveManifest writes the setup manifest.
func SaveManifest(m *Manifest) error {
	path := ManifestPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create manifest dir: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the manifest lists every file DFMT has installed or modified
	// in user config dirs — useful info for an attacker planning tampering.
	return os.WriteFile(path, data, 0o600)
}

// BackupFile creates a backup of a config file before modification.
// BackupFile writes a .dfmt.bak sibling of path. If one already exists, the
// existing backup is preserved (rolled to .dfmt.bak.<unix-nano>) so a second
// setup run can't clobber the pristine copy captured on the first run.
func BackupFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	backup := path + ".dfmt.bak"
	if _, statErr := os.Stat(backup); statErr == nil {
		rolled := fmt.Sprintf("%s.%d", backup, time.Now().UnixNano())
		if renameErr := os.Rename(backup, rolled); renameErr != nil {
			return fmt.Errorf("preserve existing backup %s: %w", backup, renameErr)
		}
	}
	// Preserve the source mode if we can stat it; otherwise default to 0600
	// so a backup of a previously-0600 file isn't suddenly world-readable.
	mode := os.FileMode(0o600)
	if fi, ferr := os.Stat(path); ferr == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(backup, data, mode)
}
