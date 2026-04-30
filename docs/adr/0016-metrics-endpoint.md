# ADR-0016: Prometheus `/metrics` Endpoint

- **Status:** Accepted
- **Date:** 2026-05-01 (amended 2026-05-02 — per-tool counters + dedup-hit counter wired)
- **Supersedes:** —
- **Superseded by:** —
- **Related:** ADR-0001 (Per-Project Daemon), ADR-0004 (Stdlib-First Deps), ADR-0009 (Cross-Call Content Dedup)

## Context

DFMT exposes structured logs (`slog`) and a self-tuning compression
telemetry path (mcp.go reads journal events to compute byte-savings
ratios for `tools/list`). Neither is suitable for production
observability:

- Logs are line-oriented, not aggregable. An operator can't ask "how
  many `exec` calls per minute hit the deny list" without `grep | wc`,
  and the answer is gone the moment log rotation runs.
- Self-tuning telemetry is internal: it shapes the agent-facing
  `tools/list` description but does not surface to dashboards or
  alerts.
- The `/api/stats` endpoint emits JSON-RPC stats for the dashboard, not
  a metric format any third-party scraper recognizes.

Operators running DFMT alongside other daemons want a single Prometheus
scrape config that covers everything. Without `/metrics`, DFMT is the
odd one out — observable only via journal grepping.

The audit (2026-04-28) flagged this as a v1.0 gate; ADR-0016 records
the wire-up.

## Decision

### Endpoint

`GET /metrics` on the existing HTTP server. Same wrapSecurity middleware
that gates `/dashboard` and `/api/stats` enforces:

- Loopback-only Host header (DNS-rebinding defense, F-17 closure).
- Same-origin / no-Origin requests from browsers (`/metrics` is not
  exposed cross-origin by default).
- Methods: `GET` and `HEAD` only; `POST` returns 405.

Response Content-Type: `text/plain; version=0.0.4` (Prometheus text
exposition format spec).

### Format primitives in-tree

`internal/transport/metrics.go` ships ~250 lines covering:

- `Counter` and `Gauge` types (atomic.Int64 wrappers, lockless
  Inc/Add/Load — zero-alloc on the increment path)
- A package registry of (name, help, kind, collect-fn) tuples
- `WriteProm(io.Writer)` that walks the registry and emits the
  Prometheus text format with sorted output for stable diffs
- HELP / label-value escaping per the spec

We do **not** vendor `prometheus/client_golang`. ADR-0004 caps
runtime dependencies at `golang.org/x/sys` + `gopkg.in/yaml.v3`;
adding 50+ MB of transitive deps for a 250-line emitter inverts the
cost/benefit ratio. Histograms and summaries are intentionally
omitted at this stage — we have no current consumer, and a premature
histogram with arbitrary bucket choices is harder to migrate than
adding it later.

### What's published in v0.3

Daemon-level gauges, scrape counter, per-tool counters, and dedup-hit
counter. All read at scrape time:

| Metric | Type | Purpose |
|---|---|---|
| `dfmt_metrics_scrapes_total` | counter | Confirms a Prometheus scraper is reaching the daemon (rate should match scrape interval) |
| `dfmt_process_uptime_seconds` | gauge | Detects daemon restarts (a sudden drop is a restart) |
| `dfmt_process_goroutines` | gauge | Goroutine leak detector |
| `dfmt_memstats_alloc_bytes` | gauge | Heap allocations live |
| `dfmt_memstats_heap_inuse_bytes` | gauge | In-use spans |
| `dfmt_memstats_gc_pause_total_ns` | gauge | GC pause budget |
| `dfmt_memstats_num_gc` | gauge | GC cycle count |
| `dfmt_tool_calls_total{tool,status}` | counter | MCP tool invocations by name (exec/read/fetch/glob/grep/edit/write/recall/search) and status (ok/error) |
| `dfmt_dedup_hits_total` | counter | Cross-call content-store dedup cache hits (ADR-0009) |

#### Per-tool counter cardinality

Closed set: 9 tools × 2 statuses = 18 children under `dfmt_tool_calls_total`.
Operators wanting cancel-vs-failure separation should look at journal
events (the `tool.*` events carry full error reasons) — keeping the
counter binary stops label cardinality from drifting if a future tool
gains a richer status taxonomy. `context.Canceled` returns from the
handler are explicitly *not* counted as errors: the agent gave up
before the daemon finished work, which is not a daemon-side fault and
would noise-up alerting.

#### Wiring pattern

Each `Handlers` method takes a named `err` return and a one-line
defer:

```go
func (h *Handlers) Exec(ctx context.Context, p ExecParams) (_ *ExecResponse, err error) {
    defer recordToolCall("exec", ctx, &err)
    // ... existing body
}
```

The blank identifier on the response avoids colliding with `resp` as a
local variable in the existing handler bodies. `recordToolCall` does
one map lookup + one atomic Add on the hot path; ~5 ns per invocation
on a M2-class CPU. The counters are registered at package init via
`registerToolMetrics()`, so they are available the moment any
`HTTPServer` (or any test) imports the package.

#### Still deferred to v0.4

- **Duration histograms** (`dfmt_tool_call_duration_seconds_bucket{tool, le}`).
  Bucket choice is not yet defensible — the right percentile budget
  depends on operator workload, and a premature `[0.005, 0.01, …]`
  set is harder to migrate than adding it later.
- **Index size, journal byte total, active-session gauges**. One-line
  RegisterGaugeFunc wirings; deferred only because each one needs a
  read-side method on the underlying type and that touch should be
  bundled with a coverage push for the same package.
- **Per-scrape rate-limit + MemStats TTL**. A rogue scraper at 100 Hz
  measurably impacts request latency. Mitigation lives behind a config
  knob if it ever surfaces in real operator reports.

### Scrape cost

`runtime.ReadMemStats` is the dominant per-scrape cost (~100 µs
stop-the-world on a healthy heap). At 1 Hz scrape (the Prometheus
default), this is 0.01% of CPU; at 10 Hz it climbs to 0.1%. We do
not cache MemStats across calls — the operator looking at the
dashboard wants the current value, not the value 10 seconds ago.

## Alternatives Considered

### `expvar` only (stdlib, no Prometheus format)

Go's `expvar` registers `/debug/vars` automatically and exposes JSON.
Rejected: integrates with no real observability stack, exposes
goroutine count and CommandLine by default (which we'd then need to
selectively suppress), and forces every operator to hand-write a JSON
parser to feed Grafana.

### Vendor `prometheus/client_golang`

The "official" Go Prometheus library. Rejected per ADR-0004 — it
brings ~10 transitive deps and a histogram implementation we don't
yet need. We can revisit if the registry surface grows large enough
that hand-maintenance becomes the cost.

### `/api/stats` extension with a Prometheus content negotiation

Add a `format=prometheus` query parameter to `/api/stats`. Rejected:
`/api/stats` is JSON-RPC POST; a Prometheus scraper expects GET. The
mismatch means we'd need a parallel route anyway — easier to make
that route the dedicated `/metrics`.

### Loopback-bind enforcement at the metrics endpoint

Refuse to serve `/metrics` if the listener is bound to a non-loopback
address. Rejected because the existing wrapSecurity middleware already
enforces loopback Host headers globally — duplicate gates would just
diverge over time. If a future ADR loosens the loopback restriction
on the HTTP server, `/metrics` should be re-evaluated separately.

## Consequences

### Positive

- DFMT is now scrapeable by any Prometheus / VictoriaMetrics /
  observability stack with a single line of scrape config.
- Daemon liveness, memory, and GC pressure are now first-class signals
  — previously buried in `slog` debug lines.
- Per-tool counter wiring in v0.4 is purely additive; the registry,
  endpoint, and format don't change.
- Adding new metrics is a 3-line patch (RegisterGauge / RegisterCounter)
  with no infrastructure churn.

### Negative

- Hand-rolled emitter must track future Prometheus format changes.
  Mitigated: the format is stable (v0.0.4 has been current since
  2014); a v1.0 change would be cataloged with months of lead time
  and an upstream client_golang migration is then a contained ADR.
- Duration histograms still deferred — operators see call rates and
  errors but cannot yet alert on p95 latency. The 2026-05-02 amendment
  delivered counters but explicitly held back on bucket choices.
- `runtime.ReadMemStats` per scrape is a stop-the-world. Not visible
  at 1 Hz, but a rogue scraper at 100 Hz could meaningfully impact
  request latency. Future hardening (per-scrape rate limit, cached
  MemStats with TTL) deferred to v0.4.

## Implementation Notes

- `metrics.go::resetRegistryForTest` rebuilds the init-time registry
  after each test that reset it, so test ordering doesn't leak empty
  state between cases. Tests that need a controlled registry call
  `resetRegistryForTest()` once and reason from a known baseline.
- The format primitives are deliberately `io.Writer`-based, not
  `[]byte`-returning: large registries can stream straight to the
  HTTP response without a buffer copy.
- Registry mutex is `sync.RWMutex`; scrape acquires Read, register
  acquires Write. Per-counter Inc/Add never touch the mutex (atomic
  on the value itself).

## Migration

None for end users. Operators running DFMT pre-this-ADR see the same
endpoints (`/dashboard`, `/api/stats`, `/healthz`, `/readyz`) plus the
new `/metrics`. No config knob to enable — the endpoint is on
whenever HTTP is on (`transport.http.enabled: true`).
