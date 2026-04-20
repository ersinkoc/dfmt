package setup

import (
	"os"
	"path/filepath"
)

func detectClaudeCode() *Agent {
	paths := []string{
		filepath.Join(os.Getenv("HOME"), ".claude"),
		filepath.Join(os.Getenv("HOME"), ".local", "bin", "claude"),
		"/usr/local/bin/claude",
		"/opt/claude/bin/claude",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			version := getClaudeVersion(p)
			return &Agent{
				ID:         "claude-code",
				Name:       "Claude Code",
				Version:    version,
				InstallDir: filepath.Dir(p),
				Detected:   true,
				Confidence: 0.95,
			}
		}
	}

	// Check for .claude directory
	home, _ := os.UserHomeDir()
	claudeDir := filepath.Join(home, ".claude")
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		return &Agent{
			ID:         "claude-code",
			Name:       "Claude Code",
			Version:    "detected",
			InstallDir: claudeDir,
			Detected:   true,
			Confidence: 0.8,
		}
	}

	return nil
}

func detectCodex() *Agent {
	paths := []string{
		filepath.Join(os.Getenv("HOME"), ".codex"),
		filepath.Join(os.Getenv("HOME"), ".local", "bin", "codex"),
		"/usr/local/bin/codex",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			version := getCodexVersion(p)
			return &Agent{
				ID:         "codex",
				Name:       "Codex CLI",
				Version:    version,
				InstallDir: filepath.Dir(p),
				Detected:   true,
				Confidence: 0.9,
			}
		}
	}

	return nil
}

func getClaudeVersion(path string) string {
	return "unknown"
}

func getCodexVersion(path string) string {
	return "unknown"
}