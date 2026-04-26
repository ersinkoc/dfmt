package sandbox

import (
	"strings"
	"testing"
)

// TestApplyReturnPolicy_RawAlwaysInlines verifies that explicit "raw" mode
// inlines the body and skips the excerpt machinery — agents that opt into
// the full token cost get exactly what they asked for.
func TestApplyReturnPolicy_RawAlwaysInlines(t *testing.T) {
	body := strings.Repeat("line\n", 5000) // ~25 KB, well above InlineThreshold
	out := ApplyReturnPolicy(body, "anything", "raw")
	if out.Body != body {
		t.Errorf("raw mode must inline full body; got %d bytes, want %d", len(out.Body), len(body))
	}
	if out.Summary != "" || out.Matches != nil || out.Vocabulary != nil {
		t.Errorf("raw mode must not produce excerpts; got %+v", out)
	}
}

// TestApplyReturnPolicy_AutoLargeNoIntent locks in the fix for the
// empty-intent → full-output token leak. Before this fix, the default
// behavior for a 1MB grep with no intent was to inline 1MB into the agent's
// context. That is the entire reason this project exists.
func TestApplyReturnPolicy_AutoLargeNoIntent(t *testing.T) {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 200) // > 4KB
	if len(body) <= InlineThreshold {
		t.Fatalf("test setup error: body must exceed InlineThreshold; got %d", len(body))
	}
	out := ApplyReturnPolicy(body, "", "auto")
	if out.Body != "" {
		t.Errorf("auto mode with large body must drop inline body; got %d bytes", len(out.Body))
	}
	if out.Summary == "" {
		t.Error("auto mode must produce a summary so the agent gets a hint")
	}
}

// TestApplyReturnPolicy_AutoSmallNoIntent verifies that small outputs still
// flow through inline — we don't want to penalize a 5-byte echo with a
// pointless summary-and-stash round trip.
func TestApplyReturnPolicy_AutoSmallNoIntent(t *testing.T) {
	body := "hello world\n"
	out := ApplyReturnPolicy(body, "", "auto")
	if out.Body != body {
		t.Errorf("auto mode with small body must inline; got %q, want %q", out.Body, body)
	}
}

// TestApplyReturnPolicy_AutoLargeWithIntent verifies the happy path: large
// output + intent → matches + summary, no inline body.
func TestApplyReturnPolicy_AutoLargeWithIntent(t *testing.T) {
	body := strings.Repeat("noise line\n", 400) +
		"this is the LINE_WITH_TARGET we want\n" +
		strings.Repeat("more noise\n", 400)
	out := ApplyReturnPolicy(body, "target", "auto")
	if out.Body != "" {
		t.Errorf("auto mode with large intentful body must drop inline body; got %d bytes", len(out.Body))
	}
	if len(out.Matches) == 0 {
		t.Error("auto mode with intent must produce matches when keywords hit")
	}
}

// TestApplyReturnPolicy_SummaryNeverInlines locks in the contract that
// "summary" mode is a hard "never inline" signal regardless of size.
func TestApplyReturnPolicy_SummaryNeverInlines(t *testing.T) {
	body := "tiny" // smaller than InlineThreshold
	out := ApplyReturnPolicy(body, "tiny", "summary")
	if out.Body != "" {
		t.Errorf("summary mode must never inline body; got %q", out.Body)
	}
	if out.Summary == "" {
		t.Error("summary mode must produce a summary")
	}
}

// TestApplyReturnPolicy_SearchOnlyMatches verifies that "search" mode is the
// minimal-token mode: just matches and vocabulary, no body, no summary.
func TestApplyReturnPolicy_SearchOnlyMatches(t *testing.T) {
	body := "alpha beta gamma\nalpha beta\nalpha\n"
	out := ApplyReturnPolicy(body, "alpha", "search")
	if out.Body != "" {
		t.Errorf("search mode must not inline body; got %q", out.Body)
	}
	if out.Summary != "" {
		t.Errorf("search mode must not include summary; got %q", out.Summary)
	}
	if len(out.Matches) == 0 {
		t.Error("search mode must include matches")
	}
}

// TestApplyReturnPolicy_EmptyMode treats empty string as "auto" — this is
// what JSON-RPC clients that omit the field will send, and the previous
// behavior here was the leakiest path of all.
func TestApplyReturnPolicy_EmptyMode(t *testing.T) {
	body := strings.Repeat("x", InlineThreshold+100)
	out := ApplyReturnPolicy(body, "", "")
	if out.Body != "" {
		t.Errorf("empty mode + large body must behave as auto and drop inline; got %d bytes", len(out.Body))
	}
}
