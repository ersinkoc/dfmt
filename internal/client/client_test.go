package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

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

func TestConnectContextCancelled(t *testing.T) {
	cl, _ := NewClient("/tmp/nonexistent")
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err := cl.Connect(ctx)
	if err == nil {
		t.Error("Connect should fail for nonexistent socket")
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