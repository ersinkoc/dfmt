package cli

import (
	"context"
	"sync"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// reconnectingBackend keeps a `dfmt mcp` proxy usable across the death of
// the daemon it was talking to.
//
// The proxy resolved its backend exactly once, at startup, and never
// revalidated it. That made one failure mode certain rather than unlikely:
// the daemon idle-exits after 30 minutes, an agent session routinely idles
// longer than that, and from that moment every tool call returned a raw
// dial error ("connectex: No connection could be made") for the rest of the
// session. Nothing recovered on its own — the user had to notice the tools
// were dead and reconnect the MCP server by hand.
//
// On failure this re-runs ensureGlobalDaemon (which spawns a daemon if none
// is running, and replaces a stale one), rebuilds the client, and retries
// the call once.
//
// Detection deliberately does NOT parse the error. Dial failures surface as
// wrapped, platform-specific strings, and a string match would rot the first
// time a Windows locale or Go release reworded them. Instead we ask the
// question we can actually answer — is a daemon reachable right now? — via
// the same liveness probe every other code path uses. If one is reachable,
// the error came from the call itself and is returned untouched.
type reconnectingBackend struct {
	projectPath string

	// Collaborators are fields rather than direct package calls so tests can
	// control the reconnect decision. Calling client.DaemonRunning and
	// ensureGlobalDaemon directly made the behavior depend on whatever
	// daemon happened to be live on the developer's machine and on state
	// other tests in this package leave behind (CWD changes, stray .dfmt
	// dirs), which is not a property a unit test can pin.
	daemonAlive func(projectPath string) bool
	ensure      func() error
	dial        func(projectPath string) transport.Backend

	mu    sync.Mutex
	inner transport.Backend
}

// newReconnectingBackend wraps an already-connected backend. inner may be
// nil: the first call then establishes the connection, which is what lets an
// MCP session that started with no reachable daemon recover once one exists,
// instead of returning -32603 forever.
func newReconnectingBackend(projectPath string, inner transport.Backend) *reconnectingBackend {
	return &reconnectingBackend{
		projectPath: projectPath,
		daemonAlive: client.DaemonRunning,
		ensure:      ensureGlobalDaemon,
		dial:        dialBackend,
		inner:       inner,
	}
}

// current returns the live backend, dialing one if we don't have it yet.
func (b *reconnectingBackend) current() transport.Backend {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inner == nil {
		b.inner = b.dial(b.projectPath)
	}
	return b.inner
}

// renew drops the current backend and builds a fresh one, ensuring a daemon
// is running first. Returns nil when no daemon could be reached, in which
// case the caller reports the original error.
func (b *reconnectingBackend) renew() transport.Backend {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inner = nil
	if err := b.ensure(); err != nil {
		logging.Warnf("reconnect: %v", err)
		return nil
	}
	b.inner = b.dial(b.projectPath)
	if b.inner != nil {
		logging.Warnf("reconnected to the global daemon after a dropped connection")
	}
	return b.inner
}

// dialBackend builds a client backend without spawning anything. Callers
// that want a daemon guaranteed call ensureGlobalDaemon first.
func dialBackend(projectPath string) transport.Backend {
	cl, err := client.NewClient(projectPath)
	if err != nil {
		return nil
	}
	return client.NewBackend(cl)
}

// callRetrying runs one Backend method, retrying once against a rebuilt
// connection when the daemon turns out to be gone.
//
// pick selects the method off whichever backend instance is live, so the
// retry binds to the NEW connection rather than re-invoking a method value
// captured from the dead one.
func callRetrying[P any, R any](
	b *reconnectingBackend,
	ctx context.Context,
	params P,
	pick func(transport.Backend) func(context.Context, P) (R, error),
) (R, error) {
	var zero R
	be := b.current()
	if be == nil {
		if be = b.renew(); be == nil {
			return zero, errDaemonUnreachable
		}
	}
	resp, err := pick(be)(ctx, params)
	if err == nil {
		return resp, nil
	}
	// Only a vanished daemon is retryable. A policy denial, a bad path, or a
	// failing command must surface immediately and unchanged — retrying them
	// would double every side effect the first attempt already had.
	if b.daemonAlive(b.projectPath) {
		return resp, err
	}
	fresh := b.renew()
	if fresh == nil {
		return resp, err
	}
	return pick(fresh)(ctx, params)
}

var errDaemonUnreachable = errNoDaemonReachable{}

type errNoDaemonReachable struct{}

func (errNoDaemonReachable) Error() string {
	return "daemon not reachable and could not be started; run `dfmt doctor`"
}

// ── transport.Backend ───────────────────────────────────────────────
//
// Each method is the same one-line delegation through callRetrying; the
// generic helper carries the reconnect logic so it exists in exactly one
// place rather than twelve.

func (b *reconnectingBackend) Exec(ctx context.Context, p transport.ExecParams) (*transport.ExecResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.ExecParams) (*transport.ExecResponse, error) {
			return be.Exec
		},
	)
}

func (b *reconnectingBackend) Read(ctx context.Context, p transport.ReadParams) (*transport.ReadResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.ReadParams) (*transport.ReadResponse, error) {
			return be.Read
		},
	)
}

func (b *reconnectingBackend) Fetch(ctx context.Context, p transport.FetchParams) (*transport.FetchResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.FetchParams) (*transport.FetchResponse, error) {
			return be.Fetch
		},
	)
}

func (b *reconnectingBackend) Glob(ctx context.Context, p transport.GlobParams) (*transport.GlobResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.GlobParams) (*transport.GlobResponse, error) {
			return be.Glob
		},
	)
}

func (b *reconnectingBackend) Grep(ctx context.Context, p transport.GrepParams) (*transport.GrepResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.GrepParams) (*transport.GrepResponse, error) {
			return be.Grep
		},
	)
}

func (b *reconnectingBackend) Edit(ctx context.Context, p transport.EditParams) (*transport.EditResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.EditParams) (*transport.EditResponse, error) {
			return be.Edit
		},
	)
}

func (b *reconnectingBackend) Write(ctx context.Context, p transport.WriteParams) (*transport.WriteResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.WriteParams) (*transport.WriteResponse, error) {
			return be.Write
		},
	)
}

func (b *reconnectingBackend) Remember(
	ctx context.Context,
	p transport.RememberParams,
) (*transport.RememberResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.RememberParams) (*transport.RememberResponse, error) {
			return be.Remember
		},
	)
}

func (b *reconnectingBackend) Search(ctx context.Context, p transport.SearchParams) (*transport.SearchResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.SearchParams) (*transport.SearchResponse, error) {
			return be.Search
		},
	)
}

func (b *reconnectingBackend) Recall(ctx context.Context, p transport.RecallParams) (*transport.RecallResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.RecallParams) (*transport.RecallResponse, error) {
			return be.Recall
		},
	)
}

func (b *reconnectingBackend) Stats(ctx context.Context, p transport.StatsParams) (*transport.StatsResponse, error) {
	return callRetrying(
		b, ctx, p,
		func(be transport.Backend) func(context.Context, transport.StatsParams) (*transport.StatsResponse, error) {
			return be.Stats
		},
	)
}

// StreamEvents is not retried. It hands back a channel whose lifetime
// outlives this call, so a mid-stream daemon death is the consumer's to
// notice (the channel closes); transparently re-subscribing would silently
// restart the event position and duplicate or skip events.
func (b *reconnectingBackend) StreamEvents(ctx context.Context, from string) (<-chan core.Event, error) {
	be := b.current()
	if be == nil {
		if be = b.renew(); be == nil {
			return nil, errDaemonUnreachable
		}
	}
	return be.StreamEvents(ctx, from)
}

// Compile-time guard, mirroring the pattern in transport/backend.go and
// client/backend.go: drift between transport.Backend and this wrapper
// surfaces here rather than at the call site.
var _ transport.Backend = (*reconnectingBackend)(nil)
