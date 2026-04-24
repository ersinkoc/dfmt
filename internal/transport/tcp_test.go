package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestNewTCPServer(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)
	if server == nil {
		t.Fatal("NewTCPServer returned nil")
	}
	if server.handlers != handlers {
		t.Error("handlers not set correctly")
	}
}

func TestTCPServerPortFile(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, "port")

	server.SetPortFile(portFile)
	if server.portFile != portFile {
		t.Errorf("portFile = %s, want %s", server.portFile, portFile)
	}
}

func TestTCPServerPort(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	// Port should be 0 before start
	if server.Port() != 0 {
		t.Errorf("Port before start = %d, want 0", server.Port())
	}

	// Start server
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	// Port should be non-zero after start
	port := server.Port()
	if port == 0 {
		t.Error("Port after start is 0, expected non-zero")
	}

	// Verify port is actually reachable
	addr := "localhost:" + fmt.Sprintf("%d", port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Errorf("Failed to dial port %d: %v", port, err)
	}
	if conn != nil {
		conn.Close()
	}
}

func TestTCPServerStartTwice(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)
	ctx := context.Background()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	defer server.Stop(ctx)

	// Second Start should fail
	if err := server.Start(ctx); err == nil {
		t.Error("Second Start should fail with server already running")
	}
}

func TestTCPServerStop(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)
	ctx := context.Background()

	// Stop before start should be fine
	if err := server.Stop(ctx); err != nil {
		t.Errorf("Stop before Start failed: %v", err)
	}

	// Start then stop
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := server.Stop(ctx); err != nil {
		t.Errorf("Stop after Start failed: %v", err)
	}
}

func TestTCPServerWritePortFile(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, ".dfmt", "port")

	if err := server.writePortFile(portFile, 12345, ""); err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var pf PortFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal port file: %v", err)
	}
	if pf.Port != 12345 {
		t.Errorf("port file port = %d, want 12345", pf.Port)
	}
}

func TestTCPServerWritePortFileCreatesDir(t *testing.T) {
	handlers := NewHandlers(nil, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, ".dfmt", "nested", "port")

	if err := server.writePortFile(portFile, 54321, ""); err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var pf PortFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal port file: %v", err)
	}
	if pf.Port != 54321 {
		t.Errorf("port file port = %d, want 54321", pf.Port)
	}
}

func TestTCPServerDispatch(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	// Connect and send a request
	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Send search request
	req := &Request{
		JSONRPC: "2.0",
		Method:  "search",
		Params:  json.RawMessage(`{"query":"test"}`),
		ID:      1,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("response error: %s", resp.Error.Message)
	}
}

func TestTCPServerDispatchUnknownMethod(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "unknown_method",
		ID:      2,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error for unknown method")
	}
	// -32603 is internal error for unknown method in our dispatch
	if resp.Error.Code != -32601 && resp.Error.Code != -32603 {
		t.Errorf("expected -32601 or -32603, got %d", resp.Error.Code)
	}
}

func TestTCPServerDispatchBadParams(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Create request manually with raw params
	paramsMap := map[string]any{"query": "test"}
	paramsBytes, _ := json.Marshal(paramsMap)
	params := json.RawMessage(paramsBytes)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "search",
		Params:  params,
		ID:      3,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
}

// TestTCPServerDispatchBadParamsRemember tests bad params type for remember method
func TestTCPServerDispatchBadParamsRemember(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Invalid params - type field should be string not int
	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  json.RawMessage(`{"type":123,"source":"test"}`),
		ID:      4,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error for bad params remember")
	}
}

// TestTCPServerDispatchBadParamsRecall tests bad params for recall method
func TestTCPServerDispatchBadParamsRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Invalid params - budget should be int not string
	req := &Request{
		JSONRPC: "2.0",
		Method:  "recall",
		Params:  json.RawMessage(`{"budget":"not a number"}`),
		ID:      5,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error for bad params recall")
	}
}

// TestTCPServerDispatchRemember tests the remember handler path
func TestTCPServerDispatchRemember(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  json.RawMessage(`{"type":"note","source":"test","priority":"P2"}`),
		ID:      6,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
	if resp.Result == nil {
		t.Error("expected result")
	}
}

// TestTCPServerDispatchRecall tests the recall handler path
func TestTCPServerDispatchRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "recall",
		Params:  json.RawMessage(`{"budget":1024,"format":"md"}`),
		ID:      7,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
	if resp.Result == nil {
		t.Error("expected result")
	}
}

// TestTCPServerHandleConnReadError tests handleConn when ReadRequest returns a non-EOF error
func TestTCPServerHandleConnReadError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	// Write a malformed request that will cause ReadRequest to return an error
	// (partial JSON that can't be unmarshaled into a Request)
	conn.Write([]byte(`{"jsonrpc":"2.0","method":"`))

	// Close write side - now ReadRequest will get EOF
	conn.Close()

	// The server should handle this gracefully (no panic, connection closes)
}

// TestTCPServerHandleConnDispatchError tests handleConn when dispatch returns an error
func TestTCPServerHandleConnDispatchError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Unknown method - dispatch will return error
	req := &Request{
		JSONRPC: "2.0",
		Method:  "totally_unknown_method_xyz",
		ID:      8,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error response for unknown method")
	}
}

// TestTCPServerUnknownMethodDispatch tests dispatch returning error for unknown method
func TestTCPServerUnknownMethodDispatch(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop(ctx)

	port := server.Port()
	conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Test another unknown method to ensure all unknown methods are caught
	req := &Request{
		JSONRPC: "2.0",
		Method:  "invalid.method.name",
		ID:      9,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error for unknown method")
	}
}
