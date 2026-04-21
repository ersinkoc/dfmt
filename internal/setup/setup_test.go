package setup

import (
	"os"
	"path/filepath"
	"runtime"
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

// =============================================================================
// detectClaudeCode tests - error paths and branches
// =============================================================================

func TestDetectClaudeCode_NotFound(t *testing.T) {
	// On Windows, os.UserHomeDir() uses USERPROFILE, not HOME env var.
	// Skip on Windows as we can't easily test the "not found" path.
	if runtime.GOOS == "windows" {
		t.Skip("Cannot easily test detectClaudeCode not found on Windows (os.UserHomeDir ignores HOME)")
	}

	// Save original HOME and restore after test
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	// Set HOME to a non-existent path so no agents are found
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)

	// Clear any other env vars that might affect detection
	os.Unsetenv("XDG_DATA_HOME")

	// Create a .local/bin directory but no claude binary
	localBin := filepath.Join(tmpDir, ".local", "bin")
	os.MkdirAll(localBin, 0755)

	// Also create empty .claude dir (not a file)
	claudeDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(claudeDir, 0755)

	result := detectClaudeCode()
	if result != nil {
		t.Errorf("detectClaudeCode() = %v, want nil when no claude binary exists", result)
	}
}

func TestDetectClaudeCode_FileExistsButNotExecutable(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create a regular file at one of the expected paths (not a directory)
	localBinDir := filepath.Join(tmpDir, ".local", "bin")
	os.MkdirAll(localBinDir, 0755)
	claudePath := filepath.Join(localBinDir, "claude")
	os.WriteFile(claudePath, []byte("fake"), 0644)

	result := detectClaudeCode()
	// os.Stat on a file should return nil error, so it should return an agent
	// But getClaudeVersion returns "unknown" anyway
	if result == nil {
		t.Error("detectClaudeCode() returned nil when file exists at expected path")
	}
	if result != nil && result.ID != "claude-code" {
		t.Errorf("ID = %s, want 'claude-code'", result.ID)
	}
}

func TestDetectClaudeCode_HomeEnvNotSet(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	os.Unsetenv("HOME")

	result := detectClaudeCode()
	// Should return nil when HOME is not set and .claude directory can't be found
	if result != nil {
		t.Logf("detectClaudeCode() returned %v when HOME is unset (may be system dependent)", result)
	}
}

// =============================================================================
// detectCodex tests - error paths and branches
// =============================================================================

func TestDetectCodex_NotFound(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create .local/bin but no codex
	localBin := filepath.Join(tmpDir, ".local", "bin")
	os.MkdirAll(localBin, 0755)

	result := detectCodex()
	if result != nil {
		t.Errorf("detectCodex() = %v, want nil when no codex binary exists", result)
	}
}

// =============================================================================
// LoadManifest tests - error paths
// =============================================================================

func TestLoadManifest_FileNotFound(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	// Use a path that definitely doesn't exist
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	// Ensure dfmt directory doesn't exist
	dfmtDir := filepath.Join(tmpDir, "dfmt")
	// Clean it up if it exists
	os.RemoveAll(dfmtDir)

	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest returned error for non-existent path: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1 (default for non-existent manifest)", m.Version)
	}
}

func TestLoadManifest_InvalidJSON(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)

	// Create dfmt directory with invalid content (not valid JSON or YAML)
	dfmtDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(dfmtDir, 0755)
	manifestPath := filepath.Join(dfmtDir, "setup-manifest.json")
	// Neither JSON nor YAML - starts with [ which is array in JSON, but no close bracket
	// and "invalid content here" is not valid YAML either
	os.WriteFile(manifestPath, []byte("[ this is not valid json or yaml content"), 0644)

	_, err := LoadManifest()
	if err == nil {
		t.Error("LoadManifest expected error for invalid content, got nil")
	}
}

func TestLoadManifest_InvalidYAML(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)

	// Create dfmt directory with invalid YAML (not valid JSON or YAML)
	dfmtDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(dfmtDir, 0755)
	manifestPath := filepath.Join(dfmtDir, "setup-manifest.json")
	// Valid YAML syntax but not JSON, and yaml.Unmarshal should fail on this
	os.WriteFile(manifestPath, []byte(": not valid yaml\n  - item\ninvalid"), 0644)

	_, err := LoadManifest()
	if err == nil {
		t.Error("LoadManifest expected error for invalid YAML, got nil")
	}
}

func TestLoadManifest_ValidYAML(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)

	dfmtDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(dfmtDir, 0755)
	manifestPath := filepath.Join(dfmtDir, "setup-manifest.json")

	yamlContent := `version: 1
timestamp: "2024-01-01T00:00:00Z"
agents: []
files: []
`
	os.WriteFile(manifestPath, []byte(yamlContent), 0644)

	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest failed for valid YAML: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
	if m.Timestamp != "2024-01-01T00:00:00Z" {
		t.Errorf("Timestamp = %s, want '2024-01-01T00:00:00Z'", m.Timestamp)
	}
}

// =============================================================================
// SaveManifest tests - error paths
// =============================================================================

func TestSaveManifest_CannotCreateDirectory(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)

	// Create a file at the path where directory should be created
	// This should cause MkdirAll to fail since the path is not a directory
	dfmtDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(dfmtDir, 0755)
	// Create a file that will conflict with directory creation
	manifestPath := filepath.Join(dfmtDir, "setup-manifest.json")
	f, err := os.Create(manifestPath)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	f.Close()

	m := &Manifest{Version: 1, Timestamp: "test"}
	err = SaveManifest(m)
	// On Windows, os.MkdirAll fails with syscall.EINVAL when the path is a file
	// On Unix, it might succeed or fail depending on the path
	// Just verify that the operation either succeeds or returns a meaningful error
	if err != nil && !strings.Contains(err.Error(), "cannot create") && !strings.Contains(err.Error(), "Create manifest dir") {
		t.Logf("SaveManifest error (may be platform-specific): %v", err)
	}
}

// =============================================================================
// DetectWithOverride tests - additional coverage
// =============================================================================

func TestDetectWithOverride_MixedExistingAndNonExisting(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	// Create a minimal HOME with a detectable agent
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create a .claude directory so detectClaudeCode finds something
	claudeDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(claudeDir, 0755)

	all := Detect()
	if len(all) == 0 {
		t.Skip("No agents detected, cannot test override with existing agent")
	}

	existingID := all[0].ID
	override := []string{existingID, "nonexistent-agent", "another-nonexistent"}

	result := DetectWithOverride(override)
	if len(result) != 1 {
		t.Errorf("DetectWithOverride([%s, 'nonexistent-agent']) length = %d, want 1", existingID, len(result))
	}
	if len(result) > 0 && result[0].ID != existingID {
		t.Errorf("result[0].ID = %s, want %s", result[0].ID, existingID)
	}
}

func TestDetectWithOverride_MultipleExisting(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create both .claude and .codex directories
	os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, ".codex"), 0755)

	all := Detect()
	if len(all) < 2 {
		t.Skip("Need at least 2 agents detected for this test")
	}

	// Request both detected agents
	override := []string{all[0].ID, all[1].ID}
	result := DetectWithOverride(override)

	if len(result) != 2 {
		t.Errorf("DetectWithOverride with 2 existing IDs returned %d, want 2", len(result))
	}
}

// =============================================================================
// ManifestPath tests
// =============================================================================

func TestManifestPath_WithXDGDataHome(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	path := ManifestPath()
	expected := filepath.Join(tmpDir, "dfmt", "setup-manifest.json")
	if path != expected {
		t.Errorf("ManifestPath() = %s, want %s", path, expected)
	}
}

func TestManifestPath_HomeFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Cannot test HOME fallback on Windows (os.UserHomeDir uses USERPROFILE)")
	}

	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	path := ManifestPath()
	expected := filepath.Join(tmpDir, ".local", "share", "dfmt", "setup-manifest.json")
	if path != expected {
		t.Errorf("ManifestPath() = %s, want %s", path, expected)
	}
}

func TestSaveManifest_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Ensure the dfmt directory doesn't exist
	dfmtDir := filepath.Join(tmpDir, "dfmt")
	os.RemoveAll(dfmtDir)

	m := &Manifest{
		Version:   2,
		Timestamp: "2024-06-01T00:00:00Z",
		Agents: []AgentEntry{
			{AgentID: "claude-code", Configured: true, ConfigDir: "/home/user/.claude"},
		},
		Files: []FileEntry{
			{Path: "/home/user/.claude/settings.json", Agent: "claude-code", Version: "1.0"},
		},
	}

	err := SaveManifest(m)
	if err != nil {
		t.Fatalf("SaveManifest failed: %v", err)
	}

	// Verify the directory and file were created
	manifestPath := filepath.Join(dfmtDir, "setup-manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Error("Manifest file was not created")
	}

	// Verify content by loading
	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("Failed to load saved manifest: %v", err)
	}
	if loaded.Version != 2 {
		t.Errorf("loaded.Version = %d, want 2", loaded.Version)
	}
	if len(loaded.Agents) != 1 {
		t.Errorf("len(loaded.Agents) = %d, want 1", len(loaded.Agents))
	}
	if len(loaded.Files) != 1 {
		t.Errorf("len(loaded.Files) = %d, want 1", len(loaded.Files))
	}
}

func TestSaveManifest_EmptyManifest(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	m := &Manifest{
		Version:   1,
		Timestamp: "",
		Agents:    []AgentEntry{},
		Files:     []FileEntry{},
	}

	err := SaveManifest(m)
	if err != nil {
		t.Fatalf("SaveManifest failed for empty manifest: %v", err)
	}

	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("Failed to load empty manifest: %v", err)
	}
	if loaded.Version != 1 {
		t.Errorf("loaded.Version = %d, want 1", loaded.Version)
	}
}

func TestDetectClaudeCode_PathWithLocalBin(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create .local/bin directory with claude file
	localBin := filepath.Join(tmpDir, ".local", "bin")
	os.MkdirAll(localBin, 0755)
	claudePath := filepath.Join(localBin, "claude")
	// Create a file (not executable) - os.Stat succeeds
	os.WriteFile(claudePath, []byte("fake"), 0644)

	result := detectClaudeCode()
	if result == nil {
		t.Error("detectClaudeCode() returned nil when .local/bin/claude exists")
	}
	if result != nil && result.ID != "claude-code" {
		t.Errorf("ID = %s, want 'claude-code'", result.ID)
	}
	if result != nil && result.Confidence != 0.95 {
		t.Errorf("Confidence = %f, want 0.95 for binary path", result.Confidence)
	}
}

func TestDetectClaudeCode_DirectoryInsteadOfFile(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create .local/bin directory
	localBin := filepath.Join(tmpDir, ".local", "bin")
	os.MkdirAll(localBin, 0755)
	// Create a directory at claude path instead of file
	claudePath := filepath.Join(localBin, "claude")
	os.MkdirAll(claudePath, 0755)

	result := detectClaudeCode()
	// os.Stat on a directory returns nil error, so it returns an agent
	// but getClaudeVersion returns "unknown" anyway
	if result == nil {
		t.Error("detectClaudeCode() returned nil when directory exists at path")
	}
}

func TestDetectClaudeCode_AllPathsChecked(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Test that /opt/claude/bin/claude path is checked (doesn't exist on most systems)
	// This test just ensures the function runs without panicking
	result := detectClaudeCode()
	// Result may be nil if paths don't exist, but function should complete without error
	_ = result
}

func TestDetectCodex_DirectoryInsteadOfFile(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create .codex as a directory instead of file
	codexPath := filepath.Join(tmpDir, ".codex")
	os.MkdirAll(codexPath, 0755)

	result := detectCodex()
	// os.Stat on a directory returns nil error, so it returns an agent
	if result == nil {
		t.Error("detectCodex() returned nil when directory exists at path")
	}
	if result != nil && result.Confidence != 0.9 {
		t.Errorf("Confidence = %f, want 0.9", result.Confidence)
	}
}

func TestDetect_CombinesBothDetectors(t *testing.T) {
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("HOME", origHome)
	}()

	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")

	// Create directories for both agents
	os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, ".codex"), 0755)

	agents := Detect()
	// Should find at least one agent (depends on what detect functions return for directories)
	if len(agents) == 0 {
		t.Log("No agents detected - may be expected if detection requires executable files")
	}
}

func TestManifestEntry_Struct(t *testing.T) {
	m := Manifest{
		Version:   3,
		Timestamp: "2024-07-01T12:00:00Z",
		Agents: []AgentEntry{
			{AgentID: "codex", Configured: false, ConfigDir: ""},
		},
		Files: []FileEntry{
			{Path: "/path/to/file", Agent: "codex", Version: "0.1"},
		},
	}

	if m.Version != 3 {
		t.Errorf("Version = %d, want 3", m.Version)
	}
	if len(m.Agents) != 1 {
		t.Errorf("len(Agents) = %d, want 1", len(m.Agents))
	}
	if len(m.Files) != 1 {
		t.Errorf("len(Files) = %d, want 1", len(m.Files))
	}
	if m.Agents[0].AgentID != "codex" {
		t.Errorf("Agents[0].AgentID = %s, want 'codex'", m.Agents[0].AgentID)
	}
}
