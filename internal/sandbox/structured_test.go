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

// TestCompactStructured_NDJSONUnchanged: line-delimited JSON has multiple
// roots and isn't valid JSON as a single document; the function must
// no-op rather than try to parse the first line in isolation.
func TestCompactStructured_NDJSONUnchanged(t *testing.T) {
	in := `{"k":1,"created_at":"a"}
{"k":2,"created_at":"b"}`
	if got := CompactStructured(in); got != in {
		t.Errorf("NDJSON must pass through unchanged; got %q", got)
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
