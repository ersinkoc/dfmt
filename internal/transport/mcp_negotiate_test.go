package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

func initializeAs(t *testing.T, m *MCPProtocol, protocolVersion, clientName string) MCPInitializeResult {
	t.Helper()
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": protocolVersion,
		"clientInfo":      map[string]any{"name": clientName, "version": "1.0"},
	})
	resp, err := m.Handle(context.Background(), &MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "initialize", Params: params,
	})
	if err != nil || resp == nil {
		t.Fatalf("initialize: resp=%v err=%v", resp, err)
	}
	res, ok := resp.Result.(MCPInitializeResult)
	if !ok {
		t.Fatalf("Result type = %T, want MCPInitializeResult", resp.Result)
	}
	return res
}

func TestNegotiateMCPProtocolVersion(t *testing.T) {
	cases := map[string]string{
		"":           config.DefaultMCPProtocolVersion,
		"2024-11-05": "2024-11-05",
		"2025-06-18": "2025-06-18",
		"2025-11-25": "2025-11-25",
		"2099-01-01": supportedMCPProtocolVersions[0],
	}
	for in, want := range cases {
		if got := negotiateMCPProtocolVersion(in); got != want {
			t.Errorf("negotiate(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInitializeEchoesClientProtocolVersion: the server used to answer
// 2024-11-05 to everyone, a revision that predates structuredContent.
func TestInitializeEchoesClientProtocolVersion(t *testing.T) {
	m := NewMCPProtocol(nil)
	if got := initializeAs(t, m, "2025-06-18", "claude-code").ProtocolVersion; got != "2025-06-18" {
		t.Errorf("ProtocolVersion = %q, want 2025-06-18", got)
	}
}

// TestToolResultContentByClient pins issue #1: clients not known to read
// structuredContent (Windsurf) must get the payload in content[0].text;
// known ones keep the token-saving sentinel.
func TestToolResultContentByClient(t *testing.T) {
	t.Setenv("DFMT_MCP_LEGACY_CONTENT", "")
	cases := []struct {
		client   string
		wantFull bool
	}{
		{"claude-code", false},
		{"Cursor", false},
		{"codex-mcp-client", false},
		{"windsurf", true},
		{"", true},
	}
	for _, tc := range cases {
		sb := &stubSandbox{readResp: sandbox.ReadResp{Content: "payload-marker"}}
		m := newTestMCPProtocol(sb, nil)
		initializeAs(t, m, "2025-06-18", tc.client)
		resp := callTool(t, m, methodRead, ReadParams{Path: "/x"})
		res, ok := resp.Result.(MCPCallToolResult)
		if !ok {
			t.Fatalf("%q: Result type = %T", tc.client, resp.Result)
		}
		gotFull := strings.Contains(res.Content[0].Text, "payload-marker")
		if gotFull != tc.wantFull {
			t.Errorf("client %q: content[0].text = %q, want full payload = %v",
				tc.client, res.Content[0].Text, tc.wantFull)
		}
		if res.StructuredContent == nil {
			t.Errorf("client %q: structuredContent dropped", tc.client)
		}
	}
}

func TestToolResultContentEnvOverride(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{Content: "payload-marker"}}

	t.Setenv("DFMT_MCP_LEGACY_CONTENT", "0")
	m := newTestMCPProtocol(sb, nil)
	initializeAs(t, m, "2025-06-18", "windsurf")
	res := callTool(t, m, methodRead, ReadParams{Path: "/x"}).Result.(MCPCallToolResult)
	if res.Content[0].Text != mcpLegacyContentSentinel {
		t.Errorf("override 0: content[0].text = %q, want sentinel", res.Content[0].Text)
	}

	t.Setenv("DFMT_MCP_LEGACY_CONTENT", "1")
	m = newTestMCPProtocol(sb, nil)
	initializeAs(t, m, "2025-06-18", "claude-code")
	res = callTool(t, m, methodRead, ReadParams{Path: "/x"}).Result.(MCPCallToolResult)
	if !strings.Contains(res.Content[0].Text, "payload-marker") {
		t.Errorf("override 1: content[0].text = %q, want full payload", res.Content[0].Text)
	}
}
