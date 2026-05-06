package transport

import (
	"context"
	"errors"
	"time"
)

// Per-tool call counters (ADR-0016 v0.4 follow-up). Each MCP tool entry
// point increments dfmt_tool_calls_total{tool, status} once per
// invocation: status="ok" on a nil error return, status="error"
// otherwise. Cardinality is fixed and bounded — 9 tools × 2 statuses =
// 18 children — so this is safe to publish without a label-vector
// allocation cap.
//
// Why pre-registered fixed map (not a CounterVec with dynamic
// WithLabelValues): the tool surface is closed-set, so paying a hash
// lookup per call to discover a child counter is wasted work. Direct
// map access keeps the hot path at one map read + one atomic add.
//
// Status taxonomy:
//
//   ok    — handler returned (resp, nil). Includes wire-dedup
//           short-circuits (the tool ran, the response is just thinner).
//   error — handler returned (_, non-nil). Includes context.Canceled,
//           sandbox policy denials, journal-not-attached, validation
//           errors. Operators wanting to break the cancel-vs-fault
//           apart should look at journal events, not this counter —
//           the cardinality bargain was status binary.

var toolCallCounters = registerToolCounters()
var toolCallHistograms = registerToolHistograms()

type toolCallChild struct {
	ok  Counter
	err Counter
}

// trackedTools is the closed set of MCP tools this counter publishes.
// Keep in lockstep with the Handlers methods that wrap recordToolCall —
// adding a tool here without adding the defer is harmless (zero
// emissions) but the reverse silently drops counts on the floor.
var trackedTools = []string{
	"exec", "read", "fetch", "glob", "grep",
	"edit", "write", "recall", "search",
}

func registerToolCounters() map[string]*toolCallChild {
	m := make(map[string]*toolCallChild, len(trackedTools))
	for _, tool := range trackedTools {
		m[tool] = &toolCallChild{}
	}
	return m
}

// registerToolHistograms allocates one Histogram per tracked tool,
// sharing the package-default bucket set per ADR-0018. The histograms
// themselves are wired into the registry via registerToolMetrics()
// so test reset behaves consistently with the counter wiring.
func registerToolHistograms() map[string]*Histogram {
	m := make(map[string]*Histogram, len(trackedTools))
	for _, tool := range trackedTools {
		m[tool] = NewHistogram(defaultLatencyBuckets)
	}
	return m
}

// registerToolMetrics wires the per-tool counters AND histograms into
// the package registry. Called from init() and resetRegistryForTest()
// so test ordering is independent.
func registerToolMetrics() {
	const counterHelp = "Total MCP tool calls by tool name and status (ok|error)."
	for _, tool := range trackedTools {
		c := toolCallCounters[tool]
		RegisterCounterWithLabels("dfmt_tool_calls_total", counterHelp,
			map[string]string{"tool": tool, "status": "ok"}, &c.ok)
		RegisterCounterWithLabels("dfmt_tool_calls_total", counterHelp,
			map[string]string{"tool": tool, "status": "error"}, &c.err)
	}

	const histHelp = "Per-tool call latency in seconds (ADR-0018 default buckets, append-only)."
	for _, tool := range trackedTools {
		RegisterHistogram("dfmt_tool_call_duration_seconds", histHelp,
			map[string]string{"tool": tool}, toolCallHistograms[tool])
	}
}

// recordToolCall increments the right child counter and observes the
// elapsed duration into the per-tool histogram at handler return.
// Callers wire it via a one-line prologue:
//
//	func (h *Handlers) Exec(ctx context.Context, p ExecParams) (resp *ExecResponse, err error) {
//	    defer recordToolCall("exec", ctx, &err, time.Now())
//	    ...
//	}
//
// errPtr is a pointer to the named return so the defer observes the
// final value. ctx is consulted to suppress observations on
// caller-side cancellation: the elapsed time is the agent's patience
// budget, not a daemon-side latency datum, and folding it into the
// histogram dilutes the p95.
//
// `start` is the moment the handler began work (typically `time.Now()`
// at the same call). Passing the start time as a parameter rather than
// capturing it inside the helper keeps recordToolCall pure and lets
// the call site decide whether to bracket extra setup work.
func recordToolCall(tool string, ctx context.Context, errPtr *error, start time.Time) {
	c, ok := toolCallCounters[tool]
	if !ok {
		return
	}
	var e error
	if errPtr != nil {
		e = *errPtr
	}
	canceled := ctx != nil && errors.Is(e, context.Canceled) && ctx.Err() != nil

	if e == nil {
		c.ok.Inc()
	} else if !canceled {
		c.err.Inc()
	}

	// Histogram observations: skip the cancellation path so p95 stays
	// honest. Histograms are tool-scoped (no status label) per
	// ADR-0018; the operator-visible status is in the counter pair.
	if canceled {
		return
	}
	if h, ok := toolCallHistograms[tool]; ok && h != nil {
		h.Observe(time.Since(start))
	}
}

// WireHandlerMetrics binds Handlers-instance metrics into the package
// registry. Called once from HTTPServer.Start so the registry sees the
// live Handlers value.
//
// The pattern intentionally mirrors registerProcessMetrics rather than
// growing a per-instance registry: there is exactly one Handlers per
// daemon, and per-tool counters above already use the shared registry
// — keeping all metrics in one place keeps WriteProm a single walk.
//
// The closures take their lock at scrape time; on a 1 Hz scraper this
// is invisible, but it does mean a slow scrape can briefly serialize
// against a hot stash path. Acceptable trade-off for honest live
// values; a TTL cache is available if a real workload ever surfaces
// the contention.
func WireHandlerMetrics(h *Handlers) {
	if h == nil {
		return
	}
	RegisterCounterFunc("dfmt_dedup_hits_total",
		"Total cross-call content dedup cache hits since daemon start (ADR-0009).",
		func() int64 { return h.dedupHits.Load() })

	RegisterGaugeFunc("dfmt_index_docs",
		"Number of documents currently in the in-memory inverted index.",
		func() int64 {
			if h.index == nil {
				return 0
			}
			return int64(h.index.TotalDocs())
		})

	RegisterGaugeFunc("dfmt_wire_dedup_entries",
		"Number of content_ids currently in the wire-dedup cache (ADR-0011 per-session scope).",
		func() int64 { return int64(h.wireDedupSize()) })

	RegisterGaugeFunc("dfmt_content_dedup_entries",
		"Number of bytes-hash entries currently in the content-store dedup cache.",
		func() int64 { return int64(h.contentDedupSize()) })

	// dfmt_journal_bytes is registered only when a journal is wired —
	// degraded-mode handlers (no project) skip it rather than reporting
	// a permanent -1 that would noise alerting. ADR-0017.
	if h.journal != nil {
		RegisterGaugeFunc("dfmt_journal_bytes",
			"On-disk byte size of the active journal file. -1 means the underlying Size() "+
				"call failed (filesystem hiccup, file removed).",
			func() int64 {
				n, err := h.journal.Size()
				if err != nil {
					return -1
				}
				return n
			})
	}
}
