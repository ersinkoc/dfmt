package sandbox

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// encodeUTF16LE is the test-side inverse of decodeUTF16LE: it produces the
// bytes a Windows console would hand us for a string, surrogate pairs and all.
func encodeUTF16LE(s string) []byte {
	var out []byte
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			hi := 0xD800 + (r >> 10)
			lo := 0xDC00 + (r & 0x3FF)
			out = append(out, byte(hi), byte(hi>>8), byte(lo), byte(lo>>8))
			continue
		}
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// Regression: each 16-bit unit was encoded independently, so a surrogate pair
// became two 3-byte sequences (CESU-8). encoding/json then replaced them with
// U+FFFD and the agent saw replacement characters where the terminal had a
// glyph.
func TestDecodeUTF16LEHandlesSurrogatePairs(t *testing.T) {
	cases := []string{"plain ascii",
		"Türkçe karakterler: çğıöşü",
		"emoji: 🚀 done",
		"mixed 日本語 and 𝄞 clef",
		"𐍈 gothic at the start",
	}
	for _, want := range cases {
		got := decodeUTF16LE(encodeUTF16LE(want))
		if got != want {
			t.Errorf("round-trip = %q, want %q", got, want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("round-trip of %q produced invalid UTF-8", want)
		}
	}
}

// An unpaired high surrogate is malformed input; it must degrade to a single
// replacement character rather than emitting an invalid encoding.
func TestDecodeUTF16LEUnpairedSurrogate(t *testing.T) {
	// 0xD800 with no low half, followed by 'A'.
	data := []byte{0x00, 0xD8, 'A', 0x00}
	got := decodeUTF16LE(data)
	if !utf8.ValidString(got) {
		t.Fatalf("decode produced invalid UTF-8: %q", got)
	}
	if want := string(utf8.RuneError) + "A"; got != want {
		t.Errorf("decode = %q, want %q", got, want)
	}
}

// SBX-8 regression: short BOM-less UTF-16LE output (PowerShell / Git Bash
// emitting `echo hi` = 8 bytes, 4 even-position NULs) must be decoded as
// text, not misdetected as binary and replaced with a summary. The old
// heuristic required >15 NULs over the first 100 bytes, which short output
// never meets — the NULs then tripped CompactBinary's single-NUL threshold.
func TestConvertUTF16LEToUTF8ShortBOMlessOutput(t *testing.T) {
	for _, want := range []string{"hi", "Get-Date", "ok"} {
		data := encodeUTF16LE(want)
		if len(data) > 100 {
			t.Fatalf("test input %q too long for the short-output case", want)
		}
		got := convertUTF16LEToUTF8(data)
		if got != want {
			t.Errorf("convertUTF16LEToUTF8(%d bytes) = %q, want %q (SBX-8)", len(data), got, want)
		}
	}
}

// SBX-8 companion through the full pipeline: after conversion, the decoded
// text must NOT be flagged binary by CompactBinary, and NormalizeOutput must
// not return the `(binary; ...)` summary.
func TestNormalizeOutputShortUTF16LEIsNotBinary(t *testing.T) {
	data := encodeUTF16LE("hi")
	out := NormalizeOutput(string(data))
	if strings.Contains(out, "(binary;") {
		t.Fatalf("short UTF-16LE output misdetected as binary: %q", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("decoded text missing from normalized output: %q", out)
	}
}
