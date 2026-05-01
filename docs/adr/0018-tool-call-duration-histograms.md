# ADR-0018: Duration Histograms for Per-Tool Latency

- **Status:** Accepted
- **Date:** 2026-05-02
- **Supersedes:** —
- **Superseded by:** —
- **Related:** ADR-0016 (Prometheus `/metrics` Endpoint), ADR-0017 (Journal Size)

## Context

ADR-0016 explicitly deferred duration histograms with this rationale:

> Histograms and summaries are intentionally omitted at this stage —
> we have no current consumer, and a premature histogram with arbitrary
> bucket choices is harder to migrate than adding it later.

The 2026-05-02 amendments to ADR-0016 wired counters and gauges; the
last gap is per-tool latency. Operators want:

- **p95 / p99 alerting**. The `histogram_quantile` PromQL function on
  cumulative buckets gives this; counters with `_sum / _count` give
  only mean latency, which hides tail behavior.
- **Latency regression detection**. A code change that doubles
  `Handlers.Recall` from 50 ms to 100 ms is invisible if you only
  watch error rate.
- **Capacity planning**. p95 numbers across the 9 tools shape
  decisions about goroutine pool sizes and timeouts.

The bucket-choice migration risk is real but bounded: once published,
operators set up alerts referencing specific bucket boundaries
(`le="0.05"`), and changing those bucket boundaries breaks dashboards.
The fix isn't to defer indefinitely — it's to commit to a
migration-friendly default and to a contract about how the bucket set
will evolve.

## Decision

### Bucket boundaries

Use the Prometheus client_golang **default buckets**:

```
[0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]   seconds
```

(plus the implicit `+Inf` bucket every histogram has).

Why this set:

- Widely deployed across thousands of services. Operators have
  pre-built dashboard and alert templates referencing these bucket
  boundaries; matching them lets DFMT slot into existing
  observability stacks zero-config.
- Covers 5 ms (e.g. `Handlers.Search` on a small index) through 10 s
  (long-running `Exec`) — three orders of magnitude, the realistic
  envelope for our 9 tools.
- The `+Inf` bucket catches outliers (timeouts, pathological agent
  loops) without distorting the rest.

### Migration contract

Once published, this bucket set is **append-only**. We may add finer
buckets between existing ones (e.g. inserting `0.0025` to better
resolve sub-millisecond search calls) but we will not remove
boundaries or change their values. New finer buckets do not break
existing alerts because cumulative buckets — the alert's `le="0.05"`
threshold has the same value before and after a `0.025` bucket is
added between `0.01` and `0.05`.

If a tool's latency distribution turns out to need different
boundaries entirely (e.g. `Fetch` p99 sitting in 30+ s territory), we
will publish a *separate* metric name with tool-specific buckets
rather than mutating this one. The cost of a parallel series is one
new metric name; the cost of re-pointing every dashboard's `le="…"`
selector is operator labor across every deployment.

### Metric name and labels

```
dfmt_tool_call_duration_seconds_bucket{tool="exec", le="0.005"}
dfmt_tool_call_duration_seconds_bucket{tool="exec", le="0.01"}
...
dfmt_tool_call_duration_seconds_bucket{tool="exec", le="+Inf"}
dfmt_tool_call_duration_seconds_sum{tool="exec"}
dfmt_tool_call_duration_seconds_count{tool="exec"}
```

One label dimension: `tool`. Cardinality is 9 tools × 11 buckets +
9 sums + 9 counts = 117 child series under the single metric name.

We do **not** label by `status` (ok/error). Errors are often
either very fast (early validation rejection: < 1 ms) or very slow
(timeout: full timeout budget). Mixing them dilutes both signals,
but doubling histogram cardinality just to split them is unjustified
when operators wanting clean "p95 latency of successful calls" can
already filter via `dfmt_tool_calls_total{status="ok"}` for the count
and apply latency-budget alerting at the tool level. If a real
operator workflow surfaces the need for status-split histograms, this
is purely additive — a new metric name, no migration.

### Cancellation handling

Same as the counter: a `context.Canceled` return is **not** observed
into the histogram. The agent gave up before the daemon's work
finished, so the elapsed time isn't a meaningful latency datum — it's
the agent's patience budget. Excluding it keeps the p95 honest.

### Hot-path cost

Per `Observe(d)`:

- 11 atomic Adds (one per bucket where `d.Seconds() <= upperBound`).
  For a typical observation falling in the middle of the range, this
  is ~5–6 atomic ops + 11 float comparisons.
- 1 atomic Add to the nanosecond-sum (int64 accumulator; emitted as
  float seconds at scrape time).
- 1 atomic Add to the count.

Total: ~15 atomic ops, ~30 ns on M2-class CPU. Negligible against the
work the tool itself just did (typically 1+ ms).

## Alternatives Considered

### `_sum / _count` counters only (no histogram)

Rejected. Gives mean latency only; tail is invisible. The whole point
of adding histograms is `histogram_quantile`-driven p95 / p99
alerting. Sum / count without buckets is a worse summary than
counters alone (the count is already there).

### Native Prometheus summary type (with `quantile=` labels)

Rejected. Summaries compute quantiles client-side using a streaming
algorithm; they are non-aggregatable across instances (you cannot
sensibly average a p95 across two daemons). DFMT runs one daemon per
project and a future federated dashboard might aggregate across
projects — histograms aggregate via `histogram_quantile(0.95,
sum(rate(..._bucket[5m])) by (le))`.

### Per-tool custom buckets

Rejected for v0.3. We don't yet have workload data justifying tool-
specific bucket sets, and committing prematurely to nine different
bucket arrays is exactly the migration risk ADR-0016 worried about.
The shared default is a one-deferred-commitment knob; refining per
tool can come once production data tells us where the actual
distributions sit.

### Vendor `prometheus/client_golang`

ADR-0004 caps runtime dependencies. The bucket data structure is
straightforward (sorted float array + parallel atomic counter array).
~80 lines beats 50+ MB of transitive deps for a primitive we now
need. Same trade-off as ADR-0016 made for the text emitter.

## Consequences

### Positive

- p95 / p99 alerting becomes a single PromQL expression on a single
  metric name.
- Operators with existing client_golang-based dashboards get
  bucket-compatible series with zero config.
- Bucket migration policy is explicit: append boundaries, never
  mutate. New tools or finer-resolution needs land as additive
  changes.

### Negative

- Cardinality grows by 117 series under one metric name. Acceptable
  on local-daemon scale; documented for any future federation.
- 11 atomic adds per tool call is cheap but not free. Bench-tested at
  ~30 ns; remains acceptable against typical 1+ ms tool work.
- The histogram primitive's text-format emission is more complex than
  counter / gauge (multiple lines per child, `le=` ordering rules,
  `+Inf` requirement). The emitter's complexity grows by ~30 LOC.

## Implementation Notes

- `Histogram` lives in `internal/transport/metrics.go`. Bucket array
  is shared across instances (`defaultLatencyBuckets`); pre-allocated
  once.
- The `+Inf` bucket is implicit at the data-structure level (it is
  the total `count`) and emitted explicitly at the text-format level.
- Emission keeps `_bucket` lines in ascending `le` order, then
  `_sum`, then `_count`, per the Prometheus exposition-format spec.
- `recordToolCall` captures `time.Now()` at the prologue and the
  defer measures elapsed. Same defer that already does
  ok/err counter increment — one extra `Observe` call.
- `time.Now()` cost (~25 ns on Linux/Windows) is the dominant new
  per-call overhead, not the histogram itself.

## Migration

Operators using `/metrics` see new series:

- `dfmt_tool_call_duration_seconds_bucket{tool, le}` — 9 × 12 = 108 lines
- `dfmt_tool_call_duration_seconds_sum{tool}` — 9 lines
- `dfmt_tool_call_duration_seconds_count{tool}` — 9 lines

No existing series change. Dashboards and alerts already consuming
the previous gauges and counters keep working.

The bucket set is locked under the migration contract above. A future
ADR will be required to add finer buckets; the contract specifies
that addition is allowed without supersession (additive change), but
boundary changes or removals require a new ADR explicitly superseding
this one.
