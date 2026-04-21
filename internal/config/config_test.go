package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	// Version
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}

	// Capture defaults
	if !cfg.Capture.MCP.Enabled {
		t.Error("Capture.MCP.Enabled should be true")
	}
	if !cfg.Capture.FS.Enabled {
		t.Error("Capture.FS.Enabled should be true")
	}
	if cfg.Capture.FS.DebounceMS != 500 {
		t.Errorf("Capture.FS.DebounceMS = %d, want 500", cfg.Capture.FS.DebounceMS)
	}

	// Storage defaults
	if cfg.Storage.Durability != "batched" {
		t.Errorf("Storage.Durability = %q, want 'batched'", cfg.Storage.Durability)
	}
	if cfg.Storage.MaxBatchMS != 100 {
		t.Errorf("Storage.MaxBatchMS = %d, want 100", cfg.Storage.MaxBatchMS)
	}
	if cfg.Storage.JournalMaxBytes != 10*1024*1024 {
		t.Errorf("JournalMaxBytes = %d, want 10MB", cfg.Storage.JournalMaxBytes)
	}

	// Index defaults
	if cfg.Index.BM25K1 != 1.2 {
		t.Errorf("Index.BM25K1 = %f, want 1.2", cfg.Index.BM25K1)
	}
	if cfg.Index.BM25B != 0.75 {
		t.Errorf("Index.BM25B = %f, want 0.75", cfg.Index.BM25B)
	}

	// Transport defaults
	if cfg.Transport.MCP.Enabled != true {
		t.Error("Transport.MCP.Enabled should be true")
	}
	if cfg.Transport.Socket.Enabled != true {
		t.Error("Transport.Socket.Enabled should be true")
	}

	// Lifecycle defaults
	if cfg.Lifecycle.IdleTimeout != "30m" {
		t.Errorf("Lifecycle.IdleTimeout = %q, want '30m'", cfg.Lifecycle.IdleTimeout)
	}

	// Privacy defaults
	if cfg.Privacy.Telemetry != false {
		t.Error("Privacy.Telemetry should be false")
	}
}

func TestValidate(t *testing.T) {
	cfg := Default()

	// Valid config
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() failed: %v", err)
	}

	// Invalid durability
	cfg.Storage.Durability = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail for invalid durability")
	}
	cfg.Storage.Durability = "batched"

	// Invalid max_batch_ms
	cfg.Storage.MaxBatchMS = -1
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail for negative max_batch_ms")
	}
	cfg.Storage.MaxBatchMS = 100

	// Invalid idle_timeout
	cfg.Lifecycle.IdleTimeout = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail for invalid idle_timeout")
	}
}

func TestLoadGlobalConfig(t *testing.T) {
	// Create temp config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "dfmt", "config.yaml")

	// Create .dfmt directory
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	configContent := `
version: 1
capture:
  mcp:
    enabled: false
storage:
  durability: durable
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Set env to point to our config
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// MCP should be disabled by config
	if cfg.Capture.MCP.Enabled {
		t.Error("MCP.Enabled should be false from config")
	}
}

func TestMerge(t *testing.T) {
	cfg := Default()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.yaml")

	// Write a partial config
	configContent := `
capture:
  mcp:
    enabled: false
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Manually merge
	if err := merge(cfg, configPath); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	// MCP disabled by config (yaml.Unmarshal replaces entire struct)
	if cfg.Capture.MCP.Enabled {
		t.Error("MCP.Enabled should be false")
	}
}

func TestGlobalConfigPath(t *testing.T) {
	// With XDG_DATA_HOME
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	path := globalConfigPath()
	expected := filepath.Join(tmpDir, "dfmt", "config.yaml")
	if path != expected {
		t.Errorf("globalConfigPath() = %q, want %q", path, expected)
	}

	os.Unsetenv("XDG_DATA_HOME")

	// Without XDG_DATA_HOME, uses os.UserHomeDir() which on Windows
	// doesn't respect HOME env var. Skip this part on Windows.
	if runtime.GOOS != "windows" {
		home, err := os.UserHomeDir()
		if err == nil {
			os.Setenv("HOME", tmpDir)
			defer os.Unsetenv("HOME")

			path = globalConfigPath()
			expected = filepath.Join(home, ".local", "share", "dfmt", "config.yaml")
			if path != expected {
				t.Errorf("globalConfigPath() = %q, want %q", path, expected)
			}
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"1s", time.Second, false},
		{"1m", time.Minute, false},
		{"1h", time.Hour, false},
		{"500ms", 500 * time.Millisecond, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		got, err := ParseDuration(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDuration(%q) should fail", tt.input)
			}
		} else {
			if err != nil {
				t.Errorf("ParseDuration(%q) failed: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		}
	}
}

func TestValidateAllFields(t *testing.T) {
	cfg := Default()

	// Valid config should pass
	if err := cfg.Validate(); err != nil {
		t.Errorf("Default config should be valid: %v", err)
	}

	// Invalid shutdown_timeout
	cfg.Lifecycle.ShutdownTimeout = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Error("Validate should fail for invalid shutdown_timeout")
	}
}

func TestValidateJournalMaxBytes(t *testing.T) {
	cfg := Default()

	// Negative journal max bytes
	cfg.Storage.JournalMaxBytes = -1
	if err := cfg.Validate(); err == nil {
		t.Error("Validate should fail for negative journal_max_bytes")
	}
}

func TestLoadProjectConfig(t *testing.T) {
	// Create temp project with config
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)

	configContent := `
version: 1
capture:
  fs:
    enabled: false
`
	configPath := filepath.Join(dfmtDir, "config.yaml")
	os.WriteFile(configPath, []byte(configContent), 0644)

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// FS should be disabled by project config
	if cfg.Capture.FS.Enabled {
		t.Error("Capture.FS.Enabled should be false from project config")
	}
}

func TestLoadBothConfigs(t *testing.T) {
	// Create global config
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(globalDir, 0755)
	globalConfigPath := filepath.Join(globalDir, "config.yaml")

	// Create project config
	projectDir := filepath.Join(tmpDir, "project")
	dfmtDir := filepath.Join(projectDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)
	projectConfigPath := filepath.Join(dfmtDir, "config.yaml")

	globalContent := `
version: 1
capture:
  fs:
    enabled: true
  shell:
    enabled: false
`
	projectContent := `
version: 1
capture:
  fs:
    enabled: false
`

	os.WriteFile(globalConfigPath, []byte(globalContent), 0644)
	os.WriteFile(projectConfigPath, []byte(projectContent), 0644)

	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	cfg, err := Load(projectDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Project config should override global
	if cfg.Capture.FS.Enabled {
		t.Error("Capture.FS.Enabled should be false from project config")
	}

	// Global config values not overridden should remain
	if cfg.Capture.Shell.Enabled {
		t.Error("Capture.Shell.Enabled should be false from global config")
	}
}

func TestMergeNonExistent(t *testing.T) {
	cfg := Default()
	// Should not error for missing file
	err := merge(cfg, "/nonexistent/config.yaml")
	if err != nil {
		t.Errorf("merge should not error for missing file: %v", err)
	}
}

func TestLoadNonExistentProject(t *testing.T) {
	// Should return default config even for nonexistent path
	cfg, err := Load("/nonexistent/project/path")
	if err != nil {
		t.Fatalf("Load failed for nonexistent project: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}
	if cfg.Version != 1 {
		t.Errorf("Default config should have version 1, got %d", cfg.Version)
	}
}

func TestConfigFields(t *testing.T) {
	cfg := Default()

	// Test all field types
	if cfg.Storage.CompressRotated != true {
		t.Error("Storage.CompressRotated should be true by default")
	}

	if cfg.Storage.JournalMaxBytes != 10*1024*1024 {
		t.Errorf("Storage.JournalMaxBytes = %d, want 10MB", cfg.Storage.JournalMaxBytes)
	}

	if cfg.Retrieval.DefaultBudget != 4096 {
		t.Errorf("Retrieval.DefaultBudget = %d, want 4096", cfg.Retrieval.DefaultBudget)
	}

	if cfg.Index.BM25K1 != 1.2 {
		t.Errorf("Index.BM25K1 = %f, want 1.2", cfg.Index.BM25K1)
	}

	if cfg.Index.HeadingBoost != 5.0 {
		t.Errorf("Index.HeadingBoost = %f, want 5.0", cfg.Index.HeadingBoost)
	}

	if cfg.Transport.HTTP.Bind != "127.0.0.1:8765" {
		t.Errorf("Transport.HTTP.Bind = %s, want '127.0.0.1:8765'", cfg.Transport.HTTP.Bind)
	}

	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level = %s, want 'info'", cfg.Logging.Level)
	}

	if cfg.Logging.Format != "text" {
		t.Errorf("Logging.Format = %s, want 'text'", cfg.Logging.Format)
	}
}

func TestCaptureFSDefaults(t *testing.T) {
	cfg := Default()

	if len(cfg.Capture.FS.Watch) != 1 || cfg.Capture.FS.Watch[0] != "**" {
		t.Errorf("Capture.FS.Watch = %v, want ['**']", cfg.Capture.FS.Watch)
	}

	if len(cfg.Capture.FS.Ignore) != 3 {
		t.Errorf("Capture.FS.Ignore = %v, want 3 items", cfg.Capture.FS.Ignore)
	}
}

func TestRetrievalThrottleDefaults(t *testing.T) {
	cfg := Default()

	if cfg.Retrieval.Throttle.FirstTierCalls != 10 {
		t.Errorf("FirstTierCalls = %d, want 10", cfg.Retrieval.Throttle.FirstTierCalls)
	}
	if cfg.Retrieval.Throttle.SecondTierCalls != 5 {
		t.Errorf("SecondTierCalls = %d, want 5", cfg.Retrieval.Throttle.SecondTierCalls)
	}
	if cfg.Retrieval.Throttle.ResultsFirstTier != 20 {
		t.Errorf("ResultsFirstTier = %d, want 20", cfg.Retrieval.Throttle.ResultsFirstTier)
	}
	if cfg.Retrieval.Throttle.ResultsSecondTier != 10 {
		t.Errorf("ResultsSecondTier = %d, want 10", cfg.Retrieval.Throttle.ResultsSecondTier)
	}
}

func TestLoadWithEmptyProjectPath(t *testing.T) {
	// When projectPath is empty, Load should still work using global config only
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(globalDir, 0755)
	globalConfigPath := filepath.Join(globalDir, "config.yaml")

	configContent := `
version: 1
capture:
  mcp:
    enabled: false
`
	os.WriteFile(globalConfigPath, []byte(configContent), 0644)

	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Empty project path should still load global config
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") failed: %v", err)
	}
	// MCP should be disabled from global config
	if cfg.Capture.MCP.Enabled {
		t.Error("MCP.Enabled should be false from global config")
	}
}

func TestLoadWithGlobalConfigMergeError(t *testing.T) {
	// Create a global config with invalid YAML that will cause merge error
	tmpDir := t.TempDir()
	globalDir := filepath.Join(tmpDir, "dfmt")
	os.MkdirAll(globalDir, 0755)
	globalConfigPath := filepath.Join(globalDir, "config.yaml")

	// Write an invalid YAML (leading spaces not valid in YAML without document start)
	invalidContent := `version: 1
  invalid indent: value
`
	os.WriteFile(globalConfigPath, []byte(invalidContent), 0644)

	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Load should fail due to merge error
	_, err := Load("")
	if err == nil {
		t.Error("Load should fail when global config has invalid YAML")
	}
}

func TestLoadWithProjectConfigMergeError(t *testing.T) {
	// Create a project with invalid config YAML
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	os.MkdirAll(dfmtDir, 0755)
	configPath := filepath.Join(dfmtDir, "config.yaml")

	// Write invalid YAML
	invalidContent := `version: 1
  invalid indent: value
`
	os.WriteFile(configPath, []byte(invalidContent), 0644)

	os.Setenv("XDG_DATA_HOME", "/nonexistent")
	defer os.Unsetenv("XDG_DATA_HOME")

	// Load should fail due to project config merge error
	_, err := Load(tmpDir)
	if err == nil {
		t.Error("Load should fail when project config has invalid YAML")
	}
}

func TestGlobalConfigPathEmptyXDGHomeOnWindows(t *testing.T) {
	// On Windows, os.UserHomeDir() doesn't use HOME env var,
	// so we can only test that XDG_DATA_HOME is used when set
	// and that globalConfigPath() returns a valid path structure
	origXDG := os.Getenv("XDG_DATA_HOME")
	defer func() {
		if origXDG != "" {
			os.Setenv("XDG_DATA_HOME", origXDG)
		} else {
			os.Unsetenv("XDG_DATA_HOME")
		}
	}()

	os.Unsetenv("XDG_DATA_HOME")
	path := globalConfigPath()

	// On Windows, UserHomeDir should return something like C:\Users\<name>
	// The path should end with dfmt\config.yaml
	if !strings.HasSuffix(path, filepath.Join("dfmt", "config.yaml")) {
		t.Errorf("globalConfigPath() = %q, doesn't end with dfmt\\config.yaml", path)
	}
}

func TestGlobalConfigPathWithXDGHOME(t *testing.T) {
	// When XDG_DATA_HOME is set, it should be used
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	path := globalConfigPath()
	expected := filepath.Join(tmpDir, "dfmt", "config.yaml")
	if path != expected {
		t.Errorf("globalConfigPath() = %q, want %q", path, expected)
	}
}

func TestMergeReadFileError(t *testing.T) {
	cfg := Default()

	// Attempt to merge a non-existent path
	// Should not error for missing file (line 183-185 in config.go)
	err := merge(cfg, "/nonexistent/config.yaml")
	if err != nil {
		t.Errorf("merge should not error for missing file: %v", err)
	}
}

func TestMergeYAMLUnmarshalError(t *testing.T) {
	cfg := Default()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "bad.yaml")

	// Write invalid YAML content
	os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0644)

	err := merge(cfg, configPath)
	if err == nil {
		t.Error("merge should fail for invalid YAML")
	}
}
