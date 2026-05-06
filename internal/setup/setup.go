package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

const maxManifestBytes = 256 << 10

// IsWSL reports true when the current process is running inside WSL.
// Detection checks for the "microsoft" or "WSL" signature in /proc/version,
// which is the canonical indicator used by WSL itself and tools like VS Code.
func IsWSL() bool {
	if runtime.GOOS == goosWindows {
		return false
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(data), []byte("microsoft")) ||
		bytes.Contains(bytes.ToLower(data), []byte("wsl"))
}

// TargetOS represents the operating system environment of an agent config.
type TargetOS int

const (
	TargetOSLocal   TargetOS = iota // current process OS
	TargetOSWindows                 // Windows agent config
	TargetOSUnix                    // Linux/macOS/WSL agent config
)

// ResolveDFMTCommandForEnv returns an absolute path (or command name) for
// the dfmt binary suitable for writing into an MCP config file that will be
// read by an agent running on the specified target OS.
//
// When target is TargetOSLocal, this is equivalent to ResolveDFMTCommand().
// When target is TargetOSWindows, the returned path uses Windows format
// (C:\...) even if the current process is running on WSL -- because the
// Windows-side agent (e.g. Windows Claude Code) cannot interpret Unix paths.
// When target is TargetOSUnix, the returned path uses Unix format even if the
// current process is running on Windows -- because the Unix agent cannot
// interpret Windows paths.
//
// For WSL agents writing Windows configs: if dfmt was installed via WSL's
// install.sh, the binary lives in ~/.dfmt/ and the path must be converted
// to the Windows equivalent (USERPROFILE/.dfmt/dfmt.exe) for Windows agents
// to find it.
func ResolveDFMTCommandForEnv(target TargetOS) string {
	// Try LookPath first (same logic as ResolveDFMTCommand)
	if path, err := exec.LookPath("dfmt"); err == nil && path != "" {
		if target == TargetOSWindows && IsWSL() {
			// We are on WSL but writing a Windows config.
			// LookPath returned a WSL path like /home/user/.dfmt/dfmt.
			// Convert to Windows path.
			path = wslPathToWindowsPath(path)
		}
		if abs, aerr := filepath.Abs(path); aerr == nil {
			return abs
		}
		return path
	}
	// Fall back to os.Executable
	if ex, err := os.Executable(); err == nil && ex != "" {
		if target == TargetOSWindows && IsWSL() {
			ex = wslPathToWindowsPath(ex)
		}
		return ex
	}
	return "dfmt"
}

// wslPathToWindowsPath converts a WSL Unix path to a Windows path.
// Converts /home/<user>/.dfmt/dfmt -> C:\Users\<user>\.dfmt\dfmt.exe
// Converts /mnt/c/... -> C:\...
func wslPathToWindowsPath(p string) string {
	// /home/<user> -> C:\Users\<user>
	if strings.HasPrefix(p, "/home/") {
		rest := strings.TrimPrefix(p, "/home/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) >= 1 {
			user := parts[0]
			remainder := ""
			if len(parts) > 1 {
				remainder = "/" + parts[1]
			}
			// Convert remaining Unix path segments to Windows
			winRemainder := strings.ReplaceAll(remainder, "/", "\\")
			return "C:\\Users\\" + user + winRemainder
		}
	}
	// /mnt/c/... -> C:\...
	if strings.HasPrefix(p, "/mnt/c") {
		return "C:" + strings.ReplaceAll(p[5:], "/", "\\")
	}
	// already a Windows path or can't convert
	return p
}

// ResolveDFMTCommand returns the command string to embed in MCP `command`
// fields written into agent configs. Resolution order:
//
//  1. exec.LookPath("dfmt") -- absolute path of whichever dfmt is first on PATH.
//     This is the right answer when the user just installed via dev.ps1/install.sh
//     because PATH includes the canonical install directory.
//  2. os.Executable() -- absolute path of the currently running binary. Used
//     when dfmt is invoked from a directory that isn't on PATH (rare; e.g.
//     freshly built `./dfmt setup` from the repo root).
//  3. Literal "dfmt" -- last-resort relative fallback. The agent will need
//     dfmt on its PATH at launch time for this to work.
//
// On WSL, exec.LookPath and os.Executable both return Linux-style paths
// (/home/... or /proc/...). These are correct for WSL-native agents but
// must NOT be written into configs that Windows-side agents (e.g. Windows
// Claude Code) will read -- Windows cannot interpret Unix paths.
// The callers in detect.go decide which config file to write and apply
// the appropriate path format for the target environment.
func ResolveDFMTCommand() string {
	return ResolveDFMTCommandForEnv(TargetOSLocal)
}

// HomeDir returns the user's home directory, respecting HOME env var for testability.
// On Windows, $HOME is ignored unless it's a native absolute path — git-bash/MSYS
// set it to Unix-style forms like "/c/Users/foo" which filepath.Join cannot use.
func HomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		if runtime.GOOS != goosWindows || filepath.IsAbs(home) {
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

// FileKind drives uninstall's per-entry teardown strategy. Empty string
// (zero value) means delete-the-whole-file, which is the historical
// behavior for entries written before this field existed; new code
// should set Kind explicitly.
const (
	// FileKindDelete: os.Remove the path on uninstall, optionally
	// restoring a .dfmt.bak backup if one was captured. Default for
	// files DFMT created from scratch (~/.claude/mcp.json,
	// ~/.codex/mcp.json, etc.).
	FileKindDelete = "delete"

	// FileKindStrip: the file is a *user-owned* document (CLAUDE.md,
	// AGENTS.md, .cursorrules) into which DFMT injected a marker-
	// delimited block. Uninstall removes only the block, leaving the
	// rest of the file alone. If the file is empty after stripping
	// (DFMT was the sole resident), it is removed.
	FileKindStrip = "strip"
)

// FileEntry records a file written by setup.
//
// All fields use omitempty for both encodings. The legacy on-disk
// manifests written by older dfmt versions had unconditional fields
// (`"Path":""`, etc.); JSON unmarshal is case-insensitive and accepts
// missing fields as zero values, so dropping the empty serializations
// is forward-compatible. The asymmetry where only Kind had omitempty
// produced manifests like:
//
//	{"Path":"a","Agent":"b","Version":"v1"}                       // legacy
//	{"Path":"c","Agent":"d","Version":"v1","Kind":"strip"}        // new
//
// which made diffing across versions noisy. Now all entries write
// only the fields they populate.
type FileEntry struct {
	Path    string `yaml:"path,omitempty" json:",omitempty"`
	Agent   string `yaml:"agent,omitempty" json:",omitempty"`
	Version string `yaml:"version,omitempty" json:",omitempty"`
	// Kind selects the uninstall strategy. Empty string is treated as
	// FileKindDelete so manifests written by older dfmt versions still
	// uninstall correctly.
	Kind string `yaml:"kind,omitempty" json:",omitempty"`
}

// samePath compares two filesystem paths for equality. On Windows the
// underlying filesystem is case-insensitive, so the comparison is too;
// on Unix-like systems it is exact.
func samePath(a, b string) bool {
	if runtime.GOOS == goosWindows {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// AddFile upserts a FileEntry into the manifest, replacing any existing
// entry that targets the same Path. Without this, every `dfmt setup`
// run would append a fresh row per agent and the manifest would grow
// unboundedly across re-runs.
func (m *Manifest) AddFile(entry FileEntry) {
	for i, existing := range m.Files {
		if samePath(existing.Path, entry.Path) {
			m.Files[i] = entry
			return
		}
	}
	m.Files = append(m.Files, entry)
}

// dedupFiles collapses any duplicate-Path entries that may have accumulated
// from older dfmt versions that blindly appended on every setup run. The
// last entry wins so the most recent agent/version metadata is preserved.
func dedupFiles(files []FileEntry) []FileEntry {
	if len(files) < 2 {
		return files
	}
	out := make([]FileEntry, 0, len(files))
	for _, f := range files {
		replaced := false
		for i, existing := range out {
			if samePath(existing.Path, f.Path) {
				out[i] = f
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, f)
		}
	}
	return out
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
	data, err := readManifestFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{Version: 1}, nil
		}
		return nil, err
	}

	var m Manifest
	// Try JSON first, fall back to YAML
	if err := decodeManifestJSON(data, &m); err != nil {
		if err := decodeManifestYAML(data, &m); err != nil {
			return nil, err
		}
	}
	m.Files = dedupFiles(m.Files)
	return &m, nil
}

func decodeManifestJSON(data []byte, m *Manifest) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(m); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("manifest JSON must contain exactly one value")
	}
	return nil
}

func decodeManifestYAML(data []byte, m *Manifest) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("manifest YAML must contain exactly one document")
	}
	return nil
}

func readManifestFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("manifest file too large: exceeds %d bytes", maxManifestBytes)
	}
	return data, nil
}

// SaveManifest writes the setup manifest.
func SaveManifest(m *Manifest) error {
	path := ManifestPath()
	dir := filepath.Dir(path)
	// 0o700 to match the manifest file (0600). Idempotent on existing dirs —
	// a pre-existing user-managed parent keeps its mode.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create manifest dir: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the manifest lists every file DFMT has installed or modified
	// in user config dirs — useful info for an attacker planning tampering.
	// V-20: route through safefs.WriteFileAtomic so a symlink planted at
	// the manifest path can't redirect the write outside the intended
	// directory. Atomic rename also avoids torn writes if a concurrent
	// dfmt setup --uninstall reads mid-write.
	return safefs.WriteFileAtomic(dir, path, data, 0o600)
}

// BackupFile creates a backup of a config file before modification.
// BackupFile writes a .dfmt.bak sibling of path. If one already exists, the
// existing backup is preserved (rolled to .dfmt.bak.<unix-nano>) so a second
// setup run can't clobber the pristine copy captured on the first run.
//
// safefs.WriteFile gates the write on a Lstat-walk that refuses any symlink
// in the parent path or at the backup target itself — closes F-07 (attacker
// plants `path.dfmt.bak -> /etc/cron.d/x` so that BackupFile writes the
// original config contents into the symlink target).
func BackupFile(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve backup source: %w", err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	backup := absPath + ".dfmt.bak"
	if _, statErr := os.Stat(backup); statErr == nil {
		rolled := fmt.Sprintf("%s.%d", backup, time.Now().UnixNano())
		if renameErr := os.Rename(backup, rolled); renameErr != nil {
			return fmt.Errorf("preserve existing backup %s: %w", backup, renameErr)
		}
	}
	// Preserve the source mode if we can stat it; otherwise default to 0600
	// so a backup of a previously-0600 file isn't suddenly world-readable.
	mode := os.FileMode(0o600)
	if fi, ferr := os.Stat(absPath); ferr == nil {
		mode = fi.Mode().Perm()
	}
	return safefs.WriteFile(filepath.Dir(absPath), backup, data, mode)
}
