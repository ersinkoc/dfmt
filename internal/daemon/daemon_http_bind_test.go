package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestNew_HTTPBindFromConfig_Windows verifies cfg.Transport.HTTP.Bind is
// honored on Windows when HTTP is opted in. Before this wiring the daemon
// hardcoded 127.0.0.1:0 and silently ignored the configured bind, so the
// dashboard URL was never stable.
func TestNew_HTTPBindFromConfig_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only: Unix daemon uses a Unix socket listener, not TCP bind")
	}
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig()
	cfg.Transport.HTTP.Enabled = true
	cfg.Transport.HTTP.Bind = "127.0.0.1:64321"

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
	if got := hs.Bind(); got != "127.0.0.1:64321" {
		t.Errorf("HTTPServer.Bind() = %q, want \"127.0.0.1:64321\"", got)
	}
}

// TestNew_HTTPBindEphemeralWhenDisabled verifies the default ephemeral
// bind kicks in when HTTP is not opted in — preserves the pre-existing
// behavior (and avoids two daemons fighting for the same fixed port).
func TestNew_HTTPBindEphemeralWhenDisabled(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only: Unix daemon uses a Unix socket listener, not TCP bind")
	}
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig()
	// Default(): HTTP.Enabled=false, HTTP.Bind="127.0.0.1:8765".
	// Daemon must ignore the bind when not enabled and stick to ephemeral.
	if cfg.Transport.HTTP.Enabled {
		t.Fatalf("test config invariant: HTTP.Enabled must default false; got true")
	}

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
		t.Errorf("HTTPServer.Bind() = %q, want \"127.0.0.1:0\" (ephemeral)", got)
	}
}

// TestNew_HTTPBindEnabledButEmptyFallsBackToEphemeral guards against an
// operator setting enabled=true without supplying a bind. Without the
// nil-guard the daemon would try to net.Listen("") and crash at Start.
func TestNew_HTTPBindEnabledButEmptyFallsBackToEphemeral(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only: Unix daemon uses a Unix socket listener, not TCP bind")
	}
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig()
	cfg.Transport.HTTP.Enabled = true
	cfg.Transport.HTTP.Bind = "" // bad config — must not crash

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
		t.Errorf("HTTPServer.Bind() = %q, want \"127.0.0.1:0\" (empty bind must fall back to ephemeral)", got)
	}
}
