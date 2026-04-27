package transport

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

func newTestMCPProtocol(sb sandbox.Sandbox, journal core.Journal) *MCPProtocol {
	idx := core.NewIndex()
	if journal == nil {
		journal = &mockJournal{}
	}
	handlers := NewHandlers(idx, journal, sb)
	return NewMCPProtocol(handlers)
}

func callTool(t *testing.T, m *MCPProtocol, name string, args any) *MCPResponse {
	t.Helper()
	argsRaw, _ := json.Marshal(args)
	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      name,
		"arguments": json.RawMessage(argsRaw),
	})
	resp, err := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion,
		ID:      1,
		Method:  "tools/call",
		Params:  paramsRaw,
	})
	if err != nil {
		t.Fatalf("handleToolsCall error: %v", err)
	}
	return resp
}

func TestMCPToolsCall_Exec_Success(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{Exit: 0, Stdout: "hi"}}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodExec, ExecParams{Code: "echo hi"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Error("expected result")
	}
}

func TestMCPToolsCall_Exec_HandlerError(t *testing.T) {
	sb := &stubSandbox{execErr: errors.New("bad")}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodExec, ExecParams{Code: "x"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603, got %d", resp.Error.Code)
	}
}

func TestMCPToolsCall_Exec_InvalidArgs(t *testing.T) {
	sb := &stubSandbox{}
	m := newTestMCPProtocol(sb, nil)

	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      methodExec,
		"arguments": json.RawMessage(`"not-an-object"`),
	})
	resp, err := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion,
		ID:      9,
		Method:  "tools/call",
		Params:  paramsRaw,
	})
	if err != nil {
		t.Fatalf("handleToolsCall: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602 invalid params, got %+v", resp.Error)
	}
}

func TestMCPToolsCall_Read_Success(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{Content: "abc"}}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodRead, ReadParams{Path: "/x"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestMCPToolsCall_Read_HandlerError(t *testing.T) {
	sb := &stubSandbox{readErr: errors.New("ENOENT")}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodRead, ReadParams{Path: "/x"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestMCPToolsCall_Read_InvalidArgs(t *testing.T) {
	m := newTestMCPProtocol(&stubSandbox{}, nil)
	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      methodRead,
		"arguments": json.RawMessage(`"bad"`),
	})
	resp, _ := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/call", Params: paramsRaw,
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602, got %+v", resp.Error)
	}
}

func TestMCPToolsCall_Fetch_Success(t *testing.T) {
	sb := &stubSandbox{fetchResp: sandbox.FetchResp{Status: 200}}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodFetch, FetchParams{URL: "https://x"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestMCPToolsCall_Fetch_HandlerError(t *testing.T) {
	sb := &stubSandbox{fetchErr: errors.New("dns")}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodFetch, FetchParams{URL: "x"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestMCPToolsCall_Fetch_InvalidArgs(t *testing.T) {
	m := newTestMCPProtocol(&stubSandbox{}, nil)
	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      methodFetch,
		"arguments": json.RawMessage(`"bad"`),
	})
	resp, _ := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/call", Params: paramsRaw,
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602, got %+v", resp.Error)
	}
}

func TestMCPToolsCall_Stats_Success(t *testing.T) {
	m := newTestMCPProtocol(nil, nil)
	resp := callTool(t, m, methodStats, StatsParams{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestMCPToolsCall_Stats_NoArgs(t *testing.T) {
	m := newTestMCPProtocol(nil, nil)
	paramsRaw, _ := json.Marshal(map[string]any{"name": methodStats})
	resp, err := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/call", Params: paramsRaw,
	})
	if err != nil {
		t.Fatalf("handleToolsCall: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestMCPToolsCall_Stats_HandlerError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failSearch: true}
	handlers := NewHandlers(idx, journal, nil)
	m := NewMCPProtocol(handlers)

	resp := callTool(t, m, methodStats, StatsParams{})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestMCPToolsCall_Remember_HandlerError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failRemember: true}
	handlers := NewHandlers(idx, journal, nil)
	m := NewMCPProtocol(handlers)

	resp := callTool(t, m, methodRemember, RememberParams{Type: "note", Source: "x"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestMCPToolsCall_Remember_InvalidArgs(t *testing.T) {
	m := newTestMCPProtocol(nil, nil)
	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      methodRemember,
		"arguments": json.RawMessage(`"bad"`),
	})
	resp, _ := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/call", Params: paramsRaw,
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602, got %+v", resp.Error)
	}
}

func TestMCPToolsCall_Search_Success(t *testing.T) {
	m := newTestMCPProtocol(nil, nil)
	resp := callTool(t, m, methodSearch, SearchParams{Query: "q"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestMCPToolsCall_Search_InvalidArgs(t *testing.T) {
	m := newTestMCPProtocol(nil, nil)
	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      methodSearch,
		"arguments": json.RawMessage(`"bad"`),
	})
	resp, _ := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/call", Params: paramsRaw,
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602, got %+v", resp.Error)
	}
}

func TestMCPToolsCall_Recall_HandlerError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failRecall: true}
	handlers := NewHandlers(idx, journal, nil)
	m := NewMCPProtocol(handlers)

	// Recall streams the journal; the mock returns an error when failRecall
	// is set, which exercises the error branch in handleToolsCall.
	resp := callTool(t, m, methodRecall, RecallParams{})
	if resp.Error == nil {
		t.Fatal("expected error (recall stream fail)")
	}
}

func TestMCPToolsCall_Recall_InvalidArgs(t *testing.T) {
	m := newTestMCPProtocol(nil, nil)
	paramsRaw, _ := json.Marshal(map[string]any{
		"name":      methodRecall,
		"arguments": json.RawMessage(`"bad"`),
	})
	resp, _ := m.handleToolsCall(&MCPRequest{
		JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/call", Params: paramsRaw,
	})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("expected -32602, got %+v", resp.Error)
	}
}

// TestMCPToolsCall_Read_CallToolResultShape locks down the MCP CallToolResult
// envelope. Returning a bare ReadResponse used to make Claude Code reject the
// reply with `expected array, received string` because ReadResponse.Content
// is a string while MCP requires content to be a content-block array.
//
// In the default (token-optimized) path, content[0].text is a short sentinel
// and the payload travels in structuredContent. Asserting payload bytes in
// content[0].text would re-introduce the duplicate-payload tax this server
// exists to remove, so the payload check lives on structuredContent.
func TestMCPToolsCall_Read_CallToolResultShape(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{Content: "hello"}}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodRead, ReadParams{Path: "/x"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	wrapped, ok := resp.Result.(MCPCallToolResult)
	if !ok {
		t.Fatalf("Result type = %T, want MCPCallToolResult", resp.Result)
	}
	if len(wrapped.Content) == 0 || wrapped.Content[0].Type != "text" {
		t.Errorf("Content = %+v, want at least one text block", wrapped.Content)
	}
	if wrapped.StructuredContent == nil {
		t.Fatal("StructuredContent is nil; want original payload")
	}
	rr, ok := wrapped.StructuredContent.(*ReadResponse)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want *ReadResponse", wrapped.StructuredContent)
	}
	if rr.Content != "hello" {
		t.Errorf("StructuredContent.Content = %q, want %q", rr.Content, "hello")
	}
	// content[0].text is intentionally NOT the JSON payload — it's a short
	// sentinel. Re-stuffing the payload here is exactly the regression we
	// removed.
	if strings.Contains(wrapped.Content[0].Text, "\"content\":\"hello\"") {
		t.Errorf("Content[0].Text duplicates structuredContent payload (%q); the modern path must keep it minimal", wrapped.Content[0].Text)
	}
}

// TestMCPToolsCall_LegacyContentEnvelope verifies that DFMT_MCP_LEGACY_CONTENT=1
// restores the pre-optimization behavior of duplicating the payload into
// content[0].text. The escape hatch is documented support for text-only MCP
// clients that ignore structuredContent.
func TestMCPToolsCall_LegacyContentEnvelope(t *testing.T) {
	t.Setenv("DFMT_MCP_LEGACY_CONTENT", "1")
	sb := &stubSandbox{readResp: sandbox.ReadResp{Content: "hello-legacy"}}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodRead, ReadParams{Path: "/x"})
	wrapped, ok := resp.Result.(MCPCallToolResult)
	if !ok {
		t.Fatalf("Result type = %T, want MCPCallToolResult", resp.Result)
	}
	if !strings.Contains(wrapped.Content[0].Text, "hello-legacy") {
		t.Errorf("legacy mode must inline payload in content[0].text; got %q", wrapped.Content[0].Text)
	}
}

// TestMCPToolsCall_Exec_CallToolResultShape — same shape contract for exec.
func TestMCPToolsCall_Exec_CallToolResultShape(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{Exit: 0, Stdout: "ok"}}
	m := newTestMCPProtocol(sb, nil)
	resp := callTool(t, m, methodExec, ExecParams{Code: "echo ok"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	wrapped, ok := resp.Result.(MCPCallToolResult)
	if !ok {
		t.Fatalf("Result type = %T, want MCPCallToolResult", resp.Result)
	}
	if len(wrapped.Content) == 0 || wrapped.Content[0].Type != "text" {
		t.Errorf("Content = %+v, want at least one text block", wrapped.Content)
	}
}
