// Package transport — Backend interface seam.
//
// Backend abstracts the surface MCPProtocol calls into so the same
// dispatch code works against either the in-process Handlers (legacy
// daemon and unit tests) or a remote *client.Client wrapped in an
// adapter (v0.5.0 dfmt mcp proxy mode). The interface is the cleanest
// place to draw the line — every method here has a 1:1 counterpart on
// both sides; nothing else MCPProtocol does crosses the boundary.
//
// Adding a new tool method is a four-touch change:
//  1. Add the method to this interface.
//  2. Add the method on Handlers (the local impl).
//  3. Add a delegation on ClientBackend (internal/client/backend.go).
//  4. Wire it into MCPProtocol.handleToolsCall.
//
// Keep the surface narrow — operator-only RPCs (drop project, refresh,
// metrics) do NOT belong here; they go through HTTP only.

package transport

import (
	"context"

	"github.com/ersinkoc/dfmt/internal/core"
)

// Backend is the contract MCPProtocol expects from whatever object
// answers tool calls. The local implementation is *Handlers; the
// proxy implementation is *client.ClientBackend.
//
// All method signatures match Handlers exactly so converting
// MCPProtocol from `handlers *Handlers` to `backend Backend` is a
// drop-in: existing call sites compile against the interface.
//
// StreamEvents is the one method whose signature was simplified for
// the interface — it takes a `from` cursor string instead of the full
// StreamParams struct. The HTTP transport is project-pinned at the
// connection level, so project_id does not need to ride on every
// stream request when a Client is on the other end. Local Handlers
// bridges the call into Stream(ctx, StreamParams{From: from,
// ProjectID: ProjectIDFrom(ctx)}) so the wire-side resolution still
// works for tests that build a Handlers directly.
type Backend interface {
	// Tool RPCs — agent-facing. Order mirrors the MCP tools/list output
	// so a reader scanning either file sees the same enumeration.
	Exec(ctx context.Context, params ExecParams) (*ExecResponse, error)
	Read(ctx context.Context, params ReadParams) (*ReadResponse, error)
	Fetch(ctx context.Context, params FetchParams) (*FetchResponse, error)
	Glob(ctx context.Context, params GlobParams) (*GlobResponse, error)
	Grep(ctx context.Context, params GrepParams) (*GrepResponse, error)
	Edit(ctx context.Context, params EditParams) (*EditResponse, error)
	Write(ctx context.Context, params WriteParams) (*WriteResponse, error)

	// Memory RPCs — agent-facing. Same ordering rationale.
	Remember(ctx context.Context, params RememberParams) (*RememberResponse, error)
	Search(ctx context.Context, params SearchParams) (*SearchResponse, error)
	Recall(ctx context.Context, params RecallParams) (*RecallResponse, error)
	Stats(ctx context.Context, params StatsParams) (*StatsResponse, error)

	// StreamEvents tails the project's journal from the given cursor.
	// MCPProtocol uses this only to populate the tool-description
	// compression telemetry cache (compressionStats), so a remote
	// implementation that returns nil with a budgeted timeout is
	// acceptable — tools/list will just lack the per-tool savings hint
	// for that session. Empty `from` means "stream from the start".
	StreamEvents(ctx context.Context, from string) (<-chan core.Event, error)
}

// StreamEvents on *Handlers bridges the simplified Backend signature
// into the existing Stream(ctx, StreamParams) call. Without this
// adapter the local in-process path would have to be different from
// the remote client path and MCPProtocol would need to branch — which
// is the coupling we are trying to remove.
//
// Intentionally short. The work happens in Stream; this only fills in
// the project_id from ctx so the resolver still routes correctly when
// the caller is a unit test that built ctx via WithProjectID.
func (h *Handlers) StreamEvents(ctx context.Context, from string) (<-chan core.Event, error) {
	return h.Stream(ctx, StreamParams{
		From:      from,
		ProjectID: ProjectIDFrom(ctx),
	})
}

// Compile-time assertion that *Handlers satisfies Backend. Without
// this the only way to discover a missing method would be at the
// MCPProtocol callsite which is much further from the interface
// definition. Failing here on the next `go build` after Backend
// gains a method is the early-warning we want.
var _ Backend = (*Handlers)(nil)
