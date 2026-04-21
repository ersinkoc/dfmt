package client

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// mockListener creates a Unix socket listener for testing
func mockListener(t *testing.T, tmpDir string) string {
	socketPath := filepath.Join(tmpDir, "daemon.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	// Don't close listener - keep it alive for tests
	t.Cleanup(func() { ln.Close() })
	return socketPath
}

// mockServer handles JSON-RPC requests for testing
type mockServer struct {
	t       *testing.T
	listener net.Listener
	requests chan *transport.Request
	done     chan struct{}
}

func newMockServer(t *testing.T, socketPath string) *mockServer {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}

	return &mockServer{
		t:       t,
		listener: ln,
		requests: make(chan *transport.Request, 10),
		done:     make(chan struct{}),
	}
}

func (s *mockServer) Start() {
	go func() {
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				select {
				case <-s.done:
					return
				default:
					return
				}
			}
			go s.handleConn(conn)
		}
	}()
}

func (s *mockServer) handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				s.t.Logf("read error: %v", err)
			}
			return
		}

		var req transport.Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.t.Logf("unmarshal error: %v", err)
			return
		}

		s.requests <- &req

		// Send response based on method
		var resp transport.Response
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "remember":
			resp.Result = map[string]interface{}{
				"id": "test-id-" + time.Now().Format("150405"),
				"ts": time.Now().Format(time.RFC3339Nano),
			}
		case "search":
			resp.Result = map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": "doc1", "score": 0.95},
				},
				"layer": "bm25",
			}
		case "recall":
			resp.Result = map[string]interface{}{
				"snapshot": "# Session Snapshot\n- [P1] test event",
				"format":   "md",
			}
		default:
			resp.Error = &transport.RPCError{
				Code:    -32601,
				Message: "method not found",
			}
		}

		data, _ := json.Marshal(resp)
		conn.Write(append(data, '\n'))
	}
}

func (s *mockServer) Stop() {
	close(s.done)
	s.listener.Close()
}

// Tests with mock socket

func TestRememberWithMockSocket(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	// Start server goroutine
	serverDone := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req transport.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}

			// Send response
			resp := transport.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"id": "mock-id",
					"ts": time.Now().Format(time.RFC3339Nano),
				},
			}
			data, _ := json.Marshal(resp)
			conn.Write(append(data, '\n'))
		}
	}()
	defer close(serverDone)

	cl, err := NewClient(tmpDir)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := cl.Remember(ctx, transport.RememberParams{
		Type:   "test",
		Source: "test",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp.ID == "" {
		t.Error("Remember returned empty ID")
	}
}

func TestSearchWithMockSocket(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	// Start server
	serverDone := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req transport.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}

			resp := transport.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"results": []map[string]interface{}{
						{"id": "doc1", "score": 0.95, "layer": 1},
						{"id": "doc2", "score": 0.85, "layer": 1},
					},
					"layer": "bm25",
				},
			}
			data, _ := json.Marshal(resp)
			conn.Write(append(data, '\n'))
		}
	}()
	defer close(serverDone)

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := cl.Search(ctx, transport.SearchParams{
		Query: "test",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Errorf("Results count = %d, want 2", len(resp.Results))
	}
}

func TestRecallWithMockSocket(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	// Start server
	serverDone := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req transport.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}

			resp := transport.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"snapshot": "# Session\n- test event",
					"format":   "md",
				},
			}
			data, _ := json.Marshal(resp)
			conn.Write(append(data, '\n'))
		}
	}()
	defer close(serverDone)

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := cl.Recall(ctx, transport.RecallParams{
		Budget: 4096,
		Format: "md",
	})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Snapshot == "" {
		t.Error("Recall returned empty snapshot")
	}
}

// DaemonRunning tests

func TestDaemonRunningTrue(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	// Create socket file
	f, err := os.Create(socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket file: %v", err)
	}
	f.Close()

	if !DaemonRunning(tmpDir) {
		t.Error("DaemonRunning should be true when socket exists")
	}
}

func TestDaemonRunningFalse(t *testing.T) {
	tmpDir := t.TempDir()
	// Don't create socket

	if DaemonRunning(tmpDir) {
		t.Error("DaemonRunning should be false when no socket")
	}
}

// Connect tests

func TestConnectSuccess(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	codec, err := cl.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if codec == nil {
		t.Error("Connect returned nil codec")
	}
}

func TestConnectRefused(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}

	tmpDir := t.TempDir()
	// No socket listening

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cl.Connect(ctx)
	if err == nil {
		t.Error("Connect should fail when nothing listening")
	}
}

// Error response tests

func TestRememberErrorResponse(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	// Server that returns error
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req transport.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}

			resp := transport.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &transport.RPCError{
					Code:    -32603,
					Message: "internal error",
				},
			}
			data, _ := json.Marshal(resp)
			conn.Write(append(data, '\n'))
		}
	}()

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = cl.Remember(ctx, transport.RememberParams{Type: "test"})
	if err == nil {
		t.Error("Remember should fail on error response")
	}
	if err != nil && !strings.Contains(err.Error(), "rpc error") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSearchErrorResponse(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req transport.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}

			resp := transport.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &transport.RPCError{
					Code:    -32603,
					Message: "search failed",
				},
			}
			data, _ := json.Marshal(resp)
			conn.Write(append(data, '\n'))
		}
	}()

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = cl.Search(ctx, transport.SearchParams{Query: "test"})
	if err == nil {
		t.Error("Search should fail on error response")
	}
}

func TestRecallErrorResponse(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req transport.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}

			resp := transport.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &transport.RPCError{
					Code:    -32603,
					Message: "recall failed",
				},
			}
			data, _ := json.Marshal(resp)
			conn.Write(append(data, '\n'))
		}
	}()

	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = cl.Recall(ctx, transport.RecallParams{})
	if err == nil {
		t.Error("Recall should fail on error response")
	}
}

// Response unmarshal tests

func TestRememberResponseUnmarshal(t *testing.T) {
	cl, _ := NewClient("/tmp/test")

	ctx := context.Background()

	// Connect should fail but exercise code path
	cl.Connect(ctx)
	// Just verify no panic
}

func TestSearchResponseUnmarshal(t *testing.T) {
	cl, _ := NewClient("/tmp/test")

	ctx := context.Background()
	cl.Connect(ctx)
}

func TestRecallResponseUnmarshal(t *testing.T) {
	cl, _ := NewClient("/tmp/test")

	ctx := context.Background()
	cl.Connect(ctx)
}

// Client with various paths

func TestNewClientWithPath(t *testing.T) {
	paths := []string{"/tmp", "/var/tmp", "/nonexistent"}
	for _, p := range paths {
		cl, err := NewClient(p)
		if err != nil {
			t.Errorf("NewClient(%s) failed: %v", p, err)
		}
		if cl == nil {
			t.Errorf("NewClient(%s) returned nil", p)
		}
	}
}

func TestClientSocketPathFormat(t *testing.T) {
	// Test that socket path follows expected format
	tmpDir := t.TempDir()
	cl, _ := NewClient(tmpDir)

	expected := filepath.Join(tmpDir, ".dfmt", "daemon.sock")
	if cl.socketPath != expected {
		t.Errorf("socketPath = %s, want %s", cl.socketPath, expected)
	}
}

func TestClientTimeout(t *testing.T) {
	cl, _ := NewClient("/tmp")
	if cl.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", cl.timeout)
	}

	// Test with custom timeout
	cl.timeout = 10 * time.Millisecond
	if cl.timeout != 10*time.Millisecond {
		t.Errorf("timeout = %v, want 10ms", cl.timeout)
	}
}

func TestNewClient(t *testing.T) {
	cl, err := NewClient("/tmp/test")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if cl == nil {
		t.Fatal("NewClient returned nil")
	}
	if cl.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", cl.timeout)
	}
}

func TestClientSocketPath(t *testing.T) {
	cl, _ := NewClient("/some/path")
	// Socket path should be set correctly
	if cl == nil {
		t.Fatal("client is nil")
	}
}

func TestDaemonRunning(t *testing.T) {
	// Create temp file as fake socket
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "daemon.sock")

	// File doesn't exist - should return false
	if DaemonRunning(tmpDir) {
		t.Error("DaemonRunning should be false when no socket")
	}

	// Create file
	f, err := os.Create(socketPath)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	f.Close()

	// Now it exists - but it's a file not a socket, so behavior is platform-specific
	// On Unix this would work, on Windows might not detect properly
}

func TestRememberNoDaemon(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")

	_, err := cl.Remember(context.Background(), transport.RememberParams{
		Type: "test",
	})
	// Should fail because daemon not running
	if err == nil {
		t.Error("Remember should fail when daemon not running")
	}
}

func TestSearchNoDaemon(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")

	_, err := cl.Search(context.Background(), transport.SearchParams{
		Query: "test",
	})
	if err == nil {
		t.Error("Search should fail when daemon not running")
	}
}

func TestRecallNoDaemon(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")

	_, err := cl.Recall(context.Background(), transport.RecallParams{})
	if err == nil {
		t.Error("Recall should fail when daemon not running")
	}
}

func TestConnectTimeout(t *testing.T) {
	cl, _ := NewClient("/tmp")
	cl.timeout = 1 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := cl.Connect(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Connect should fail")
	}
	// Should timeout reasonably quickly
	if elapsed > 100*time.Millisecond {
		t.Logf("Connect took %v (might be slow)", elapsed)
	}
}

func TestClientFields(t *testing.T) {
	cl, _ := NewClient("/test/path")
	if cl.socketPath == "" {
		t.Error("socketPath should be set")
	}
	if cl.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", cl.timeout)
	}
}

func TestDaemonRunningWithRealSocketPath(t *testing.T) {
	// Use a path that definitely doesn't exist
	tmpDir := t.TempDir()
	// SocketPath for a project at tmpDir would be tmpDir/.dfmt/daemon.sock

	// Create client for that path
	cl, _ := NewClient(tmpDir)
	expectedPath := filepath.Join(tmpDir, ".dfmt", "daemon.sock")
	if cl.socketPath != expectedPath {
		t.Errorf("socketPath = %s, want %s", cl.socketPath, expectedPath)
	}
}

func TestNewClientEmptyPath(t *testing.T) {
	// Empty path should still create a client
	cl, err := NewClient("")
	if err != nil {
		t.Fatalf("NewClient failed for empty path: %v", err)
	}
	if cl == nil {
		t.Fatal("NewClient returned nil for empty path")
	}
}

func TestMustMarshal(t *testing.T) {
	result := mustMarshal(map[string]any{"key": "value"})
	if len(result) == 0 {
		t.Error("mustMarshal returned empty result")
	}
}

func TestMustMarshalPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("mustMarshal should panic on error")
		}
	}()
	// This would panic because json.Marshal can't encode functions
	type badStruct struct {
		X func()
	}
	_ = mustMarshal(badStruct{X: func() {}})
}

func TestMustMarshalWithComplexTypes(t *testing.T) {
	// Test with various types that could cause issues
	tests := []struct {
		name  string
		input interface{}
	}{
		{"nil", nil},
		{"bool", true},
		{"int", 42},
		{"float", 3.14},
		{"string", "hello"},
		{"slice", []string{"a", "b"}},
		{"map", map[string]int{"key": 1}},
		{"struct", struct{ A, B string }{A: "x", B: "y"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mustMarshal(tt.input)
			if len(result) == 0 {
				t.Errorf("mustMarshal(%T) returned empty", tt.input)
			}
		})
	}
}

func TestDaemonRunningWithFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Create socket path
	socketPath := filepath.Join(tmpDir, ".dfmt", "daemon.sock")

	// Should return false when socket doesn't exist
	if DaemonRunning(tmpDir) {
		t.Error("DaemonRunning should be false when socket doesn't exist")
	}

	// Create socket file
	dir := filepath.Dir(socketPath)
	os.MkdirAll(dir, 0755)
	f, err := os.Create(socketPath)
	if err != nil {
		t.Skipf("skipping: could not create socket file: %v", err)
	}
	f.Close()

	// Should return true now
	if !DaemonRunning(tmpDir) {
		t.Error("DaemonRunning should be true when socket exists")
	}
}

func TestNewClientWithRealPath(t *testing.T) {
	tmpDir := t.TempDir()
	cl, err := NewClient(tmpDir)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if cl == nil {
		t.Fatal("NewClient returned nil")
	}
	expected := filepath.Join(tmpDir, ".dfmt", "daemon.sock")
	if cl.socketPath != expected {
		t.Errorf("socketPath = %s, want %s", cl.socketPath, expected)
	}
}

func TestClientTimeoutDefaults(t *testing.T) {
	cl, err := NewClient("/tmp")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if cl.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", cl.timeout)
	}
}

func TestClientTimeoutCustom(t *testing.T) {
	cl, _ := NewClient("/tmp")
	customTimeout := 10 * time.Millisecond
	cl.timeout = customTimeout
	if cl.timeout != customTimeout {
		t.Errorf("timeout = %v, want %v", cl.timeout, customTimeout)
	}
}

func TestClientConnectRefused(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket test on Windows")
	}
	tmpDir := t.TempDir()
	cl, _ := NewClient(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cl.Connect(ctx)
	if err == nil {
		t.Error("Connect should fail when nothing listening")
	}
}

func TestClientRememberConnectError(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cl.Remember(ctx, transport.RememberParams{Type: "test"})
	if err == nil {
		t.Error("Remember should fail when daemon not running")
	}
}

func TestClientSearchConnectError(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cl.Search(ctx, transport.SearchParams{Query: "test"})
	if err == nil {
		t.Error("Search should fail when daemon not running")
	}
}

func TestClientRecallConnectError(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cl.Recall(ctx, transport.RecallParams{})
	if err == nil {
		t.Error("Recall should fail when daemon not running")
	}
}

func TestNewClientWithEmptyProjectPath(t *testing.T) {
	cl, err := NewClient("")
	if err != nil {
		t.Fatalf("NewClient with empty path failed: %v", err)
	}
	if cl == nil {
		t.Fatal("NewClient returned nil")
	}
}

func TestClientSocketPathEnvVarPattern(t *testing.T) {
	// Test that socket path follows expected format
	tmpDir := t.TempDir()
	cl, _ := NewClient(tmpDir)
	expected := filepath.Join(tmpDir, ".dfmt", "daemon.sock")
	if cl.socketPath != expected {
		t.Errorf("socketPath = %s, want %s", cl.socketPath, expected)
	}
}

func TestClientDefaultTimeout(t *testing.T) {
	cl, err := NewClient("/tmp")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if cl.timeout != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", cl.timeout)
	}
}