package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgent(t *testing.T) {
	a := Agent{
		ID:         "claude-code",
		Name:       "Claude Code",
		Version:    "1.0.0",
		InstallDir: "/usr/local/bin",
		Detected:   true,
		Confidence: 0.95,
	}

	if a.ID != "claude-code" {
		t.Errorf("ID = %s, want claud-code", a.ID)
	}
	if a.Name != "Claude Code" {
		t.Errorf("Name = %s, want 'Claude Code'", a.Name)
	}
	if a.Confidence != 0.95 {
		t.Errorf("Confidence = %f, want 0.95", a.Confidence)
	}
}

func TestManifest(t *testing.T) {
	m := Manifest{
		Version:   1,
		Timestamp: "2024-01-01T00:00:00Z",
		Agents:    []AgentEntry{},
		Files:     []FileEntry{},
	}

	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
	if m.Timestamp != "2024-01-01T00:00:00Z" {
		t.Errorf("Timestamp = %s, want '2024-01-01T00:00:00Z'", m.Timestamp)
	}
}

func TestAgentEntry(t *testing.T) {
	e := AgentEntry{
		AgentID:    "claude-code",
		Configured: true,
		ConfigDir:  "/home/user/.claude",
	}

	if e.AgentID != "claude-code" {
		t.Errorf("AgentID = %s, want 'claude-code'", e.AgentID)
	}
	if !e.Configured {
		t.Error("Configured = false, want true")
	}
}

func TestFileEntry(t *testing.T) {
	e := FileEntry{
		Path:    "/home/user/.claude/settings.json",
		Agent:   "claude-code",
		Version: "1.0.0",
	}

	if e.Path != "/home/user/.claude/settings.json" {
		t.Errorf("Path = %s, want '/home/user/.claude/settings.json'", e.Path)
	}
}

func TestDetect(t *testing.T) {
	agents := Detect()
	// May or may not find agents depending on system
	if agents == nil {
		t.Error("Detect returned nil")
	}
}

func TestDetectWithOverride(t *testing.T) {
	// With no override, should return all detected
	all := Detect()
	override := DetectWithOverride(nil)
	if len(override) != len(all) {
		t.Errorf("DetectWithOverride(nil) length = %d, want %d", len(override), len(all))
	}

	// With empty override, should also return all
	override = DetectWithOverride([]string{})
	if len(override) != len(all) {
		t.Errorf("DetectWithOverride([]) length = %d, want %d", len(override), len(all))
	}

	// With specific ID that won't exist
	override = DetectWithOverride([]string{"nonexistent-agent"})
	if len(override) != 0 {
		t.Errorf("DetectWithOverride(['nonexistent-agent']) length = %d, want 0", len(override))
	}
}

func TestManifestPath(t *testing.T) {
	path := ManifestPath()
	if path == "" {
		t.Error("ManifestPath returned empty string")
	}
	if !strings.Contains(path, "dfmt") {
		t.Errorf("ManifestPath = %s, doesn't contain 'dfmt'", path)
	}
}

func TestLoadManifestNonExistent(t *testing.T) {
	// Set a temp path via environment
	os.Setenv("XDG_DATA_HOME", "/tmp/nonexistent")
	defer os.Unsetenv("XDG_DATA_HOME")

	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
}

func TestSaveAndLoadManifest(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	m := &Manifest{
		Version:   1,
		Timestamp: "2024-01-01T00:00:00Z",
		Agents: []AgentEntry{
			{AgentID: "claude-code", Configured: true},
		},
	}

	if err := SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest failed: %v", err)
	}

	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if loaded.Version != 1 {
		t.Errorf("loaded.Version = %d, want 1", loaded.Version)
	}
}

func TestBackupFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	backupPath := configPath + ".dfmt.bak"

	// Write original file
	os.WriteFile(configPath, []byte(`{"key":"value"}`), 0644)

	// Backup
	if err := BackupFile(configPath); err != nil {
		t.Fatalf("BackupFile failed: %v", err)
	}

	// Check backup exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("Backup file was not created")
	}

	// Check content
	data, _ := os.ReadFile(backupPath)
	if string(data) != `{"key":"value"}` {
		t.Errorf("Backup content = %s, want '{\"key\":\"value\"}'", string(data))
	}
}

func TestBackupFileNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentPath := filepath.Join(tmpDir, "nonexistent.json")

	// Backup of non-existent should succeed
	if err := BackupFile(nonExistentPath); err != nil {
		t.Fatalf("BackupFile failed for non-existent: %v", err)
	}
}

func TestDetectClaudeCode(t *testing.T) {
	// This just checks the function runs - actual detection is system dependent
	a := detectClaudeCode()
	if a == nil {
		t.Log("Claude Code not detected (expected on some systems)")
	} else {
		if a.ID != "claude-code" {
			t.Errorf("ID = %s, want 'claude-code'", a.ID)
		}
		if a.Name != "Claude Code" {
			t.Errorf("Name = %s, want 'Claude Code'", a.Name)
		}
	}
}

func TestDetectCodex(t *testing.T) {
	a := detectCodex()
	if a == nil {
		t.Log("Codex not detected (expected on some systems)")
	} else {
		if a.ID != "codex" {
			t.Errorf("ID = %s, want 'codex'", a.ID)
		}
	}
}

func TestGetClaudeVersion(t *testing.T) {
	version := getClaudeVersion("/nonexistent/path")
	if version != "unknown" {
		t.Errorf("version = %s, want 'unknown'", version)
	}
}

func TestGetCodexVersion(t *testing.T) {
	version := getCodexVersion("/nonexistent/path")
	if version != "unknown" {
		t.Errorf("version = %s, want 'unknown'", version)
	}
}
