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
//
// Tail-bias: when no intent matches landed, we surface the LAST TailBytes
// instead of dropping body entirely — test/build/CI verdicts live at the
// bottom and the agent should see them without a return=raw round-trip.
func TestApplyReturnPolicy_AutoLargeNoIntent(t *testing.T) {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 200) // > 4KB
	if len(body) <= InlineThreshold {
		t.Fatalf("test setup error: body must exceed InlineThreshold; got %d", len(body))
	}
	out := ApplyReturnPolicy(body, "", "auto")
	if out.Body == "" {
		t.Error("auto + large + no-matches must surface a tail snippet; got empty Body")
	}
	if len(out.Body) > TailBytes+100 { // +100 for the marker line
		t.Errorf("tail Body must be capped near TailBytes (%d); got %d", TailBytes, len(out.Body))
	}
	if !strings.Contains(out.Body, "tail; earlier output dropped") {
		t.Errorf("tail Body must carry the truncation marker; got %q", out.Body[:min(80, len(out.Body))])
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
// behavior here was the leakiest path of all. Body is now a tail snippet
// (still bounded by TailBytes), not the full content — that's the
// difference between "leaky default" and "useful default".
func TestApplyReturnPolicy_EmptyMode(t *testing.T) {
	body := strings.Repeat("x", InlineThreshold+100)
	out := ApplyReturnPolicy(body, "", "")
	if len(out.Body) >= len(body) {
		t.Errorf("empty mode + large body must NOT inline full content; got %d of %d bytes", len(out.Body), len(body))
	}
	if len(out.Body) > TailBytes+100 {
		t.Errorf("empty mode + large body Body must be tail-capped near TailBytes (%d); got %d", TailBytes, len(out.Body))
	}
}

// TestApplyReturnPolicy_AutoSmallSkipsExcerpts locks in the token-saving rule
// that auto + body-fits-inline emits *only* the body — no summary, no
// matches, no vocabulary. Those would duplicate bytes the agent is already
// reading inline. The pre-fix behavior emitted all four for every small
// output and roughly doubled the response size for echo-sized commands.
func TestApplyReturnPolicy_AutoSmallSkipsExcerpts(t *testing.T) {
	body := "alpha beta gamma\nalpha beta\nalpha\n"
	if len(body) > InlineThreshold {
		t.Fatalf("test setup: body must fit inline; got %d", len(body))
	}
	out := ApplyReturnPolicy(body, "alpha", "auto")
	if out.Body != body {
		t.Errorf("Body = %q, want full body inlined", out.Body)
	}
	if out.Summary != "" {
		t.Errorf("Summary must be empty when body is inlined; got %q", out.Summary)
	}
	if out.Matches != nil {
		t.Errorf("Matches must be nil when body is inlined; got %d", len(out.Matches))
	}
	if out.Vocabulary != nil {
		t.Errorf("Vocabulary must be nil when body is inlined; got %d terms", len(out.Vocabulary))
	}
}

// TestApplyReturnPolicy_AutoTierScaling locks in size-tiered match/vocab
// counts: mid-tier (4 KB – 64 KB) caps at 5 matches and 10 vocab terms,
// big-tier (>64 KB) gets the historical 10/20. The intent has to genuinely
// hit unique-per-line content for the cap to bite — synthetic uniform input
// would cap on diversity, not on the slice length.
func TestApplyReturnPolicy_AutoTierScaling(t *testing.T) {
	// Mid-tier body: ~10 KB with > 5 distinct intent hits and > 10 distinct
	// non-stopword tokens.
	var midBuilder strings.Builder
	for i := 0; i < 200; i++ {
		midBuilder.WriteString("noise filler line about logging system\n")
	}
	for i := 0; i < 8; i++ {
		// Make each match line unique so MatchContent can return >5.
		midBuilder.WriteString("MATCH_TARGET hit ")
		for j := 0; j < i+1; j++ {
			midBuilder.WriteString("uniqueXYZ ")
		}
		midBuilder.WriteString("\n")
	}
	mid := midBuilder.String()
	if len(mid) <= InlineThreshold || len(mid) > MediumThreshold {
		t.Fatalf("test setup: mid body must land in 4 KB – 64 KB tier; got %d", len(mid))
	}
	midOut := ApplyReturnPolicy(mid, "MATCH_TARGET", "auto")
	if len(midOut.Matches) > 5 {
		t.Errorf("mid-tier matches must cap at 5; got %d", len(midOut.Matches))
	}
	if len(midOut.Vocabulary) > 10 {
		t.Errorf("mid-tier vocabulary must cap at 10; got %d", len(midOut.Vocabulary))
	}

	// Big-tier body: > 64 KB to trigger the 10/20 caps.
	var bigBuilder strings.Builder
	for i := 0; i < 4000; i++ {
		bigBuilder.WriteString("noise filler line about logging system\n")
	}
	for i := 0; i < 20; i++ {
		bigBuilder.WriteString("MATCH_TARGET hit unique-")
		for j := 0; j < i+1; j++ {
			bigBuilder.WriteString("token ")
		}
		bigBuilder.WriteString("\n")
	}
	big := bigBuilder.String()
	if len(big) <= MediumThreshold {
		t.Fatalf("test setup: big body must exceed MediumThreshold; got %d", len(big))
	}
	bigOut := ApplyReturnPolicy(big, "MATCH_TARGET", "auto")
	if len(bigOut.Matches) > 10 {
		t.Errorf("big-tier matches must cap at 10; got %d", len(bigOut.Matches))
	}
	if len(bigOut.Vocabulary) > 20 {
		t.Errorf("big-tier vocabulary must cap at 20; got %d", len(bigOut.Vocabulary))
	}
}

// TestApplyReturnPolicy_TailBiasOnlyWhenNoMatches verifies the tail snippet
// is suppressed once intent matches land — the matches already point the
// agent at the relevant lines and a tail on top would re-bloat the response.
func TestApplyReturnPolicy_TailBiasOnlyWhenNoMatches(t *testing.T) {
	body := strings.Repeat("noise filler line\n", 300) +
		"this is the LINE_WITH_TARGET we want\n" +
		strings.Repeat("more filler\n", 300)
	out := ApplyReturnPolicy(body, "TARGET", "auto")
	if len(out.Matches) == 0 {
		t.Fatal("test setup: intent should produce matches for this body")
	}
	if out.Body != "" {
		t.Errorf("auto + matches present must not also emit tail Body; got %d bytes", len(out.Body))
	}
}

// TestNormalizeOutput_StripsANSI verifies CSI/OSC sequences are removed.
// The npm/cargo/gradle output category routinely contains color codes that
// dominate the token count; stripping happens before stash and excerpt.
func TestNormalizeOutput_StripsANSI(t *testing.T) {
	in := "\x1b[31mfailed\x1b[0m: \x1b[1mboom\x1b[22m\nok\n"
	got := NormalizeOutput(in)
	want := "failed: boom\nok\n"
	if got != want {
		t.Errorf("StripANSI: got %q, want %q", got, want)
	}
	// OSC (set window title) — terminator BEL.
	osc := "\x1b]0;build dashboard\x07ready\n"
	if g := NormalizeOutput(osc); g != "ready\n" {
		t.Errorf("StripOSC: got %q, want %q", g, "ready\n")
	}
}

// TestNormalizeOutput_CollapsesCarriageReturns verifies progress-bar
// rewrites collapse to their final state. The dominant cost source for
// `npm install`, `cargo build`, and any spinner-driven CLI.
func TestNormalizeOutput_CollapsesCarriageReturns(t *testing.T) {
	in := "downloading\r[#  ] 10%\r[## ] 50%\r[###] 100%\ndone\n"
	got := NormalizeOutput(in)
	want := "[###] 100%\ndone\n"
	if got != want {
		t.Errorf("collapseCR: got %q, want %q", got, want)
	}
	// CRLF must NOT be collapsed to empty — every line of a Windows-style
	// log file has \r\n at the end.
	crlf := "line one\r\nline two\r\n"
	if g := NormalizeOutput(crlf); g != "line one\nline two\n" {
		t.Errorf("CRLF: got %q, want CRLF preserved as line content", g)
	}
}

// TestNormalizeOutput_RunLengthEncodes verifies repeated-line compaction.
// The threshold (rleMinReps) means short bursts pass through untouched.
func TestNormalizeOutput_RunLengthEncodes(t *testing.T) {
	in := "dialing...\ndialing...\ndialing...\ndialing...\ndialing...\nconnected\n"
	got := NormalizeOutput(in)
	want := "dialing...\n... (line above repeated 4 more times)\nconnected\n"
	if got != want {
		t.Errorf("RLE: got %q, want %q", got, want)
	}
	// Below threshold: pass through.
	short := "ping\nping\nping\nok\n"
	if g := NormalizeOutput(short); g != short {
		t.Errorf("RLE under threshold must pass through; got %q", g)
	}
}

// TestNormalizeOutput_NoEscapeFastPath verifies the strip-ansi fast path
// when the string contains no ESC byte — the regex shouldn't even run.
// This is mostly a behavior assertion; the perf benefit isn't measured
// here.
func TestNormalizeOutput_NoEscapeFastPath(t *testing.T) {
	in := "plain text\nno escapes\n"
	if got := NormalizeOutput(in); got != in {
		t.Errorf("no-escape passthrough mismatch; got %q", got)
	}
}

// TestTailLines_AlignsToNewline verifies the tail starts on a clean line
// boundary. Cutting mid-line leaves the agent with garbage like
// "...e_test.go:42: expected", which is worse than the marker.
func TestTailLines_AlignsToNewline(t *testing.T) {
	body := "alpha alpha alpha alpha\nbravo bravo bravo bravo\ncharlie line one\ncharlie line two\n"
	got := tailLines(body, 30)
	if !strings.HasPrefix(got, "...(tail;") {
		t.Errorf("tailLines must prefix with marker; got %q", got)
	}
	// Strip the marker line and verify the snippet starts on a fresh line.
	parts := strings.SplitN(got, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("tailLines must contain at least one newline; got %q", got)
	}
	if !strings.Contains(parts[1], "charlie") {
		t.Errorf("tail must reach the last line of the input; got %q", parts[1])
	}
}

// TestTailLines_PassThroughWhenSmall verifies short inputs return verbatim
// (no marker, no slice).
func TestTailLines_PassThroughWhenSmall(t *testing.T) {
	body := "tiny output\n"
	if got := tailLines(body, 1024); got != body {
		t.Errorf("tailLines on small input must pass through; got %q", got)
	}
}
