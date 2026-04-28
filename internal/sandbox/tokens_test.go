package sandbox

import (
	"strings"
	"testing"
)

// TestApproxTokens_ASCII: pure-ASCII English prose hits the bytes/4
// baseline. Since the heuristic uses integer division, exact bytes/4
// is the floor; a small rounding band is acceptable.
func TestApproxTokens_ASCII(t *testing.T) {
	cases := []struct {
		in     string
		minTok int
		maxTok int
	}{
		{"", 0, 0},
		{"hello", 1, 2},
		{"the quick brown fox jumps over the lazy dog", 10, 12},
		{strings.Repeat("a", 100), 25, 25},
		{strings.Repeat("hello ", 50), 75, 75},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := ApproxTokens(c.in)
			if got < c.minTok || got > c.maxTok {
				t.Errorf("ApproxTokens(%q) = %d, want between %d and %d",
					c.in, got, c.minTok, c.maxTok)
			}
		})
	}
}

// TestApproxTokens_Turkish: Turkish prose with non-ASCII letters
// (ç/ğ/ı/ö/ş/ü) — each non-ASCII rune adds 1 token. Tests calibration
// against text where ASCII is mixed with multi-byte runes.
func TestApproxTokens_Turkish(t *testing.T) {
	in := "Türkçe içerik: dosya yapılandırma ve günlük kayıtları"
	got := ApproxTokens(in)
	// Hand-counted: ~10 non-ASCII runes (ü,ç,i̇,ç,ı,ı,ı,ı,ü,ı), rest ASCII.
	// ASCII bytes ≈ 35; non-ASCII runes ≈ 10. ApproxTokens ≈ 35/4 + 10 = 18.
	if got < 14 || got > 22 {
		t.Errorf("ApproxTokens(Turkish) = %d, want 14..22", got)
	}
}

// TestApproxTokens_CJK: pure CJK content costs roughly 1 token per
// character. A 100-char Chinese string should yield ~100 tokens.
func TestApproxTokens_CJK(t *testing.T) {
	in := strings.Repeat("你好世界", 25) // 100 CJK runes
	got := ApproxTokens(in)
	// Each rune ≈ 1 token, no ASCII content.
	if got != 100 {
		t.Errorf("ApproxTokens(CJK 100 runes) = %d, want 100", got)
	}
}

// TestApproxTokens_Empty: empty string is 0 tokens. Pinning the
// boundary so a future "always return at least 1" change doesn't
// silently inflate budgets.
func TestApproxTokens_Empty(t *testing.T) {
	if got := ApproxTokens(""); got != 0 {
		t.Errorf("ApproxTokens(\"\") = %d, want 0", got)
	}
}

// TestApproxTokens_PureWhitespace: long whitespace runs are mostly
// merged into adjacent tokens by real BPE tokenizers. Our heuristic
// over-counts (1 byte = 0.25 tokens), but the threshold doesn't
// matter — whitespace bodies are rare and never near the inline cap.
func TestApproxTokens_PureWhitespace(t *testing.T) {
	in := strings.Repeat(" ", 100)
	got := ApproxTokens(in)
	if got != 25 {
		t.Errorf("ApproxTokens(100 spaces) = %d, want 25 (heuristic over-counts whitespace)", got)
	}
}

// TestApproxTokens_HeavyPunctuation: code/JSON shapes have lots of
// punctuation. The heuristic treats them as ASCII bytes → bytes/4,
// which slightly under-counts vs real BPE (where each punctuation
// char often becomes its own token). Under-counting is the safer
// failure mode for inline-vs-summary policy.
func TestApproxTokens_HeavyPunctuation(t *testing.T) {
	in := `{"a":1,"b":[1,2,3],"c":{"d":"e"}}`
	got := ApproxTokens(in)
	// 33 ASCII bytes → 8 tokens by heuristic; real ≈ 13. Within
	// the documented ±25-50% band; we only need order-of-magnitude
	// agreement for tier decisions.
	if got < 6 || got > 12 {
		t.Errorf("ApproxTokens(JSON) = %d, want 6..12", got)
	}
}

// TestApproxTokens_Monotonic: a longer string has at least as many
// tokens as a shorter prefix. Pinning monotonicity catches a
// regression where the heuristic could underflow on certain rune
// sequences.
func TestApproxTokens_Monotonic(t *testing.T) {
	short := "the quick"
	long := short + " brown fox"
	if ApproxTokens(short) > ApproxTokens(long) {
		t.Errorf("monotonicity violation: tokens(%q)=%d > tokens(%q)=%d",
			short, ApproxTokens(short), long, ApproxTokens(long))
	}
}
