package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// blockingSandbox's Exec runs until released or canceled.
type blockingSandbox struct {
	sandbox.Sandbox
	started chan struct{}
	release chan struct{}
}

func (s *blockingSandbox) Exec(ctx context.Context, _ sandbox.ExecReq) (sandbox.ExecResp, error) {
	s.started <- struct{}{}
	select {
	case <-s.release:
		return sandbox.ExecResp{RawStdout: "done"}, nil
	case <-ctx.Done():
		return sandbox.ExecResp{Exit: -1}, ctx.Err()
	}
}

// TestIdleMonitorSparesInFlightWork: activity is recorded at RPC entry only,
// so an async job (up to 2 h) left unpolled for the idle timeout used to be
// killed by idle-exit's StopJobs. Running work now keeps the daemon up, and
// the countdown restarts when it ends.
func TestIdleMonitorSparesInFlightWork(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".dfmt"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig()
	cfg.Lifecycle.IdleTimeout = "1s"
	d, err := New(tmpDir, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sb := &blockingSandbox{started: make(chan struct{}, 1), release: make(chan struct{})}
	d.handlers = transport.NewHandlers(core.NewIndex(), nil, sb)

	ctx := context.Background()
	if _, err := d.handlers.Exec(ctx, transport.ExecParams{Code: "soak", Async: true}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-sb.started
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = d.Stop(ctx) }()

	time.Sleep(3 * time.Second) // three idle timeouts with the job running
	if !d.running.Load() {
		t.Fatal("daemon idle-exited while an async job was running")
	}

	close(sb.release)
	deadline := time.Now().Add(10 * time.Second)
	for d.running.Load() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if d.running.Load() {
		t.Error("daemon never idle-exited after the job finished")
	}
}
