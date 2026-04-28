package sandbox

import (
	"strings"
	"testing"
)

// TestCompactBinary_DetectsPNG: PNG magic number triggers the refusal
// even on a tiny input. Captures the dominant fast-path.
func TestCompactBinary_DetectsPNG(t *testing.T) {
	body := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00\x01\x02", 100)
	out := CompactBinary(body)
	if !strings.HasPrefix(out, "(binary; type=image/png;") {
		t.Errorf("expected PNG-typed binary summary; got %q", out)
	}
	if !strings.Contains(out, "sha256=") {
		t.Errorf("hash missing from summary: %q", out)
	}
	// Output is much smaller than input.
	if len(out) >= len(body) {
		t.Errorf("summary should shrink the body; in=%d out=%d", len(body), len(out))
	}
}

// TestCompactBinary_DetectsPDF: %PDF- prefix.
func TestCompactBinary_DetectsPDF(t *testing.T) {
	body := "%PDF-1.7\n" + strings.Repeat("\x00binary garbage", 50)
	out := CompactBinary(body)
	if !strings.HasPrefix(out, "(binary; type=application/pdf;") {
		t.Errorf("expected PDF summary; got %q", out)
	}
}

// TestCompactBinary_DetectsGzip: gzip magic 1F 8B.
func TestCompactBinary_DetectsGzip(t *testing.T) {
	body := "\x1f\x8b\x08\x00" + strings.Repeat("\x00", 200)
	out := CompactBinary(body)
	if !strings.HasPrefix(out, "(binary; type=application/gzip;") {
		t.Errorf("expected gzip summary; got %q", out)
	}
}

// TestCompactBinary_FallsBackOnInvalidUTF8: bytes that don't match
// any magic number but contain invalid UTF-8 still get classified as
// binary with a generic octet-stream type.
func TestCompactBinary_FallsBackOnInvalidUTF8(t *testing.T) {
	// Continuation byte without a leader byte = invalid UTF-8.
	body := strings.Repeat("\x80\x81\x82", 100)
	out := CompactBinary(body)
	if !strings.Contains(out, "application/octet-stream") {
		t.Errorf("expected octet-stream fallback; got %q", out)
	}
}

// TestCompactBinary_NULBytesTriggerRefusal: NUL bytes in a body force
// the refusal even when the surrounding bytes are valid ASCII.
// Catches binary-with-text-prefix shapes (e.g. some legacy formats).
func TestCompactBinary_NULBytesTriggerRefusal(t *testing.T) {
	body := "version=1\nname=test\x00\x00\x00\xff\xfe binary tail"
	out := CompactBinary(body)
	if !strings.HasPrefix(out, "(binary;") {
		t.Errorf("NUL bytes should trigger refusal; got %q", out)
	}
}

// TestCompactBinary_TextPassesThrough: well-formed UTF-8 — including
// non-ASCII (Turkish, CJK) — must not be misclassified.
func TestCompactBinary_TextPassesThrough(t *testing.T) {
	cases := []string{
		"plain ASCII text",
		"Türkçe içerik: dosya yapılandırma",
		"中文文本",
		"{\"key\":\"value\"}",
		"<!doctype html><html><body>x</body></html>",
		"# Markdown heading\n\nbody",
		"",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := CompactBinary(in); got != in {
				t.Errorf("text input misclassified as binary; in=%q out=%q", in, got)
			}
		})
	}
}

// TestCompactBinary_NormalizeOutputIntegration: pipeline-level wiring
// — binary detection runs before all other transforms so they never
// see the corrupted byte stream.
func TestCompactBinary_NormalizeOutputIntegration(t *testing.T) {
	body := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 1000)
	out := NormalizeOutput(body)
	if !strings.HasPrefix(out, "(binary;") {
		t.Errorf("NormalizeOutput must invoke CompactBinary first: %q", out)
	}
	if len(out) > 200 {
		t.Errorf("binary summary should be short; got %d bytes", len(out))
	}
}

// TestCompactBinary_HashStability: running the same body twice
// produces the same summary. The sha256 prefix is a stable handle.
func TestCompactBinary_HashStability(t *testing.T) {
	body := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 500)
	a := CompactBinary(body)
	b := CompactBinary(body)
	if a != b {
		t.Errorf("CompactBinary not deterministic: %q vs %q", a, b)
	}
}
