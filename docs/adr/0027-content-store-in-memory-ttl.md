# ADR-0027: Content Store is In-Memory with a Default TTL

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-07-31 |
| Deciders | Ersin Koç |
| Supersedes | ADR-0007 (only the persistence half; the "separate from events" half holds) |
| Related | ADR-0003, ADR-0006, ADR-0009, ADR-0011 |

## Context

ADR-0007 split the content store from the event journal so that large,
ephemeral tool output would not drown the permanent JSONL journal. That
separation is correct and stays. What did not hold up is the **optional
persistence to `<proj>/.dfmt/content/<set-id>.json.gz`**, which ADR-0007
framed as opt-in via `ttl: forever`.

In the shipped code (refactor.md §I.1, findings CORE-2 and CORE-3):

- `internal/content/store.go` writes a gzip-encoded `<id>.json.gz` file on
  every `PutChunkSet` whose `TTL==0` (the default), driven by `SetContentStore`
  in `internal/daemon/daemon.go:192` and `internal/daemon/projectres.go:463`.
- `PutChunk` appends to `set.Chunks` **in memory only** and never re-persists.
- The only code path that re-writes the on-disk set after chunks arrive is
  `Store.Close()`. `Close()` is unreachable from production (no shutdown
  hook calls it); `PruneExpired` is unreachable for the same reason.
- `GetChunk`/`GetChunkSet`/`GetChunks` have no production callers — the
  `content_id` returned to agents cannot be dereferenced by any RPC, and
  the wire-dedup feature (`handlers.sentCache`) does not need on-disk
  bytes.

Consequence: every Exec/Read/Fetch grows `.dfmt/content/` by one gzip
file forever, the persisted set's `Chunks` list is empty, chunk bodies
are never persisted, and nothing prunes. The "optional persistence" knob
is a write-only failure mode that produces a plausible-looking disk
footprint and zero retrieval value.

The cross-call wire dedup (ADR-0009, ADR-0011) already keeps what it
needs — an in-memory `(projectID, kind, source, body)` cache that returns
the chunk-set ID — without touching the on-disk store. Persistence is
neither necessary nor sufficient for the feature it appears to enable.

## Decision

The content store is **in-memory only**, with a configurable default
TTL. There is no on-disk persistence path. Concretely:

- `StoreOptions.Path` is removed. `NewStore` does not create or accept
  a content directory.
- `PutChunkSet` does not write to disk in any code path.
- `LoadChunkSet` is removed (its only purpose was to read what
  `PutChunkSet` wrote).
- `Store.Close()` is removed; the previous flush-on-close was the only
  caller of `persistChunkSetToDisk`.
- `persistChunkSet` and `persistChunkSetToDisk` are removed, along with
  the `compress/gzip`, `encoding/json`, `io`, `os`, and `path/filepath`
  imports they required.
- `StoreOptions.DefaultChunkTTL` becomes the canonical way to age out
  stale content. Callers that want long-lived content set an explicit
  `set.TTL`. Callers that want session-scoped content rely on the
  default TTL and the existing `PruneExpired` / lazy-on-Get eviction.
- The daemon no longer creates `<proj>/.dfmt/content/` and no longer
  passes a path to `NewStore`.
- `content_id` continues to identify a chunk set for the lifetime of
  the daemon process and for at most `DefaultChunkTTL` after the last
  access. It is **not** advertised as a retrieval handle across
  daemon restarts — that property was never real in the shipped
  code, and removing the persistence hook removes the only signal
  that implied otherwise.

The "Content lives in a separate store from events" half of ADR-0007
remains the active decision; this ADR tightens the storage choice
within that boundary.

## Alternatives Considered

### A. Make persistence real — add a retrieval handler and a sweep

Add a `dfmt_content(id)` RPC that resolves `content_id` to chunk bytes,
add a startup sweep that reaps expired sets, add an authenticated
shutdown endpoint that calls `Store.Close()`, and wire
`Store.Close()` into the daemon's `shutdownCh`.

Rejected because:

- No user-facing surface currently dereferences `content_id`. The
  dashboard's "show content" link is rendered from in-memory state
  (`handlers.sentCache`), not from `.dfmt/content/`. Building a real
  retrieval path solves a problem nobody has hit.
- The persistence write is non-atomic (no `fsync`, deferred gzip close
  swallows errors, `O_TRUNC` over the live file). Doing it right
  (temp file + rename + `fsync`, chunk-level atomicity, the `index_persist.go`
  pattern) is a Wave-5-sized change on its own.
- A sweep is one more background job to size, schedule, and recover
  from. The "unbounded disk growth" failure mode only exists because
  the original code never ran a sweep; fixing it correctly is more
  work than dropping the feature.
- Privacy. Tool output stored at `0o700` is still on disk after a
  daemon restart; for an "agent capture" tool, the safer default is
  "stays in memory unless the user explicitly persists it" — which is
  what dropping persistence gives us.

### B. Keep `Path` as an opt-in knob; default it off

Keep `StoreOptions.Path`, default it to `""` in the daemon, and let
operators who want persistence set it. Existing tests that pass
`Path` keep working.

Rejected because:

- The persistence code path is currently the source of the bug. Leaving
  it in the tree means a future caller can opt in to the broken
  behavior without anyone noticing.
- The "operator opts in" scenario is hypothetical. ADR-0007 framed
  persistence as user-driven (`ttl: forever`); the shipped config has
  no such knob, and no user has asked for one.
- The persistence tests exist (store_test.go persistence paths,
  handlers_test.go with `Path: filepath.Join(tmp, "content")`) but they
  exercise a write path with no read counterpart — the only correct
  outcome for those tests under this decision is "the path is
  ignored".

### C. Persist only chunk bodies, not the set metadata

A middle ground: keep chunks on disk under their content-id and drop
the gzip-encoded set wrapper. Lets `dfmt_content(id)` resolve to bytes
without the meta-blank chunk-list problem.

Rejected because:

- Still requires a retrieval handler, a sweep, and the atomic-write
  rewrite. Same effort as A, with a smaller payoff (still no caller).
- Re-introduces the "disk full before idle-exit" failure mode that
  ADR-0007 §B called out as the reason not to go disk-first.

### D. Move content into the journal as a typed event stream

Embed chunks in the JSONL journal as a dedicated event type and let
the existing index infrastructure index them.

Rejected because:

- ADR-0007 §A already considered and rejected this; the reasoning
  (journal size explosion, load-time penalty, eviction-semantics
  mismatch) applies unchanged.

## Consequences

### Positive

- `.dfmt/content/` no longer exists. Daemon restart no longer leaves
  orphaned gzip files. There is no disk growth to bound.
- `Store.Close()` is no longer needed, removing a long-standing
  dangling lifecycle requirement. Daemon shutdown drops one
  responsibility.
- `content_id` semantics match reality: it identifies a chunk set
  for the lifetime of the daemon, plus any TTL the caller asked for.
  The misleading wire-level implication that it survives a restart
  is gone.
- The content store's hot path (`PutChunkSet` → `PutChunk` →
  `dedupRecord`) drops two `os`/`gzip`/`filepath` round-trips per
  Exec/Read/Fetch. This is small in absolute terms but cumulative
  on the busy path.
- `StoreOptions` shrinks to `MaxSize` and `DefaultChunkTTL`. The
  surviving API is the one callers were already using
  semantically.

### Negative

- Any future feature that needs to dereference `content_id` across
  a daemon restart has to invent its own persistence (and
  probably its own ADR). This is a deliberate narrowing of the
  design surface, not an oversight.
- The `chunkIDPattern` validation stays even though it was
  originally a path-safety guard. The IDs are still used as map
  keys and are returned on the wire; the validation now defends
  against log-injection rather than path-traversal, and the comment
  in `store.go` should say so.
- The persistence tests in `store_test.go` and the `Path`-using
  setup in `handlers_test.go` / `handlers_wiredup_test.go` are
  deleted. They were exercising code paths with no read counterpart
  and no production caller; the test signal they provided was
  "this writes without failing", which is not what we want to
  assert going forward.
- Migration: any operator who manually inspected `.dfmt/content/`
  to debug a content issue loses that affordance. `dfmt doctor`
  can list in-flight chunk-set IDs if needed, but that is a
  follow-up and not part of this ADR.

## Implementation Notes

- `internal/content/store.go` deletes: `maxDecompressedChunkSetBytes`,
  `persistChunkSet`, `persistChunkSetToDisk`, `LoadChunkSet`, `Close`,
  the `path` field on `Store`, the `path`-aware branches in
  `NewStore` and `PutChunkSet`, and the `compress/gzip`,
  `encoding/json`, `io`, `os`, `path/filepath`, and the now-unused
  `regexp` (re-evaluate — `chunkIDPattern` may stay) imports.
- `internal/content/store_test.go` deletes: the persistence-flavoured
  cases (any test that uses `Path` to round-trip a set through disk).
  `store_ttl_test.go` covers the surviving API and stays as-is.
- `internal/daemon/daemon.go` and `internal/daemon/projectres.go`
  stop computing `contentDir` and stop passing `Path` to
  `content.NewStore`. Their `defer ... Close()`-style cleanup (if any)
  is removed.
- `internal/transport/handlers.go::stashContent` does not change.
  Its semantics (write into the store, get a chunk-set ID, return it
  to the caller) are exactly what in-memory TTL gives it.
- `internal/transport/handlers_test.go` and
  `internal/transport/handlers_wiredup_test.go` drop the
  `Path: filepath.Join(tmp, "content")` argument and the
  `filepath` import if it becomes unused.
- `refactor.md` §I.1 marks CORE-2 and CORE-3 as `✅ FIXED` with the
  resulting commit hash. Both findings collapse under this change:
  the persistence path is gone (CORE-2) and the "set persisted before
  chunks exist" failure mode (CORE-3) is no longer reachable because
  no set is ever persisted.
- No new ADR is required for the `chunkIDPattern` retention; the
  comment edit is part of the same commit.

## Revisit

Revisit if:

- A user-visible feature actually needs `content_id` to survive a
  daemon restart. The current dashboard affordance uses
  `handlers.sentCache` for content retrieval within a session, which
  is unaffected.
- Cross-host content sharing (e.g. fetching content stored on
  another machine's daemon) becomes a goal. That is a separate
  transport problem and would need its own ADR.
- An operator asks for a forensic disk artifact ("the tool call
  produced this body, dump it to a file for me"). A
  `dfmt doctor dump <content_id>` that writes a one-off file at
  operator request — never automatically — is a reasonable
  follow-up that does not change this ADR.
