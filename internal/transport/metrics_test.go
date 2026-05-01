package transport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestCounter_AtomicAdd(t *testing.T) {
	var c Counter
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()
	if got := c.Load(); got != 100000 {
		t.Errorf("Counter.Load() = %d, want 100000", got)
	}
}

func TestGauge_SetAdd(t *testing.T) {
	var g Gauge
	g.Set(100)
	if g.Load() != 100 {
		t.Errorf("Gauge.Set didn't take")
	}
	g.Add(-25)
	if g.Load() != 75 {
		t.Errorf("Gauge.Add(-25) -> %d, want 75", g.Load())
	}
}

func TestWriteProm_FormatShape(t *testing.T) {
	resetRegistryForTest()

	var hits Counter
	hits.Add(7)
	RegisterCounter("test_hits_total", "Test counter help.", &hits)

	var size Gauge
	size.Set(42)
	RegisterGauge("test_size", "Test gauge help.", &size)

	RegisterGaugeFunc("test_dynamic", "Dynamic value via closure.", func() int64 {
		return 99
	})

	var buf bytes.Buffer
	if err := WriteProm(&buf); err != nil {
		t.Fatalf("WriteProm: %v", err)
	}
	out := buf.String()

	// Output is sorted by metric name. Check the three blocks landed
	// with the expected HELP / TYPE / value triples.
	want := []string{
		"# HELP test_dynamic Dynamic value via closure.",
		"# TYPE test_dynamic gauge",
		"test_dynamic 99",
		"# HELP test_hits_total Test counter help.",
		"# TYPE test_hits_total counter",
		"test_hits_total 7",
		"# HELP test_size Test gauge help.",
		"# TYPE test_size gauge",
		"test_size 42",
	}
	for _, line := range want {
		if !strings.Contains(out, line) {
			t.Errorf("WriteProm output missing %q\nfull output:\n%s", line, out)
		}
	}
}

func TestWriteProm_HelpEscaping(t *testing.T) {
	resetRegistryForTest()
	var c Counter
	RegisterCounter("test_escaped",
		"Help with backslash \\ and newline\nand quote \" — should be escaped per Prom spec.",
		&c)
	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	// Backslash → \\, newline → \n. Quote stays unescaped in HELP per
	// the Prometheus spec (only \ and \n are escaped in HELP text).
	if !strings.Contains(out, `\\`) || !strings.Contains(out, `\n`) {
		t.Errorf("HELP escaping missing in:\n%s", out)
	}
}

func TestWriteProm_DuplicateRegisterReplaces(t *testing.T) {
	resetRegistryForTest()
	var first Counter
	first.Add(1)
	RegisterCounter("test_dup", "first registration.", &first)

	var second Counter
	second.Add(99)
	RegisterCounter("test_dup", "second registration.", &second)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	// Re-registration must not produce duplicate # TYPE lines and must
	// reflect the second counter's value (replacement, not append).
	if strings.Count(out, "# TYPE test_dup") != 1 {
		t.Errorf("expected exactly one TYPE line for test_dup, got:\n%s", out)
	}
	if !strings.Contains(out, "test_dup 99") {
		t.Errorf("expected replaced counter value 99, got:\n%s", out)
	}
}

func TestHandleMetrics_PrometheusContentType(t *testing.T) {
	s := &HTTPServer{}
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	s.handleMetrics(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", ct)
	}
	body := w.Body.String()
	// At minimum the registered scrape counter and uptime gauge must
	// appear — those are init-time registrations so any non-empty
	// scrape includes them.
	if !strings.Contains(body, "dfmt_metrics_scrapes_total") {
		t.Errorf("body missing dfmt_metrics_scrapes_total:\n%s", body)
	}
	if !strings.Contains(body, "dfmt_process_uptime_seconds") {
		t.Errorf("body missing dfmt_process_uptime_seconds:\n%s", body)
	}
}

func TestHandleMetrics_MethodNotAllowed(t *testing.T) {
	s := &HTTPServer{}
	r := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	w := httptest.NewRecorder()
	s.handleMetrics(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to /metrics: status = %d, want 405", w.Code)
	}
}

func TestHandleMetrics_ScrapeCounterIncrements(t *testing.T) {
	s := &HTTPServer{}
	before := metricsScrapesTotal.Load()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	s.handleMetrics(w, r)
	if got := metricsScrapesTotal.Load(); got != before+1 {
		t.Errorf("scrape counter: before=%d after=%d, want before+1", before, got)
	}
}

func TestRegisterCounterFunc_DynamicCollect(t *testing.T) {
	resetRegistryForTest()
	var src Counter
	src.Add(13)
	RegisterCounterFunc("test_dyn_counter",
		"Counter via closure-collect.",
		func() int64 { return src.Load() * 2 })

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	if !strings.Contains(out, "# TYPE test_dyn_counter counter") {
		t.Errorf("missing TYPE line for closure counter:\n%s", out)
	}
	if !strings.Contains(out, "test_dyn_counter 26") {
		t.Errorf("closure counter value not 26:\n%s", out)
	}
}

func TestRegisterCounterWithLabels_LabelEmission(t *testing.T) {
	resetRegistryForTest()
	var ok, fail Counter
	ok.Add(42)
	fail.Add(3)
	RegisterCounterWithLabels("test_calls_total", "Test labeled counter.",
		map[string]string{"tool": "exec", "status": "ok"}, &ok)
	RegisterCounterWithLabels("test_calls_total", "Test labeled counter.",
		map[string]string{"tool": "exec", "status": "error"}, &fail)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	// HELP/TYPE block emitted exactly once even though two children share
	// the metric name. Both child lines must appear with sorted labels.
	if got := strings.Count(out, "# TYPE test_calls_total"); got != 1 {
		t.Errorf("TYPE line for test_calls_total: got %d, want 1\n%s", got, out)
	}
	if !strings.Contains(out, `test_calls_total{status="ok",tool="exec"} 42`) {
		t.Errorf("missing ok-status child:\n%s", out)
	}
	if !strings.Contains(out, `test_calls_total{status="error",tool="exec"} 3`) {
		t.Errorf("missing error-status child:\n%s", out)
	}
}

func TestRecordToolCall_OkAndError(t *testing.T) {
	resetRegistryForTest()
	beforeOk := toolCallCounters["exec"].ok.Load()
	beforeErr := toolCallCounters["exec"].err.Load()

	// nil err → ok bucket
	var nilErr error
	recordToolCall("exec", nil, &nilErr)
	if got := toolCallCounters["exec"].ok.Load(); got != beforeOk+1 {
		t.Errorf("nil-err recordToolCall: ok=%d want %d", got, beforeOk+1)
	}
	if got := toolCallCounters["exec"].err.Load(); got != beforeErr {
		t.Errorf("nil-err recordToolCall must not bump err counter: got %d want %d", got, beforeErr)
	}

	// real err → error bucket
	someErr := errors.New("sandbox denied")
	recordToolCall("exec", nil, &someErr)
	if got := toolCallCounters["exec"].err.Load(); got != beforeErr+1 {
		t.Errorf("err recordToolCall: err=%d want %d", got, beforeErr+1)
	}
}

func TestRecordToolCall_CancelSuppressed(t *testing.T) {
	resetRegistryForTest()
	beforeErr := toolCallCounters["exec"].err.Load()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelErr := context.Canceled
	recordToolCall("exec", ctx, &cancelErr)
	if got := toolCallCounters["exec"].err.Load(); got != beforeErr {
		t.Errorf("ctx-cancel recordToolCall must NOT bump err: got %d want %d", got, beforeErr)
	}
}

func TestRecordToolCall_UnknownToolNoOp(t *testing.T) {
	resetRegistryForTest()
	// Snapshot all tool counters so an unknown tool name doesn't
	// mysteriously bump some other bucket.
	type pair struct{ ok, err int64 }
	before := make(map[string]pair)
	for tool, c := range toolCallCounters {
		before[tool] = pair{c.ok.Load(), c.err.Load()}
	}
	var e error = errors.New("boom")
	recordToolCall("not-a-real-tool", nil, &e)
	for tool, c := range toolCallCounters {
		if c.ok.Load() != before[tool].ok || c.err.Load() != before[tool].err {
			t.Errorf("unknown-tool recordToolCall mutated %s: before=%v after=(%d,%d)",
				tool, before[tool], c.ok.Load(), c.err.Load())
		}
	}
}

func TestToolCounters_RegisteredAtInit(t *testing.T) {
	resetRegistryForTest()
	// Bump every (tool, status) pair so every bucket has a non-zero
	// value, then assert WriteProm emits all 18 children under one
	// metric name with one HELP/TYPE block.
	for _, tool := range trackedTools {
		toolCallCounters[tool].ok.Inc()
		toolCallCounters[tool].err.Inc()
	}
	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	if got := strings.Count(out, "# TYPE dfmt_tool_calls_total"); got != 1 {
		t.Errorf("dfmt_tool_calls_total TYPE line count = %d, want 1\n%s", got, out)
	}
	for _, tool := range trackedTools {
		needOk := `dfmt_tool_calls_total{status="ok",tool="` + tool + `"}`
		needErr := `dfmt_tool_calls_total{status="error",tool="` + tool + `"}`
		if !strings.Contains(out, needOk) {
			t.Errorf("missing ok line for tool=%s", tool)
		}
		if !strings.Contains(out, needErr) {
			t.Errorf("missing error line for tool=%s", tool)
		}
	}
}

func TestWireHandlerMetrics_DedupHits(t *testing.T) {
	resetRegistryForTest()
	h := &Handlers{}
	h.dedupHits.Add(7)
	WireHandlerMetrics(h)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	if !strings.Contains(out, "# TYPE dfmt_dedup_hits_total counter") {
		t.Errorf("missing TYPE line for dedup hits:\n%s", out)
	}
	if !strings.Contains(out, "dfmt_dedup_hits_total 7") {
		t.Errorf("dedup hits value not 7:\n%s", out)
	}

	// Bump and re-scrape — the closure must read live, not cached.
	h.dedupHits.Add(5)
	buf.Reset()
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_dedup_hits_total 12") {
		t.Errorf("dedup hits not refreshed on second scrape:\n%s", buf.String())
	}
}

// TestHandlers_Search_BumpsCounter is the end-to-end check: a real
// Handlers.Search call exits and the dfmt_tool_calls_total{tool=search,
// status=ok} counter advances by 1. Pairs with the unit tests above to
// catch a wiring regression where the defer is removed but the package-
// level counters still register.
func TestHandlers_Search_BumpsCounter(t *testing.T) {
	resetRegistryForTest()
	h := NewHandlers(nil, nil, nil) // nil index → returns empty result, nil err
	beforeOk := toolCallCounters["search"].ok.Load()
	beforeErr := toolCallCounters["search"].err.Load()

	if _, err := h.Search(context.Background(), SearchParams{Query: "x", Limit: 5}); err != nil {
		t.Fatalf("Search returned err: %v", err)
	}
	if got := toolCallCounters["search"].ok.Load(); got != beforeOk+1 {
		t.Errorf("search.ok counter: got %d want %d", got, beforeOk+1)
	}
	if got := toolCallCounters["search"].err.Load(); got != beforeErr {
		t.Errorf("search.err counter mutated: got %d want %d", got, beforeErr)
	}
}

// TestHandlers_Recall_BumpsErrorCounter exercises the failure path:
// Recall on a degraded handler (no journal attached) returns errNoProject,
// and the error bucket advances.
func TestHandlers_Recall_BumpsErrorCounter(t *testing.T) {
	resetRegistryForTest()
	h := NewHandlers(nil, nil, nil) // no journal → errNoProject
	beforeOk := toolCallCounters["recall"].ok.Load()
	beforeErr := toolCallCounters["recall"].err.Load()

	if _, err := h.Recall(context.Background(), RecallParams{}); err == nil {
		t.Fatalf("Recall on nil journal must return errNoProject, got nil")
	}
	if got := toolCallCounters["recall"].err.Load(); got != beforeErr+1 {
		t.Errorf("recall.err counter: got %d want %d", got, beforeErr+1)
	}
	if got := toolCallCounters["recall"].ok.Load(); got != beforeOk {
		t.Errorf("recall.ok counter mutated: got %d want %d", got, beforeOk)
	}
}

func TestWireHandlerMetrics_NilSafe(t *testing.T) {
	resetRegistryForTest()
	WireHandlerMetrics(nil) // must not panic
	var buf bytes.Buffer
	_ = WriteProm(&buf)
	if strings.Contains(buf.String(), "dfmt_dedup_hits_total") {
		t.Errorf("nil handler must NOT register dedup metric")
	}
}

// TestWireHandlerMetrics_IndexDocs covers the dfmt_index_docs gauge:
// the closure must read TotalDocs live so a doc added between scrapes
// shows up on the next /metrics hit.
func TestWireHandlerMetrics_IndexDocs(t *testing.T) {
	resetRegistryForTest()
	idx := core.NewIndex()
	h := NewHandlers(idx, nil, nil)
	WireHandlerMetrics(h)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_index_docs 0") {
		t.Errorf("expected dfmt_index_docs 0 on empty index:\n%s", buf.String())
	}

	idx.Add(core.Event{ID: "test1", Type: core.EventType("note"), Priority: core.Priority("P3")})
	buf.Reset()
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_index_docs 1") {
		t.Errorf("expected dfmt_index_docs 1 after Add:\n%s", buf.String())
	}
}

func TestWireHandlerMetrics_IndexDocs_NilIndex(t *testing.T) {
	resetRegistryForTest()
	h := NewHandlers(nil, nil, nil) // degraded mode, no index
	WireHandlerMetrics(h)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_index_docs 0") {
		t.Errorf("nil index must report 0 docs, not panic:\n%s", buf.String())
	}
}

// sizeReportingJournal is a mockJournal-style stub that returns a
// caller-controlled (size, err) pair from Size(). Used to verify the
// gauge encodes errors as -1 and reads live values on each scrape.
type sizeReportingJournal struct {
	size int64
	err  error
}

func (j *sizeReportingJournal) Append(ctx context.Context, e core.Event) error { return nil }
func (j *sizeReportingJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	ch := make(chan core.Event)
	close(ch)
	return ch, nil
}
func (j *sizeReportingJournal) Checkpoint(ctx context.Context) (string, error) { return "", nil }
func (j *sizeReportingJournal) Rotate(ctx context.Context) error               { return nil }
func (j *sizeReportingJournal) Size() (int64, error)                           { return j.size, j.err }
func (j *sizeReportingJournal) Close() error                                   { return nil }

func TestWireHandlerMetrics_JournalBytes_Live(t *testing.T) {
	resetRegistryForTest()
	j := &sizeReportingJournal{size: 1024}
	h := NewHandlers(nil, j, nil)
	WireHandlerMetrics(h)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_journal_bytes 1024") {
		t.Errorf("expected dfmt_journal_bytes 1024, got:\n%s", buf.String())
	}

	// Mutation between scrapes must show up — the closure must read live.
	j.size = 2048
	buf.Reset()
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_journal_bytes 2048") {
		t.Errorf("expected dfmt_journal_bytes 2048 after mutation, got:\n%s", buf.String())
	}
}

func TestWireHandlerMetrics_JournalBytes_ErrorEncoding(t *testing.T) {
	resetRegistryForTest()
	j := &sizeReportingJournal{size: 999, err: errors.New("disk gone")}
	h := NewHandlers(nil, j, nil)
	WireHandlerMetrics(h)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	if !strings.Contains(buf.String(), "dfmt_journal_bytes -1") {
		t.Errorf("error from Size must encode as -1, got:\n%s", buf.String())
	}
	// And the size value when the error is set must NOT leak through.
	if strings.Contains(buf.String(), "dfmt_journal_bytes 999") {
		t.Errorf("error must mask the underlying size, got:\n%s", buf.String())
	}
}

func TestWireHandlerMetrics_JournalBytes_NilJournal(t *testing.T) {
	resetRegistryForTest()
	h := NewHandlers(nil, nil, nil) // degraded mode
	WireHandlerMetrics(h)

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	if strings.Contains(buf.String(), "dfmt_journal_bytes") {
		t.Errorf("nil journal must NOT register dfmt_journal_bytes (would emit permanent -1):\n%s", buf.String())
	}
}

func TestWireHandlerMetrics_DedupCacheSizes(t *testing.T) {
	resetRegistryForTest()
	h := NewHandlers(nil, nil, nil)
	WireHandlerMetrics(h)

	// Both caches start empty. Bump sentCache and dedupCache to verify
	// the closures observe live mutation.
	h.sentMu.Lock()
	h.sentCache = map[string]time.Time{
		"a": time.Now().Add(sentTTL),
		"b": time.Now().Add(sentTTL),
		"c": time.Now().Add(sentTTL),
	}
	h.sentMu.Unlock()

	h.dedupMu.Lock()
	h.dedupCache = map[string]dedupEntry{
		"x": {contentID: "x-id", expiresAt: time.Now().Add(dedupTTL)},
		"y": {contentID: "y-id", expiresAt: time.Now().Add(dedupTTL)},
	}
	h.dedupMu.Unlock()

	var buf bytes.Buffer
	_ = WriteProm(&buf)
	out := buf.String()
	if !strings.Contains(out, "dfmt_wire_dedup_entries 3") {
		t.Errorf("expected wire dedup entries = 3, got:\n%s", out)
	}
	if !strings.Contains(out, "dfmt_content_dedup_entries 2") {
		t.Errorf("expected content dedup entries = 2, got:\n%s", out)
	}
}

// TestTrackedTools_MatchesHandlersSurface is the maintenance guard:
// every name in trackedTools must correspond to an exported Handlers
// method, and every (ctx, params) -> (resp, error) tool method on
// Handlers must have a tracked name. Adding a new MCP tool method
// without adding a trackedTools entry would silently drop counts on
// the floor; this test catches that at CI time.
func TestTrackedTools_MatchesHandlersSurface(t *testing.T) {
	expected := map[string]string{
		"Exec":   "exec",
		"Read":   "read",
		"Fetch":  "fetch",
		"Glob":   "glob",
		"Grep":   "grep",
		"Edit":   "edit",
		"Write":  "write",
		"Recall": "recall",
		"Search": "search",
	}

	hType := reflect.TypeOf((*Handlers)(nil))
	for methodName, toolName := range expected {
		m, ok := hType.MethodByName(methodName)
		if !ok {
			t.Errorf("expected Handlers method %s missing — trackedTools entry %q would never fire", methodName, toolName)
			continue
		}
		// Signature contract: (h, ctx, params) -> (resp, err).
		// 4 in (receiver + ctx + params), 2 out (resp + err).
		if m.Type.NumIn() != 3 {
			t.Errorf("Handlers.%s NumIn = %d, want 3 (ctx, params)", methodName, m.Type.NumIn())
		}
		if m.Type.NumOut() != 2 {
			t.Errorf("Handlers.%s NumOut = %d, want 2 (resp, err)", methodName, m.Type.NumOut())
		}
	}

	tracked := make(map[string]bool, len(trackedTools))
	for _, tool := range trackedTools {
		tracked[tool] = true
	}
	for _, want := range expected {
		if !tracked[want] {
			t.Errorf("trackedTools missing %q (Handlers method exists but counter would never fire)", want)
		}
	}
	if len(trackedTools) != len(expected) {
		t.Errorf("trackedTools size = %d, want %d (drift between handler surface and counter set)",
			len(trackedTools), len(expected))
	}
}
