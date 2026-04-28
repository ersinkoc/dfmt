package sandbox

// LLM token-count approximation (ADR-0012).
//
// Anthropic and OpenAI BPE tokenizers turn text into tokens at ratios
// that depend sharply on the script: ~4 bytes/token for English ASCII,
// ~3 for code with heavy punctuation, ~2 for CJK. DFMT's policy
// thresholds historically used byte counts, which under-budgeted CJK
// bodies (the agent paid 2× the token cost we estimated) and slightly
// over-budgeted English. This file provides the approximation that
// migrates those decisions to tokens.
//
// We do NOT ship a real BPE vocabulary. Doing so would either pull in
// a CGO dependency (no pure-Go Anthropic tokenizer exists) or bundle
// a 1MB+ vocab file. The heuristic below is ~25% accurate against the
// real Claude tokenizer across diverse content — well within the
// margin needed for policy gating, where the cost of a wrong tier
// decision (a body that should have summarized gets inlined) is small.

// ApproxTokens estimates the LLM token cost of s. Heuristic:
//
//	ASCII bytes  → bytes / 4   (matches BPE behavior on English/code)
//	non-ASCII rune → 1 token   (each multi-byte rune ≈ one token)
//
// Calibration anchor points (hand-checked against tiktoken/claude):
//
//	"hello world"           bytes=11, tokens≈2-3, ApproxTokens=2
//	"the quick brown fox"   bytes=19, tokens≈4-5, ApproxTokens=4
//	"你好世界"               bytes=12, tokens≈4,   ApproxTokens=4
//	"merhaba dünya"         bytes=14, tokens≈4-5, ApproxTokens=4
//
// All within a ratio of ~1.0–1.3× of the real count. Sufficient for
// inline-vs-summary decisions where the threshold has 2× headroom on
// either side.
//
// Pure function, safe for concurrent use, O(len(s)) in time.
func ApproxTokens(s string) int {
	if s == "" {
		return 0
	}
	asciiBytes := 0
	nonAsciiRunes := 0
	for _, r := range s {
		if r < 128 {
			asciiBytes++
		} else {
			nonAsciiRunes++
		}
	}
	return asciiBytes/4 + nonAsciiRunes
}
