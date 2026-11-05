package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// Claude Code defers MCP tools by default; an agent that never loads them
// uses native tools instead (0 dfmt calls in 19 of 21 observed sessions).
// Every tool must ask to be loaded upfront.
func TestToolsAskToLoadUpfront(t *testing.T) {
	tools := mcpToolsBuiltin()
	if len(tools) != 11 {
		t.Fatalf("tools = %d, want 11", len(tools))
	}
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		meta, _ := wire["_meta"].(map[string]any)
		if meta["anthropic/alwaysLoad"] != true {
			t.Errorf("%s: _meta = %v, want anthropic/alwaysLoad=true", tool.Name, wire["_meta"])
		}
	}
}

// Meta maps are per tool: a caller editing one must not change the others.
func TestToolMetaIsNotShared(t *testing.T) {
	tools := mcpToolsBuiltin()
	tools[0].Meta["x"] = 1
	if _, ok := tools[1].Meta["x"]; ok {
		t.Fatal("tools share one Meta map")
	}
}

// Instructions reach the model even while tools are deferred, and Claude
// Code truncates them at 4096 characters.
func TestInitializeCarriesInstructions(t *testing.T) {
	res := initializeAs(t, NewMCPProtocol(nil), "2025-06-18", "claude-code")
	if res.Instructions == "" {
		t.Fatal("initialize returned no instructions")
	}
	if len(res.Instructions) > 1024 {
		t.Errorf("instructions are %d bytes; they are paid on every session", len(res.Instructions))
	}
	for _, want := range []string{"dfmt_read", "dfmt_exec", "intent", "ToolSearch"} {
		if !strings.Contains(res.Instructions, want) {
			t.Errorf("instructions do not mention %q", want)
		}
	}
}
