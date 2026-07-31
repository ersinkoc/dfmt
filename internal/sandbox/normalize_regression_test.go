package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// SBX-7: collapseCarriageReturns must run BEFORE normalizeLineEndings.
// normalizeLineEndings maps every standalone \r to \n when the body
// contains any CRLF — running it first would explode progress-bar output
// into dozens of lines before the collapse stage whose entire purpose is
// compressing progress bars gets to run.
func TestNormalizeOutput_CollapsesProgressBarBeforeLineEndingNormalization(t *testing.T) {
	in := "Downloading [###    ] 30%\rDownloading [#####  ] 60%\rDownloading [#######] 90%\r\nDone\n"
	out := NormalizeOutput(in)
	// The progress bar must collapse to its final state, not expand.
	if strings.Count(out, "Downloading") != 1 {
		t.Fatalf("progress bar not collapsed: %q", out)
	}
	if !strings.Contains(out, "90%") {
		t.Errorf("expected final progress state 90%%, got %q", out)
	}
	if !strings.Contains(out, "Done") {
		t.Errorf("expected trailing content preserved, got %q", out)
	}
}

// SBX-5: runLengthEncode must not corrupt structured bodies. Pretty-printed
// JSON arrays routinely contain repeated identical element lines
// (`{"value": 0},` x6); RLE rewriting them produces invalid JSON, and
// CompactStructured then declines because json.Valid fails — the agent gets
// corrupted JSON AND loses the compaction.
func TestNormalizeOutput_DoesNotRLECorruptPrettyJSON(t *testing.T) {
	in := `[
  {"value": 0},
  {"value": 0},
  {"value": 0},
  {"value": 0},
  {"value": 0},
  {"value": 0}
]`
	out := NormalizeOutput(in)
	if !json.Valid([]byte(out)) {
		t.Fatalf("NormalizeOutput produced invalid JSON: %q", out)
	}
	// The full array must be preserved (json.Valid already proves this, but
	// keep an explicit assertion so a future RLE regression reads clearly).
	if strings.Count(out, `"value"`) != 6 {
		t.Errorf("expected 6 array elements preserved, got %q", out)
	}
}

// SBX-6: CompactYAML must not fire on ordinary text that merely contains a
// `kind: ` substring. A `grep -rn "kind: "` result set or a log line
// `kind: warning` would previously be re-marshaled as YAML, dropping
// comments and re-sorting keys.
func TestCompactYAML_LogLineWithKindSubstringPassesThrough(t *testing.T) {
	cases := []string{
		"2024-03-15 10:00:00 kind: warning connection reset\n",
		"grep output: src/main.go:12 kind: this is code, not YAML\n",
		"error kind: unknown\n",
	}
	for _, in := range cases {
		if got := CompactYAML(in); got != in {
			t.Errorf("ordinary text containing 'kind:' must pass through; got %q", got)
		}
	}
}

// SBX-6 companion: the apiVersion/kind marker must be anchored at the START
// of the body, not anywhere in it. A markdown file with a horizontal rule
// (`---`) mid-document must not be treated as YAML either.
func TestCompactYAML_MidBodyMarkerDoesNotFire(t *testing.T) {
	in := "Some prose before\n\n---\n\nmore prose\n"
	if got := CompactYAML(in); got != in {
		t.Errorf("mid-body '---' must pass through; got %q", got)
	}
}

// SBX-4: NormalizeOutputFile must apply terminal hygiene but preserve the
// document's structure — a CRLF file's content must not be RLE/JSON/
// YAML-compacted, because Read's visible output is copied back into Edit.
func TestNormalizeOutputFile_PreservesStructure(t *testing.T) {
	in := "line1\r\nline1\r\nline1\r\nline1\r\n"
	out := NormalizeOutputFile(in)
	// The four duplicate lines must NOT be RLE-collapsed (that's the
	// structural compaction reserved for NormalizeOutput), but CRLF should
	// still be normalized to LF.
	if strings.Count(out, "line1") != 4 {
		t.Errorf("NormalizeOutputFile collapsed duplicate lines; got %q", out)
	}
	if strings.Contains(out, "\r\n") {
		t.Errorf("NormalizeOutputFile left CRLF; got %q", out)
	}
}

// SBX-4: Fetch must run the full normalization pipeline so HTML bodies are
// converted to markdown instead of arriving as raw div soup.
func TestNormalizeOutput_AppliesToFetchedHTML(t *testing.T) {
	in := "<!doctype html><html><body><h1>Hello</h1><p>World</p></body></html>"
	out := NormalizeOutput(in)
	if !strings.Contains(out, "Hello") {
		t.Fatalf("HTML heading lost: %q", out)
	}
	if strings.Contains(out, "<div") || strings.Contains(out, "<html") {
		t.Errorf("HTML boilerplate survived normalization: %q", out)
	}
}
