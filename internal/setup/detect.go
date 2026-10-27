package setup

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ersinkoc/dfmt/internal/osutil"
)

// Agent IDs used across the codebase.
//
// The roster tracks agents that are maintained and reachable today. Three
// were dropped in 0.8.1: Gemini CLI (stopped serving individual accounts on
// 2026-06-18; Google's successor is Antigravity), Windsurf (renamed Devin
// Desktop on 2026-06-02; same product, new config location), and
// Continue.dev (frozen read-only after its team joined Cursor in 2026-06).
const (
	AgentClaudeCode  = "claude-code"
	AgentCursor      = "cursor"
	AgentVSCode      = "vscode"
	AgentCopilotCLI  = "copilot-cli"
	AgentCodex       = "codex"
	AgentAntigravity = "antigravity"
	AgentDevin       = "devin"
	AgentZed         = "zed"
	AgentOpenCode    = "opencode"
	AgentKiro        = "kiro"
	AgentCline       = "cline"
)

func lookPath(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if !osutil.IsWindows() {
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

	if spec.skipOnWindows && osutil.IsWindows() {
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

// IsClaudeCodeInstalled reports whether Claude Code is detected on this
// machine. Returns true on the same evidence detectClaudeCode() uses
// (binary on PATH, ~/.claude/ directory, etc.). Used by `dfmt init` to
// decide whether to patch ~/.claude.json — the previous unconditional
// patch created/modified that file even on machines that don't run
// Claude Code, polluting the home of Cursor/Codex/Zed/etc. users.
func IsClaudeCodeInstalled() bool {
	return detectClaudeCode() != nil
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

func detectCopilotCLI() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:               AgentCopilotCLI,
		name:             "GitHub Copilot CLI",
		paths:            []string{filepath.Join(home, ".copilot")},
		binary:           "copilot",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
	})
}

func detectAntigravity() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentAntigravity,
		name: "Google Antigravity",
		paths: []string{
			filepath.Join(home, ".gemini", "config", "mcp_config.json"),
			filepath.Join(home, ".gemini", "antigravity"),
			filepath.Join(home, "AppData", "Local", "Programs", "Antigravity"),
			filepath.Join(home, "Applications", "Antigravity.app"),
			"/Applications/Antigravity.app",
		},
		binary:           "agy",
		confidencePath:   0.85,
		confidenceBinary: 0.85,
	})
}

func detectDevin() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentDevin,
		name: "Devin Desktop (formerly Windsurf)",
		paths: []string{
			filepath.Join(home, ".config", "devin"),
			filepath.Join(home, "AppData", "Roaming", "devin"),
			filepath.Join(home, "Applications", "Devin.app"),
			"/Applications/Devin.app",
			// A Windsurf install updates itself into Devin Desktop.
			filepath.Join(home, ".codeium", "windsurf"),
			filepath.Join(home, "AppData", "Local", "Programs", "Windsurf"),
			"/Applications/Windsurf.app",
		},
		binary:           "devin",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
	})
}

func detectKiro() *Agent {
	home := HomeDir()
	return detectAgent(agentSpec{
		id:   AgentKiro,
		name: "Kiro",
		paths: []string{
			filepath.Join(home, ".kiro"),
			filepath.Join(home, "AppData", "Local", "Programs", "Kiro"),
			filepath.Join(home, "Applications", "Kiro.app"),
			"/Applications/Kiro.app",
		},
		binary:           "kiro",
		confidencePath:   0.85,
		confidenceBinary: 0.8,
	})
}

func detectCline() *Agent {
	return detectAgent(agentSpec{
		id:   AgentCline,
		name: "Cline",
		// The VS Code extension keeps its state under the editor's
		// globalStorage, keyed by its marketplace ID.
		paths:          []string{filepath.Join(vscodeUserDir(HomeDir()), "globalStorage", clineExtensionID)},
		binary:         "cline",
		confidencePath: 0.85,
		// The standalone CLI alone does not read the extension's file.
		confidenceBinary: 0.6,
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
