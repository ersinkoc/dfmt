# ADR-0008: Bundle a Minimal HTML Parser Rather Than Depend on `x/net/html`

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | ADR-0004 (stdlib-only deps) |

## Context

The sandbox layer's `dfmt.fetch` capability converts HTML responses to markdown before chunking and indexing. The conversion is nontrivial — it must handle headings, paragraphs, lists, links, code blocks, tables, and must strip script/style/nav content cleanly enough that indexed markdown represents the page's meaning, not its chrome.

An implementation question: should DFMT depend on `golang.org/x/net/html` (the canonical HTML tokenizer for Go, maintained by the Go team), or write the tokenizer in-house?

ADR-0004 permits `golang.org/x/sys`, `golang.org/x/crypto`, and `gopkg.in/yaml.v3`. Adding `x/net` would be a fourth exception. The question is whether `x/net/html` belongs in the same category as the three already-permitted packages, or whether we hold the line.

## Decision

**Bundle a minimal HTML tokenizer in `internal/sandbox/htmltok.go`. Do not take a dependency on `x/net/html`.**

The bundled tokenizer is ~350 lines of Go. It handles the subset of HTML needed for markdown conversion:

- Tag open, tag close, self-closing
- Text content with basic entity resolution (`&amp;`, `&lt;`, `&gt;`, `&quot;`, `&#NNN;`, named entities for the 50 most common cases)
- Attribute extraction (name + value)
- CDATA blocks (treated as opaque text)
- Comment skipping
- Malformed-HTML recovery (close tags that aren't explicit, unclosed tags at EOF)

It is not a full HTML5 parser. It does not construct a DOM tree. It produces a flat token stream that the markdown converter walks.

## Alternatives Considered

### A. Add `x/net/html` to the permitted dependency list

Implementation effort: zero. The `x/net/html` package is mature, thoroughly tested, and maintained by the Go team. It produces a full HTML5-compliant parse tree.

Rejected because:
- Adding `x/net` as a permitted prefix opens the door to a much larger surface than just HTML parsing. `x/net` also contains `http2`, `websocket`, `idna`, and others — the precedent is hard to bound.
- ADR-0004's rationale was that every exception costs. We have three permitted exceptions; a fourth requires the same bar as the original three, and HTML parsing does not clear it: our need is narrow (markdown conversion), the in-house effort is bounded (hundreds of lines), and we have no downstream need for the rest of `x/net`.
- `x/net/html` is larger than what we need. It constructs parse trees, handles foreign elements (SVG, MathML), and implements HTML5 error recovery rules that don't matter for markdown extraction.
- If we eventually need other `x/net` capabilities, we revisit then with a specific case, not by broadening the permitted set now.

### B. Use stdlib `html` package only

The stdlib `html` package provides entity escaping/unescaping but no tokenizer.

Rejected because insufficient on its own — we need tokenization. This option does not exist in practice.

### C. Regex-based extraction

Strip HTML tags with a regex, keep text content.

Rejected because:
- Loses all structure. No headings, no lists, no code blocks. The resulting markdown is a blob of text with no navigable structure, defeating the purpose of chunking by heading.
- Robustness problems: quotes inside attributes, script tags containing `<`, HTML comments — each a regex edge case. The classic "don't parse HTML with regex" problem.

### D. Shell out to `pandoc`

Invoke `pandoc --from html --to markdown` as a subprocess.

Rejected because:
- Introduces a runtime dependency (pandoc) on the user's machine. DFMT's distribution claim is "single static binary, no runtime deps beyond the OS kernel."
- Adds subprocess overhead per fetch.
- pandoc's HTML-to-markdown conversion has its own opinions about output style that are hard to control.

## Consequences

### Positive

- **Dependency policy holds.** Three permitted external dependencies stays three. ADR-0004's discipline is preserved.
- **Controlled scope.** We implement exactly what we need: tokenize, walk, emit markdown. No DOM tree, no foreign elements, no edge cases we won't use.
- **Smaller binary.** `x/net/html` brings ~400 KB of code; our subset fits in ~15 KB of source.
- **Full visibility of HTML handling.** Any markdown-conversion bug is in our code, debuggable with our test suite.
- **Reference material.** A few widely-studied implementations exist (Go's tokenizer predecessor code, Python's html.parser) — we can draw structure from them without taking a dependency.

### Negative

- **Implementation effort.** ~350 lines of tokenizer + ~200 lines of markdown walker + comprehensive test corpus. Estimated 2-3 days to build and test properly.
- **Less battle-tested.** `x/net/html` has handled millions of real-world HTML inputs. Our tokenizer will handle the inputs our test corpus exercises. Unknown edge cases exist.
- **Entity handling is limited.** We support ~50 named entities plus numeric references. Exotic entities render as literal text. This is acceptable because HTML fetched for AI-agent context does not typically contain exotic entities in content-bearing positions.
- **No HTML5 error recovery.** Severely malformed HTML may parse differently than a browser parses it. Acceptable for our use case: the content that matters is usually well-formed within broken pages, and we only need approximate extraction, not byte-perfect reconstruction.

## Implementation Notes

- Tokenizer lives in `internal/sandbox/htmltok.go`. Tests against a golden corpus of real-world HTML samples live in `internal/sandbox/htmltok_test.go`.
- Corpus includes: simple blog posts, documentation pages (e.g., Go stdlib docs, MDN), GitHub readme rendering, Stack Overflow question pages, Wikipedia article excerpts. Enough variety to exercise the tokenizer's main code paths.
- Entity map is generated once from the HTML5 named-character-reference list, filtered to ~50 entities by frequency analysis of the corpus. Hard-coded as a `map[string]string` in the source.
- Malformed recovery: when a closing tag does not match the open stack, we pop until match or empty. No panic, no error.
- Walker (in `htmlmd.go`) consumes tokens and emits markdown. Supported element coverage:
  - Headings `<h1>`–`<h6>` → `#` through `######`
  - Paragraphs `<p>` → blank-line-separated paragraphs
  - Line breaks `<br>` → newline
  - Bold `<b>`, `<strong>` → `**…**`
  - Italic `<i>`, `<em>` → `*…*`
  - Inline code `<code>` → `` `…` ``
  - Code blocks `<pre><code class="language-X">` → fenced block with language hint
  - Lists `<ul>`, `<ol>`, `<li>` → markdown lists
  - Links `<a href>` → `[text](url)`
  - Images `<img alt src>` → `![alt](src)`
  - Tables `<table><tr><td>` → GFM table syntax
  - Strip completely: `<script>`, `<style>`, `<nav>`, `<header>`, `<footer>`, `<form>`, `<button>`, `<aside>`, `<iframe>`, `<noscript>`
- Unknown/unhandled elements: pass through text content, drop tags.
- Performance target: parse + convert a 100 KB HTML page in <20 ms.

## Revisit

Revisit if:
- Users report significant loss of meaning in markdown conversions for real pages they rely on. Mitigation: grow the supported element set, or — if widespread — reconsider `x/net/html`.
- We gain another concrete need for something in `x/net` (http2 support, websocket client). At that point, adding `x/net` as a permitted prefix becomes a separate decision we evaluate on its own merits, and may retroactively justify switching HTML parsing over.
- The tokenizer accumulates enough bug reports that its maintenance cost exceeds what `x/net/html` would have cost. Unlikely given the narrow scope, but possible.

## Implementation Status (Updated 2026-04-28)

**Full path landed.** ADR-0008 is now fully implemented.

- **Tokenizer:** `internal/sandbox/htmltok.go` (~280 LOC). Eight-state machine: text, tag-open, tag-name, attr-name, attr-value, comment, raw-text (script/style), end-tag. Recovery is "best effort" — malformed input degrades silently, never panics. Entity decoding via stdlib `html.UnescapeString` (replaces the planned hard-coded 50-entity map; stdlib gives the full HTML5 set without breaking ADR-0004).
- **Markdown walker:** `internal/sandbox/htmlmd.go` (~330 LOC). Stack-based emitter with a "drop depth" counter for boilerplate elements (`script`, `style`, `nav`, `footer`, `aside`, `head`, `noscript`, `svg`, `form`, `button`, `iframe`). Recognises headings, paragraphs, emphasis, inline + fenced code (with language hint from `class="language-X"`), lists, links, images, blockquotes, hr, basic tables.
- **Pipeline gate:** `ConvertHTML` is detection-prefix-gated (same `<!doctype html>` / `<html>` check `CompactHTML` used). On cap regression — output ≥ input bytes — falls back to `CompactHTML`'s regex strip so wire bytes never inflate.
- **Lite path retained as fallback:** `CompactHTML` (regex) is no longer the primary path but stays in the codebase as the cap-regression fallback. Pages where the walker would inflate the body still get script/style/nav stripping.
- **Pipeline order:** `NormalizeOutput` now calls `ConvertHTML` after `CompactStructured` and after the ANSI/CR/RLE transforms. Order matters: structural transforms run first so the body arriving at the HTML detector hasn't been mangled.

**Bench delta** (`dfmt-bench tokensaving`, `fetched doc page (HTML boilerplate)` scenario, 5042 raw bytes): lite path 1438 modern bytes (72.2% savings); full tokenizer drops further — final number recorded in the implementation commit message.

**Test corpus:** `internal/sandbox/testdata/html-doc-page.html`, `html-blog-post.html`, `html-issue-page.html`. Real-world page shapes (documentation, blog post, GitHub issue page); each fixture asserts: walker doesn't panic, output strictly shrinks, content-bearing keywords survive, drop-set boilerplate is absent.

**Out of scope (deferred for a future ADR if a need surfaces):**
- Browser-grade HTML5 error recovery (foster parenting, anchored re-parsing).
- CSS-style `display:none` boilerplate detection.
- Streaming tokenizer for bodies > 8 MiB (current input cap is well below).
