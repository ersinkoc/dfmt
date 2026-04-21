//go:build unix

package transport

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketServerCreation(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

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

	if info.Mode() != 0700 {
		t.Errorf("expected socket mode 0700, got %v", info.Mode())
	}

	server.Stop(ctx)
}

func TestDecodeParams_Empty(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

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
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

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
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	data := []byte(`invalid json`)
	var v map[string]interface{}
	err := decodeParams(data, &v)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestSocketServer_Dispatch_UnknownMethod(t *testing.T) {
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

	req := &Request{
		ID:     1,
		Method: "unknownMethod",
		Params: nil,
	}

	ctx := context.Background()
	_, err = server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for unknown method, got nil")
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

func TestSocketServer_Stop_NotRunning(t *testing.T) {
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

	err = server.Stop(ctx)
	if err != nil {
		t.Errorf("server.Stop on not-running server failed: %v", err)
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

func TestSocketServer_Dispatch_RememberMethod_BadParams(t *testing.T) {
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

	params := []byte(`invalid json`)
	req := &Request{
		ID:     1,
		Method: "remember",
		Params: params,
	}

	ctx := context.Background()
	_, err = server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid params, got nil")
	}
}

func TestSocketServer_Dispatch_SearchMethod_BadParams(t *testing.T) {
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

	params := []byte(`invalid json`)
	req := &Request{
		ID:     2,
		Method: "search",
		Params: params,
	}

	ctx := context.Background()
	_, err = server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid params, got nil")
	}
}

func TestSocketServer_Dispatch_RecallMethod_BadParams(t *testing.T) {
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

	params := []byte(`invalid json`)
	req := &Request{
		ID:     3,
		Method: "recall",
		Params: params,
	}

	ctx := context.Background()
	_, err = server.dispatch(ctx, req)
	if err == nil {
		t.Error("expected error for invalid params, got nil")
	}
}
