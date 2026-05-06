package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestGlobalDaemonTargetReadsGlobalPaths verifies the helper resolves
// to ~/.dfmt/{port|sock} via DFMT_GLOBAL_DIR. Without this, the
// client's connection ordering would silently fall back to the legacy
// per-project path even when the user has a global daemon running.
func TestGlobalDaemonTargetReadsGlobalPaths(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	if runtime.GOOS == goosWindows {
		// Seed a port file in the override dir.
		pf := transport.PortFile{Port: 54321, Token: "test-token"}
		data, err := json.Marshal(pf)
		if err != nil {
			t.Fatalf("marshal port file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "port"), data, 0o600); err != nil {
			t.Fatalf("write port file: %v", err)
		}

		address, token, network, _ := globalDaemonTarget()
		if network != "tcp" {
			t.Errorf("network = %q, want tcp", network)
		}
		if address != "127.0.0.1:54321" {
			t.Errorf("address = %q, want 127.0.0.1:54321", address)
		}
		if token != "test-token" {
			t.Errorf("token = %q, want test-token", token)
		}
	} else {
		address, _, network, socketPath := globalDaemonTarget()
		if network != netUnix {
			t.Errorf("network = %q, want %q", network, netUnix)
		}
		want := filepath.Join(tmp, "daemon.sock")
		if address != want {
			t.Errorf("address = %q, want %q", address, want)
		}
		if socketPath != want {
			t.Errorf("socketPath = %q, want %q", socketPath, want)
		}
	}
}

// TestGlobalDaemonTargetEmptyWhenNoFile is the negative case: with an
// empty override directory there's no running daemon to talk to. On
// Windows the address must be empty so NewClient falls through to
// legacy/spawn; on Unix the socket path is always returned but the
// fastDialOK probe later filters it out via os.Stat.
func TestGlobalDaemonTargetEmptyWhenNoFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)

	address, token, network, _ := globalDaemonTarget()
	_ = token
	_ = network
	if runtime.GOOS == goosWindows {
		if address != "" {
			t.Errorf("Windows address with no port file = %q, want empty", address)
		}
	} else {
		// On Unix the function always returns the would-be socket path;
		// the liveness check happens in fastDialOK.
		want := filepath.Join(tmp, "daemon.sock")
		if address != want {
			t.Errorf("Unix address = %q, want %q", address, want)
		}
	}
}

// TestFastDialOKRejectsMissingTarget guards the empty-address branch
// — without it the client could attempt a TCP dial to "" or a Unix
// dial to a missing socket path and burn the 500ms timeout for nothing.
func TestFastDialOKRejectsMissingTarget(t *testing.T) {
	if fastDialOK("tcp", "") {
		t.Error("empty address should not be dialable")
	}
	if fastDialOK(netUnix, filepath.Join(t.TempDir(), "no-such-socket")) {
		t.Error("missing Unix socket should not be dialable")
	}
}

// TestNewClientPrefersRunningGlobalDaemon is the integration claim
// for Phase 2: when a global daemon is listening at the well-known
// path, NewClient picks it over starting a new per-project daemon.
// The test uses an HTTP-server-on-loopback as the stand-in for the
// daemon so we don't need to bring up the real one.
func TestNewClientPrefersRunningGlobalDaemon(t *testing.T) {
	if runtime.GOOS != goosWindows {
		// On Unix the global socket path lives at ~/.dfmt/daemon.sock and
		// is constructed by net.Listen on a real Unix socket. Building
		// that fake here would duplicate transport plumbing; the
		// Windows-side proves the same selection logic and the daemon-
		// level integration tests cover the Unix path end-to-end.
		t.Skip("Windows-only: validates port-file-based selection")
	}

	tmp := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", tmp)
	t.Setenv("DFMT_DISABLE_AUTOSTART", "1")

	// Spin up a minimal HTTP listener on loopback so fastDialOK
	// succeeds. We don't need the body to be RPC-shaped for NewClient
	// to return — the constructor returns as soon as the dial works.
	ln, err := net.Listen("tcp", addrLocalhost0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	srv := &http.Server{
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	pf := transport.PortFile{Port: port, Token: "test-token"}
	data, err := json.Marshal(pf)
	if err != nil {
		t.Fatalf("marshal port file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "port"), data, 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	projectPath := t.TempDir()
	c, err := NewClient(projectPath)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if !c.globalMode {
		t.Error("client should be in globalMode when global daemon is up")
	}
	if !strings.Contains(c.address, strconv.Itoa(port)) {
		t.Errorf("client address %q should contain global port %d", c.address, port)
	}
	if c.authToken != "test-token" {
		t.Errorf("client authToken = %q, want test-token", c.authToken)
	}
}
