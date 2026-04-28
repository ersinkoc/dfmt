package sandbox

import (
	"html"
	"strings"
)

// HTML tokenizer for ADR-0008. Implements a small subset of the HTML5
// parsing algorithm — enough to recover element boundaries, attribute
// values, and text content from real-world web pages without panicking
// on malformed input. Output is a flat token stream consumed by the
// markdown walker in htmlmd.go.
//
// Recovery strategy: we never fail. Malformed input degrades to "best
// effort" — unmatched closing tags pop until match-or-empty, truncated
// comments terminate at EOF, unterminated quoted attributes close at
// the next `>`, and stray `<` characters fall back to literal text.
// This matches ADR-0008 §"Negative consequences": browser-grade error
// recovery (foster parenting, anchored re-parsing) is out of scope.

// tokenKind enumerates the shapes of token the tokenizer emits.
type tokenKind int

const (
	tokText        tokenKind = iota // literal text content
	tokStartTag                     // <foo attr="x">
	tokEndTag                       // </foo>
	tokSelfClosing                  // <br>, <img/>, etc.
	tokComment                      // <!-- ... -->
)

// token is a single output unit from the tokenizer. attrs is nil for
// text and comment tokens; tag is "" for text and comment tokens.
type token struct {
	kind  tokenKind
	tag   string
	text  string
	attrs map[string]string
}

// htmlVoidElements lists tags that the HTML5 spec defines as void
// (no end tag, no content). The tokenizer emits these as tokSelfClosing
// even when the source omits the trailing `/`. Set is small and stable;
// future HTML revisions add tags rarely.
var htmlVoidElements = map[string]struct{}{
	"area":   {},
	"base":   {},
	"br":     {},
	"col":    {},
	"embed":  {},
	"hr":     {},
	"img":    {},
	"input":  {},
	"link":   {},
	"meta":   {},
	"param":  {},
	"source": {},
	"track":  {},
	"wbr":    {},
}

// htmlRawTextElements lists tags whose content is treated as opaque
// text — `<script>` and `<style>` bodies are NOT tokenized as HTML even
// when they contain `<` characters. Without this, a script that does
// `if (a < b)` would be mis-parsed as a tag open. Matches HTML5's
// raw-text element class.
var htmlRawTextElements = map[string]struct{}{
	"script": {},
	"style":  {},
}

// tokenizeHTML converts an HTML body to a token stream. The function
// always returns — there is no error path. Adversarial or truncated
// input produces a best-effort token list.
//
// Caller responsibility: gate by HTML detection (CompactHTML's prefix
// check) before calling. tokenizeHTML on plain text returns one giant
// tokText, which is wasteful but not wrong.
func tokenizeHTML(s string) []token {
	t := &tokenizer{src: s}
	t.run()
	return t.out
}

// tokenizer carries the byte cursor and accumulated output across the
// state-machine steps. Kept as a struct (not closures) so each helper
// can advance the cursor and emit independently.
type tokenizer struct {
	src string
	pos int
	out []token
}

func (t *tokenizer) run() {
	for t.pos < len(t.src) {
		switch t.src[t.pos] {
		case '<':
			t.consumeTagLike()
		default:
			t.consumeText()
		}
	}
}

// consumeText accumulates literal text until the next `<` or EOF, then
// emits one tokText. Whitespace is preserved verbatim — the walker is
// responsible for collapsing runs.
func (t *tokenizer) consumeText() {
	start := t.pos
	for t.pos < len(t.src) && t.src[t.pos] != '<' {
		t.pos++
	}
	if t.pos > start {
		text := t.src[start:t.pos]
		t.out = append(t.out, token{kind: tokText, text: html.UnescapeString(text)})
	}
}

// consumeTagLike dispatches on the byte after `<`: comment (`<!--`),
// doctype/declaration (`<!`), end tag (`</`), or start tag (`<a..`).
// Anything else is treated as a literal `<` text character.
func (t *tokenizer) consumeTagLike() {
	// Lookahead window — guard against EOF bytes.
	if t.pos+1 >= len(t.src) {
		t.out = append(t.out, token{kind: tokText, text: t.src[t.pos:]})
		t.pos = len(t.src)
		return
	}
	switch {
	case strings.HasPrefix(t.src[t.pos:], "<!--"):
		t.consumeComment()
	case t.src[t.pos+1] == '!' || t.src[t.pos+1] == '?':
		// `<!doctype html>` or `<?xml ?>` — drop wholesale. The walker
		// has no use for them; passing them through as text would leak
		// boilerplate the lite path also drops.
		t.consumeUntil('>')
	case t.src[t.pos+1] == '/':
		t.consumeEndTag()
	case isASCIILetter(t.src[t.pos+1]):
		t.consumeStartTag()
	default:
		// Stray `<` (e.g. `a < b` in unescaped text). Treat as literal.
		t.out = append(t.out, token{kind: tokText, text: "<"})
		t.pos++
	}
}

// consumeComment skips from `<!--` to `-->`. Truncated comments (no
// closing `-->`) terminate at EOF — emitted but with the comment body
// being whatever bytes remained.
func (t *tokenizer) consumeComment() {
	t.pos += 4 // skip "<!--"
	end := strings.Index(t.src[t.pos:], "-->")
	if end < 0 {
		t.out = append(t.out, token{kind: tokComment, text: t.src[t.pos:]})
		t.pos = len(t.src)
		return
	}
	t.out = append(t.out, token{kind: tokComment, text: t.src[t.pos : t.pos+end]})
	t.pos += end + 3
}

// consumeUntil advances pos past the next occurrence of c (inclusive).
// On EOF, advances to end. Used for declaration/processing-instruction
// dropping where the contents are not retained.
func (t *tokenizer) consumeUntil(c byte) {
	idx := strings.IndexByte(t.src[t.pos:], c)
	if idx < 0 {
		t.pos = len(t.src)
		return
	}
	t.pos += idx + 1
}

// consumeEndTag handles `</tagname>`. Whitespace before `>` is
// tolerated; attributes on closing tags (which HTML5 forbids but
// sloppy emitters produce) are skipped silently.
func (t *tokenizer) consumeEndTag() {
	t.pos += 2 // skip "</"
	tag := t.consumeTagName()
	t.skipUntilGT()
	if tag == "" {
		return
	}
	t.out = append(t.out, token{kind: tokEndTag, tag: tag})
}

// consumeStartTag handles `<tagname attr="value" ...>` and
// `<tagname />`. Distinguishes self-closing form, void elements, and
// raw-text elements (script/style) — the latter switches the tokenizer
// into "consume body until matching close tag" mode.
func (t *tokenizer) consumeStartTag() {
	t.pos++ // skip "<"
	tag := t.consumeTagName()
	if tag == "" {
		return
	}
	attrs := t.consumeAttributes()
	selfClose := false
	// Trailing `/` before `>` signals XHTML-style self-closing.
	if t.pos < len(t.src) && t.src[t.pos] == '/' {
		selfClose = true
		t.pos++
	}
	t.skipUntilGT()
	if _, void := htmlVoidElements[tag]; void {
		selfClose = true
	}
	kind := tokStartTag
	if selfClose {
		kind = tokSelfClosing
	}
	t.out = append(t.out, token{kind: kind, tag: tag, attrs: attrs})
	if _, raw := htmlRawTextElements[tag]; raw && !selfClose {
		t.consumeRawTextUntilClose(tag)
	}
}

// consumeRawTextUntilClose accumulates everything from after the opening
// `<script>`/`<style>` tag up to (but not including) the matching close
// tag. The body is emitted as a single tokText so the walker can drop
// it wholesale — but without this raw-text mode, a script containing
// `if (a < b)` would be mis-tokenized into bogus tags.
//
// The match is case-insensitive (HTML tags are case-insensitive) and
// uses a substring search for `</tagname` rather than a full parse.
func (t *tokenizer) consumeRawTextUntilClose(tag string) {
	start := t.pos
	lowerSrc := strings.ToLower(t.src[t.pos:])
	closeNeedle := "</" + tag
	end := strings.Index(lowerSrc, closeNeedle)
	if end < 0 {
		// EOF before close tag: emit everything as text and stop.
		t.out = append(t.out, token{kind: tokText, text: t.src[start:]})
		t.pos = len(t.src)
		return
	}
	t.out = append(t.out, token{kind: tokText, text: t.src[start : start+end]})
	t.pos = start + end + 2 // skip past `</`
	closeTag := t.consumeTagName()
	t.skipUntilGT()
	if closeTag != "" {
		t.out = append(t.out, token{kind: tokEndTag, tag: closeTag})
	}
}

// consumeTagName reads ASCII letters/digits/`-`/`_` from pos onward and
// returns the lowercased name. Stops at the first non-name byte. An
// empty result indicates malformed input — caller should bail out.
func (t *tokenizer) consumeTagName() string {
	start := t.pos
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if isASCIILetter(c) || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':' {
			t.pos++
		} else {
			break
		}
	}
	if t.pos == start {
		return ""
	}
	return strings.ToLower(t.src[start:t.pos])
}

// consumeAttributes reads zero or more `name=value` pairs separated by
// whitespace. Stops at `>` or `/>` (does not consume them). Tolerates
// boolean attributes (`disabled`), unquoted values (`width=100`),
// single-quoted values (`href='/'`), and double-quoted values
// (`class="x"`). Values are entity-decoded.
func (t *tokenizer) consumeAttributes() map[string]string {
	var attrs map[string]string
	for t.pos < len(t.src) {
		t.skipWhitespace()
		if t.pos >= len(t.src) {
			return attrs
		}
		c := t.src[t.pos]
		if c == '>' || c == '/' {
			return attrs
		}
		name := t.consumeAttrName()
		if name == "" {
			// Garbage byte we can't classify — skip it to avoid an
			// infinite loop. Recovery: treat as malformed and move on.
			t.pos++
			continue
		}
		t.skipWhitespace()
		var value string
		if t.pos < len(t.src) && t.src[t.pos] == '=' {
			t.pos++
			t.skipWhitespace()
			value = t.consumeAttrValue()
		}
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs[name] = value
	}
	return attrs
}

// consumeAttrName reads an attribute name. Same rules as tag names.
func (t *tokenizer) consumeAttrName() string {
	start := t.pos
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if isASCIILetter(c) || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':' {
			t.pos++
		} else {
			break
		}
	}
	if t.pos == start {
		return ""
	}
	return strings.ToLower(t.src[start:t.pos])
}

// consumeAttrValue reads a quoted or unquoted attribute value and
// returns it entity-decoded. Unterminated quoted values close at the
// next `>` (or EOF) — we never block on a missing quote.
func (t *tokenizer) consumeAttrValue() string {
	if t.pos >= len(t.src) {
		return ""
	}
	c := t.src[t.pos]
	switch c {
	case '"', '\'':
		quote := c
		t.pos++
		start := t.pos
		for t.pos < len(t.src) && t.src[t.pos] != quote {
			// Tolerate `>` inside the quoted region — quoted values
			// can legitimately contain it (rare but valid HTML).
			t.pos++
		}
		val := t.src[start:t.pos]
		if t.pos < len(t.src) {
			t.pos++ // skip closing quote
		}
		return html.UnescapeString(val)
	default:
		start := t.pos
		for t.pos < len(t.src) {
			c := t.src[t.pos]
			if c == '>' || c == '/' || isASCIIWhitespace(c) {
				break
			}
			t.pos++
		}
		return html.UnescapeString(t.src[start:t.pos])
	}
}

// skipWhitespace advances pos over ASCII whitespace.
func (t *tokenizer) skipWhitespace() {
	for t.pos < len(t.src) && isASCIIWhitespace(t.src[t.pos]) {
		t.pos++
	}
}

// skipUntilGT advances pos past the next `>` (inclusive). EOF is safe.
func (t *tokenizer) skipUntilGT() {
	for t.pos < len(t.src) {
		if t.src[t.pos] == '>' {
			t.pos++
			return
		}
		t.pos++
	}
}

// isASCIILetter is the strict A-Z/a-z check the tokenizer uses to
// distinguish a tag start from a stray `<`. Unicode letters in element
// names are extremely rare in real HTML and are treated as malformed.
func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isASCIIWhitespace covers the HTML5 whitespace set: space, tab, LF,
// CR, FF. Avoids unicode.IsSpace because we only care about ASCII —
// non-ASCII whitespace inside attribute values must be preserved.
func isASCIIWhitespace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	}
	return false
}

