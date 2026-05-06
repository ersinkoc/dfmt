package transport

import "context"

// sessionIDKey is the unexported context key transport layers use to attach
// a per-connection (or per-request, for stateless HTTP) session identifier.
// The unexported struct type ensures no other package — including tests in
// this package — can collide on the same key, even by accident.
type sessionIDKey struct{}

// WithSessionID returns ctx with sessionID attached. Empty IDs are stored
// as-is rather than being silently substituted: callers explicitly handle
// the absence so the contract stays visible at the call site.
//
// The session ID flows through to Handlers.seenBefore / Handlers.markSent
// so the wire-dedup cache (ADR-0009, refined by ADR-0011) keys per-session
// instead of daemon-wide. Two distinct callers see independent dedup
// histories; one caller's repeated reads of the same body still trigger
// the "(unchanged; same content_id)" short-circuit.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFrom returns the session ID attached to ctx, or "" when none was
// set. An empty session ID is treated by Handlers as "default session" so
// pre-existing tests and any not-yet-threaded path keep working — empty
// just means "no isolation, share the default bucket."
func SessionIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey{}).(string)
	return v
}

// projectIDKey is the unexported context key for the canonical project root
// path that an inbound RPC targets. The global daemon (Phase 2) routes each
// call to per-project resources (journal, index, content store, redact /
// permission overrides) using this value; legacy single-project daemons
// ignore it and use Handlers.project instead.
type projectIDKey struct{}

// WithProjectID returns ctx with projectID attached. Empty IDs are stored
// as-is — callers must handle the absence explicitly so the legacy fallback
// (h.project) remains visible at the resolution site.
func WithProjectID(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, projectIDKey{}, projectID)
}

// ProjectIDFrom returns the project ID attached to ctx, or "" when none was
// set. Handlers fall back to the per-process default project when this is
// empty so pre-Phase-2 callers (and the degraded MCP-without-project mode)
// keep working unchanged.
func ProjectIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(projectIDKey{}).(string)
	return v
}
