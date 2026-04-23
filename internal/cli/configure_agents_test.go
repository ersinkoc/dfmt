package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/setup"
)

// Each configure<Agent> helper has the same shape: MkdirAll under $HOME plus
// writeMCPConfig. Exercising each branch via configureAgent gets them all
// without touching production code.

func setHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")
	return tmp
}

func TestConfigureAgent_Cursor(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "cursor"}); err != nil {
		t.Fatalf("configureAgent cursor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "mcp.json")); err != nil {
		t.Errorf("expected cursor mcp.json: %v", err)
	}
}

func TestConfigureAgent_VSCode(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "vscode"}); err != nil {
		t.Fatalf("configureAgent vscode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".vscode", "mcp.json")); err != nil {
		t.Errorf("expected vscode mcp.json: %v", err)
	}
}

func TestConfigureAgent_Gemini(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "gemini"}); err != nil {
		t.Fatalf("configureAgent gemini: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "mcp.json")); err != nil {
		t.Errorf("expected gemini mcp.json: %v", err)
	}
}

func TestConfigureAgent_Windsurf(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "windsurf"}); err != nil {
		t.Fatalf("configureAgent windsurf: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".windsurf", "mcp.json")); err != nil {
		t.Errorf("expected windsurf mcp.json: %v", err)
	}
}

func TestConfigureAgent_Zed(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "zed"}); err != nil {
		t.Fatalf("configureAgent zed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "zed", "mcp.json")); err != nil {
		t.Errorf("expected zed mcp.json: %v", err)
	}
}

func TestConfigureAgent_Continue(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "continue"}); err != nil {
		t.Fatalf("configureAgent continue: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "continue", "mcp.json")); err != nil {
		t.Errorf("expected continue mcp.json: %v", err)
	}
}

func TestConfigureAgent_OpenCode(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "opencode"}); err != nil {
		t.Fatalf("configureAgent opencode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "mcp.json")); err != nil {
		t.Errorf("expected opencode mcp.json: %v", err)
	}
}

func TestConfigureAgent_Codex_Direct(t *testing.T) {
	home := setHome(t)
	if err := configureAgent(setup.Agent{ID: "codex"}); err != nil {
		t.Fatalf("configureAgent codex: %v", err)
	}
	// configureCodex writes into ~/.codex
	if _, err := os.Stat(filepath.Join(home, ".codex")); err != nil {
		t.Errorf("expected ~/.codex: %v", err)
	}
}
