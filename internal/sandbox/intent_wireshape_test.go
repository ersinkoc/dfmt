package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestContentMatch_JSON_LowercaseAndScoreDropped pins the Phase A wire-shape
// fix: agents read match payloads as text/source/line (lowercase), and Score
// never crosses the wire because no agent has ever read it back. A regression
// here would silently re-inflate every Match by ~12 bytes per call.
func TestContentMatch_JSON_LowercaseAndScoreDropped(t *testing.T) {
	m := ContentMatch{
		Text:   "needle in haystack",
		Score:  3.14,
		Source: "main.go",
		Line:   42,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"text":"needle in haystack"`, `"source":"main.go"`, `"line":42`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, banned := range []string{`"Text"`, `"Score"`, `"score"`, `"Source"`, `"Line"`} {
		if strings.Contains(got, banned) {
			t.Errorf("unexpected %q in wire form: %s", banned, got)
		}
	}
}

// TestMatchContent_TruncatesLongLines confirms long log lines don't blow up
// per-match wire bytes. The truncate helper is rune-aligned, so we also pass
// a non-ASCII variant to make sure UTF-8 isn't cut mid-rune.
func TestMatchContent_TruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", 200) + " needle " + strings.Repeat("y", 200)
	matches := MatchContent(long, []string{"needle"}, 5)
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}
	if got := len(matches[0].Text); got > 120 {
		t.Errorf("Text len = %d, want <= 120", got)
	}

	turkish := strings.Repeat("ç", 100) + " hedef " + strings.Repeat("ğ", 100)
	tm := MatchContent(turkish, []string{"hedef"}, 5)
	if len(tm) == 0 {
		t.Fatal("expected match in Turkish input")
	}
	// truncate must leave the byte slice on a rune boundary so JSON marshal
	// doesn't substitute U+FFFD; round-trip through json.Marshal verifies.
	if _, err := json.Marshal(tm[0]); err != nil {
		t.Errorf("marshal Turkish-trimmed match: %v", err)
	}
}

// TestGenerateSummary_NoMatchInlining is the regression for A3: Summary used
// to print the top matches inline in addition to ApplyReturnPolicy emitting
// them in Matches[]. Now Summary is a counts-only one-liner.
func TestGenerateSummary_NoMatchInlining(t *testing.T) {
	body := "alpha\nbeta gamma\ndelta needle epsilon\nzeta\n"
	got := GenerateSummary(body, []string{"needle"})
	for _, banned := range []string{"Top matches", "Found ", "L3:"} {
		if strings.Contains(got, banned) {
			t.Errorf("Summary leaked old format substring %q: %q", banned, got)
		}
	}
	if !strings.Contains(got, "matched") {
		t.Errorf("Summary should report match count: %q", got)
	}
}

// TestGenerateSummary_NoKeywordsCountsOnly: when no intent is given, Summary
// is a pure line-count line. Avoids re-introducing the old "Total lines: N"
// verbosity by pinning the new short shape.
func TestGenerateSummary_NoKeywordsCountsOnly(t *testing.T) {
	body := "a\nb\nc\n"
	got := GenerateSummary(body, nil)
	if !strings.HasPrefix(got, "3 lines") {
		t.Errorf("got %q, want prefix '3 lines'", got)
	}
}

// TestApplyReturnPolicy_VocabGatedWhenMatchesLanded: when matches[] already
// carries enough signal, the vocabulary slice is dropped to avoid shipping
// the same words twice. matchN is 10 in big-tier, so matches >= 5 must
// produce no vocab.
func TestApplyReturnPolicy_VocabGatedWhenMatchesLanded(t *testing.T) {
	// Build a body large enough to exit inline-tier and big-tier, with
	// many keyword hits so matches fill the budget.
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		if i%50 == 0 {
			b.WriteString("needle on this line\n")
		} else {
			b.WriteString("filler text without target\n")
		}
	}
	out := ApplyReturnPolicy(b.String(), "needle", "auto")
	if len(out.Matches) < 5 {
		t.Fatalf("test setup: need >= 5 matches to exercise gate; got %d", len(out.Matches))
	}
	if len(out.Vocabulary) != 0 {
		t.Errorf("Vocabulary should be empty when matches>=5; got %d terms", len(out.Vocabulary))
	}
}

// TestApplyReturnPolicy_VocabPresentWhenMatchesSparse is the negative
// counterpart: when matches are below the gate, the agent still gets vocab
// to navigate the body.
func TestApplyReturnPolicy_VocabPresentWhenMatchesSparse(t *testing.T) {
	var b strings.Builder
	// 5000 lines of filler with exactly one keyword hit — well below the
	// matchN/2 threshold, so vocab must appear.
	for i := 0; i < 5000; i++ {
		b.WriteString("filler line of moderate width here\n")
	}
	b.WriteString("singular needle line\n")
	out := ApplyReturnPolicy(b.String(), "needle", "auto")
	if len(out.Matches) >= 5 {
		t.Fatalf("test setup: matches should stay below gate; got %d", len(out.Matches))
	}
	if len(out.Vocabulary) == 0 {
		t.Error("Vocabulary should be present when matches are sparse")
	}
}

// TestExtractKeywords_TurkishStopwords pins A5: Turkish function words from
// core.TurkishStopwords are filtered alongside English ones. Without this,
// vocabulary lists for Turkish content surface "ile"/"için" as "distinctive"
// terms — useless signal.
func TestExtractKeywords_TurkishStopwords(t *testing.T) {
	got := ExtractKeywords("ile için dosya yapılandırma")
	for _, banned := range []string{"ile", "için"} {
		for _, k := range got {
			if k == banned {
				t.Errorf("Turkish stopword %q leaked into keywords: %v", banned, got)
			}
		}
	}
	want := map[string]bool{"dosya": false, "yapılandırma": false}
	for _, k := range got {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for w, seen := range want {
		if !seen {
			t.Errorf("expected keyword %q not in %v", w, got)
		}
	}
}
