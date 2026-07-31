//nolint:staticcheck // SA5011 false positives on t.Fatal nil-guarded pointer dereferences
package transport

import (
	"context"
	"fmt"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestMCPProtocol_Handle_ToolsCall_NilHandlers(t *testing.T) {
	// Create protocol with nil handlers
	p := &MCPProtocol{backend: nil}

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
		return
	}

	if resp.Error == nil {
		t.Fatal("expected error response for nil handlers")
		return
	}

	if resp.Error.Code != -32603 {
		t.Errorf("expected error code -32603, got %d", resp.Error.Code)
	}
}

func TestMCPProtocol_Handle_ToolsCall_EmptyParams(t *testing.T) {
	// Create protocol with nil handlers - should error on params parse
	p := &MCPProtocol{backend: nil}

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
		return
	}

	// Should error because handlers is nil
	if resp.Error == nil {
		t.Fatal("expected error response for nil handlers")
		return
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
		return
	}

	if resp.Error == nil {
		t.Fatal("expected error response for unknown tool")
		return
	}

	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", resp.Error.Code)
	}
}

// TRN-7: execution errors must be returned as tool results with IsError:true,
// not as JSON-RPC -32603 errors. The model needs to see the error text to
// recover (e.g. "no dfmt project" → the agent opens a project). A -32603 is
// surfaced by many MCP hosts as "tool failed" without the message.
func TestMCPProtocol_ExecutionErrorReturnsIsError(t *testing.T) {
	p := &MCPProtocol{backend: &errBackend{}}

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  []byte(`{"name":"dfmt.exec","arguments":{"code":"echo hi"}}`),
		ID:      4,
	}

	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}
	rr := *resp // dereference the guaranteed-non-nil pointer to satisfy SA5011
	if rr.Error != nil {
		t.Fatalf("TRN-7: expected IsError tool result, got JSON-RPC error: %s", rr.Error.Message)
	}
	result, ok := rr.Result.(MCPCallToolResult)
	if !ok {
		t.Fatalf("result type = %T, want MCPCallToolResult", resp.Result)
	}
	if !result.IsError {
		t.Error("expected IsError=true for execution failure (TRN-7)")
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected error message in content[0].text (TRN-7)")
	}
}

// errBackend is a minimal Backend whose methods return errors.
type errBackend struct{}

func (e *errBackend) Exec(ctx context.Context, params ExecParams) (*ExecResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Read(ctx context.Context, params ReadParams) (*ReadResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Fetch(ctx context.Context, params FetchParams) (*FetchResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Glob(ctx context.Context, params GlobParams) (*GlobResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Grep(ctx context.Context, params GrepParams) (*GrepResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Edit(ctx context.Context, params EditParams) (*EditResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Write(ctx context.Context, params WriteParams) (*WriteResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Remember(ctx context.Context, params RememberParams) (*RememberResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Recall(ctx context.Context, params RecallParams) (*RecallResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) Stats(ctx context.Context, params StatsParams) (*StatsResponse, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
func (e *errBackend) StreamEvents(ctx context.Context, from string) (<-chan core.Event, error) {
	return nil, fmt.Errorf("simulated execution failure")
}
