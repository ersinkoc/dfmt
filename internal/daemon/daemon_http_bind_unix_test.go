//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestNew_UnixTCPOptIn_SwapsToTCPListener — the dashboard requires HTTP
// over a routable address; a browser cannot dial a Unix socket. When the
// operator sets transport.http.enabled=true with a loopback bind, the
// daemon must spin up a TCP listener instead of the default Unix socket.
// Before B-3 this option was honored on Windows only and silently
// ignored on Unix.
func TestNew_UnixTCPOptIn_SwapsToTCPListener(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig()
	cfg.Transport.HTTP.Enabled = true
	cfg.Transport.HTTP.Bind = "127.0.0.1:0" // ephemeral; we just need TCP, not a fixed port

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	}()

	hs, ok := d.server.(*transport.HTTPServer)
	if !ok {
		t.Fatalf("d.server type = %T, want *transport.HTTPServer", d.server)
	}
	if got := hs.Bind(); got != "127.0.0.1:0" {
		t.Errorf("HTTPServer.Bind() = %q, want \"127.0.0.1:0\"", got)
	}

	// Socket file must NOT have been created — the two modes are
	// mutually exclusive. A stray socket file would mislead CLI clients
	// that look for it before the port file.
	socketFile := filepath.Join(dfmtDir, "dfmt.sock")
	if _, err := os.Stat(socketFile); !os.IsNotExist(err) {
		t.Errorf("socket file %s should not exist in TCP mode (err=%v)", socketFile, err)
	}
}

// TestNew_UnixDefaultUsesSocketAndAssignsServer covers two regressions:
// (1) the default Unix path must continue to use a Unix socket listener,
// and (2) d.server must be non-nil after New() — the previous Unix code
// path declared `var server Server` and never assigned it, so any test
// or runtime call to d.server.Start(ctx) would panic with a nil-interface
// method invocation. The bug went unnoticed because CI runs on Windows.
func TestNew_UnixDefaultUsesSocketAndAssignsServer(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig()
	// Default: HTTP.Enabled=false → Unix socket path.

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	}()

	if d.server == nil {
		t.Fatal("d.server is nil — Start(ctx) would panic on a nil-interface call")
	}
	if _, ok := d.server.(*transport.HTTPServer); !ok {
		t.Fatalf("d.server type = %T, want *transport.HTTPServer", d.server)
	}
}

// TestNew_UnixTCPOptInWithEmptyBindFallsBackToSocket — guards the
// "tcpOptIn" predicate: enabled=true alone is insufficient, the bind
// must also be non-empty. Without this nil-guard, NewHTTPServer("")
// would later fail at net.Listen with an opaque error after the daemon
// claimed startup success.
func TestNew_UnixTCPOptInWithEmptyBindFallsBackToSocket(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig()
	cfg.Transport.HTTP.Enabled = true
	cfg.Transport.HTTP.Bind = "" // bad config — must fall back to socket, not crash

	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	}()

	if d.server == nil {
		t.Fatal("d.server is nil after fallback to socket")
	}
}
