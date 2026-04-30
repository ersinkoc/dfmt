package sandbox

import (
	"testing"
	"unicode/utf8"
)

// FuzzApproxTokens enforces the invariants the policy filter relies on:
// the result must be non-negative, must not exceed the byte length of the
// input (worst case: all-ASCII at 1 token / 4 bytes is bounded above by
// len(s); all-non-ASCII at 1 token / rune is bounded above by len(s)
// because every non-ASCII rune is at least 2 bytes), and must equal zero
// for the empty string. ApproxTokens is a hot path for ApplyReturnPolicy;
// any panic here corrupts the inline / summary / big tier gate.
func FuzzApproxTokens(f *testing.F) {
	seeds := []string{
		"",
		"hello world",
		"the quick brown fox",
		"你好世界",
		"merhaba dünya",
		"\x00\x01\xff",
		"á",
		"\xff\xfe\xfd",
		"\nline\n\nblock\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := ApproxTokens(s)
		if got < 0 {
			t.Fatalf("ApproxTokens(%q) = %d; must be non-negative", s, got)
		}
		if got > len(s) {
			t.Fatalf("ApproxTokens(%q) = %d; must not exceed len(s)=%d", s, got, len(s))
		}
		if s == "" && got != 0 {
			t.Fatalf("ApproxTokens(\"\") = %d; want 0", got)
		}
	})
}

// FuzzNormalizeOutput guards the 8-stage pipeline against panics on
// arbitrary bytes. The output is not required to be a strict subset of
// the input (the pipeline can introduce annotations like "(repeated N
// times)"), but it must always be valid UTF-8 — downstream consumers
// (the summary builder, the journal writer) assume that. ANSI / CR
// rewriting / RLE / structured-output stages have all been individually
// tested; this catches cross-stage interactions and unanticipated byte
// sequences.
func FuzzNormalizeOutput(f *testing.F) {
	seeds := []string{
		"",
		"plain text",
		"\x1b[31mred\x1b[0m",                         // ANSI
		"line1\rline1-overwritten\nline2",            // CR rewrite
		"same\nsame\nsame\nsame\nsame\nsame\n",       // RLE
		"{\"a\":1,\"created_at\":\"x\",\"b\":2}",     // structured
		"---\ntitle: x\n---\n# heading\n",            // markdown frontmatter
		"<html><body><h1>hi</h1></body></html>",      // HTML
		"\xff\xfe\x00\x01",                           // binary-ish
		"diff --git a/x b/x\nindex abc..def 100644\n+foo\n", // git diff
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := NormalizeOutput(s)
		if !utf8.ValidString(out) {
			t.Fatalf("NormalizeOutput produced invalid UTF-8 from %q", s)
		}
	})
}
