# ADR-0011: Per-Session Scoping for Wire-Dedup Cache

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-28 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | ADR-0009 |

## Context

ADR-0009 introduced `Handlers.sentCache` (`internal/transport/handlers.go`) — a daemon-wide LRU that strips `Stdout`/`Body`/`Summary`/`Matches`/`Vocabulary` from a tool response when the response's `content_id` was already emitted earlier. The trade-off section explicitly listed an unresolved risk:

> A daemon serves one agent per project. We do not currently track per-MCP-connection identity (no session ID flows through `MCPProtocol.Handle`). A daemon-wide cache means: if two distinct agents (e.g. Claude Code and Cursor) connect to the same project daemon and read the same file, the second agent gets a `(unchanged; same content_id)` response for content it has never seen.

A follow-up exploration found the picture is partly different from what the ADR-0009 trade-off implied:

- **MCP-over-stdio is already isolated by process boundary.** Each `dfmt mcp` subprocess (`internal/cli/dispatch.go::runMCP`) gets its own `Handlers` instance via `transport.NewHandlers`. Two stdio agents are two separate processes with two separate caches. The "Claude Code vs Cursor" case is already safe.
- **The HTTP and Unix-socket transports share `Handlers`** across every connection, because `internal/daemon/daemon.go` constructs one `Handlers` and threads it into one `HTTPServer` / `SocketServer`. The genuine risk is two CLI invocations from different shells, or a CLI command racing the dashboard's `/api/recall` poll, both pulling from the same dedup bucket.
- **No session identity flows through `context.Context`** anywhere in transport. The MCP `initialize` request's `clientInfo` payload is parsed nowhere — it's discarded silently.

The intended outcome of this ADR: two distinct callers (across any transport) maintain independent wire-dedup histories. One caller's repeated reads still trigger the `(unchanged; same content_id)` short-circuit; the other caller still gets the full payload on its first read.

## Decision

**Session identity flows through `context.Context`.** Each transport entry point attaches a session ID before dispatching to `Handlers`; `seenBefore` / `markSent` key their cache by `sessionID + "\x00" + contentID`.

Concretely:

1. **`internal/transport/session.go` (new)** exposes `WithSessionID(ctx, id) context.Context` and `SessionIDFrom(ctx) string`. The context key is an unexported `sessionIDKey struct{}` so no other package can collide on the literal string `"session"`.

2. **`Handlers.seenBefore` / `Handlers.markSent`** take `context.Context` as a first argument. The cache key is `sessionID + "\x00" + contentID` — same NUL-separator trick `stashDedupKey` already uses to block prefix collisions. Empty session IDs fall into a single shared "default" bucket so any path that hasn't been threaded yet (and every existing test) still dedupes under the prior daemon-wide semantics.

3. **`SocketServer.handleConn`** mints one ULID per accepted connection and attaches it to the connection-derived `serverCtx`. Every dispatch call inside the read loop inherits that session ID.

4. **`HTTPServer.handle`** mints a session per *request*, not per connection. HTTP is stateless; connections are pooled and may serve requests from genuinely unrelated callers. A header-driven opt-in (`X-DFMT-Session: <ulid>`) lets disciplined callers (CLI, dashboard) bucket their own multi-call sessions; absent header → fresh ULID per request → effectively no dedup. CLI-side plumbing of the header is a separate follow-up; the server-side accept is here.

5. **`MCPProtocol`** gains a `sessionID string` field, populated with a ULID at `NewMCPProtocol`. `handleInitialize` parses `clientInfo` from `req.Params` and prefixes the ULID with `name/version:` when present (telemetry hook — agents that omit `clientInfo` still get a unique ID). `handleToolsCall` calls `WithSessionID(ctx, m.sessionID)` before dispatching to handlers.

The cache structure is unchanged: still `map[string]time.Time`, still capped at `sentCap`, still FIFO-evicted via `sentOrder`, still TTL'd at `sentTTL`. Only the key shape changes.

## Trade-offs

**Memory bound.** The cache cap is now per-key, where keys are `sessionID + "\x00" + contentID`. Worst case across `N` concurrent sessions × `M` distinct content_ids per session is `N * M`, but the FIFO eviction caps total entries at `sentCap = 256`. Two sessions × 200 content_ids each fits; ten sessions × 100 evicts the oldest. This is the right behaviour: dedup is a best-effort optimization, never a correctness contract.

**HTTP per-request fresh ULIDs.** A naive HTTP caller (curl, dashboard with no session header) gets effectively no wire-dedup — every request opens a new bucket. This matches HTTP's stateless nature and keeps the surprise surface small ("if I want dedup, I send the header"). The CLI client and the dashboard are the only realistic candidates to plumb the header; both are operator-controlled, both can be updated incrementally.

**Why not move `sentCache` to `MCPProtocol` / `HTTPServer` / `SocketServer`?** That would require three independent caches with the same eviction policy, the same TTL, and three separate test surfaces. Composite-keying the existing cache reuses the LRU machinery and keeps the contract centralized in one file.

**Why not bucket by `r.RemoteAddr`?** Loopback is always `127.0.0.1`. Remote addresses give zero entropy on the path that matters (CLI ↔ daemon). The header-based approach degrades gracefully (no header = unique session) without ever lying to a caller.

**`clientInfo` use is informational only.** A malicious client could spoof `clientInfo` to share a bucket with another client. The fallback ULID prevents accidental collisions (two Claude Code instances both initializing). Active spoofing would only DoS the spoofer's own dedup window, since the bucket is keyed by the full string — defense against confused agents, not active adversaries (the daemon is loopback-only by ADR-0006).

**Empty-session default-bucket fallback.** Tests and pre-ADR-0011 callers that don't attach a session ID share a single global "default" bucket. This is intentional — it preserves the ADR-0009 semantics for any path that hasn't been migrated, and makes the migration to per-session keys monotonic. A future tightening could refuse the default bucket entirely, but that would break tests and require synchronized roll-out.

## Consequences

- `internal/transport/session.go` is new (~30 LOC including comments).
- `internal/transport/handlers.go` gains a `sentCacheKey` helper and updates 6 call sites (3 `seenBefore`, 3 `markSent`).
- `internal/transport/socket.go` imports `core` for `NewULID`.
- `internal/transport/http.go` imports `core` for `NewULID`.
- `internal/transport/mcp.go` parses `clientInfo` in `handleInitialize` and attaches `sessionID` in `handleToolsCall`.
- `internal/transport/handlers_wiredup_test.go` retrofits all `seenBefore`/`markSent` callers to pass `context.Background()` and adds `TestWireDedup_SessionIsolation` + `TestWireDedup_NoSessionFallsBackToDefault`.
- No changes to the public dashboard or CLI surface. The `X-DFMT-Session` header is opt-in; absent header preserves prior behaviour (per-request session = effectively no dedup for HTTP). MCP and socket users see the same external surface — dedup just gets sharper.
- A future ADR can plumb `X-DFMT-Session` from `internal/client/client.go` so multi-command CLI sessions automatically benefit.

## Verification

- `internal/transport/handlers_wiredup_test.go::TestWireDedup_SessionIsolation` — two sessions reading the same body, each gets full payload on first read, dedup on second-within-session, no cross-session leakage.
- `internal/transport/handlers_wiredup_test.go::TestWireDedup_NoSessionFallsBackToDefault` — empty session ID still dedupes via the default bucket.
- `go build ./...` and `go test -count=1 ./...` stay green.
- `go vet ./...` clean.
- `dfmt-bench tokensaving` numbers unchanged (session attachment is one map lookup; effectively free on the bench's single-scenario path).

## Alternatives considered

1. **Per-transport caches** — `MCPProtocol`/`HTTPServer`/`SocketServer` each hold their own `sentCache`. Three caches × three eviction policies × three test surfaces. Rejected for surface area.
2. **Bucket by remote address** — useless on loopback. Rejected.
3. **Defer the HTTP fix; only handle MCP** — would close the false-positive risk for the primary agent path while leaving CLI ↔ daemon shared. Rejected because the composite-key change is the same one-line edit either way; doing it once gets all three transports.
4. **No fallback for empty session IDs (refuse to cache)** — cleanest semantics but breaks every existing test and requires a synchronized rollout. Rejected for migration cost; the default bucket is a benign compromise.
