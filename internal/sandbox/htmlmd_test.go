package sandbox

import (
	"strings"
	"testing"
)

// convert is a tiny helper to wrap html in a doctype so ConvertHTML's
// detection gate fires. Saves verbosity in test cases.
func convert(html string) string {
	return ConvertHTML("<!doctype html>" + html)
}

// TestConvertHTML_V07DropSetExpanded pins V-07: tags added to
// htmlDropElements (object/embed/applet/link/template/frame/frameset/
// math/portal/meta) must have their body content suppressed entirely.
// Pre-fix the unknown-tag default branch emitted body text verbatim
// for these, leaking <object> data, MathML formula payloads,
// <link rel=stylesheet> URLs, and <meta http-equiv=refresh> redirect
// targets into the LLM consumer's context.
func TestConvertHTML_V07DropSetExpanded(t *testing.T) {
	cases := []struct {
		name    string
		html    string
		mustNot string
	}{
		{"object", `<object data="evil.swf">SECRET_PAYLOAD</object>`, "SECRET_PAYLOAD"},
		// embed is a void element so its "body" text is actually
		// document text, not element body — the V-07 win for embed
		// is suppressing attribute-derived URL leakage.
		{"embed-attr", `<embed src="https://attacker/leak.swf">`, "attacker"},
		{"applet", `<applet code="x">SECRET_PAYLOAD</applet>`, "SECRET_PAYLOAD"},
		{"link", `<link rel="stylesheet" href="https://attacker/leak.css">`, "attacker"},
		{"template", `<template>SECRET_PAYLOAD</template>`, "SECRET_PAYLOAD"},
		{"frame", `<frame src="x">SECRET_PAYLOAD</frame>`, "SECRET_PAYLOAD"},
		{"frameset", `<frameset>SECRET_PAYLOAD</frameset>`, "SECRET_PAYLOAD"},
		{"math", `<math>SECRET_PAYLOAD</math>`, "SECRET_PAYLOAD"},
		{"portal", `<portal>SECRET_PAYLOAD</portal>`, "SECRET_PAYLOAD"},
		{"meta-refresh", `<meta http-equiv="refresh" content="0;url=https://attacker/">`, "attacker"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := convert(c.html)
			if strings.Contains(got, c.mustNot) {
				t.Errorf("%s body leaked through: convert(%q) = %q (must not contain %q)", c.name, c.html, got, c.mustNot)
			}
		})
	}
}

// TestConvertHTML_Headings: each heading level emits `#` repeated
// `level` times. Markdown standard is N hashes + space + content.
func TestConvertHTML_Headings(t *testing.T) {
	cases := map[string]string{
		"<h1>One</h1>":   "# One",
		"<h2>Two</h2>":   "## Two",
		"<h3>Three</h3>": "### Three",
		"<h4>Four</h4>":  "#### Four",
		"<h5>Five</h5>":  "##### Five",
		"<h6>Six</h6>":   "###### Six",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got := convert(in)
			if !strings.Contains(got, want) {
				t.Errorf("want %q in output; got %q", want, got)
			}
		})
	}
}

// TestConvertHTML_Paragraphs: <p> blocks separated by blank lines.
func TestConvertHTML_Paragraphs(t *testing.T) {
	got := convert("<p>first</p><p>second</p>")
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("paragraph content lost: %q", got)
	}
	// Markdown paragraphs separated by blank line.
	if !strings.Contains(got, "first\n\nsecond") {
		t.Errorf("paragraph break missing: %q", got)
	}
}

// TestConvertHTML_Emphasis: bold and italic emit ** and * pairs.
func TestConvertHTML_Emphasis(t *testing.T) {
	got := convert("<p>this is <strong>bold</strong> and <em>italic</em></p>")
	if !strings.Contains(got, "**bold**") {
		t.Errorf("strong should produce **bold**: %q", got)
	}
	if !strings.Contains(got, "*italic*") {
		t.Errorf("em should produce *italic*: %q", got)
	}
}

// TestConvertHTML_InlineCode: <code> outside <pre> renders as backticks.
func TestConvertHTML_InlineCode(t *testing.T) {
	got := convert("<p>use <code>fmt.Println</code> here</p>")
	if !strings.Contains(got, "`fmt.Println`") {
		t.Errorf("inline code should be backtick-wrapped: %q", got)
	}
}

// TestConvertHTML_CodeBlockWithLang: <pre><code class="language-go">
// renders as fenced block with language hint.
func TestConvertHTML_CodeBlockWithLang(t *testing.T) {
	got := convert(`<pre><code class="language-go">fmt.Println("hi")</code></pre>`)
	if !strings.Contains(got, "```go") {
		t.Errorf("expected ```go fence; got %q", got)
	}
	if !strings.Contains(got, `fmt.Println("hi")`) {
		t.Errorf("code body lost: %q", got)
	}
	if !strings.Contains(got, "```\n") {
		t.Errorf("expected closing fence; got %q", got)
	}
}

// TestConvertHTML_CodeBlockNoLang: <pre><code> without language hint
// produces a bare fence — markdown renderers default to no syntax
// highlighting, which is the right behavior.
func TestConvertHTML_CodeBlockNoLang(t *testing.T) {
	got := convert(`<pre><code>raw block</code></pre>`)
	if !strings.Contains(got, "```\nraw block\n```") {
		t.Errorf("bare fence wrong: %q", got)
	}
}

// TestConvertHTML_UnorderedList: <ul><li> emits dash-prefixed items.
func TestConvertHTML_UnorderedList(t *testing.T) {
	got := convert("<ul><li>alpha</li><li>beta</li><li>gamma</li></ul>")
	for _, want := range []string{"- alpha", "- beta", "- gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
}

// TestConvertHTML_OrderedList: <ol><li> emits `1.` (markdown auto-numbers).
func TestConvertHTML_OrderedList(t *testing.T) {
	got := convert("<ol><li>first</li><li>second</li></ol>")
	if !strings.Contains(got, "1. first") {
		t.Errorf("missing '1. first': %q", got)
	}
	// All ordered items use `1.` per markdown convention.
	if !strings.Contains(got, "1. second") {
		t.Errorf("missing '1. second': %q", got)
	}
}

// TestConvertHTML_LinkWithText: <a href> wraps text in [text](href).
func TestConvertHTML_LinkWithText(t *testing.T) {
	got := convert(`<p>see <a href="https://example.com">the docs</a> for more</p>`)
	if !strings.Contains(got, "[the docs](https://example.com)") {
		t.Errorf("link wrong: %q", got)
	}
}

// TestConvertHTML_LinkWithoutHref: <a> with no href degrades to just
// the text content. Markdown links require a URL; we don't fabricate one.
func TestConvertHTML_LinkWithoutHref(t *testing.T) {
	got := convert(`<p>see <a>the docs</a> for more</p>`)
	if !strings.Contains(got, "the docs") {
		t.Errorf("anchor text lost: %q", got)
	}
	// Empty href: the markdown is `[the docs]()` which is still valid.
	// We don't check for a specific shape — just that text survives.
}

// TestConvertHTML_Image: <img alt src> emits ![alt](src).
func TestConvertHTML_Image(t *testing.T) {
	got := convert(`<p>look: <img alt="diagram" src="/img/d.png"> here</p>`)
	if !strings.Contains(got, "![diagram](/img/d.png)") {
		t.Errorf("image wrong: %q", got)
	}
}

// TestConvertHTML_BoilerplateDropped: script/style/nav/footer/aside/head
// content must not appear in output.
func TestConvertHTML_BoilerplateDropped(t *testing.T) {
	in := `<head><title>Page</title><script>x()</script><style>body{}</style></head>
<body>
<nav><a href="/">home</a></nav>
<main><p>real content</p></main>
<aside>related</aside>
<footer>(c) 2024</footer>
</body>`
	got := convert(in)
	for _, banned := range []string{"x()", "body{}", "(c) 2024", "related", "<title>"} {
		if strings.Contains(got, banned) {
			t.Errorf("boilerplate %q leaked: %q", banned, got)
		}
	}
	if !strings.Contains(got, "real content") {
		t.Errorf("main content lost: %q", got)
	}
}

// TestConvertHTML_NotHTMLPassesThrough: input that doesn't match the
// detection prefix returns unchanged.
func TestConvertHTML_NotHTMLPassesThrough(t *testing.T) {
	cases := []string{
		"plain text",
		"<div>div without doctype</div>",
		"",
		"{\"json\":true}",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := ConvertHTML(in); got != in {
				t.Errorf("non-HTML must pass through; got %q", got)
			}
		})
	}
}

// TestConvertHTML_MalformedRecovers: mismatched tags must not panic
// and must produce some output.
func TestConvertHTML_MalformedRecovers(t *testing.T) {
	in := `<!doctype html>
<p>start <strong>bold <em>italic</strong> mismatched</em> end</p>`
	got := ConvertHTML(in)
	if got == "" {
		t.Fatal("malformed input produced empty output")
	}
	// Just verify it didn't panic and key text survives.
	for _, want := range []string{"start", "bold", "italic", "end"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
}

// TestConvertHTML_HardBreak: <br> inside text becomes a markdown hard break.
func TestConvertHTML_HardBreak(t *testing.T) {
	got := convert("<p>line one<br>line two</p>")
	if !strings.Contains(got, "line one  \nline two") {
		t.Errorf("br should produce '  \\n' hard break: %q", got)
	}
}

// TestConvertHTML_HorizontalRule: <hr> renders as `---`.
func TestConvertHTML_HorizontalRule(t *testing.T) {
	got := convert("<p>before</p><hr><p>after</p>")
	if !strings.Contains(got, "---") {
		t.Errorf("hr should produce '---': %q", got)
	}
}

// TestConvertHTML_Entities: HTML entities in text and attributes are
// decoded before the markdown form is built.
func TestConvertHTML_Entities(t *testing.T) {
	got := convert(`<p>copyright &copy; 2024 &amp; later</p>`)
	if !strings.Contains(got, "copyright © 2024 & later") {
		t.Errorf("entities not decoded: %q", got)
	}
}

// TestConvertHTML_NestedLists: nested lists must produce a markdown
// shape that round-trips through a CommonMark renderer. We don't pin
// exact indentation — just verify both levels emit list items.
func TestConvertHTML_NestedLists(t *testing.T) {
	got := convert(`<ul><li>outer one<ul><li>inner a</li><li>inner b</li></ul></li><li>outer two</li></ul>`)
	for _, want := range []string{"outer one", "outer two", "inner a", "inner b"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
}

// TestConvertHTML_CapRegression: a body that's already minimal (e.g.
// just `<html><body>x</body></html>`) — make sure our cap-regression
// guard doesn't accidentally fire on legitimate small input. The
// fallback to CompactHTML is acceptable; what we test is that some
// output is produced.
func TestConvertHTML_CapRegression(t *testing.T) {
	in := `<!doctype html><html><body><p>x</p></body></html>`
	got := ConvertHTML(in)
	if got == "" {
		t.Fatal("empty output")
	}
	// Either the walker produced markdown OR CompactHTML returned the
	// stripped version. Both are valid; just verify text survives.
	if !strings.Contains(got, "x") {
		t.Errorf("text lost: %q", got)
	}
}

// TestConvertHTML_SkipNestedDrop: a drop-set element containing nested
// drop-set elements must drop everything — depth counter handles the
// nesting correctly.
func TestConvertHTML_SkipNestedDrop(t *testing.T) {
	in := `<!doctype html><body><nav><ul><script>x()</script><li>menu</li></ul></nav><p>kept</p></body>`
	got := ConvertHTML(in)
	if strings.Contains(got, "menu") {
		t.Errorf("nested drop content leaked: %q", got)
	}
	if !strings.Contains(got, "kept") {
		t.Errorf("non-drop content lost: %q", got)
	}
}

// TestConvertHTML_TableEmitsGFMSeparator: GFM tables need a
// `| --- | --- |` separator after the first row. Without it
// CommonMark renderers fall back to plain text and agents lose the
// table structure entirely.
func TestConvertHTML_TableEmitsGFMSeparator(t *testing.T) {
	in := `<table>
<thead><tr><th>Code</th><th>Meaning</th></tr></thead>
<tbody>
<tr><td>E001</td><td>Bad input</td></tr>
<tr><td>E002</td><td>Duplicate key</td></tr>
</tbody>
</table>`
	out := convert(in)
	if !strings.Contains(out, "| --- | --- |") {
		t.Errorf("expected GFM separator '| --- | --- |' after first row; got: %s", out)
	}
	// Separator should appear exactly once per table.
	if strings.Count(out, "| --- | --- |") != 1 {
		t.Errorf("separator must appear exactly once per table; got %d", strings.Count(out, "| --- | --- |"))
	}
	// All rows should still emit.
	for _, want := range []string{"Code", "Meaning", "E001", "Bad input", "E002", "Duplicate key"} {
		if !strings.Contains(out, want) {
			t.Errorf("table content %q missing: %s", want, out)
		}
	}
}

// TestConvertHTML_TableMultipleSeparators: two distinct tables get
// independent separators (separator state must reset per table).
func TestConvertHTML_TableMultipleSeparators(t *testing.T) {
	in := `<table><tr><td>A</td><td>B</td></tr><tr><td>1</td><td>2</td></tr></table>
<p>between</p>
<table><tr><td>X</td><td>Y</td><td>Z</td></tr><tr><td>x</td><td>y</td><td>z</td></tr></table>`
	out := convert(in)
	// First table: 2 columns. Second table: 3 columns.
	if !strings.Contains(out, "| --- | --- |\n") {
		t.Errorf("first table separator (2-col) missing: %s", out)
	}
	if !strings.Contains(out, "| --- | --- | --- |\n") {
		t.Errorf("second table separator (3-col) missing: %s", out)
	}
}

// TestConvertHTML_DefinitionList: <dl><dt><dd> shape becomes
// `**term**: definition` in markdown. Common in API reference docs.
func TestConvertHTML_DefinitionList(t *testing.T) {
	in := `<dl>
<dt>strict</dt><dd>boolean, default true. Enables strict mode.</dd>
<dt>encoding</dt><dd>string, default utf-8.</dd>
</dl>`
	out := convert(in)
	if !strings.Contains(out, "**strict**:") {
		t.Errorf("definition term should render as **term**:; got %s", out)
	}
	if !strings.Contains(out, "Enables strict mode") {
		t.Errorf("definition body lost: %s", out)
	}
	if !strings.Contains(out, "**encoding**:") {
		t.Errorf("second term lost: %s", out)
	}
}

// TestConvertHTML_NormalizeOutputIntegration: the pipeline-level wiring
// — NormalizeOutput must call ConvertHTML.
func TestConvertHTML_NormalizeOutputIntegration(t *testing.T) {
	in := `<!doctype html><html><head><script>x()</script></head><body><h1>Title</h1><p>body</p></body></html>`
	out := NormalizeOutput(in)
	if strings.Contains(out, "x()") {
		t.Errorf("script content leaked: %q", out)
	}
	if !strings.Contains(out, "# Title") {
		t.Errorf("heading not converted to markdown: %q", out)
	}
}
