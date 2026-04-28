# ADR-0009: Cross-Call Content Dedup on the MCP Wire

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-28 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | ADR-0006, ADR-0007 |

## Context

`stashContent` (`internal/transport/handlers.go`) already deduplicates *storage*: when an Exec/Read/Fetch produces bytes whose `(kind, source, body)` SHA-256 matches a recently-seen tuple, it returns the existing chunk-set ID instead of writing a fresh copy. The cache is keyed by hash, scoped to 30 seconds, capped at 64 entries, and lives next to the redactor on the `Handlers` instance.

That layer saves *disk* bytes. It does not save *wire* bytes. A second tool call with the same body still ships the full filtered payload — `Stdout`/`Body`, `Summary`, `Matches`, `Vocabulary` — back to the agent. The agent already has those bytes in its context from the first call; the daemon is paying tokens to send them again.

Realistic agent loops produce this re-send pattern often:

- An agent reads `package.json`, decides to run `npm install`, then re-reads `package.json` to walk the dependency tree.
- A test agent runs `go test ./...` after every edit, getting the same PASS lines until something breaks.
- A research loop fetches the same documentation page from three angles ("how does it work?", "what about edge cases?", "what are the gotchas?").

In each case, the second-and-later responses contain exactly the bytes the model already saw.

## Decision

When a tool response carries a `content_id` whose value has been emitted to the agent earlier in this daemon's lifetime (within an LRU window), the response payload is **stripped** to a thin acknowledgement:

- `ContentID` — present, identical to the prior call so downstream tooling can correlate.
- `Summary` — set to the constant string `"(unchanged; same content_id)"`.
- `Exit` / `DurationMs` / `TimedOut` (Exec) / `Status` / `Headers` (Fetch) / `Size` / `ReadBytes` (Read) — present; these are operation metadata the agent needs even when the body hasn't changed.
- `Stdout` / `Stderr` / `Body` / `Content` / `Matches` / `Vocabulary` — empty, `omitempty` drops them off the wire.

Opt-out: when the call passes `Return: "raw"`, the dedup short-circuit is bypassed. Agents that explicitly want bytes get bytes.

The cache:

- Keyed by `content_id` (the chunk-set ULID), not by hash. ULID uniqueness is already guaranteed by `stashContent`'s upstream dedup, so per-call ULIDs collide only when the upstream cache also said "this is the same body."
- Capacity: 256 entries (4× the storage dedup cache; wire dedup needs to remember more, since it doesn't get evicted on a fresh stash).
- TTL: 10 minutes (20× the storage TTL; the wire dedup window matches a typical agent turn, not just a short retry).
- Eviction: FIFO via parallel `[]string`, like the existing dedupCache.

Storage layer (`Handlers.dedupCache` + `stashContent`) is unchanged. The two caches are independent so a regression in one cannot silently disable the other.

## Trade-offs

**Information loss.** If the agent's context has been compacted away between the first and second emission, the agent now sees `(unchanged; same content_id)` for a body it no longer has. Recovery: pass `Return: "raw"` to force a full re-emission. The cost is one extra round-trip; the alternative — emitting the full body every time, in case — burns tokens on every duplicate, every conversation.

**Daemon-wide vs session-scoped cache.** A daemon serves one agent per project. We do not currently track per-MCP-connection identity (no session ID flows through `MCPProtocol.Handle`). A daemon-wide cache means: if two distinct agents (e.g. Claude Code and Cursor) connect to the same project daemon and read the same file, the second agent gets a `(unchanged; same content_id)` response for content it has never seen. This is a real false-positive risk. Mitigation: agents that hit this are the same agents that benefit from `Return: "raw"`; the failure mode is "ask again with raw," not "lose the content." Per-session scoping is a deferred improvement that requires plumbing session IDs through the MCP layer first.

**Cache coherence with content store.** The content store's chunk-set TTL and the wire-dedup TTL drift independently. After 10 minutes, the wire cache forgets a content_id while the chunk-set may still exist; the agent gets a full payload again. This is benign — wire dedup is a best-effort optimization, never a correctness contract.

**Why not bytes-equal compare?** We could compare the redacted payload byte-for-byte to a prior emission. Cheaper-on-RAM but more expensive at runtime, and meaningfully harder to reason about because it conflates "same body" with "same payload" (two different intents over the same body produce different Matches[]). The hash-based content_id is already computed; reusing it is the simpler path.

**Why not per-tool sub-caches?** Exec/Read/Fetch all emit a `content_id` from the same `stashContent` call. A per-tool cache would store the same key three times if the tools happened to collide. The cache is keyed by content_id alone — collisions across tools are real cache hits, not mis-attributed ones.

## Consequences

- `internal/transport/handlers.go` gains `sentMu`/`sentCache`/`sentOrder` fields, two helper methods, and short-circuit branches in Exec/Read/Fetch (~30 lines net).
- `Stats` does not yet count wire-dedup hits separately from stash-dedup hits. Adding a counter is a one-line follow-up; deferred until we have a use for the metric.
- ADR-0008 (HTML parser) is unaffected — fetched HTML still goes through the parser before reaching the dedup check; the check sees the post-parse, pre-redaction body via `stashContent`'s hash.
- The 10-minute TTL is a tunable; a future ADR can move it to `config.yaml` if real-world usage shows the default is wrong. The cap is similarly tunable. Both are declared as `const` for now to keep the cache predictable.

## Verification

- New tests in `internal/transport/handlers_test.go` cover: identical body two calls in a row strips payload on the second; `Return: "raw"` skips the strip; `content_id` is preserved.
- `make test` and `go test -race ./internal/transport/...` stay green.
- `dfmt-bench tokensaving` does not exercise this path (it runs a single scenario per invocation); a future bench scenario could iterate the same body N times to surface the savings.

## Alternatives considered

1. **Emit a hash header, let the agent decide whether to re-fetch.** Adds a round-trip on every cache hit. Rejected.
2. **Diff-only emission (`first-call body`, then `Δ` from then on).** Useful for code-review-shape outputs, but the diff format itself costs bytes, and most reuse cases are byte-identical (same `package.json`, same test output). Diffing is overkill.
3. **Per-session cache via context-attached session ID.** Cleaner but requires plumbing session identity through the MCP transport. Tracked as a follow-up; out of scope here.
