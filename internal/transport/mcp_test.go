package transport

import (
	"context"
	"testing"
)

func TestMCPProtocol_Handle_ToolsCall_NilHandlers(t *testing.T) {
	// Create protocol with nil handlers
	p := &MCPProtocol{handlers: nil}

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  []byte(`{"name":"dfmt.remember","arguments":{"type":"note"}}`),
		ID:      1,
	}

	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if resp == nil {
		t.Fatal("resp is nil")
	}

	if resp.Error == nil {
		t.Error("expected error response for nil handlers")
	}

	if resp.Error.Code != -32603 {
		t.Errorf("expected error code -32603, got %d", resp.Error.Code)
	}
}

func TestMCPProtocol_Handle_ToolsCall_EmptyParams(t *testing.T) {
	// Create protocol with nil handlers - should error on params parse
	p := &MCPProtocol{handlers: nil}

	// Empty params should still hit the nil handlers check before parsing
	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  []byte(`{}`),
		ID:      2,
	}

	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if resp == nil {
		t.Fatal("resp is nil")
	}

	// Should error because handlers is nil
	if resp.Error == nil {
		t.Error("expected error response for nil handlers")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected error code -32603, got %d", resp.Error.Code)
	}
}

func TestMCPProtocol_Handle_ToolsCall_UnknownTool(t *testing.T) {
	handlers := &Handlers{}
	p := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  []byte(`{"name":"unknown.tool","arguments":{}}`),
		ID:      3,
	}

	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if resp == nil {
		t.Fatal("resp is nil")
	}

	if resp.Error == nil {
		t.Error("expected error response for unknown tool")
	}

	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", resp.Error.Code)
	}
}
