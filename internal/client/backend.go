// Package client — ClientBackend adapter.
//
// ClientBackend wraps *Client into a transport.Backend so MCPProtocol
// can dispatch tool calls remotely (over the daemon's HTTP / Unix
// socket transport) without knowing it isn't talking to an in-process
// *Handlers. This is the load-bearing piece that lets v0.5.0's
// `dfmt mcp` subprocess be a thin proxy with no local journal/index
// state of its own.
//
// Why an adapter and not "*Client implements transport.Backend":
//   - The transport package defines Backend; client imports transport
//     for the param/response types. If *Client implemented Backend
//     directly, the interface assertion would still live in client/.
//   - More importantly, defining the methods on a wrapper type keeps
//     the Client surface simpler — Client has lifecycle methods like
//     Connect, ensureDaemon, doHTTP that don't belong on the Backend
//     interface but DO belong on the underlying transport object.
//     The wrapper makes the backend role explicit.

package client

import (
	"context"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// ClientBackend adapts a *Client to transport.Backend. Construct with
// NewBackend(cl); pass to transport.NewMCPProtocol when running a
// proxy MCP subprocess. The wrapped Client owns project-id stamping,
// daemon spawn, retries, and auth — every call here is a one-line
// delegation.
type ClientBackend struct {
	cl *Client
}

// NewBackend wraps an existing Client so it can be used as a
// transport.Backend. The Client's project-id was set at NewClient
// time, so subsequent Backend calls automatically carry the right
// project_id without extra parameter plumbing.
func NewBackend(cl *Client) *ClientBackend {
	return &ClientBackend{cl: cl}
}

// ── Tool RPCs ──────────────────────────────────────────────────────

// Exec forwards to the daemon's sandbox.exec handler.
func (b *ClientBackend) Exec(ctx context.Context, params transport.ExecParams) (*transport.ExecResponse, error) {
	return b.cl.Exec(ctx, params)
}

// Read forwards to the daemon's sandbox.read handler.
func (b *ClientBackend) Read(ctx context.Context, params transport.ReadParams) (*transport.ReadResponse, error) {
	return b.cl.Read(ctx, params)
}

// Fetch forwards to the daemon's sandbox.fetch handler (HTTP via daemon).
func (b *ClientBackend) Fetch(ctx context.Context, params transport.FetchParams) (*transport.FetchResponse, error) {
	return b.cl.Fetch(ctx, params)
}

// Glob forwards to the daemon's sandbox.glob handler.
func (b *ClientBackend) Glob(ctx context.Context, params transport.GlobParams) (*transport.GlobResponse, error) {
	return b.cl.Glob(ctx, params)
}

// Grep forwards to the daemon's sandbox.grep handler.
func (b *ClientBackend) Grep(ctx context.Context, params transport.GrepParams) (*transport.GrepResponse, error) {
	return b.cl.Grep(ctx, params)
}

// Edit forwards to the daemon's sandbox.edit handler.
func (b *ClientBackend) Edit(ctx context.Context, params transport.EditParams) (*transport.EditResponse, error) {
	return b.cl.Edit(ctx, params)
}

// Write forwards to the daemon's sandbox.write handler.
func (b *ClientBackend) Write(ctx context.Context, params transport.WriteParams) (*transport.WriteResponse, error) {
	return b.cl.Write(ctx, params)
}

// ── Memory RPCs ────────────────────────────────────────────────────

// Remember forwards to the daemon's remember handler. The daemon
// owns the journal write; the MCP subprocess no longer has its own.
func (b *ClientBackend) Remember(
	ctx context.Context, params transport.RememberParams,
) (*transport.RememberResponse, error) {
	return b.cl.Remember(ctx, params)
}

// Search forwards to the daemon's search handler. v0.5.0 closes the
// pre-existing index drift: with the proxy backend there is only one
// index in the system (the daemon's), so search results converge
// immediately rather than requiring a 3-second tail-poll cycle.
func (b *ClientBackend) Search(ctx context.Context, params transport.SearchParams) (*transport.SearchResponse, error) {
	return b.cl.Search(ctx, params)
}

// Recall forwards to the daemon's recall handler.
func (b *ClientBackend) Recall(ctx context.Context, params transport.RecallParams) (*transport.RecallResponse, error) {
	return b.cl.Recall(ctx, params)
}

// Stats forwards to the daemon's stats handler.
func (b *ClientBackend) Stats(ctx context.Context, params transport.StatsParams) (*transport.StatsResponse, error) {
	return b.cl.Stats(ctx, params)
}

// ── Stream ─────────────────────────────────────────────────────────

// StreamEvents forwards to the daemon's /api/stream SSE endpoint via
// Client.StreamEvents. Used by MCPProtocol.compressionStats to populate
// the tools/list telemetry cache; a misbehaving stream from a slow
// daemon is bounded by the caller's context timeout (currently 2 s).
func (b *ClientBackend) StreamEvents(ctx context.Context, from string) (<-chan core.Event, error) {
	return b.cl.StreamEvents(ctx, from)
}

// Compile-time guard: any future drift between transport.Backend and
// ClientBackend produces an error here, immediately next to the
// adapter file. The same pattern is used in transport/backend.go for
// *Handlers.
var _ transport.Backend = (*ClientBackend)(nil)
