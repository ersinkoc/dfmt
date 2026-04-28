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
