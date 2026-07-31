package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// fakeBackend records calls and fails a configurable number of times, so the
// retry path can be exercised without standing up a real daemon.
type fakeBackend struct {
	calls    int
	failFor  int // fail the first N calls
	lastCode string
}

var errBoom = errors.New("dial tcp 127.0.0.1:3490: connection refused")

func (f *fakeBackend) Exec(_ context.Context, p transport.ExecParams) (*transport.ExecResponse, error) {
	f.calls++
	f.lastCode = p.Code
	if f.calls <= f.failFor {
		return nil, errBoom
	}
	return &transport.ExecResponse{Exit: 0, Stdout: "ok"}, nil
}

func (f *fakeBackend) Read(context.Context, transport.ReadParams) (*transport.ReadResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Fetch(context.Context, transport.FetchParams) (*transport.FetchResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Glob(context.Context, transport.GlobParams) (*transport.GlobResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Grep(context.Context, transport.GrepParams) (*transport.GrepResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Edit(context.Context, transport.EditParams) (*transport.EditResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Write(context.Context, transport.WriteParams) (*transport.WriteResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Remember(context.Context, transport.RememberParams) (*transport.RememberResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Search(context.Context, transport.SearchParams) (*transport.SearchResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Recall(context.Context, transport.RecallParams) (*transport.RecallResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) Stats(context.Context, transport.StatsParams) (*transport.StatsResponse, error) {
	return nil, errBoom
}
func (f *fakeBackend) StreamEvents(context.Context, string) (<-chan core.Event, error) {
	return nil, errBoom
}

var _ transport.Backend = (*fakeBackend)(nil)

// A successful call must pass straight through, with no reconnect attempt
// and no duplicate execution.
func TestReconnectingBackendPassesThroughSuccess(t *testing.T) {
	f := &fakeBackend{}
	b := newReconnectingBackend(t.TempDir(), f)

	resp, err := b.Exec(context.Background(), transport.ExecParams{Code: "echo hi"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.Stdout != "ok" {
		t.Errorf("Stdout = %q, want %q", resp.Stdout, "ok")
	}
	if f.calls != 1 {
		t.Errorf("calls = %d, want 1 — a successful call must not be retried", f.calls)
	}
}

// offlineBackend returns a wrapper whose collaborators are pinned: no daemon
// is reachable and none can be started. Injecting these keeps the test
// independent of whatever daemon happens to be live on the machine and of
// state other tests in this package leave behind (CWD changes, stray .dfmt
// directories) — the reason an earlier version of these tests passed alone
// and failed inside the full suite.
func offlineBackend(t *testing.T, inner transport.Backend) *reconnectingBackend {
	t.Helper()
	b := newReconnectingBackend(t.TempDir(), inner)
	b.daemonAlive = func(string) bool { return false }
	b.ensure = func() error { return errors.New("no daemon (test)") }
	b.dial = func(string) transport.Backend { return nil }
	return b
}

// A failure that a reconnect cannot fix must surface unchanged. The caller
// must never lose the real error to a failed reconnect attempt.
func TestReconnectingBackendPreservesOriginalErrorWhenRenewFails(t *testing.T) {
	f := &fakeBackend{failFor: 99} // always fails
	b := offlineBackend(t, f)

	_, err := b.Exec(context.Background(), transport.ExecParams{Code: "boom"})
	if err == nil {
		t.Fatal("Exec returned nil error for an always-failing backend")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want the original backend error preserved", err)
	}
}

// A failure while a daemon IS reachable must not trigger a reconnect at all:
// exec and write are not idempotent, so a retry would double their side
// effects. Pin daemonAlive=true and assert the inner backend saw exactly one
// call.
func TestReconnectingBackendDoesNotRetryWhileDaemonIsAlive(t *testing.T) {
	f := &fakeBackend{failFor: 99}
	b := newReconnectingBackend(t.TempDir(), f)
	b.daemonAlive = func(string) bool { return true }
	b.ensure = func() error { t.Fatal("ensure called while the daemon was alive"); return nil }
	b.dial = func(string) transport.Backend { t.Fatal("dial called while the daemon was alive"); return nil }

	_, err := b.Exec(context.Background(), transport.ExecParams{Code: "rm -rf important"})
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want the original error", err)
	}
	if f.calls != 1 {
		t.Errorf("calls = %d, want 1 — a non-idempotent call must not be retried "+
			"when the daemon is still reachable", f.calls)
	}
}

// The recovery path itself: the first call fails, the daemon is gone, and a
// renewed connection succeeds on the retry. This is the behavior that keeps
// an agent session alive across a daemon idle-exit.
func TestReconnectingBackendRecoversOnRetry(t *testing.T) {
	dead := &fakeBackend{failFor: 99}
	live := &fakeBackend{}

	b := newReconnectingBackend(t.TempDir(), dead)
	b.daemonAlive = func(string) bool { return false } // daemon vanished
	b.ensure = func() error { return nil }             // respawn succeeds
	b.dial = func(string) transport.Backend { return live }

	resp, err := b.Exec(context.Background(), transport.ExecParams{Code: "echo hi"})
	if err != nil {
		t.Fatalf("Exec after reconnect: %v", err)
	}
	if resp.Stdout != "ok" {
		t.Errorf("Stdout = %q, want %q", resp.Stdout, "ok")
	}
	if dead.calls != 1 {
		t.Errorf("dead backend calls = %d, want 1", dead.calls)
	}
	if live.calls != 1 {
		t.Errorf("renewed backend calls = %d, want 1", live.calls)
	}
}

// With no initial backend and no reachable daemon, the wrapper must report a
// clear, actionable error rather than panicking on a nil inner backend.
func TestReconnectingBackendNilInnerReportsActionableError(t *testing.T) {
	b := offlineBackend(t, nil)
	_, err := b.Exec(context.Background(), transport.ExecParams{Code: "x"})
	if err == nil {
		t.Fatal("expected an error when no daemon is reachable")
	}
	if !errors.Is(err, errDaemonUnreachable) {
		t.Errorf("err = %v, want errDaemonUnreachable", err)
	}
}

// Every Backend method must route through the wrapper. A method that
// forgot to delegate would only surface when an agent happened to call
// that one tool, so exercise the whole surface and assert each reaches the
// inner backend (the fake fails everything except Exec, so an error from
// the fake IS proof the call was delegated).
func TestReconnectingBackendDelegatesEveryMethod(t *testing.T) {
	ctx := context.Background()

	// A fresh wrapper per method. After a failed renew the wrapper
	// deliberately leaves its inner backend nil so the NEXT call re-dials —
	// correct in production, where the daemon may have come back, but it
	// means a single shared wrapper would discard the fake after the first
	// call and every later method would hit a real (dead) endpoint instead.
	calls := map[string]func(transport.Backend) error{
		"Read":     func(b transport.Backend) error { _, err := b.Read(ctx, transport.ReadParams{}); return err },
		"Fetch":    func(b transport.Backend) error { _, err := b.Fetch(ctx, transport.FetchParams{}); return err },
		"Glob":     func(b transport.Backend) error { _, err := b.Glob(ctx, transport.GlobParams{}); return err },
		"Grep":     func(b transport.Backend) error { _, err := b.Grep(ctx, transport.GrepParams{}); return err },
		"Edit":     func(b transport.Backend) error { _, err := b.Edit(ctx, transport.EditParams{}); return err },
		"Write":    func(b transport.Backend) error { _, err := b.Write(ctx, transport.WriteParams{}); return err },
		"Remember": func(b transport.Backend) error { _, err := b.Remember(ctx, transport.RememberParams{}); return err },
		"Search":   func(b transport.Backend) error { _, err := b.Search(ctx, transport.SearchParams{}); return err },
		"Recall":   func(b transport.Backend) error { _, err := b.Recall(ctx, transport.RecallParams{}); return err },
		"Stats":    func(b transport.Backend) error { _, err := b.Stats(ctx, transport.StatsParams{}); return err },
		"Stream":   func(b transport.Backend) error { _, err := b.StreamEvents(ctx, ""); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			b := offlineBackend(t, &fakeBackend{failFor: 99})
			if err := call(b); !errors.Is(err, errBoom) {
				t.Errorf("err = %v, want the inner backend's error (proof of delegation)", err)
			}
		})
	}
}
