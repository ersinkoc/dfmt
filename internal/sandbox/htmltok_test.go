package sandbox

import (
	"strings"
	"testing"
	"time"
)

// TestTokenizeHTML_V08LinearOnRepeatedRawText pins V-08: a body with
// many <script> opens must run in O(N) total work, not O(N²). The
// pre-fix implementation called strings.ToLower on the full remaining
// haystack at every <script> open, which on N opens spaced linearly
// across the body produces Σ(N-i)*work-per-byte = O(N²). Test pattern:
// 5 000 script blocks at ~100 bytes each (~500 KiB total). Pre-fix
// this took >5 s on a 2026-era box; post-fix is sub-second. We assert
// completion within 2 s — generous slack so the test isn't flaky on a
// loaded CI runner but tight enough to catch the pathological regime.
func TestTokenizeHTML_V08LinearOnRepeatedRawText(t *testing.T) {
	const blocks = 5000
	var b strings.Builder
	for i := 0; i < blocks; i++ {
		b.WriteString("<script>var x=1;var y=2;var z=3;</script>")
	}
	in := b.String()

	start := time.Now()
	got := tokenizeHTML(in)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("tokenizeHTML took %v on %d <script> blocks; suggests O(N²) regression", elapsed, blocks)
	}
	// Sanity: we should have ~3 tokens per block (start, text, end).
	if len(got) < blocks*2 {
		t.Errorf("got only %d tokens for %d script blocks; tokenizer ate too much", len(got), blocks)
	}
}

// TestTokenizeHTML_V08TokenCap pins the maxHTMLTokens floor: a body
// with millions of trivial tags must NOT allocate a proportional
// number of token structs. Once the cap is reached, the remainder
// flushes as a single tokText.
func TestTokenizeHTML_V08TokenCap(t *testing.T) {
	// Generate enough tiny tags to overflow maxHTMLTokens. Each `<a>`
	// is ~3 tokens (start, text-or-empty, end), so ~maxHTMLTokens/2
	// blocks suffices.
	const blocks = 150_000
	var b strings.Builder
	for i := 0; i < blocks; i++ {
		b.WriteString("<a>x</a>")
	}
	got := tokenizeHTML(b.String())

	// We expect either exactly maxHTMLTokens or maxHTMLTokens+1
	// (the trailing flushed tokText). Assert it's bounded — never
	// proportional to blocks.
	if len(got) > maxHTMLTokens+1 {
		t.Errorf("token count %d exceeds cap %d+1", len(got), maxHTMLTokens)
	}
	if len(got) < maxHTMLTokens {
		// If the input fit naturally under the cap, the test as
		// written wouldn't be exercising the cap. Make sure that's
		// the case.
		t.Errorf("test input did not reach the cap; got %d tokens, expected ≥%d", len(got), maxHTMLTokens)
	}
}

// TestTokenizeHTML_V08CloseTagBoundary pins the case-fold matcher's
// boundary check: `</scriptable>` must NOT close a `<script>` block,
// even though the lowercase prefix matches. The terminator-byte
// requirement (`>`, `/`, whitespace) prevents extension matches.
func TestTokenizeHTML_V08CloseTagBoundary(t *testing.T) {
	in := `<script>"</scriptable>"</script>`
	got := tokenizeHTML(in)
	// Expect: start, text=`"</scriptable>"`, end. The text MUST
	// contain the would-be-false-match.
	if len(got) != 3 {
		t.Fatalf("want 3 tokens; got %d: %+v", len(got), got)
	}
	if got[1].kind != tokText || !strings.Contains(got[1].text, "</scriptable>") {
		t.Errorf("token[1] should keep the `</scriptable>` literal; got %+v", got[1])
	}
}

// TestTokenizeHTML_PureText: input with no tags collapses to a single
// tokText. Pinning this so a future "always emit at least one token"
// refactor doesn't accidentally split text on whitespace.
func TestTokenizeHTML_PureText(t *testing.T) {
	got := tokenizeHTML("just plain text with spaces")
	if len(got) != 1 {
		t.Fatalf("want 1 token, got %d: %+v", len(got), got)
	}
	if got[0].kind != tokText || got[0].text != "just plain text with spaces" {
		t.Errorf("token[0] = %+v, want tokText('just plain text with spaces')", got[0])
	}
}

// TestTokenizeHTML_SimpleParagraph: classic <p>x</p> shape — the smallest
// non-trivial input. Three tokens: start, text, end.
func TestTokenizeHTML_SimpleParagraph(t *testing.T) {
	got := tokenizeHTML("<p>hello</p>")
	wantKinds := []tokenKind{tokStartTag, tokText, tokEndTag}
	if len(got) != 3 {
		t.Fatalf("want 3 tokens, got %d: %+v", len(got), got)
	}
	for i, k := range wantKinds {
		if got[i].kind != k {
			t.Errorf("token[%d].kind = %d, want %d", i, got[i].kind, k)
		}
	}
	if got[0].tag != "p" || got[1].text != "hello" || got[2].tag != "p" {
		t.Errorf("paragraph tokens wrong: %+v", got)
	}
}

// TestTokenizeHTML_AttributesAllQuoteStyles: HTML in the wild uses all
// three quoting styles. Tokenizer must recognize each and decode
// entity-encoded values uniformly.
func TestTokenizeHTML_AttributesAllQuoteStyles(t *testing.T) {
	got := tokenizeHTML(`<a href="https://x.com" title='a&amp;b' class=link>x</a>`)
	if len(got) < 1 || got[0].kind != tokStartTag || got[0].tag != "a" {
		t.Fatalf("first token wrong: %+v", got)
	}
	attrs := got[0].attrs
	if attrs["href"] != "https://x.com" {
		t.Errorf("href = %q, want https://x.com", attrs["href"])
	}
	if attrs["title"] != "a&b" {
		t.Errorf("title = %q, want 'a&b' (entity decoded)", attrs["title"])
	}
	if attrs["class"] != "link" {
		t.Errorf("class = %q, want 'link' (unquoted value)", attrs["class"])
	}
}

// TestTokenizeHTML_VoidElementsSelfClose: void elements (br, img, hr,
// input, ...) emit tokSelfClosing even without the trailing slash.
func TestTokenizeHTML_VoidElementsSelfClose(t *testing.T) {
	cases := []string{"<br>", "<br/>", "<br />", "<img src=x>", "<hr>", "<input type=text>"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := tokenizeHTML(in)
			if len(got) == 0 {
				t.Fatal("no tokens emitted")
			}
			if got[0].kind != tokSelfClosing {
				t.Errorf("expected tokSelfClosing for %q; got kind=%d", in, got[0].kind)
			}
		})
	}
}

// TestTokenizeHTML_ScriptRawText: a script body containing `<` must NOT
// be parsed as nested tags. Without the raw-text mode, `if (a < b)` would
// produce bogus tag tokens.
func TestTokenizeHTML_ScriptRawText(t *testing.T) {
	in := `<script>if (a < b) { console.log("<not a tag>"); }</script>`
	got := tokenizeHTML(in)
	if len(got) != 3 {
		t.Fatalf("want 3 tokens (start, text, end); got %d: %+v", len(got), got)
	}
	if got[0].kind != tokStartTag || got[0].tag != "script" {
		t.Errorf("token[0] wrong: %+v", got[0])
	}
	if got[1].kind != tokText || !strings.Contains(got[1].text, "if (a < b)") {
		t.Errorf("token[1] should be raw text with `if (a < b)`; got %+v", got[1])
	}
	if got[2].kind != tokEndTag || got[2].tag != "script" {
		t.Errorf("token[2] wrong: %+v", got[2])
	}
}

// TestTokenizeHTML_StyleRawText: same raw-text rule as <script>, for <style>.
func TestTokenizeHTML_StyleRawText(t *testing.T) {
	in := `<style>body > .x { color: red; }</style>`
	got := tokenizeHTML(in)
	if len(got) != 3 {
		t.Fatalf("want 3 tokens; got %d: %+v", len(got), got)
	}
	if got[1].kind != tokText || !strings.Contains(got[1].text, "body > .x") {
		t.Errorf("style body should be raw text; got %+v", got[1])
	}
}

// TestTokenizeHTML_Comment: `<!-- -->` shape produces a tokComment. The
// walker drops these but the tokenizer keeps them visible for tests.
func TestTokenizeHTML_Comment(t *testing.T) {
	got := tokenizeHTML(`<!-- a comment --><p>after</p>`)
	if len(got) < 1 || got[0].kind != tokComment {
		t.Fatalf("first token should be tokComment; got %+v", got)
	}
	if got[0].text != " a comment " {
		t.Errorf("comment body = %q, want ' a comment '", got[0].text)
	}
}

// TestTokenizeHTML_TruncatedComment: no closing `-->` should not cause a
// panic or hang. The tokenizer terminates at EOF.
func TestTokenizeHTML_TruncatedComment(t *testing.T) {
	got := tokenizeHTML(`<!-- never closes`)
	if len(got) != 1 {
		t.Fatalf("want 1 token (truncated comment); got %d: %+v", len(got), got)
	}
	if got[0].kind != tokComment {
		t.Errorf("expected tokComment; got %+v", got[0])
	}
}

// TestTokenizeHTML_Entities: named and numeric character references
// must be decoded in both text and attribute values. Stdlib
// html.UnescapeString does the heavy lifting.
func TestTokenizeHTML_Entities(t *testing.T) {
	got := tokenizeHTML(`<p title="A &amp; B">copy &copy; &#65; &#x42;</p>`)
	if got[0].attrs["title"] != "A & B" {
		t.Errorf("attr decode wrong: %q", got[0].attrs["title"])
	}
	if got[1].kind != tokText || got[1].text != "copy © A B" {
		t.Errorf("text decode wrong: %q", got[1].text)
	}
}

// TestTokenizeHTML_StrayLessThan: `<` in a content-bearing position
// without a following alpha character should be treated as a literal,
// not a malformed tag. Real HTML written by humans does this.
func TestTokenizeHTML_StrayLessThan(t *testing.T) {
	got := tokenizeHTML("the value is < 5 not >= 5")
	// Tokenizer should produce some tokens with `<` preserved as text.
	combined := ""
	for _, tk := range got {
		if tk.kind == tokText {
			combined += tk.text
		}
	}
	if !strings.Contains(combined, "<") {
		t.Errorf("stray `<` lost; combined text: %q", combined)
	}
}

// TestTokenizeHTML_MismatchedClose: `</wrong>` with no matching open
// must not cause a tokenizer error. The token stream emits the
// dangling end tag; the walker is responsible for stack-based recovery.
func TestTokenizeHTML_MismatchedClose(t *testing.T) {
	got := tokenizeHTML(`<p>x</span>y</p>`)
	if len(got) < 1 {
		t.Fatal("no tokens")
	}
	// Just verify we didn't panic and got a stream including the spurious </span>.
	sawSpan := false
	for _, tk := range got {
		if tk.kind == tokEndTag && tk.tag == "span" {
			sawSpan = true
		}
	}
	if !sawSpan {
		t.Errorf("dangling </span> end tag should be emitted; got %+v", got)
	}
}

// TestTokenizeHTML_DoctypeDropped: `<!DOCTYPE html>` is opaque
// boilerplate; the walker has no use for it. Tokenizer drops it.
func TestTokenizeHTML_DoctypeDropped(t *testing.T) {
	got := tokenizeHTML(`<!DOCTYPE html><html><body>x</body></html>`)
	for _, tk := range got {
		if tk.kind == tokText && strings.Contains(strings.ToLower(tk.text), "doctype") {
			t.Errorf("doctype leaked into text token: %+v", tk)
		}
	}
}

// TestTokenizeHTML_BooleanAttribute: HTML allows `<input disabled>` (no
// value). The attribute should appear in the attrs map with an empty
// value, matching how the spec defines presence-only attributes.
func TestTokenizeHTML_BooleanAttribute(t *testing.T) {
	got := tokenizeHTML(`<input disabled type=text>`)
	if got[0].attrs["disabled"] != "" {
		t.Errorf("boolean attr should be present with empty value; got attrs=%+v", got[0].attrs)
	}
	if _, ok := got[0].attrs["disabled"]; !ok {
		t.Errorf("disabled attr missing")
	}
}

// TestTokenizeHTML_UnterminatedQuotedAttribute: a `"` with no matching
// close should not hang. Tokenizer recovers at EOF or `>`.
func TestTokenizeHTML_UnterminatedQuotedAttribute(t *testing.T) {
	got := tokenizeHTML(`<a href="not closed`)
	if len(got) == 0 {
		t.Fatal("no tokens — tokenizer hung or emitted nothing on truncated input")
	}
}

// TestTokenizeHTML_EmptyInput: zero-length string returns zero tokens
// without panic.
func TestTokenizeHTML_EmptyInput(t *testing.T) {
	got := tokenizeHTML("")
	if len(got) != 0 {
		t.Errorf("empty input should produce zero tokens; got %d", len(got))
	}
}
