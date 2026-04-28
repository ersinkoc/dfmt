package sandbox

import (
	"strings"
)

// HTML → markdown walker for ADR-0008. Consumes the token stream from
// htmltok.go and emits a markdown-shaped string. Stack-based: open
// elements push a frame; close elements pop and emit the surrounding
// markdown punctuation.
//
// Recovery strategy mirrors the tokenizer's: never panic, never error.
// Mismatched closes pop until the stack drains. Unknown elements pass
// through their text content with tags dropped — future-proofs against
// new HTML elements without requiring tokenizer or walker updates.

// htmlDropElements lists tags whose entire content is dropped — chrome
// for the LLM consumer. Matches the lite path's regex set plus a few
// interactive elements (form/button/iframe) the lite path didn't
// originally cover, since the walker can drop them precisely without
// the regex's nested-tag concerns.
var htmlDropElements = map[string]struct{}{
	"script":   {},
	"style":    {},
	"nav":      {},
	"footer":   {},
	"aside":    {},
	"head":     {},
	"noscript": {},
	"svg":      {},
	"form":     {},
	"button":   {},
	"iframe":   {},
}

// htmlBlockElements list tags that produce a paragraph break around
// their content. The walker emits a blank line before and after.
var htmlBlockElements = map[string]struct{}{
	"p":          {},
	"div":        {},
	"section":    {},
	"article":    {},
	"main":       {},
	"header":     {},
	"figure":     {},
	"figcaption": {},
	"address":    {},
	"details":    {},
	"summary":    {},
}

// ConvertHTML converts HTML-shaped input to a markdown-shaped string.
// Detection is prefix-gated by the same `<!doctype>` / `<html>` check
// CompactHTML uses; non-HTML input returns unchanged. Cap-regression
// guard: if walker output ≥ input bytes, return input unchanged so the
// caller's wire-byte budget is never inflated.
//
// This is the ADR-0008 full path. CompactHTML is retained as the
// fast-path fallback for the rare pages where the walker regresses.
func ConvertHTML(s string) string {
	if s == "" {
		return s
	}
	if !htmlDetectPrefix.MatchString(s) {
		return s
	}
	tokens := tokenizeHTML(s)
	out := walkToMarkdown(tokens)
	if len(out) == 0 || len(out) >= len(s) {
		// Cap-regression fallback: hand off to the lite path so HTML
		// pages where markdown would inflate (already-trimmed,
		// unusual structure) still get script/style/nav stripped.
		return CompactHTML(s)
	}
	return out
}

// walkToMarkdown converts a token stream to markdown. The walker maintains:
//   - a stack of open element names so close-tag recovery can pop until
//     a match is found
//   - a "drop depth" counter — when > 0, all text and child elements
//     are silently discarded (the htmlDropElements path)
//   - a per-list-context counter for ordered-list enumeration
type walker struct {
	tokens []token
	pos    int
	out    strings.Builder
	stack  []string // open element names, lowercased
	// dropDepth > 0 means we're inside a drop-set element (script,
	// style, nav, ...). The push of a drop-set element increments it;
	// the matching pop decrements. Nested drop-set elements stack.
	dropDepth int
	// codeFenceOpen tracks <pre><code> code-block context so inline
	// markdown formatting is suppressed inside a code block.
	codeFenceOpen bool
	codeFenceLang string
	codeFenceBuf  strings.Builder
	// listStack tracks whether each open list is ordered. Top of stack
	// is the innermost list. <li> emission consults this.
	listStack []listKind
	// tableStack tracks per-table state for emitting GFM separators.
	// A separator (`| --- | --- |`) must follow the first row, but
	// only the first — emitting it after every row would break the
	// table syntax. Stack-based to support nested tables (rare but
	// not unheard of: figure captions inside table cells, for
	// instance).
	tableStack []tableState
}

// tableState tracks one open table: how many rows have closed and
// the column count of the first row (used to size the separator
// when we emit it).
type tableState struct {
	rowsClosed int
	firstCols  int
	curCols    int
}

type listKind int

const (
	listNone listKind = iota
	listUnordered
	listOrdered
)

func walkToMarkdown(tokens []token) string {
	w := &walker{tokens: tokens}
	w.run()
	return w.finalize()
}

func (w *walker) run() {
	for w.pos < len(w.tokens) {
		tk := w.tokens[w.pos]
		w.pos++
		w.handle(tk)
	}
}

func (w *walker) handle(tk token) {
	switch tk.kind {
	case tokText:
		if w.dropDepth > 0 {
			return
		}
		if w.codeFenceOpen {
			w.codeFenceBuf.WriteString(tk.text)
			return
		}
		w.writeText(tk.text)
	case tokComment:
		// Comments never reach output. Lite path drops them via regex;
		// walker drops them by ignoring the token kind.
		return
	case tokStartTag:
		w.handleStart(tk)
	case tokSelfClosing:
		w.handleSelfClose(tk)
	case tokEndTag:
		w.handleEnd(tk)
	}
}

func (w *walker) handleStart(tk token) {
	if _, drop := htmlDropElements[tk.tag]; drop {
		w.dropDepth++
		w.stack = append(w.stack, tk.tag)
		return
	}
	if w.dropDepth > 0 {
		w.stack = append(w.stack, tk.tag)
		return
	}
	switch tk.tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		w.ensureBlockBreak()
		level := int(tk.tag[1] - '0')
		w.out.WriteString(strings.Repeat("#", level))
		w.out.WriteByte(' ')
	case "br":
		// Hard break inside a paragraph: two trailing spaces + newline.
		w.out.WriteString("  \n")
	case "strong", "b":
		w.out.WriteString("**")
	case "em", "i":
		w.out.WriteByte('*')
	case "code":
		// <code> inside <pre> opens a fenced block; bare <code> is inline.
		if w.topOfStack() == "pre" {
			w.codeFenceOpen = true
			w.codeFenceLang = extractCodeLanguage(tk.attrs)
		} else {
			w.out.WriteByte('`')
		}
	case "pre":
		w.ensureBlockBreak()
		// pre alone (without nested code) renders as a fenced block.
		// We open the fence here and let the next code or text
		// continue. If the next token is <code>, it'll set the lang.
	case "ul":
		w.ensureBlockBreak()
		w.listStack = append(w.listStack, listUnordered)
	case "ol":
		w.ensureBlockBreak()
		w.listStack = append(w.listStack, listOrdered)
	case "li":
		w.writeListItemPrefix()
	case "a":
		// Open bracket; close on </a>. Don't write the URL yet — we
		// emit it after the close so the link text is captured.
		w.out.WriteByte('[')
	case "blockquote":
		w.ensureBlockBreak()
		// The actual ">" prefix happens line-by-line in writeText.
	case "dl":
		w.ensureBlockBreak()
	case "dt":
		// Definition term renders as bold; closing </dt> drops the
		// bold marker and a trailing colon kicks off the definition.
		w.out.WriteString("**")
	case "dd":
		// Definition body indents under the term; markdown definition
		// list extensions vary, but the simplest "term: body" form
		// renders correctly across CommonMark + GFM.
		w.out.WriteByte(' ')
	case "table":
		w.ensureBlockBreak()
		w.tableStack = append(w.tableStack, tableState{})
	case "thead", "tbody", "tfoot":
		// Markdown tables don't distinguish; just structural in HTML.
	case "tr":
		// Row break. Markdown rows end with `\n` after the cells.
		// Reset the per-row column counter so we can capture this
		// row's width for the GFM separator (when we emit one).
		if i := len(w.tableStack) - 1; i >= 0 {
			w.tableStack[i].curCols = 0
		}
	case "th", "td":
		w.out.WriteString("| ")
		if i := len(w.tableStack) - 1; i >= 0 {
			w.tableStack[i].curCols++
		}
	default:
		// Block elements get a paragraph break around them; inline
		// elements pass through transparently.
		if _, isBlock := htmlBlockElements[tk.tag]; isBlock {
			w.ensureBlockBreak()
		}
	}
	w.stack = append(w.stack, tk.tag)
}

func (w *walker) handleSelfClose(tk token) {
	if w.dropDepth > 0 {
		return
	}
	switch tk.tag {
	case "br":
		w.out.WriteString("  \n")
	case "hr":
		w.ensureBlockBreak()
		w.out.WriteString("---\n\n")
	case "img":
		alt := tk.attrs["alt"]
		src := tk.attrs["src"]
		if src == "" {
			return
		}
		w.out.WriteString("![")
		w.out.WriteString(alt)
		w.out.WriteString("](")
		w.out.WriteString(src)
		w.out.WriteByte(')')
	}
	// Other self-closing elements (input, meta, link, ...) emit nothing.
}

func (w *walker) handleEnd(tk token) {
	// Pop until match found, draining mismatches.
	matchIdx := -1
	for i := len(w.stack) - 1; i >= 0; i-- {
		if w.stack[i] == tk.tag {
			matchIdx = i
			break
		}
	}
	if matchIdx < 0 {
		// Stray close tag — emit no markdown punctuation, just drop.
		return
	}
	// Pop frames above the match (mismatched opens). Their close
	// punctuation never gets a chance to emit; recovery is "best
	// effort" per ADR-0008.
	for len(w.stack) > matchIdx+1 {
		w.popFrame(w.stack[len(w.stack)-1], true)
	}
	// Pop the matched frame — this is where the walker emits closing
	// punctuation for known elements.
	w.popFrame(tk.tag, false)
}

// popFrame removes the top stack frame and emits any trailing markdown
// punctuation for the named element. The stripped flag is true when
// we're popping a stranded open (no matching close); in that case we
// suppress closing punctuation since the markdown shape is already
// broken.
func (w *walker) popFrame(tag string, stranded bool) {
	if len(w.stack) == 0 {
		return
	}
	w.stack = w.stack[:len(w.stack)-1]

	if _, drop := htmlDropElements[tag]; drop {
		w.dropDepth--
		return
	}
	if w.dropDepth > 0 {
		return
	}
	if stranded {
		return
	}
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		w.out.WriteString("\n\n")
	case "p":
		w.out.WriteString("\n\n")
	case "strong", "b":
		w.out.WriteString("**")
	case "em", "i":
		w.out.WriteByte('*')
	case "code":
		if w.codeFenceOpen {
			body := w.codeFenceBuf.String()
			w.codeFenceBuf.Reset()
			w.codeFenceOpen = false
			w.out.WriteString("```")
			w.out.WriteString(w.codeFenceLang)
			w.out.WriteByte('\n')
			w.out.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				w.out.WriteByte('\n')
			}
			w.out.WriteString("```\n\n")
			w.codeFenceLang = ""
		} else {
			w.out.WriteByte('`')
		}
	case "pre":
		// If <code> already closed inside, the fence has been emitted.
		// If not (raw <pre> without code), we may still be in fence
		// mode — flush.
		if w.codeFenceOpen {
			body := w.codeFenceBuf.String()
			w.codeFenceBuf.Reset()
			w.codeFenceOpen = false
			w.out.WriteString("```\n")
			w.out.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				w.out.WriteByte('\n')
			}
			w.out.WriteString("```\n\n")
		}
	case "ul", "ol":
		if len(w.listStack) > 0 {
			w.listStack = w.listStack[:len(w.listStack)-1]
		}
		w.out.WriteByte('\n')
	case "li":
		w.out.WriteByte('\n')
	case "a":
		// Find the matching <a> in tokens (already popped from stack);
		// the href is needed. We stored nothing on the stack about
		// attrs, so we must re-read from the *current pos backward*.
		// Simpler: emit `]()` and let the text already in the buffer
		// be the link text. The URL we don't have here — recover by
		// looking back in tokens for the most recent unclosed <a>.
		href := w.findRecentAnchorHref()
		w.out.WriteString("](")
		w.out.WriteString(href)
		w.out.WriteByte(')')
	case "tr":
		w.out.WriteString("|\n")
		// GFM tables require a `| --- | --- |` separator after the
		// first row. Without it, CommonMark renderers treat the
		// markdown as plain pipe-delimited text — agents reading the
		// output miss the table structure entirely. Emit the
		// separator exactly once per table, sized to the first
		// row's column count.
		if i := len(w.tableStack) - 1; i >= 0 {
			ts := &w.tableStack[i]
			ts.rowsClosed++
			if ts.rowsClosed == 1 {
				ts.firstCols = ts.curCols
				if ts.firstCols > 0 {
					w.out.WriteString(strings.Repeat("| --- ", ts.firstCols))
					w.out.WriteString("|\n")
				}
			}
		}
	case "th", "td":
		w.out.WriteByte(' ')
	case "table":
		w.out.WriteByte('\n')
		if len(w.tableStack) > 0 {
			w.tableStack = w.tableStack[:len(w.tableStack)-1]
		}
	case "blockquote":
		w.out.WriteString("\n\n")
	case "dl":
		w.out.WriteByte('\n')
	case "dt":
		w.out.WriteString("**:")
	case "dd":
		w.out.WriteByte('\n')
	}
}

// findRecentAnchorHref walks back through tokens to find the matching
// <a> open's href attribute. Used at </a> close time since we don't
// store attrs on the stack.
//
// Starts at w.pos-2 deliberately: w.pos-1 is the </a> token we're
// currently handling (the run loop incremented w.pos before calling
// handle), and counting it would inflate depth and skip the genuine
// match. O(N) worst case but anchors close quickly in practice.
func (w *walker) findRecentAnchorHref() string {
	depth := 0
	for i := w.pos - 2; i >= 0; i-- {
		tk := w.tokens[i]
		if tk.kind == tokEndTag && tk.tag == "a" {
			depth++
			continue
		}
		if tk.kind == tokStartTag && tk.tag == "a" {
			if depth == 0 {
				return tk.attrs["href"]
			}
			depth--
		}
	}
	return ""
}

// extractCodeLanguage reads the language hint from a <code> element's
// class attribute. GitHub-style is `class="language-go"`; some sites
// use `class="lang-go"` or `class="hljs go"` — we recognise the first
// form precisely and let the rest pass without a hint.
func extractCodeLanguage(attrs map[string]string) string {
	class := attrs["class"]
	if class == "" {
		return ""
	}
	for _, part := range strings.Fields(class) {
		const prefix = "language-"
		if strings.HasPrefix(part, prefix) {
			return part[len(prefix):]
		}
	}
	return ""
}

// writeText emits text content with HTML whitespace collapsed to single
// spaces. Inside a code fence, raw text is preserved verbatim (handled
// in handle()'s early return).
func (w *walker) writeText(s string) {
	if s == "" {
		return
	}
	// Collapse runs of whitespace to single space. Block-level
	// boundaries are managed by the open/close handlers, not here.
	collapsed := collapseInlineWhitespace(s)
	if collapsed == "" {
		return
	}
	// Trim a leading space if the previous emitted byte is also a
	// whitespace boundary — prevents `<p>x</p> <p>y</p>` from
	// rendering as `x  y` with a stray space.
	if w.endsWithBlockBoundary() {
		collapsed = strings.TrimLeft(collapsed, " \t")
		if collapsed == "" {
			return
		}
	}
	w.out.WriteString(collapsed)
}

// collapseInlineWhitespace replaces runs of any ASCII whitespace
// (space, tab, LF, CR) with a single space. Newlines inside HTML text
// nodes are formatting whitespace — they don't carry meaning.
func collapseInlineWhitespace(s string) string {
	var b strings.Builder
	prevWS := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
			if !prevWS {
				b.WriteByte(' ')
				prevWS = true
			}
			continue
		}
		b.WriteByte(c)
		prevWS = false
	}
	return b.String()
}

// ensureBlockBreak ensures the buffer ends with at least two newlines,
// signaling a paragraph break in markdown. If the buffer is empty, it's
// a no-op so we don't emit leading whitespace.
func (w *walker) ensureBlockBreak() {
	s := w.out.String()
	if s == "" {
		return
	}
	if strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		w.out.WriteByte('\n')
		return
	}
	w.out.WriteString("\n\n")
}

// endsWithBlockBoundary returns true when the buffer ends at a paragraph
// or block break — used to trim leading whitespace on the next text
// emission.
func (w *walker) endsWithBlockBoundary() bool {
	s := w.out.String()
	return s == "" || strings.HasSuffix(s, "\n")
}

// writeListItemPrefix emits the `- ` or `1. ` prefix for the current
// list context. Lists outside any list context (malformed HTML) get a
// dash by default.
func (w *walker) writeListItemPrefix() {
	w.out.WriteByte('\n')
	if len(w.listStack) == 0 {
		w.out.WriteString("- ")
		return
	}
	switch w.listStack[len(w.listStack)-1] {
	case listOrdered:
		w.out.WriteString("1. ")
	default:
		w.out.WriteString("- ")
	}
}

// topOfStack returns the innermost open element name, or "" if empty.
func (w *walker) topOfStack() string {
	if len(w.stack) == 0 {
		return ""
	}
	return w.stack[len(w.stack)-1]
}

// finalize trims excessive trailing whitespace and collapses runs of
// blank lines to at most a single blank line — markdown is meaningful
// when paragraphs are separated by exactly one blank line.
func (w *walker) finalize() string {
	s := w.out.String()
	// Collapse 3+ consecutive newlines to 2 (one blank line).
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}
