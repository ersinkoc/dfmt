package sandbox

import (
	"encoding/json"
	"regexp"
	"strings"
)

// structuredNoiseFields names keys whose values are nearly always wire bloat
// for an LLM consumer: timestamps, ETags, hypermedia links, opaque IDs. The
// list is conservative — keys here must be (a) common across cloud-CLI JSON
// outputs (gh, kubectl, aws, docker) and (b) rarely needed for the kind of
// reasoning an agent does over the response. See ADR-0010 for the
// alternatives considered (per-shape projections, schema-driven compaction)
// and why a flat blocklist won.
//
// The `id` key is intentionally absent — numeric IDs are sometimes the only
// stable handle for an object, and silently dropping them changes the
// semantics of the response. A future env knob could opt in.
var structuredNoiseFields = map[string]struct{}{
	"created_at": {},
	"updated_at": {},
	"etag":       {},
	"_links":     {},
	"node_id":    {},
	"url":        {},
	"html_url":   {},
}

// structuredNoiseSuffix is matched against any key not already in the field
// set. Cloud REST APIs sprinkle dozens of `*_url` fields per object
// (events_url, labels_url, comments_url, repository_url, ...) — enumerating
// them by hand would miss the next one Github adds. Suffix matching catches
// the family.
const structuredNoiseSuffix = "_url"

// htmlBoilerplateBlocks matches HTML elements whose contents are nearly
// always wire bloat for an LLM consumer reading documentation pages: inline
// scripts/styles, HTML comments, nav, footer, aside, and the entire head.
// The (?is) flags make `.` match newlines and the match case-insensitive
// so `<SCRIPT>` and `<Footer>` are caught alongside the usual lowercase.
// Non-greedy bodies (`.*?`) prevent runaway matches when a page has multiple
// <script> blocks. The list is conservative: <header> and <main> stay in
// because some sites put the actual content in <header>/<main>; the cost
// of a false positive on a content-bearing element is much higher than
// the cost of leaving boilerplate that occupies <body> directly.
//
// This is the ADR-0008 "lite" path. A full tokenizer-driven HTML→markdown
// converter is still on the roadmap; until it lands, regex strip is the
// pragmatic 80-percent solution. See ADR-0008's implementation note.
var htmlBoilerplateBlocks = regexp.MustCompile(
	`(?is)<script[^>]*>.*?</script>|` +
		`<style[^>]*>.*?</style>|` +
		`<!--.*?-->|` +
		`<nav[^>]*>.*?</nav>|` +
		`<footer[^>]*>.*?</footer>|` +
		`<aside[^>]*>.*?</aside>|` +
		`<head[^>]*>.*?</head>|` +
		`<noscript[^>]*>.*?</noscript>|` +
		`<svg[^>]*>.*?</svg>`,
)

// htmlDetectPrefix matches a leading `<!doctype html` or `<html` (case-
// insensitive). We require an HTML-shaped prefix rather than a body scan
// because random text containing a `<script>` literal (e.g. a code review
// comment) shouldn't trigger the strip.
var htmlDetectPrefix = regexp.MustCompile(`(?is)^\s*(?:<!doctype\s+html|<html\b)`)

// CompactHTML removes script/style/comment/nav/footer/aside/head/noscript/svg
// blocks from HTML-shaped input. Detection is prefix-based — a body must
// start with a doctype or `<html>` tag — so plain text containing the word
// "<script>" stays untouched. Returns input unchanged when:
//   - input is not HTML-shaped,
//   - the stripped form is not strictly smaller than the input (cap
//     regression guard, same contract as CompactStructured).
func CompactHTML(s string) string {
	if s == "" {
		return s
	}
	if !htmlDetectPrefix.MatchString(s) {
		return s
	}
	out := htmlBoilerplateBlocks.ReplaceAllString(s, "")
	if len(out) >= len(s) {
		return s
	}
	return out
}

// CompactStructured detects JSON-shaped input and removes noise fields
// recursively. Two shapes are handled:
//
//  1. Single-document JSON — the body parses as one valid JSON value
//     starting with `{` or `[`. Walked once, noise fields dropped, re-
//     marshaled compact.
//  2. NDJSON — newline-delimited JSON, one document per line. Common
//     output of `jq -c '.items[]'` and `kubectl get … -o json | jq -c …`
//     pipelines. Each line is compacted independently; lines that aren't
//     JSON pass through unchanged so partial-NDJSON (one log line in the
//     middle) doesn't blow up the whole transform.
//
// Returns input unchanged when:
//   - input is empty or whitespace-only,
//   - input is neither single-document JSON nor multi-line NDJSON,
//   - the compacted form is not strictly smaller than the input (cap
//     regression guard — pathological cases must not increase wire bytes).
//
// The function is pure (no I/O, no mutex) and safe for concurrent use.
func CompactStructured(s string) string {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if trimmed == "" {
		return s
	}
	first := trimmed[0]
	if first != '{' && first != '[' {
		return s
	}
	// Try single-document first — cheaper than walking the body for newlines.
	if json.Valid([]byte(trimmed)) {
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			out, err := json.Marshal(walkDropNoise(v))
			if err == nil && len(out) < len(s) {
				return string(out)
			}
		}
		return s
	}
	// Single-document parse failed. Try NDJSON: each non-blank line must
	// be valid JSON on its own. We decide eagerly — the first non-JSON
	// non-blank line aborts and the original is returned, so a stray log
	// line embedded in a JSON stream doesn't get reformatted.
	if !looksLikeNDJSON(trimmed) {
		return s
	}
	out := compactNDJSON(trimmed)
	if len(out) >= len(s) {
		return s
	}
	return out
}

// looksLikeNDJSON returns true when s contains at least two non-blank lines
// and every non-blank line is valid JSON. The two-line minimum prevents the
// degenerate single-line case (already handled above) from re-entering this
// path. It also blocks the false-positive where one valid JSON line is
// surrounded by blank lines — that's a single-doc shape someone added
// whitespace to.
func looksLikeNDJSON(s string) bool {
	lines := strings.Split(s, "\n")
	nonBlank := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if !json.Valid([]byte(ln)) {
			return false
		}
		nonBlank++
		if nonBlank >= 2 {
			// Don't keep validating once the threshold is met — the first
			// two valid lines are enough to commit to the NDJSON path. If
			// a later line is invalid, compactNDJSON itself returns early.
			return true
		}
	}
	return false
}

// compactNDJSON walks the input line-by-line, compacting each JSON line in
// place and preserving blank lines (some pipelines deliberately separate
// records with blank lines for readability). If any non-blank line fails
// to parse mid-stream — which looksLikeNDJSON's two-line check can't
// catch — we abort and return the original; better to ship the body as-is
// than to ship a half-rewritten mess.
func compactNDJSON(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		stripped := strings.TrimSpace(ln)
		if stripped == "" {
			continue
		}
		if !json.Valid([]byte(stripped)) {
			return s
		}
		var v any
		if err := json.Unmarshal([]byte(stripped), &v); err != nil {
			return s
		}
		out, err := json.Marshal(walkDropNoise(v))
		if err != nil {
			return s
		}
		// Only adopt the compacted form if it's smaller — line-level
		// monotonicity is the same contract single-doc CompactStructured
		// upholds.
		if len(out) < len(stripped) {
			lines[i] = string(out)
		}
	}
	return strings.Join(lines, "\n")
}

// walkDropNoise recurses into v, dropping keys named in structuredNoiseFields
// or matching structuredNoiseSuffix. Scalars and nil pass through unchanged.
// Arrays preserve order. Map iteration order is non-deterministic but we
// re-marshal via json.Marshal which sorts keys alphabetically — so the
// output is stable regardless of input map ordering.
func walkDropNoise(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if _, drop := structuredNoiseFields[k]; drop {
				continue
			}
			if strings.HasSuffix(k, structuredNoiseSuffix) {
				continue
			}
			out[k] = walkDropNoise(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = walkDropNoise(val)
		}
		return out
	default:
		return v
	}
}
