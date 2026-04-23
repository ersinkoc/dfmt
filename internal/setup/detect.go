package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Agent IDs used across the codebase.
const (
	AgentClaudeCode = "claude-code"
	AgentCursor     = "cursor"
	AgentVSCode     = "vscode"
	AgentCodex      = "codex"
	AgentGemini     = "gemini"
	AgentWindsurf   = "windsurf"
	AgentZed        = "zed"
	AgentContinue   = "continue"
	AgentOpenCode   = "opencode"
)

const goosWindows = "windows"

func lookPath(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if runtime.GOOS != goosWindows {
		return ""
	}
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		if path, err := exec.LookPath(name + ext); err == nil {
			return path
		}
	}
	return ""
}

type agentSpec struct {
	id               string
	name             string
	paths            []string
	binary           string
	confidencePath   float64
	confidenceBinary float64
	skipOnWindows    bool
}

func detectAgent(spec agentSpec) *Agent {
	for _, p := range spec.paths {
		if _, err := os.Stat(p); err == nil {
			return &Agent{
				ID:         spec.id,
				Name:       spec.name,
				Version:    "detected",
				InstallDir: filepath.Dir(p),
				Detected:   true,
				Confidence: spec.confidencePath,
			}
		}
	}

	if spec.skipOnWindows && runtime.GOOS == goosWindows {
		return nil
	}

	if spec.binary != "" {
		if lp := lookPath(spec.binary); lp != "" {
			return &Agent{
				ID:         spec.id,
				Name:       spec.name,
				Version:    "detected",
				InstallDir: filepath.Dir(lp),
				Detected:   true,
				Confidence: spec.confidenceBinary,
			}
		}
	}

	return nil
}

func detectClaudeCode() *Agent {
	home := HomeDir()
	binaryPaths := []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, "AppData", "Roaming", "Claude"),
		"/usr/local/bin/claude",
		"/opt/claude/bin/claude",
	}
	for _, bp := range binaryPaths {
		if _, err := os.Stat(bp); err == nil {
			return &Agent{ID: AgentClaudeCode, Name: "Claude Code", Detected: true, InstallDir: bp, Confidence: 0.95}
		}
	}
	if bin, err := exec.LookPath("claude"); err == nil {
		return &Agent{ID: AgentClaudeCode, Name: "Claude Code", Detected: true, InstallDir: bin, Confidence: 0.85}
	}
	claudeDir := filepath.Join(home, ".claude")
	if entries, err := os.ReadDir(claudeDir); err == nil && len(entries) > 0 {
		return &Agent{ID: AgentClaudeCode, Name: "Claude Code", Detected: true, InstallDir: claudeDir, Confidence: 0.8}
	}
	return nil
}

func detectCursor() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentCursor,
		name: "Cursor",
		paths: []string{
			filepath.Join(home, ".cursor"),
			filepath.Join(home, "AppData", "Local", "Programs", "Cursor"),
			filepath.Join(home, "Applications", "Cursor.app"),
			"/Applications/Cursor.app",
			"/usr/local/bin/cursor",
		},
		binary:           "cursor",
		confidencePath:   0.9,
		confidenceBinary: 0.8,
	})
}

func detectVSCode() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentVSCode,
		name: "VS Code Copilot",
		paths: []string{
			filepath.Join(home, "AppData", "Local", "Programs", "Microsoft VS Code", "Code.exe"),
			filepath.Join(home, "AppData", "Local", "Programs", "Microsoft VS Code"),
			filepath.Join(home, "Applications", "Visual Studio Code.app"),
			"/Applications/Visual Studio Code.app",
			"/usr/local/bin/code",
			filepath.Join(home, ".vscode"),
		},
		binary:           "code",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
	})
}

func detectCodex() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentCodex,
		name: "Codex CLI",
		paths: []string{
			filepath.Join(home, ".codex"),
			filepath.Join(home, ".local", "bin", "codex"),
			filepath.Join(home, "AppData", "Roaming", "Codex"),
			"/usr/local/bin/codex",
		},
		binary:           "codex",
		confidencePath:   0.9,
		confidenceBinary: 0.8,
	})
}

func detectGemini() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentGemini,
		name: "Gemini CLI",
		paths: []string{
			filepath.Join(home, ".gemini"),
			filepath.Join(home, "AppData", "Roaming", "Gemini"),
			"/usr/local/bin/gemini",
		},
		binary:           "gemini",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
	})
}

func detectWindsurf() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentWindsurf,
		name: "Windsurf",
		paths: []string{
			filepath.Join(home, ".windsurf"),
			filepath.Join(home, "AppData", "Local", "Programs", "Windsurf"),
			filepath.Join(home, "Applications", "Windsurf.app"),
			"/Applications/Windsurf.app",
		},
		binary:           "windsurf",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
	})
}

func detectZed() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentZed,
		name: "Zed",
		paths: []string{
			filepath.Join(home, ".config", "zed"),
			"/Applications/Zed.app",
			filepath.Join(home, "Applications", "Zed.app"),
		},
		binary:           "zed",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
		skipOnWindows:    true,
	})
}

func detectContinue() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentContinue,
		name: "Continue.dev",
		paths: []string{
			filepath.Join(home, ".config", "continue"),
			filepath.Join(home, "AppData", "Roaming", "Continue"),
			filepath.Join(home, ".continue"),
		},
		confidencePath: 0.8,
	})
}

func detectOpenCode() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentOpenCode,
		name: "OpenCode",
		paths: []string{
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(home, "AppData", "Roaming", "OpenCode"),
			filepath.Join(home, ".opencode"),
			"/usr/local/bin/opencode",
		},
		binary:           "opencode",
		confidencePath:   0.8,
		confidenceBinary: 0.75,
	})
}
