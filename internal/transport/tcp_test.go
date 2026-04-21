package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestNewTCPServer(t *testing.T) {
	handlers := NewHandlers(nil, nil)
	server := NewTCPServer("localhost:0", handlers)
	if server == nil {
		t.Fatal("NewTCPServer returned nil")
	}
	if server.handlers != handlers {
		t.Error("handlers not set correctly")
	}
}

func TestTCPServerPortFile(t *testing.T) {
	handlers := NewHandlers(nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, "port")

	server.SetPortFile(portFile)
	if server.portFile != portFile {
		t.Errorf("portFile = %s, want %s", server.portFile, portFile)
	}
}

func TestTCPServerPort(t *testing.T) {
	handlers := NewHandlers(nil, nil)
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
	handlers := NewHandlers(nil, nil)
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
	handlers := NewHandlers(nil, nil)
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
	handlers := NewHandlers(nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, ".dfmt", "port")

	if err := server.writePortFile(portFile, 12345); err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != "12345" {
		t.Errorf("port file content = %s, want 12345", string(data))
	}
}

func TestTCPServerWritePortFileCreatesDir(t *testing.T) {
	handlers := NewHandlers(nil, nil)
	server := NewTCPServer("localhost:0", handlers)

	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, ".dfmt", "nested", "port")

	if err := server.writePortFile(portFile, 54321); err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != "54321" {
		t.Errorf("port file content = %s, want 54321", string(data))
	}
}

func TestTCPServerDefaultAddr(t *testing.T) {
	handlers := NewHandlers(nil, nil)
	server := NewTCPServer("", handlers)
	if server.addr != "" {
		t.Errorf("addr = %s, want empty", server.addr)
	}
}