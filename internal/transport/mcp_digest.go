package transport

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// digestPreviewRunes bounds the first-line preview in a digest. It is a
// glance for the human watching the terminal, not a second copy of the
// payload: the full result still travels only in structuredContent.
const digestPreviewRunes = 80

// digestTextFields are the payload fields that carry a tool's main text, in
// priority order (exec stdout, read/fetch content, ...).
var digestTextFields = []string{"stdout", "content", "body", "summary", "text"}

// mcpDigest builds the short text placed in content[0].text for hosts that
// read structuredContent. Those hosts render only content[0].text to the
// person, so a fixed sentinel left the terminal showing "dfmt: see
// structuredContent" for every call. The digest says what happened (exit
// status, size, the first line) in a few dozen bytes, which keeps the
// token saving the sentinel existed for. It returns "" when the payload has
// nothing recognizable to summarize; the caller then uses the sentinel.
func mcpDigest(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var parts []string
	if v, ok := m["exit"].(float64); ok {
		parts = append(parts, fmt.Sprintf("exit %d", int(v)))
	}
	for _, k := range digestTextFields {
		text, ok := m[k].(string)
		if !ok {
			continue
		}
		if strings.TrimSpace(text) == "" {
			parts = append(parts, "no output")
			break
		}
		if n := countLines(text); n > 1 {
			parts = append(parts, fmt.Sprintf("%d lines", n))
		}
		parts = append(parts, truncateRunes(firstLine(text), digestPreviewRunes))
		break
	}
	if len(parts) == 0 {
		return ""
	}
	return "dfmt: " + strings.Join(parts, " · ")
}

func countLines(s string) int {
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// truncateRunes cuts on a rune boundary so a multi-byte character is never
// split into invalid UTF-8.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
