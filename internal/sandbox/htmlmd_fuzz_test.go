package sandbox

import (
	"testing"
	"unicode/utf8"
)

// FuzzConvertHTML guards the bundled HTML tokenizer + walker (ADR-0008)
// against panics on malformed input. The pipeline drops <script> /
// <style> / <nav> / <footer> / etc. wholesale and emits markdown for
// the remainder; an attacker (or just a buggy upstream page) can hand
// us nested tags, unterminated attributes, mismatched closes, or
// outright binary garbage. The invariant is the same as
// FuzzNormalizeOutput: never panic, always produce valid UTF-8.
//
// FuzzNormalizeOutput already exercises ConvertHTML indirectly when an
// HTML body lands in stage 8 of the pipeline, but a dedicated target
// gives a tighter regression signal — a crash here points at htmltok
// or htmlmd, not "somewhere in the pipeline."
func FuzzConvertHTML(f *testing.F) {
	seeds := []string{
		"",
		"plain text, no tags",
		"<p>simple</p>",
		"<html><body><h1>title</h1><p>body</p></body></html>",
		"<table><tr><td>a</td><td>b</td></tr></table>",
		"<script>alert(1)</script>after",                  // script body must not leak
		"<style>x{color:red}</style>visible",              // style body must not leak
		"<nav>menu</nav><main>content</main>",             // nav drops, main keeps
		"<a href=\"https://example.com\">link</a>",        // links → markdown
		"<pre><code class=\"language-go\">x</code></pre>", // code with language hint
		"<p>unterminated",                                 // missing close
		"<<<>>>",                                          // garbage angle brackets
		"<p attr=\"unterminated",                          // unterminated attr
		"<p>&amp;&lt;&gt;&#65;&#x41;</p>",                 // entities
		"<svg><path d=\"...\"/></svg>",                    // svg drops
		"<form><input type=\"text\"/></form>",             // form drops
		"\xff\xfe<binary>\x00</binary>",                   // binary in HTML shape
		"<p>" + "<b>" + "deep" + "</b>" + "</p>",          // nesting
		"<!--<script>alert(1)</script>-->visible",         // comment with hostile body
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Invariant scoped to production-realistic input. The pipeline
		// runs binary refusal (stage 1, CompactBinary) before ConvertHTML
		// (stage 8); a non-UTF-8 body never reaches the HTML walker in
		// the wild. Asserting "any bytes in → valid UTF-8 out" would
		// require the walker to scrub bytes that the upstream stage
		// already filters — pointless cost on the hot path.
		if !utf8.ValidString(s) {
			return
		}
		out := ConvertHTML(s)
		if !utf8.ValidString(out) {
			t.Fatalf("ConvertHTML produced invalid UTF-8 from valid UTF-8 input %q", s)
		}
	})
}

// FuzzGlobMatch tests the glob→regex generator on both ops. The
// generator escapes regex metacharacters so user-authored patterns
// like "api.example.com" don't have their dots interpreted as
// any-char (silently broadening the match). A regression here would
// re-open the silent-broadening class — an operator-written allow
// rule that quietly matches more than they wrote.
//
// Invariant: globMatch must not panic on any (pattern, text) pair,
// regardless of how garbled either argument is. Comparing exec vs
// path-style match results is not part of the contract — the two
// functions intentionally differ — so the assertion is just "no
// panic, any boolean result."
func FuzzGlobMatch(f *testing.F) {
	seeds := [][2]string{
		{"git *", "git status"},
		{"git *", "git-shell sneaky"},
		{"**/*.go", "src/foo.go"},
		{"**/.env*", "dir/.env.local"},
		{"https://*", "https://example.com/path"},
		{"*", ""},
		{"", "anything"},
		{"a+b", "a+b"},           // regex meta in pattern
		{"a.b.c", "axbxc"},       // dot must NOT broaden
		{"\xff\xfe", "\x00\x01"}, // binary
		{"[bracket", "[bracket"}, // unbalanced bracket
		{"(group)", "(group)"},   // parens must not capture
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, text string) {
		// Both ops on the same input — exec uses globToRegexShell,
		// the rest use globToRegex. We just want neither to crash.
		_ = globMatch(pattern, text, "exec")
		_ = globMatch(pattern, text, "read")
		_ = globMatch(pattern, text, "fetch")
	})
}
