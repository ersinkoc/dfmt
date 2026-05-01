# ADR-0017: Journal Size() Interface Extension

- **Status:** Accepted
- **Date:** 2026-05-02
- **Supersedes:** —
- **Superseded by:** —
- **Related:** ADR-0003 (JSONL Journal + Custom Index), ADR-0016 (Prometheus `/metrics` Endpoint)

## Context

ADR-0016 published `/metrics` with daemon-level gauges (memory, GC,
goroutines) and tool-call counters. The 2026-05-02 amendment added
index-doc and dedup-cache size gauges. One observability gap remained
explicitly deferred: `dfmt_journal_bytes` — the on-disk size of the
active journal file.

Operators want this signal because:

- **Rotation health**. The journal rotates at `maxBytes` (10 MiB by
  default). A `dfmt_journal_bytes` value pinned at the cap means
  rotation is firing on schedule; a value drifting past the cap means
  rotation failed and disk usage is unbounded.
- **Activity rate**. Per-second derivative of journal bytes (PromQL
  `rate(dfmt_journal_bytes[5m])`) is a cheap proxy for tool-call
  intensity, useful for capacity planning before deploying alongside
  another local daemon.
- **Disk-pressure correlation**. When local-disk monitoring fires,
  operators want to know whether DFMT's journal is contributing.

The previous metrics commits could not surface this because the
`core.Journal` interface had no read-side accessor for size — only
the write-side `Append`, `Stream`, `Checkpoint`, `Rotate`, `Close`.
Surfacing it through `Handlers` directly (e.g. piping the journal
file path into `Handlers` and calling `os.Stat` from outside) would
leak the implementation detail that the journal is file-backed: the
`Journal` interface is also implemented by mock journals in tests
and could in principle gain a non-file backing later.

## Decision

### Add `Size() (int64, error)` to the `Journal` interface

```go
type Journal interface {
    Append(ctx context.Context, e Event) error
    Stream(ctx context.Context, from string) (<-chan Event, error)
    Checkpoint(ctx context.Context) (string, error)
    Rotate(ctx context.Context) error
    Size() (int64, error)   // NEW
    Close() error
}
```

`Size()` returns the **active journal file's** on-disk byte count.
The size MUST be measured under the same mutex that guards `Append`,
so a scrape racing with a write sees a consistent value (not a
half-written tail).

### What `Size()` does NOT include

- **Rotated archive bytes**. Rotation moves the active file to a
  timestamped backup name. Tracking total disk consumption (active +
  rotated) is a separate concern (storage-growth alerting); the
  active-file metric maps cleanly to "is rotation working?" without
  conflating the two questions. A future `dfmt_journal_archive_bytes`
  is purely additive if needed.
- **Compressed archive sizes**. Same reasoning — orthogonal to
  active-write health.
- **Index file size**. The persisted `.gob` (JSON-payload) lives next
  to the journal but is rebuilt from journal replay on startup; its
  size tracks the index, not journal write health.

### Error semantics

A non-nil error from `Size()` is reported as gauge value `-1` so the
operator can distinguish "file gone" from "empty file". This is the
same encoding `prometheus/client_golang` uses for collector errors.
A panicking implementation is treated as the registry's panic-recovery
path treats any other collect-fn panic.

### Mock journal contract

In-tree test mocks are updated to return `(0, nil)` by default. Tests
exercising the size path (none yet — surface is too new) can override
via a struct field or a configurable constructor.

## Alternatives Considered

### Pipe the file path into `Handlers`, stat from outside

Rejected: leaks the file-backed implementation through the interface
boundary. A future ADR introducing a SQLite-backed or remote journal
would have to also update `Handlers`. Putting `Size()` on the
interface is the change that's being deferred either way; doing it
now keeps `Handlers` a pure consumer of the interface contract.

### Track size via an `atomic.Int64` updated on every `Append`

Rejected: drifts from on-disk truth on rotation, on truncation
during `Checkpoint`, and on any write that fails partway. The
`os.Stat` call is ~1 µs and only happens at scrape time — for a 1 Hz
scraper, the cost is invisible compared to `runtime.ReadMemStats`
already on every scrape.

### Expose only via `Stats()` (not `Journal`)

`Stats()` already aggregates dashboard data. Rejected because
`Stats()` is a transport-layer concern that consumes `Journal`; the
size is a journal property. Wiring it through `Stats()` would mean
exposing an internal accessor *and* a `Stats()` field — twice the
surface for one signal.

### Return `int64` (no error)

Rejected: file-backed journals can hit transient `os.Stat` failures
(filesystem hiccup, file removed by an operator). Swallowing those
into a 0 value would mask a real problem.

## Consequences

### Positive

- `dfmt_journal_bytes` joins the Prometheus surface; rotation health
  is now first-class.
- The `Journal` interface evolution is a small, additive change. Both
  in-tree mocks already implement the other methods; adding `Size()`
  is one method body each.
- Future journal backings (network-replicated, columnar, etc.) get a
  natural place to report their on-disk equivalent.

### Negative

- One more method on the `Journal` interface. Out-of-tree
  implementations (none today, but the interface is exported) become
  a compile break. Mitigation: changelog entry + the change is in the
  v0.4 milestone, the same milestone introducing the rest of the v1.0
  observability surface.
- `os.Stat` per scrape is one syscall. Negligible alongside
  `runtime.ReadMemStats` (~100 µs vs 1 µs), but worth noting in
  per-scrape budgets.

## Implementation Notes

- `journalImpl.Size()` takes `j.mu` and calls `j.file.Stat()`. The
  same pattern is already used at the rotation threshold check in
  `Append` (line 229), so this is a re-expression, not a new
  invariant.
- `WireHandlerMetrics(h)` registers `dfmt_journal_bytes` via
  `RegisterGaugeFunc` only when `h.journal != nil` — degraded-mode
  handlers (no project) skip the gauge entirely rather than reporting
  a permanent -1.
- Test mocks (`internal/transport/handlers_test.go::mockJournal`,
  `internal/daemon/daemon_test.go::mockJournal`) gain a one-line
  `Size()` returning `(0, nil)`.

## Migration

External consumers of `core.Journal` must add a `Size()` method.
Recommended boilerplate for a no-op implementation:

```go
func (j *yourJournal) Size() (int64, error) { return 0, nil }
```

This ADR is purely additive on the metrics surface — operators not
scraping `/metrics` see no change.
