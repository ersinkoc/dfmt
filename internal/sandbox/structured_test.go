package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCompactStructured_DropsKnownNoise: the canonical happy path. A
// gh-api-shape issue object loses created_at/updated_at/url/html_url/_links
// and any *_url field, but keeps the bytes the agent would actually use to
// reason about the issue (number, title, state, body).
func TestCompactStructured_DropsKnownNoise(t *testing.T) {
	in := `{
  "id": 12345,
  "number": 42,
  "node_id": "MDU6SXNzdWUx",
  "url": "https://api.github.com/repos/foo/bar/issues/42",
  "html_url": "https://github.com/foo/bar/issues/42",
  "events_url": "https://api.github.com/repos/foo/bar/issues/42/events",
  "labels_url": "https://api.github.com/repos/foo/bar/issues/42/labels{/name}",
  "comments_url": "https://api.github.com/repos/foo/bar/issues/42/comments",
  "title": "Bug in parser",
  "state": "open",
  "body": "The parser crashes on empty input.",
  "created_at": "2024-01-15T10:00:00Z",
  "updated_at": "2024-02-20T14:30:00Z",
  "etag": "W/\"abc123\"",
  "_links": {"self": {"href": "..."}}
}`
	out := CompactStructured(in)
	if out == in {
		t.Fatal("expected compaction, got input unchanged")
	}
	for _, banned := range []string{"created_at", "updated_at", "node_id", "etag", "_links",
		"html_url", "events_url", "labels_url", "comments_url"} {
		if strings.Contains(out, banned) {
			t.Errorf("noise field %q leaked into compacted output: %s", banned, out)
		}
	}
	for _, want := range []string{"\"title\":", "\"number\":", "\"state\":", "\"body\":"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected payload field %q missing: %s", want, out)
		}
	}
	// id is intentionally retained (see ADR-0010).
	if !strings.Contains(out, "\"id\":12345") {
		t.Errorf("numeric id should be retained: %s", out)
	}
	// Output must still be valid JSON.
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Errorf("compacted output is not valid JSON: %v\n%s", err, out)
	}
}

// TestCompactStructured_RecursesIntoArrays: array of issue objects, each
// element gets compacted; array order is preserved.
func TestCompactStructured_RecursesIntoArrays(t *testing.T) {
	in := `[
  {"number": 1, "title": "first", "url": "x", "created_at": "t1"},
  {"number": 2, "title": "second", "url": "y", "created_at": "t2"}
]`
	out := CompactStructured(in)
	if strings.Contains(out, "created_at") || strings.Contains(out, "\"url\":") {
		t.Errorf("array element compaction failed: %s", out)
	}
	// Order check: "first" must precede "second" in the byte stream.
	i := strings.Index(out, "first")
	j := strings.Index(out, "second")
	if i < 0 || j < 0 || i > j {
		t.Errorf("array order not preserved: %s", out)
	}
}

// TestCompactStructured_NotJSON: plain text returns unchanged. Without this
// guard, log lines like "{some text}" would be silently mangled by an
// over-eager parser pass.
func TestCompactStructured_NotJSON(t *testing.T) {
	for _, in := range []string{
		"this is plain text\n",
		"{not actually json",
		"[also not\nvalid",
		"=== RUN TestFoo\n--- PASS: TestFoo (0.00s)\n",
		"",
	} {
		if got := CompactStructured(in); got != in {
			t.Errorf("non-JSON %q must return unchanged; got %q", in, got)
		}
	}
}

// TestCompactStructured_NDJSON_LinesGetCompacted: ADR-0010's deferred
// NDJSON support, now active. Each line is compacted independently —
// noise fields gone from each record, line boundaries preserved.
func TestCompactStructured_NDJSON_LinesGetCompacted(t *testing.T) {
	in := `{"k":1,"title":"first","created_at":"2024-01-01T00:00:00Z","url":"https://x"}
{"k":2,"title":"second","created_at":"2024-02-02T00:00:00Z","html_url":"https://y"}
{"k":3,"title":"third","node_id":"abc","etag":"W/\"v1\""}`
	out := CompactStructured(in)
	if out == in {
		t.Fatal("NDJSON should be compacted; got input unchanged")
	}
	for _, banned := range []string{"created_at", "node_id", "etag", "html_url", "\"url\":"} {
		if strings.Contains(out, banned) {
			t.Errorf("noise field %q leaked: %s", banned, out)
		}
	}
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(out, want) {
			t.Errorf("payload term %q lost: %s", want, out)
		}
	}
	// Must still be NDJSON-shaped (3 lines).
	if got := strings.Count(out, "\n"); got != 2 {
		t.Errorf("expected 2 newlines (3 lines); got %d in %q", got, out)
	}
}

// TestCompactStructured_NDJSON_PartialAborts: a single non-JSON line in
// the middle of an otherwise-NDJSON body forces the whole transform to
// no-op. Mangling pipelines that emit log lines between JSON records
// would be worse than shipping the original bytes.
func TestCompactStructured_NDJSON_PartialAborts(t *testing.T) {
	in := `{"k":1,"created_at":"a"}
this is a log line, not JSON
{"k":2,"created_at":"b"}`
	if got := CompactStructured(in); got != in {
		t.Errorf("partial NDJSON must pass through unchanged; got %q", got)
	}
}

// TestCompactStructured_NDJSON_PreservesBlankLines: some pipelines emit
// blank-line-separated records for readability. Blank lines must survive
// and not be parsed as JSON.
func TestCompactStructured_NDJSON_PreservesBlankLines(t *testing.T) {
	in := `{"k":1,"created_at":"a"}

{"k":2,"created_at":"b"}`
	out := CompactStructured(in)
	if out == in {
		t.Fatal("NDJSON with blank-line separator should still compact records")
	}
	if !strings.Contains(out, "\n\n") {
		t.Errorf("blank line separator lost: %q", out)
	}
}

// TestCompactStructured_SingleLineNDJSON_FallsThrough: one JSON object on
// one line is the single-doc path, not NDJSON. Make sure the NDJSON
// detection's two-line minimum doesn't pull it in by accident.
func TestCompactStructured_SingleLineNDJSON_FallsThrough(t *testing.T) {
	in := `{"k":1,"title":"x","created_at":"a"}`
	out := CompactStructured(in)
	if out == in {
		t.Errorf("single-doc JSON should still be compacted via the single-doc path")
	}
	if strings.Contains(out, "created_at") {
		t.Errorf("noise field leaked: %s", out)
	}
}

// TestCompactStructured_NoRegressionOnOnlyDropList: pathological input —
// JSON that contains only fields we'd drop. The compacted form ("{}") is
// shorter than the input but only by a tiny amount, and the input may
// already have meaningful whitespace structure. Specifically, when the
// compacted form is NOT strictly smaller than the input, we return the
// original — protects the contract that NormalizeOutput never inflates.
func TestCompactStructured_NoRegressionOnOnlyDropList(t *testing.T) {
	in := `{"created_at":"a"}` // 18 bytes; output "{}" = 2 bytes, IS smaller.
	out := CompactStructured(in)
	if out != "{}" {
		t.Errorf("expected {}, got %q", out)
	}
	// And the genuinely pathological case — input is exactly "{}" (2 bytes).
	// Output would also be "{}" (not strictly smaller), so we return input.
	in2 := "{}"
	if got := CompactStructured(in2); got != in2 {
		t.Errorf("compaction should no-op when output isn't smaller; got %q", got)
	}
}

// TestCompactStructured_NormalizeOutputIntegration verifies the wiring at
// the pipeline level: NormalizeOutput on a JSON body must apply
// CompactStructured. Without this, a future refactor that re-orders the
// pipeline could silently disable structured compaction.
func TestCompactStructured_NormalizeOutputIntegration(t *testing.T) {
	in := `{"title":"x","created_at":"2024-01-01T00:00:00Z","url":"https://example.com"}`
	out := NormalizeOutput(in)
	if strings.Contains(out, "created_at") || strings.Contains(out, "\"url\":") {
		t.Errorf("NormalizeOutput must invoke CompactStructured: %q", out)
	}
}

// TestCompactStructured_DropsEmptyValues: null / "" / [] / {} carry no
// information for an LLM consumer. Each drop saves ~15-30 bytes per
// occurrence and the wire shape stays valid JSON.
func TestCompactStructured_DropsEmptyValues(t *testing.T) {
	in := `{"title":"x","description":null,"labels":[],"metadata":{},"count":0,"flag":false,"score":3.14,"items":[1,2,3]}`
	out := CompactStructured(in)
	if out == in {
		t.Fatal("expected compaction; input unchanged")
	}
	for _, banned := range []string{"description", "labels", "metadata"} {
		if strings.Contains(out, banned) {
			t.Errorf("empty-value field %q leaked: %s", banned, out)
		}
	}
	// Numeric 0, boolean false, populated arrays/objects must survive.
	for _, want := range []string{"\"count\":0", "\"flag\":false", "\"score\":3.14", "[1,2,3]"} {
		if !strings.Contains(out, want) {
			t.Errorf("non-empty value %q lost: %s", want, out)
		}
	}
}

// TestCompactStructured_DropsPagination: list-style responses include a
// pagination block per page. Agents reasoning about items rarely need
// the cursor; drop them.
func TestCompactStructured_DropsPagination(t *testing.T) {
	in := `{"items":[{"name":"a"},{"name":"b"}],"pagination":{"next":"abc","prev":null,"total":50},"next_token":"xyz","total_count":50,"has_more":true}`
	out := CompactStructured(in)
	for _, banned := range []string{"pagination", "next_token", "total_count", "has_more"} {
		if strings.Contains(out, banned) {
			t.Errorf("pagination field %q leaked: %s", banned, out)
		}
	}
	if !strings.Contains(out, "\"name\":\"a\"") {
		t.Errorf("items lost: %s", out)
	}
}

// TestCompactStructured_DropIDEnvKnob: with the env var set, numeric IDs
// are also dropped. Off by default to preserve object identity.
func TestCompactStructured_DropIDEnvKnob(t *testing.T) {
	in := `{"id":12345,"title":"x","name":"y"}`

	// Default: id stays.
	t.Setenv(structuredDropIDEnv, "")
	out := CompactStructured(in)
	if !strings.Contains(out, "\"id\":12345") {
		t.Errorf("id should be retained by default; got %s", out)
	}

	// Opt-in: id is dropped.
	t.Setenv(structuredDropIDEnv, "1")
	out = CompactStructured(in)
	if strings.Contains(out, "\"id\":") {
		t.Errorf("DFMT_STRUCTURED_DROP_ID=1 should drop id; got %s", out)
	}
	if !strings.Contains(out, "\"title\":\"x\"") {
		t.Errorf("non-id fields must survive: %s", out)
	}
}

// TestCompactStructured_PreservesArrayPositions: nulls inside an array
// keep their positions — index-based consumers would break otherwise.
// Only top-level *keys* whose value is empty get dropped; array
// elements are positional.
func TestCompactStructured_PreservesArrayPositions(t *testing.T) {
	in := `{"vals":[1,null,2,"",3]}`
	out := CompactStructured(in)
	// JSON marshal of empty string in array stays `""`. Verify length.
	if !strings.Contains(out, "[1,null,2,\"\",3]") {
		t.Errorf("array positions altered: %s", out)
	}
}

// TestCompactStructured_LeadingWhitespace: real-world JSON often arrives
// with a leading newline (e.g. shell here-docs, indented `gh api` output).
// The detection must skip whitespace before the brace check.
func TestCompactStructured_LeadingWhitespace(t *testing.T) {
	in := "  \n\t{\"title\":\"x\",\"created_at\":\"t\"}"
	out := CompactStructured(in)
	if strings.Contains(out, "created_at") {
		t.Errorf("leading whitespace must not block detection: %q", out)
	}
	if !strings.Contains(out, "title") {
		t.Errorf("title must survive: %q", out)
	}
}

// TestCompactHTML_StripsBoilerplate: the canonical doc-page shape — most
// of the wire is script/style/nav/footer. The stripped form keeps the
// actual content (article text, headings) and discards the chrome.
func TestCompactHTML_StripsBoilerplate(t *testing.T) {
	in := `<!DOCTYPE html>
<html>
<head><title>Doc page</title>
<script>window.gtag=function(){};</script>
<style>body{margin:0}</style>
</head>
<body>
<nav><ul><li><a href="/">Home</a></li><li><a href="/x">X</a></li></ul></nav>
<main><h1>Real content</h1><p>The actual answer is here.</p></main>
<aside><div>Related links sidebar</div></aside>
<footer>Copyright 2024</footer>
<script>analytics.track('page')</script>
</body>
</html>`
	out := CompactHTML(in)
	if out == in {
		t.Fatal("expected HTML to be compacted")
	}
	for _, banned := range []string{
		"window.gtag", "body{margin", "Related links sidebar",
		"Copyright 2024", "analytics.track", "<title>",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("boilerplate %q leaked: %s", banned, out)
		}
	}
	for _, want := range []string{"Real content", "actual answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("content %q lost: %s", want, out)
		}
	}
}

// TestCompactHTML_NotHTMLPassesThrough: plain text containing the word
// "<script>" — e.g. a code review comment or a Stack Overflow answer
// quoted as plain text — must not be eaten by the regex.
func TestCompactHTML_NotHTMLPassesThrough(t *testing.T) {
	cases := []string{
		"plain text with <script>console.log()</script> as a literal",
		"# Markdown heading\n\n```html\n<script>x</script>\n```\n",
		"",
		"{\"key\":\"value\"}",
		"<div>div without doctype is not detected as HTML</div>",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := CompactHTML(in); got != in {
				t.Errorf("non-HTML input must pass through; got %q", got)
			}
		})
	}
}

// TestCompactHTML_CaseInsensitive: matches uppercase tags too. Some
// generated/legacy HTML still uses <SCRIPT> or <Style>; stripping must
// be case-blind.
func TestCompactHTML_CaseInsensitive(t *testing.T) {
	in := `<!DOCTYPE HTML><html><HEAD><STYLE>body{}</STYLE></HEAD><body>kept</body></html>`
	out := CompactHTML(in)
	for _, banned := range []string{"STYLE", "<HEAD>"} {
		if strings.Contains(out, banned) {
			t.Errorf("case-insensitive strip missed %q: %s", banned, out)
		}
	}
	if !strings.Contains(out, "kept") {
		t.Errorf("body content lost: %s", out)
	}
}

// TestCompactHTML_DoctypeOnlyMustWork: real pages often use `<!doctype html>`
// (lowercase) without the `<html>` tag immediately after. Detection must
// hit on doctype alone.
func TestCompactHTML_DoctypeOnlyMustWork(t *testing.T) {
	in := `<!doctype html>
<head><script>x()</script></head>
<body>content</body>`
	out := CompactHTML(in)
	if strings.Contains(out, "x()") {
		t.Errorf("script not stripped under doctype-only detection: %s", out)
	}
}

// TestCompactHTML_NormalizeOutputIntegration: pipeline-level wiring check.
// Without this, a future refactor that re-orders NormalizeOutput could
// silently disable HTML compaction.
func TestCompactHTML_NormalizeOutputIntegration(t *testing.T) {
	in := `<!doctype html><html><head><style>x{}</style></head><body><p>kept</p></body></html>`
	out := NormalizeOutput(in)
	if strings.Contains(out, "x{}") {
		t.Errorf("NormalizeOutput must invoke CompactHTML: %s", out)
	}
}
