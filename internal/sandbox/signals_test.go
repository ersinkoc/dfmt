package sandbox

import (
	"strings"
	"testing"
)

// TestExtractSignalLines_GoTest covers the dominant case for this codebase:
// a `go test` run with PASS lines, FAIL markers, a panic, and the trailing
// "FAIL\tpkg" summary. The extractor must surface only the verdict lines —
// not the PASS/RUN noise or the trailing newline.
func TestExtractSignalLines_GoTest(t *testing.T) {
	body := strings.Join([]string{
		"=== RUN   TestAlpha",
		"--- PASS: TestAlpha (0.00s)",
		"=== RUN   TestBeta",
		"--- FAIL: TestBeta (0.01s)",
		"    beta_test.go:12: expected 5, got 3",
		"=== RUN   TestGamma",
		"panic: runtime error: invalid memory address",
		"FAIL\tgithub.com/example/pkg\t0.123s",
		"",
	}, "\n")

	got := extractSignalLines(body)
	if len(got) < 3 {
		t.Fatalf("want at least 3 signals (FAIL marker, panic, summary); got %d", len(got))
	}
	wantPrefixes := []string{"--- FAIL: TestBeta", "panic:", "FAIL\tgithub.com/example/pkg"}
	for _, want := range wantPrefixes {
		found := false
		for _, m := range got {
			if strings.HasPrefix(m.Text, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected signal with prefix %q in extracted set; got %+v", want, got)
		}
	}
	for _, m := range got {
		if m.Score != SignalScore {
			t.Errorf("signal score = %v, want %v", m.Score, SignalScore)
		}
		if m.Line == 0 {
			t.Errorf("signal line should be 1-indexed; got 0 for %q", m.Text)
		}
	}
}

// TestExtractSignalLines_Python covers traceback + exception header,
// regardless of whether the exception is a stdlib type or a user class
// ending in "Error".
func TestExtractSignalLines_Python(t *testing.T) {
	body := strings.Join([]string{
		"running tests...",
		"Traceback (most recent call last):",
		"  File \"app.py\", line 42, in <module>",
		"    raise CustomDomainError(\"boom\")",
		"CustomDomainError: boom",
		"",
	}, "\n")

	got := extractSignalLines(body)
	hasTrace := false
	hasError := false
	for _, m := range got {
		if strings.HasPrefix(m.Text, "Traceback") {
			hasTrace = true
		}
		if strings.HasPrefix(m.Text, "CustomDomainError:") {
			hasError = true
		}
	}
	if !hasTrace {
		t.Errorf("Python traceback header must be a signal; got %+v", got)
	}
	if !hasError {
		t.Errorf("Python exception line must be a signal; got %+v", got)
	}
}

// TestExtractSignalLines_NoFalsePositive verifies clean output produces
// no signals — important so this doesn't pollute matches in healthy
// builds with phantom hits.
func TestExtractSignalLines_NoFalsePositive(t *testing.T) {
	body := strings.Join([]string{
		"compiling pkg/foo",
		"compiling pkg/bar",
		"linking",
		"BUILD SUCCESS in 1.234s",
		"",
	}, "\n")

	got := extractSignalLines(body)
	if got != nil {
		t.Errorf("clean output must produce zero signals; got %+v", got)
	}
}

// TestExtractSignalLines_RespectsCap verifies the signalCap doesn't get
// blown out by a stack trace that's all "panic:" lines (which can happen
// with goroutine dumps).
func TestExtractSignalLines_RespectsCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < signalCap*3; i++ {
		b.WriteString("panic: runtime error\n")
	}
	got := extractSignalLines(b.String())
	if len(got) > signalCap {
		t.Errorf("signal extraction must cap at %d; got %d", signalCap, len(got))
	}
}

// TestMergeSignalsIntoMatches_DedupByLine verifies a signal that's also a
// keyword match is emitted once. Both sources can flag the same line
// (e.g., "FAIL: TestX" matches both the regex AND a "test" intent).
func TestMergeSignalsIntoMatches_DedupByLine(t *testing.T) {
	signals := []ContentMatch{{Text: "panic: boom", Score: SignalScore, Line: 42}}
	matches := []ContentMatch{
		{Text: "panic: boom", Score: 3.6, Line: 42},
		{Text: "    at foo()", Score: 2.0, Line: 43},
	}
	merged := mergeSignalsIntoMatches(signals, matches, 5)
	if len(merged) != 2 {
		t.Fatalf("dedup must drop the duplicate at line 42; got %d entries: %+v", len(merged), merged)
	}
	if merged[0].Line != 42 || merged[0].Score != SignalScore {
		t.Errorf("signal must come first with SignalScore; got %+v", merged[0])
	}
	if merged[1].Line != 43 {
		t.Errorf("non-duplicate match must follow signals; got %+v", merged[1])
	}
}

// TestMergeSignalsIntoMatches_RespectsCap verifies the maxOut cap is applied
// after merging — signals get priority, then keyword matches fill the rest.
func TestMergeSignalsIntoMatches_RespectsCap(t *testing.T) {
	signals := []ContentMatch{
		{Text: "s1", Score: SignalScore, Line: 1},
		{Text: "s2", Score: SignalScore, Line: 2},
		{Text: "s3", Score: SignalScore, Line: 3},
	}
	matches := []ContentMatch{
		{Text: "m1", Score: 1, Line: 10},
		{Text: "m2", Score: 1, Line: 11},
	}
	merged := mergeSignalsIntoMatches(signals, matches, 4)
	if len(merged) != 4 {
		t.Fatalf("cap=4 must produce 4 entries; got %d", len(merged))
	}
	if merged[3].Line != 10 {
		t.Errorf("cap should drop matches[1] but keep matches[0]; got line %d", merged[3].Line)
	}
}

// TestApplyReturnPolicy_AutoPromotesSignals verifies the integration: a
// large body with no intent keywords still gets the FAIL line back as a
// match through the kind-aware path. This is the test/build verdict case.
func TestApplyReturnPolicy_AutoPromotesSignals(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("=== RUN   TestSomething\n--- PASS: TestSomething (0.00s)\n")
	}
	b.WriteString("--- FAIL: TestRegression (0.02s)\n")
	b.WriteString("    regression_test.go:88: nil pointer dereference\n")
	b.WriteString("FAIL\tgithub.com/example/pkg\t1.234s\n")
	body := b.String()
	if len(body) <= InlineThreshold {
		t.Fatalf("test setup: body must exceed InlineThreshold; got %d", len(body))
	}

	out := ApplyReturnPolicy(body, "", "auto")
	if len(out.Matches) == 0 {
		t.Fatal("auto + large + no-intent must surface signals as matches; got 0")
	}
	failPromoted := false
	for _, m := range out.Matches {
		if strings.HasPrefix(m.Text, "--- FAIL:") {
			failPromoted = true
			if m.Score != SignalScore {
				t.Errorf("promoted signal must carry SignalScore; got %v for %q", m.Score, m.Text)
			}
			break
		}
	}
	if !failPromoted {
		t.Errorf("FAIL line must appear in matches; got %+v", out.Matches)
	}
	// Tail-bias must NOT fire when signals filled matches.
	if out.Body != "" {
		t.Errorf("Body must be empty when signals supplied matches; got %d bytes", len(out.Body))
	}
}
