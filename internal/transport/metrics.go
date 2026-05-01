package transport

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Prometheus text-format primitives for the /metrics endpoint (ADR-0016).
// In-tree, zero dependencies — Prometheus's text exposition format is
// stable enough that bundling a 100-line emitter beats taking on
// `client_golang` plus its deep transitive tree.
//
// Scope discipline:
//
//   - This file owns the FORMAT (escaping, type-line emission, registry
//     walk). It does not own per-tool counter instrumentation; per-tool
//     counters are an ADR-0016 v0.4 follow-up. The shapes below are the
//     subset needed to publish daemon-level gauges + the few counters
//     that already exist (boot time, /metrics scrapes).
//
//   - Loopback-only: the /metrics route in http.go enforces 127.0.0.1
//     binding; metrics include process / index state that should not
//     leak across origins. ADR-0016 records the threat model.

// metricKind enumerates the Prometheus types we emit. Summaries are
// still omitted (non-aggregatable, ADR-0018); histograms wired in
// 2026-05-02 with the Prometheus default bucket set per ADR-0018.
type metricKind int

const (
	metricCounter metricKind = iota
	metricGauge
	metricHistogram
)

func (k metricKind) String() string {
	switch k {
	case metricCounter:
		return "counter"
	case metricGauge:
		return "gauge"
	case metricHistogram:
		return "histogram"
	}
	return "untyped"
}

// Counter is a monotonically increasing int64. Reads and writes are
// lockless via sync/atomic; the hot-path cost is a single CAS-free Add.
//
// A Counter must outlive every increment of it; instances are typically
// package-level vars registered at init time.
type Counter struct {
	v atomic.Int64
}

// Inc adds 1 atomically. Cheaper than Add(1).
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n atomically. Negative values are accepted for symmetry but
// violate Prometheus's counter contract; callers should use Gauge instead
// when negativity is meaningful.
func (c *Counter) Add(n int64) { c.v.Add(n) }

// Load returns the current value.
func (c *Counter) Load() int64 { return c.v.Load() }

// Gauge is a value that can move in either direction (memory bytes,
// goroutine count, posting-list size). Read/write semantics match
// Counter, but the Prometheus type is `gauge`.
type Gauge struct {
	v atomic.Int64
}

func (g *Gauge) Set(n int64) { g.v.Store(n) }
func (g *Gauge) Add(n int64) { g.v.Add(n) }
func (g *Gauge) Load() int64 { return g.v.Load() }

// defaultLatencyBuckets are the Prometheus client_golang default
// histogram buckets (seconds). Locked under ADR-0018's migration
// contract: this slice is append-only, never mutated. New finer
// boundaries may be inserted between existing values; existing values
// never move. Tests pin both the values and the count so a stray
// edit is caught at CI.
var defaultLatencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Histogram observes durations into cumulative buckets per the
// Prometheus exposition format. counts[i] is the number of observations
// with value <= buckets[i]; the +Inf bucket is implicit and equals the
// total count. sum is held as int64 nanoseconds (atomic-cheap) and
// converted to float seconds at scrape time.
//
// Observe takes ~15 atomic ops + 11 float comparisons (~30 ns on M2-
// class CPU) — a flat-out hot-path concern would specialize, but for
// our 9 tools this is dwarfed by the work the tool itself just did.
type Histogram struct {
	buckets  []float64
	counts   []atomic.Int64
	sumNanos atomic.Int64
	count    atomic.Int64
}

// NewHistogram allocates a Histogram with the given upper-bound
// buckets. The slice is captured by value (the implementation never
// mutates it after construction) and assumed sorted ascending.
// Callers should prefer the package-level defaultLatencyBuckets unless
// they have a strong workload-specific reason.
func NewHistogram(buckets []float64) *Histogram {
	h := &Histogram{
		buckets: append([]float64(nil), buckets...),
		counts:  make([]atomic.Int64, len(buckets)),
	}
	return h
}

// Observe records a duration. Cancellation-time observations should be
// skipped at the call site (see recordToolCall) — the histogram has no
// concept of "should not count this."
func (h *Histogram) Observe(d time.Duration) {
	secs := d.Seconds()
	for i, ub := range h.buckets {
		if secs <= ub {
			h.counts[i].Add(1)
		}
	}
	h.sumNanos.Add(int64(d))
	h.count.Add(1)
}

// snapshot returns a consistent copy of the per-bucket counts plus
// sum and total. Reads under no lock — the tearing risk on int64 is
// per-counter, not cross-counter, so callers may see a snapshot where
// `_count` is one observation ahead of `sum` and the buckets. This is
// acceptable for /metrics where eventual consistency is the contract.
func (h *Histogram) snapshot() (counts []int64, sumNanos, total int64) {
	counts = make([]int64, len(h.counts))
	for i := range h.counts {
		counts[i] = h.counts[i].Load()
	}
	return counts, h.sumNanos.Load(), h.count.Load()
}

// metricEntry is a registry record. The collector closure is called at
// scrape time so callers can wire dynamic values (runtime.MemStats,
// index.TotalDocs()) without keeping their own background goroutine in
// sync with a Gauge.
type metricEntry struct {
	name    string
	help    string
	kind    metricKind
	labels  map[string]string
	collect func() int64
}

// histogramEntry binds a Histogram to a (name, label-set) pair. Stored
// separately from metricEntry because histograms emit multiple lines
// per scrape (per-bucket + _sum + _count) and slip awkwardly into the
// counter/gauge collect-fn shape.
type histogramEntry struct {
	name   string
	help   string
	labels map[string]string
	h      *Histogram
}

// registry is the package-private store. A registry method receiver
// avoids a second indirection on the scrape path.
type registry struct {
	mu         sync.RWMutex
	entries    []metricEntry
	histograms []histogramEntry
	bootTime   time.Time
}

var defaultRegistry = &registry{bootTime: time.Now()}

// RegisterCounter binds a Counter to a metric name. The closure body
// must be cheap (a single atomic.Load) — scrape latency budgets assume
// O(registry) work, not O(work-per-metric).
func RegisterCounter(name, help string, c *Counter) {
	registerMetric(metricEntry{
		name: name, help: help, kind: metricCounter,
		collect: c.Load,
	})
}

// RegisterGauge binds a Gauge to a metric name. Same cheap-collect rule
// as RegisterCounter.
func RegisterGauge(name, help string, g *Gauge) {
	registerMetric(metricEntry{
		name: name, help: help, kind: metricGauge,
		collect: g.Load,
	})
}

// RegisterGaugeFunc is the dynamic-collect variant — used for
// runtime.MemStats and index.TotalDocs() where the value is not held
// in our own atomic but read fresh on each scrape.
func RegisterGaugeFunc(name, help string, fn func() int64) {
	registerMetric(metricEntry{
		name: name, help: help, kind: metricGauge,
		collect: fn,
	})
}

// RegisterCounterFunc is the closure-collected counter variant — used
// when the counter value lives on a struct field (e.g. handlers.dedupHits
// is an atomic.Int64 owned by Handlers, not a *Counter the registry
// can hold a pointer to). The closure must read atomically; scrape
// latency assumes O(1) per metric.
func RegisterCounterFunc(name, help string, fn func() int64) {
	registerMetric(metricEntry{
		name: name, help: help, kind: metricCounter,
		collect: fn,
	})
}

// RegisterHistogram binds a Histogram to a (name, labels) pair. The
// HELP and TYPE lines emit once per metric name across all label
// combinations. Re-registering with the same (name, labels) replaces
// the existing histogram; the previous Histogram pointer is dropped.
// ADR-0018.
func RegisterHistogram(name, help string, labels map[string]string, h *Histogram) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	for i, existing := range defaultRegistry.histograms {
		if existing.name == name && labelsEqual(existing.labels, labels) {
			defaultRegistry.histograms[i] = histogramEntry{
				name: name, help: help, labels: labels, h: h,
			}
			return
		}
	}
	defaultRegistry.histograms = append(defaultRegistry.histograms, histogramEntry{
		name: name, help: help, labels: labels, h: h,
	})
}

// RegisterCounterWithLabels binds a Counter to a (name, label-set) pair.
// Used for label-vector counters where the same metric name carries a
// fixed set of dimensions — e.g. dfmt_tool_calls_total{tool, status}.
// The label map is captured by reference; callers must not mutate it
// after registration.
func RegisterCounterWithLabels(name, help string, labels map[string]string, c *Counter) {
	registerMetric(metricEntry{
		name: name, help: help, kind: metricCounter,
		labels:  labels,
		collect: c.Load,
	})
}

func registerMetric(e metricEntry) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	// Replace by name if already registered — happens in tests that
	// re-import the package via test harness reuse.
	for i, existing := range defaultRegistry.entries {
		if existing.name == e.name && labelsEqual(existing.labels, e.labels) {
			defaultRegistry.entries[i] = e
			return
		}
	}
	defaultRegistry.entries = append(defaultRegistry.entries, e)
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// WriteProm walks the registry and emits Prometheus text format. The
// output is sorted by metric name for stable diffs in tests and golden
// files. Always returns nil (writer errors are observable on the wire).
//
// Output shape (one block per metric):
//
//	# HELP <name> <help text>
//	# TYPE <name> <counter|gauge>
//	<name>[{label="value"}] <int>
func WriteProm(w io.Writer) error {
	defaultRegistry.mu.RLock()
	entries := make([]metricEntry, len(defaultRegistry.entries))
	copy(entries, defaultRegistry.entries)
	histograms := make([]histogramEntry, len(defaultRegistry.histograms))
	copy(histograms, defaultRegistry.histograms)
	defaultRegistry.mu.RUnlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		return labelString(entries[i].labels) < labelString(entries[j].labels)
	})

	// Emit HELP/TYPE only once per metric name (Prometheus rejects
	// duplicates). Counter / gauge children with the same name share one
	// HELP/TYPE block.
	var lastName string
	for _, e := range entries {
		if e.name != lastName {
			fmt.Fprintf(w, "# HELP %s %s\n", e.name, escapeHelp(e.help))
			fmt.Fprintf(w, "# TYPE %s %s\n", e.name, e.kind)
			lastName = e.name
		}
		val := e.collect()
		if len(e.labels) == 0 {
			fmt.Fprintf(w, "%s %d\n", e.name, val)
		} else {
			fmt.Fprintf(w, "%s{%s} %d\n", e.name, labelString(e.labels), val)
		}
	}

	// Histograms are emitted last and grouped by metric name. Within
	// each metric, label sets are sorted; within each label set, bucket
	// lines are emitted in ascending `le` order (Prometheus spec
	// requirement), then `_sum`, then `_count`.
	sort.Slice(histograms, func(i, j int) bool {
		if histograms[i].name != histograms[j].name {
			return histograms[i].name < histograms[j].name
		}
		return labelString(histograms[i].labels) < labelString(histograms[j].labels)
	})
	lastName = ""
	for _, hentry := range histograms {
		if hentry.name != lastName {
			fmt.Fprintf(w, "# HELP %s %s\n", hentry.name, escapeHelp(hentry.help))
			fmt.Fprintf(w, "# TYPE %s histogram\n", hentry.name)
			lastName = hentry.name
		}
		writeHistogramChild(w, hentry)
	}
	return nil
}

// writeHistogramChild emits the per-(name, labels) lines for one
// histogram: cumulative bucket counts in ascending `le` order, the
// implicit `+Inf` bucket equal to total observations, then `_sum` (in
// seconds, formatted from the int64-nanosecond accumulator) and
// `_count`.
func writeHistogramChild(w io.Writer, e histogramEntry) {
	counts, sumNanos, total := e.h.snapshot()

	baseLabels := labelString(e.labels)
	for i, ub := range e.h.buckets {
		le := formatBucketBound(ub)
		writeHistBucketLine(w, e.name, baseLabels, le, counts[i])
	}
	// Implicit +Inf bucket: every observation lives in it.
	writeHistBucketLine(w, e.name, baseLabels, "+Inf", total)

	// _sum and _count carry the metric's labels but NOT the per-bucket
	// le label. seconds = sumNanos / 1e9; emit with %g so 0.0 stays
	// readable and large values don't round prematurely.
	sumSeconds := float64(sumNanos) / 1e9
	if baseLabels == "" {
		fmt.Fprintf(w, "%s_sum %g\n", e.name, sumSeconds)
		fmt.Fprintf(w, "%s_count %d\n", e.name, total)
	} else {
		fmt.Fprintf(w, "%s_sum{%s} %g\n", e.name, baseLabels, sumSeconds)
		fmt.Fprintf(w, "%s_count{%s} %d\n", e.name, baseLabels, total)
	}
}

func writeHistBucketLine(w io.Writer, name, baseLabels, le string, count int64) {
	if baseLabels == "" {
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, le, count)
		return
	}
	fmt.Fprintf(w, "%s_bucket{%s,le=%q} %d\n", name, baseLabels, le, count)
}

// formatBucketBound renders a bucket upper bound as the Prometheus
// `le` label uses it. Prefer integer rendering for integer-valued
// bounds (e.g. `1` not `1.0`) to match Prometheus convention.
func formatBucketBound(ub float64) string {
	if ub == float64(int64(ub)) {
		return fmt.Sprintf("%d", int64(ub))
	}
	return strconv.FormatFloat(ub, 'g', -1, 64)
}

// labelString renders a label set as `k1="v1",k2="v2"` with sorted keys
// so two metrics with identical labels emit identical lines (line-stable
// output across Go map iteration order).
func labelString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(m[k]))
		b.WriteByte('"')
	}
	return b.String()
}

// escapeHelp / escapeLabelValue follow the Prometheus text format spec:
// HELP escapes \ and \n; label values escape \ \n and ".
// https://prometheus.io/docs/instrumenting/exposition_formats/#text-format-details
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// resetRegistryForTest clears the registry and re-installs the metrics
// that init() registers, so subsequent tests don't pick up state from
// previous test cases. Re-registering the init metrics keeps tests
// independent of run order — a test that asserts "test_dup is the only
// entry" would otherwise leak its empty registry into the next test
// that expects dfmt_process_uptime_seconds.
func resetRegistryForTest() {
	defaultRegistry.mu.Lock()
	defaultRegistry.entries = nil
	defaultRegistry.histograms = nil
	defaultRegistry.bootTime = time.Now()
	defaultRegistry.mu.Unlock()
	RegisterCounter("dfmt_metrics_scrapes_total",
		"Total number of /metrics endpoint scrapes since daemon start.",
		&metricsScrapesTotal)
	registerProcessMetrics()
	registerToolMetrics()
}

// registerProcessMetrics wires the daemon-level gauges that are always
// safe to publish (uptime, MemStats, goroutine count). Called once from
// HTTPServer.Start. v0.4 will add per-tool counters via the same
// registry but through a Handlers-side instrumentation hook.
func registerProcessMetrics() {
	RegisterGaugeFunc("dfmt_process_uptime_seconds",
		"Daemon uptime in seconds since first /metrics scrape registration.",
		func() int64 {
			return int64(time.Since(defaultRegistry.bootTime).Seconds())
		})
	RegisterGaugeFunc("dfmt_process_goroutines",
		"Number of currently running goroutines.",
		func() int64 { return int64(runtime.NumGoroutine()) })

	RegisterGaugeFunc("dfmt_memstats_alloc_bytes",
		"Bytes of currently allocated heap objects.",
		func() int64 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return int64(ms.Alloc)
		})
	RegisterGaugeFunc("dfmt_memstats_heap_inuse_bytes",
		"Bytes in in-use spans.",
		func() int64 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return int64(ms.HeapInuse)
		})
	RegisterGaugeFunc("dfmt_memstats_gc_pause_total_ns",
		"Cumulative GC pause time (nanoseconds).",
		func() int64 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return int64(ms.PauseTotalNs) //nolint:gosec // overflow only after ~292 years
		})
	RegisterGaugeFunc("dfmt_memstats_num_gc",
		"Number of completed GC cycles.",
		func() int64 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return int64(ms.NumGC)
		})
}

// metricsScrapesTotal counts /metrics endpoint accesses. Useful for
// confirming a Prometheus scraper is actually reaching the daemon
// (scrape rate should match the scraper's interval).
var metricsScrapesTotal Counter

func init() {
	RegisterCounter("dfmt_metrics_scrapes_total",
		"Total number of /metrics endpoint scrapes since daemon start.",
		&metricsScrapesTotal)
	registerProcessMetrics()
	registerToolMetrics()
}
