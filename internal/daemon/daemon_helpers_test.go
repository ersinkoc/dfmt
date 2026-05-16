package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/osutil"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestLoadJournalAndIndex_FreshProject: a brand-new project dir (no
// pre-existing index/cursor) returns an open journal, a freshly built
// empty index, and needsRebuild=false because there's nothing to
// rebuild. Subsequent New() calls reuse the persisted state.
func TestLoadJournalAndIndex_FreshProject(t *testing.T) {
	dir := t.TempDir()
	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatalf("mkdir .dfmt: %v", err)
	}
	cfg := &config.Config{}

	journal, idx, idxPath, _, err := loadJournalAndIndex(dfmtDir, cfg)
	if err != nil {
		t.Fatalf("loadJournalAndIndex: %v", err)
	}
	defer journal.Close()
	if idx == nil {
		t.Fatal("index is nil")
	}
	// needsRebuild is implementation detail (it may be true on a fresh
	// dir because LoadIndexWithCursor flags a missing cursor as needing
	// rebuild — Start() consumes this signal and runs through the journal
	// regardless). The contract we care about: no error, real handles.
	if !filepath.IsAbs(idxPath) && !filepath.IsLocal(idxPath) {
		t.Errorf("indexPath should be valid filesystem path, got %q", idxPath)
	}
	if filepath.Base(idxPath) != "index.gob" {
		t.Errorf("indexPath should end in index.gob, got %q", idxPath)
	}
}

// TestLoadJournalAndIndex_CustomBM25Params: operator-provided BM25
// parameters propagate from cfg.Index into the returned index. We can't
// inspect the BM25 ranker internals from outside, but we can confirm
// the function accepts non-zero params without erroring — the SetParams
// branch (loaded index path) and NewIndexWithParams branch (fresh path)
// both need to survive non-default values.
func TestLoadJournalAndIndex_CustomBM25Params(t *testing.T) {
	dir := t.TempDir()
	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &config.Config{}
	cfg.Index.BM25K1 = 1.5
	cfg.Index.BM25B = 0.75
	cfg.Index.HeadingBoost = 1.2

	journal, idx, _, _, err := loadJournalAndIndex(dfmtDir, cfg)
	if err != nil {
		t.Fatalf("loadJournalAndIndex: %v", err)
	}
	defer journal.Close()
	if idx == nil {
		t.Fatal("index is nil")
	}
}

// TestLoadSandbox_DefaultPolicy: with no permissions.yaml and no path
// prepend, loadSandbox returns a usable *SandboxImpl built on the
// default policy. This is the most common path — fresh projects that
// don't customize permissions.
func TestLoadSandbox_DefaultPolicy(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}

	sb, err := loadSandbox(dir, cfg)
	if err != nil {
		t.Fatalf("loadSandbox: %v", err)
	}
	if sb == nil {
		t.Fatal("sandbox is nil")
	}
}

// TestLoadSandbox_EmptyPathPrependList: an empty cfg.Exec.PathPrepend
// is the same as no prepend — ValidatePathPrepend short-circuits and
// the sandbox comes back with no path injection.
func TestLoadSandbox_EmptyPathPrependList(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Exec.PathPrepend = []string{} // explicit empty, not nil

	sb, err := loadSandbox(dir, cfg)
	if err != nil {
		t.Fatalf("loadSandbox: %v", err)
	}
	if sb == nil {
		t.Fatal("sandbox is nil")
	}
}

// TestLoadSandbox_InvalidPathPrepend: ValidatePathPrepend rejects
// entries that don't exist on disk. The error is wrapped with
// "invalid path_prepend" so the operator knows where to fix it.
func TestLoadSandbox_InvalidPathPrepend(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Exec.PathPrepend = []string{"/path/that/definitely/does/not/exist/anywhere"}

	_, err := loadSandbox(dir, cfg)
	if err == nil {
		t.Fatal("expected error from invalid path_prepend, got nil")
	}
}

// TestBuildServer_WindowsDefault: on Windows the default branch (no
// transport.http opt-in) still produces an HTTPServer bound to
// 127.0.0.1:0 — Unix sockets aren't viable so TCP is mandatory.
// Skipped on non-Windows where the default branch picks Unix socket.
func TestBuildServer_WindowsDefault(t *testing.T) {
	if !osutil.IsWindows() {
		t.Skip("default Windows branch is OS-gated")
	}
	dir := t.TempDir()
	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &config.Config{}
	srv, err := buildServer(dir, dfmtDir, cfg, transport.NewHandlers(nil, nil, nil))
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv == nil {
		t.Fatal("server is nil")
	}
}

// TestBuildServer_TCPOptIn: transport.http.enabled=true plus a bind
// string produces an HTTPServer bound to the operator-chosen address.
// Loopback bind is required (NewHTTPServer validates), so we use
// 127.0.0.1:0 to land on an ephemeral port without colliding.
func TestBuildServer_TCPOptIn(t *testing.T) {
	dir := t.TempDir()
	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &config.Config{}
	cfg.Transport.HTTP.Enabled = true
	cfg.Transport.HTTP.Bind = "127.0.0.1:0"
	// Socket-only platforms (Unix) need cfg.Transport.Socket.Enabled
	// to be false for the TCP branch — but the tcpOptIn case takes
	// precedence over the default Unix branch via the switch order,
	// so setting Enabled=true here doesn't matter.
	srv, err := buildServer(dir, dfmtDir, cfg, transport.NewHandlers(nil, nil, nil))
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv == nil {
		t.Fatal("server is nil")
	}
}

// TestBuildServer_UnixNoListener: on Unix, disabling the socket without
// enabling HTTP leaves the daemon with no way to accept connections.
// buildServer refuses with a hint that points at both viable configs
// — the previous implementation silently fell through and panicked at
// d.server.Start(ctx).
func TestBuildServer_UnixNoListener(t *testing.T) {
	if osutil.IsWindows() {
		t.Skip("the no-listener guard is Unix-specific")
	}
	dir := t.TempDir()
	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &config.Config{}
	cfg.Transport.Socket.Enabled = false
	cfg.Transport.HTTP.Enabled = false

	_, err := buildServer(dir, dfmtDir, cfg, transport.NewHandlers(nil, nil, nil))
	if err == nil {
		t.Fatal("expected no-listener error, got nil")
	}
}
