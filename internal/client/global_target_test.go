package client

import (
	"net"
	"os"
	"testing"

	"github.com/ersinkoc/dfmt/internal/osutil"
	"github.com/ersinkoc/dfmt/internal/project"
)

// TestGlobalDaemonTarget_UnixTCPFallback pins the v0.7.4 release-gate fix:
// on Unix the global daemon binds a Unix socket when transport.http is
// disabled but TCP + a port file when it is enabled (the default). The
// client must find it in both cases — probe the socket first with a
// dial-based liveness check (so a stale socket file is seen through), then
// fall back to the port file. Before this fallback a TCP-bound global
// daemon was invisible on Linux (DaemonRunning probed only the socket),
// which is what broke the release gate's in-process integration tests on
// ubuntu while Windows (which reads the port file) stayed green.
func TestGlobalDaemonTarget_UnixTCPFallback(t *testing.T) {
	if osutil.IsWindows() {
		t.Skip("unix socket/TCP fallback is a unix-only path")
	}
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)

	socketPath := project.GlobalSocketPath()

	// 1. No rendezvous at all → canonical empty-daemon socket target.
	addr, token, network, sp := globalDaemonTarget()
	if network != netUnix || addr != socketPath || token != "" || sp != socketPath {
		t.Fatalf("empty: got (%q,%q,%q,%q), want (%q,\"\",unix,%q)",
			addr, token, network, sp, socketPath, socketPath)
	}

	// 2. Port file only (TCP daemon) → TCP fallback carrying port + token.
	if err := os.WriteFile(
		project.GlobalPortPath(),
		[]byte(`{"port":41999,"token":"sekrit"}`), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	addr, token, network, sp = globalDaemonTarget()
	if network != "tcp" || addr != "127.0.0.1:41999" || token != "sekrit" || sp != "" {
		t.Fatalf("tcp fallback: got (%q,%q,%q,%q), want (127.0.0.1:41999,sekrit,tcp,\"\")",
			addr, token, network, sp)
	}

	// 3. Live socket present alongside the port file → socket wins.
	ln, err := net.Listen(netUnix, socketPath)
	if err != nil {
		t.Fatalf("listen socket: %v", err)
	}
	addr, token, network, sp = globalDaemonTarget()
	if network != netUnix || addr != socketPath || token != "" || sp != socketPath {
		t.Fatalf("live socket: got (%q,%q,%q,%q), want (%q,\"\",unix,%q)",
			addr, token, network, sp, socketPath, socketPath)
	}

	// 4. Stale socket (file present, nothing listening) + port file → the
	// dial-based probe sees through the stale socket and falls back to TCP
	// instead of letting the dead socket shadow the live TCP daemon.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close() // leaves the socket file behind with no listener
	addr, token, network, sp = globalDaemonTarget()
	if network != "tcp" || addr != "127.0.0.1:41999" || token != "sekrit" || sp != "" {
		t.Fatalf("stale socket: got (%q,%q,%q,%q), want (127.0.0.1:41999,sekrit,tcp,\"\")",
			addr, token, network, sp)
	}
}
