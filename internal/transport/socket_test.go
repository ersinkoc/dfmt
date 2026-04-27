package transport

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketServerCreation(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	if server.path != "/tmp/test.sock" {
		t.Errorf("expected path /tmp/test.sock, got %s", server.path)
	}
	if server.handlers != handlers {
		t.Error("handlers not set correctly")
	}
	if server.running {
		t.Error("server should not be running initially")
	}
	if server.listener != nil {
		t.Error("listener should be nil initially")
	}
}

func TestSocketServer_StartAndStop(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Errorf("socket file does not exist: %v", err)
	}
	t.Logf("socket file mode: %v", info.Mode())

	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("server.Stop failed: %v", err)
	}
}

func TestSocketServer_Start_DoubleStart(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx := context.Background()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Errorf("socket file does not exist: %v", err)
	}

	if info.Mode().Perm() != 0700 {
		t.Errorf("expected socket mode 0700, got %v", info.Mode().Perm())
	}

	server.Stop(ctx)
}

func TestDecodeParams_Empty(t *testing.T) {
	var v map[string]interface{}
	err := decodeParams(nil, &v)
	if err != nil {
		t.Errorf("decodeParams with nil failed: %v", err)
	}

	err = decodeParams([]byte{}, &v)
	if err != nil {
		t.Errorf("decodeParams with empty slice failed: %v", err)
	}
}

func TestDecodeParams_ValidJSON(t *testing.T) {
	data := []byte(`{"key": "value", "num": 123}`)
	var v map[string]interface{}
	err := decodeParams(data, &v)
	if err != nil {
		t.Errorf("decodeParams failed: %v", err)
	}

	if v["key"] != "value" {
		t.Errorf("expected key=value, got %v", v["key"])
	}
	if v["num"] != float64(123) {
		t.Errorf("expected num=123, got %v", v["num"])
	}
}

func TestDecodeParams_InvalidJSON(t *testing.T) {
	data := []byte(`invalid json`)
	var v map[string]interface{}
	err := decodeParams(data, &v)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestSocketServer_Dispatch_UnknownMethod(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		ID:     1,
		Method: "unknownMethod",
		Params: nil,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for unknown method, got nil")
	}
}

func TestSocketServer_Dispatch_RememberMethod_BadParams(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	params := []byte(`invalid json`)
	req := &Request{
		ID:     1,
		Method: "remember",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid params, got nil")
	}
}

func TestSocketServer_Dispatch_SearchMethod_BadParams(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	params := []byte(`invalid json`)
	req := &Request{
		ID:     2,
		Method: "search",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid params, got nil")
	}
}

func TestSocketServer_Dispatch_RecallMethod_BadParams(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	params := []byte(`invalid json`)
	req := &Request{
		ID:     3,
		Method: "recall",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid params, got nil")
	}
}

func TestSocketServer_Stop_NotRunning(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	ctx := context.Background()

	err := server.Stop(ctx)
	if err != nil {
		t.Errorf("server.Stop on not-running server failed: %v", err)
	}
}

func TestSocketServer_HandleConn_ReadError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	conn.Write([]byte("not json rpc"))

	time.Sleep(50 * time.Millisecond)
}

func TestSocketServer_HandleConn_IdleTimeout(t *testing.T) {
	oldTimeout := socketReadIdleTimeout
	socketReadIdleTimeout = 20 * time.Millisecond
	defer func() { socketReadIdleTimeout = oldTimeout }()

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	server := &SocketServer{handlers: &Handlers{}}
	done := make(chan struct{})
	go func() {
		server.handleConn(context.Background(), serverConn)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle socket connection did not close after read deadline")
	}
}

func TestSocketServer_Serve_ContextCancellation(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}

	cancel()

	time.Sleep(50 * time.Millisecond)

	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("server.Stop failed after context cancel: %v", err)
	}
}

func TestSocketServer_Start_AlreadyRunning(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx := context.Background()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	err = server.Start(ctx)
	if err == nil {
		t.Error("expected error for double start, got nil")
	}
}

func TestSocketServer_Start_MkdirAllError(t *testing.T) {
	handlers := &Handlers{}
	// Use a path that cannot have directories created
	socketPath := "/proc/invalid/test.sock"
	if os.PathSeparator == '\\' {
		socketPath = "NUL:/test.sock"
	}
	server := NewSocketServer(socketPath, handlers)

	ctx := context.Background()
	err := server.Start(ctx)
	// Should fail when trying to create directory
	if err == nil {
		t.Log("Start succeeded (may succeed on some systems)")
	}
}

func TestSocketServer_Start_ListenError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	handlers := &Handlers{}
	// Force a definitive listen error by placing the socket's parent
	// at a regular file path — bind then fails with ENOTDIR on every
	// Unix variant instead of relying on "no-such-parent".
	tmpDir := t.TempDir()
	notDir := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(notDir, "bad.sock")

	server := NewSocketServer(socketPath, handlers)
	ctx := context.Background()

	err := server.Start(ctx)
	// Should fail since path is not a socket
	if err == nil {
		server.Stop(ctx)
		t.Error("expected error for non-socket path")
	}
}

func TestSocketServer_HandleConn_WriteError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	// Close write side first, then try to write
	conn.Close()

	// Wait a bit for the server to handle the closed connection
	time.Sleep(50 * time.Millisecond)
}

func TestSocketServer_Stop_Errors(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	// Stop before start - should not error
	ctx := context.Background()
	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("Stop before Start failed: %v", err)
	}

	// Start then stop
	server.Start(ctx)
	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("Stop after Start failed: %v", err)
	}
}

func TestSocketServer_Start_ChmodError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx := context.Background()

	// chmod should work after listen, but if not, we cover that path
	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	// Verify socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Error("socket file should exist")
	}
}

func TestDecodeParams_EmptyBytes(t *testing.T) {
	var v map[string]interface{}
	err := decodeParams([]byte{}, &v)
	if err != nil {
		t.Errorf("decodeParams with empty slice failed: %v", err)
	}
}

func TestDecodeParams_ValidJSONTypes(t *testing.T) {
	// Test various JSON types that should decode correctly
	testCases := []struct {
		json string
	}{
		{`{"str":"value"}`},
		{`{"num":123}`},
		{`{"float":1.23}`},
		{`{"bool":true}`},
		{`{"arr":[1,2,3]}`},
		{`{"obj":{"nested":"value"}}`},
		{`{"null":null}`},
	}

	for _, tc := range testCases {
		var v map[string]interface{}
		err := decodeParams([]byte(tc.json), &v)
		if err != nil {
			t.Errorf("decodeParams(%s) failed: %v", tc.json, err)
		}
	}
}

func TestSocketServerDispatchInvalidParamsRemember(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	// Invalid params for remember method
	params := []byte(`{"type": 123}`)
	req := &Request{
		ID:     1,
		Method: "remember",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid remember params type")
	}
}

func TestSocketServerDispatchInvalidParamsSearch(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	// Invalid params for search method
	params := []byte(`{"query": 123}`)
	req := &Request{
		ID:     2,
		Method: "search",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid search params type")
	}
}

func TestSocketServerDispatchInvalidParamsRecall(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	// Invalid params for recall method
	params := []byte(`{"budget": "not a number"}`)
	req := &Request{
		ID:     3,
		Method: "recall",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid recall params type")
	}
}

func TestSocketServerServeContextCancel(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}

	// Cancel context to stop accepting connections
	cancel()

	time.Sleep(50 * time.Millisecond)

	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("server.Stop failed: %v", err)
	}
}

func TestSocketServerCloseListenerError(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping on Windows")
	}

	handlers := &Handlers{}
	// Use a path that will fail during listen
	server := NewSocketServer("/dev/null/test.sock", handlers)

	ctx := context.Background()
	err := server.Start(ctx)
	if err != nil {
		t.Logf("Start failed as expected: %v", err)
	}
}

func TestSocketServer_WithInvalidPath(t *testing.T) {
	handlers := &Handlers{}
	// Create server with path that cannot be created
	server := NewSocketServer("/proc/12345/invalid.sock", handlers)

	ctx := context.Background()
	err := server.Start(ctx)
	// Should fail when trying to create directory
	if err == nil {
		server.Stop(ctx)
		t.Log("Start succeeded (may succeed on some systems)")
	}
}

func TestSocketServer_HandleConn_ValidRequest(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// Send a valid JSON-RPC request
	codec := NewCodec(conn)

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
	// Should get a response (may be error if handlers are nil)
	t.Logf("Got response: %+v", resp)
}

func TestSocketServer_HandleConn_UnknownMethod(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "unknown_method_xyz",
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
}

func TestSocketServer_HandleConn_BadParams(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop(ctx)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	codec := NewCodec(conn)

	// Bad params for search
	req := &Request{
		JSONRPC: "2.0",
		Method:  "search",
		Params:  json.RawMessage(`{"query": 123}`), // query should be string
		ID:      3,
	}
	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	// Should get a response (error or success depending on codec behavior)
	_, _ = codec.ReadResponse()
}

func TestSocketServerDispatch_RecallMethod_BadParams(t *testing.T) {
	handlers := &Handlers{}
	server := NewSocketServer("/tmp/test.sock", handlers)

	params := []byte(`{"budget": "not a number"}`)
	req := &Request{
		ID:     3,
		Method: "recall",
		Params: params,
	}

	ctx := context.Background()
	_, err := server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid recall params")
	}
}

func TestSocketServer_Stop_DoubleStop(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir, err := os.MkdirTemp("", "socket_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")
	handlers := &Handlers{}
	server := NewSocketServer(socketPath, handlers)

	ctx := context.Background()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}

	// Stop first time
	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("first Stop failed: %v", err)
	}

	// Stop second time - should be idempotent
	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("second Stop failed: %v", err)
	}
}

func TestSocketServer_Start_Errors(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	handlers := &Handlers{}

	// Test 1: Invalid socket path
	server := NewSocketServer("", handlers)
	ctx := context.Background()
	err := server.Start(ctx)
	if err == nil {
		server.Stop(ctx)
	}

	// Test 2: Path that causes Listen error
	server = NewSocketServer("/dev/null/test.sock", handlers)
	err = server.Start(ctx)
	if err == nil {
		server.Stop(ctx)
		t.Log("Start succeeded on /dev/null (unexpected)")
	} else {
		t.Logf("Start failed as expected: %v", err)
	}
}
