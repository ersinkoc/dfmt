package transport

import (
	"strings"
	"testing"
)

func TestMCPDigest(t *testing.T) {
	long := strings.Repeat("é", 200) // multi-byte: must be cut on a rune boundary
	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"exec one line", map[string]any{"exit": 0, "stdout": "hello\n"}, "dfmt: exit 0 · hello"},
		{"exec multi line", map[string]any{"exit": 1, "stdout": "a\nb\nc\n"}, "dfmt: exit 1 · 3 lines · a"},
		{"exec no output", map[string]any{"exit": 0, "stdout": ""}, "dfmt: exit 0 · no output"},
		{"read content", map[string]any{"content": "package x\nfunc f() {}\n"}, "dfmt: 2 lines · package x"},
		{"dedup reply", map[string]any{"content": "(unchanged; same content_id)"}, "dfmt: (unchanged; same content_id)"},
		{"nothing to say", map[string]any{"id": "01H"}, ""},
	}
	for _, tc := range cases {
		if got := mcpDigest(tc.payload); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	got := mcpDigest(map[string]any{"stdout": long})
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > len("dfmt: ")+digestPreviewRunes+1 {
		t.Errorf("long preview not bounded: %d runes", len([]rune(got)))
	}
}

func TestMCPToolResultFallsBackToSentinel(t *testing.T) {
	res := mcpToolResult(map[string]any{"id": "01H"}, false)
	if res.Content[0].Text != mcpLegacyContentSentinel {
		t.Errorf("text = %q, want sentinel", res.Content[0].Text)
	}
}
