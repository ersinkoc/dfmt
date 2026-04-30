package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
