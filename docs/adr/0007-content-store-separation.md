# ADR-0007: Separate Content Store from Event Journal

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | SPECIFICATION.md §6.4, §6.7, §7.5, ADR-0003, ADR-0006 |

## Context

ADR-0003 established that DFMT stores session events in an append-only JSONL journal, with an in-memory inverted index for search. That design handles events — structured records of "what happened" — well.

ADR-0006 expands DFMT's scope to include sandboxed tool execution. Sandboxed operations produce another kind of data: the *content* of tool output. A 56 KB page snapshot, the text of a 200-line source file, the JSON body of a 10 MB API response. This data is different from events in kind: events are small, numerous, durable, inspectable, and permanently meaningful; content is large, less numerous, transient, and usually meaningful only for the duration of the operation that produced it plus a short window of retrieval.

The question is whether to store content in the same journal as events, or in a separate subsystem.

## Decision

**Content lives in a separate store from events.**

Concretely:

- **Event journal:** `<proj>/.dfmt/journal.jsonl`. Append-only, durable, human-readable. Stores small structured events (< 4 KB each typically). Permanent by default.
- **Content store:** in-memory primary with optional persistence to `<proj>/.dfmt/content/` as gzipped JSONL per chunk-set. Stores chunks of tool output. Ephemeral by default (cleared on daemon exit unless `ttl: forever` was specified).

Both use the same `Index` implementation (BM25, trigram, Porter), but as two separate instances with separate posting lists and separate size bounds.

## Alternatives Considered

### A. Store content inline in the event journal

Each sandboxed-tool call produces one event whose `Data` field contains the full output. Events and content live together.

Rejected because:
- **Journal size explosion.** A single `dfmt.exec` call can produce a 10 MB stdout. Inlining it blows up the journal. 100 exec calls in a session, all persisted forever, means gigabytes of stale content data on disk.
- **Load-time penalty.** The journal is streamed and replayed on daemon startup to rebuild the event index. Adding large content bodies to this path slows startup proportionally. Small-event startup is ~200 ms for 10k events; if half of them carry 64 KB payloads, it becomes >5 seconds.
- **Memory pressure at rebuild.** The index adder needs to tokenize each event's searchable text. Large content inline means big tokenization passes for every content event, every startup.
- **Mixing concerns.** The event journal is the permanent record of a session's shape — decisions, files, tasks. Drowning it in raw log lines and HTML dumps makes that record hard to read with `cat` and `jq`, which is the inspectability property ADR-0003 committed to.
- **Eviction semantics mismatch.** Events shouldn't be evicted except by explicit tombstone. Content must be evictable under memory pressure or idle-exit. Having a single store with two different eviction policies is incoherent.

### B. Store content as files on disk, reference by path from events

Each exec/read/fetch creates `<proj>/.dfmt/content/<set-id>.txt` (or similar). The event for the operation carries a pointer. Retrieval opens the file.

Rejected because:
- No indexing — retrieval is only by ID, not by content. The entire value of the sandbox layer (intent-driven filtering) requires in-memory or at least in-memory-mapped indexed access.
- Disk-full risk. A runaway exec loop could exhaust disk before the idle-exit evicts. The in-memory store with byte cap makes this failure mode impossible.
- Slow. Each retrieval is a file open + read + search. For the common case of searching within recent content during an active session, this is ~100x slower than in-memory.

### C. Use a separate SQLite database for content

A SQLite file, FTS5-indexed, stores content chunks. Events stay in JSONL.

Rejected because:
- Introduces SQLite into the dep graph after ADR-0003 specifically excluded it. Reasons given there apply unchanged (CGo, build friction, opacity, size).
- Two storage engines to maintain is worse than one storage engine used twice.

### D. Unified "typed log" with distinct streams

One conceptual log with two streams: "events stream" and "content stream," sharing a single storage engine but with separate namespaces and eviction policies.

Rejected because:
- This is effectively option A dressed up. In practice, separating streams at the storage level is equivalent to having separate stores; the "unified" framing just adds an abstraction layer without saving code.
- Conceptual purity loses to operational clarity. Two stores with different purposes are easier to reason about than one store with pretend sub-spaces.

## Consequences

### Positive

- **Event journal stays small, fast, inspectable.** The properties ADR-0003 committed to are preserved. `cat journal.jsonl | jq '.type'` remains fast and useful.
- **Content store stays bounded.** 64 MB resident cap, LRU eviction, idle-exit clearance. No unbounded disk growth. Memory pressure is a planned-for failure mode with a clear policy (evict LRU chunk set whole).
- **Clean lifecycle semantics.** Events: permanent unless tombstoned. Content: ephemeral unless opted into persistence. Each policy matches the natural lifetime of its data.
- **Separate tuning knobs.** Event index parameters (BM25 k1=1.2, b=0.75 for short structured records) and content index parameters (same formula but possibly different k1/b for longer prose passages) can diverge without compromise.
- **Failure isolation.** Content store corruption is a recoverable local problem ("drop and rebuild from nothing, lose current-session retrieval only"). Journal corruption is a serious data-integrity event. Separating the two means content problems don't threaten events.

### Negative

- **Two indexes in memory.** A session with heavy content use costs more RAM than before. Bounded by `sandbox.content.max_resident_bytes` (default 64 MB), so worst case is modest.
- **Cross-search needs special handling.** A user searching "did I see any mentions of `useEffect` today?" might want results from both events and content. The `dfmt.search` implementation must query both indexes and merge; the API exposes a `scope` parameter (`events`, `content`, `both`) to control this.
- **Persistence path is a new moving part.** When users opt into `ttl: forever` for a chunk set, it becomes a file on disk that must be managed: rotated, compressed, eventually pruned. Edge cases here will need operational experience.
- **Slightly more code.** Two instances of the index type plus glue. Estimated +300–500 lines compared to a single-store design. Small enough to not meaningfully change the effort estimate from ADR-0006.

## Implementation Notes

- The `Index` type is the same struct used by events. The content store wraps it with LRU eviction and a size tracker that the event index does not have. See `internal/content/store.go` in IMPLEMENTATION.md.
- Content chunks are written to the store with a `ParentID` grouping them into chunk sets, so eviction evicts the whole set atomically. Partial eviction would leave dangling chunks visible through `dfmt.search_content` — worse than not returning them at all.
- Optional persistence uses a per-set file (`content/<set-id>.jsonl.gz`) rather than a global file. This matches the "evict set atomically" model and allows cheap deletion of one set without touching others.
- The boundary between "inline return" (< 4 KB) and "chunked in content store" is governed by `sandbox.exec.inline_threshold`. Output below this threshold never enters the content store at all — it goes directly into the caller's response. This keeps small-output latency and memory overhead minimal.
- Cross-index search (via `dfmt.search` with `scope: both`) runs the two queries in parallel goroutines and merges by score. Merge treats the two scores as comparable because BM25 is relative to its own index; cross-index normalization is not attempted.

## Revisit

Revisit if:
- Users repeatedly want content to survive daemon restarts by default. Current default (session-ephemeral) is chosen for privacy and predictability; a change to "persist for N minutes" could be a reasonable compromise.
- Content store memory usage patterns show that 64 MB is either chronically over-provisioned (wasted RAM) or chronically under-provisioned (constant eviction churn). The cap is a first guess; measurement should calibrate it.
- Cross-index search quality becomes a support issue. Mitigation: a third combined index that unifies events and content as "everything" — possible but complex.
