package content

import (
	"strconv"
	"strings"

	"github.com/ersinkoc/dfmt/internal/core"
)

// Summarizer produces human-readable summaries of content chunks.
type Summarizer struct{}

type Summary struct {
	Text       string   // Summary text
	Lines      int      // Total lines
	Warnings   []string // Detected warnings/errors
	TopPhrases []string // Most distinctive phrases
	Size       int      // Size in bytes
}

// Summarize produces a summary of content.
func (s *Summarizer) Summarize(body string, kind ChunkKind) *Summary {
	lines := strings.Split(body, "\n")
	lineCount := len(lines)

	warnings := s.detectWarnings(lines)
	topPhrases := s.extractTopPhrases(body)

	// Build summary text
	var summary strings.Builder
	summary.WriteString("exit=0")

	if lineCount > 0 {
		summary.WriteString(", ")
		summary.WriteString(intToStr(lineCount))
		summary.WriteString(" lines")
	}

	if len(warnings) > 0 {
		summary.WriteString(", ")
		summary.WriteString(intToStr(len(warnings)))
		summary.WriteString(" warnings")
	}

	if len(topPhrases) > 0 {
		summary.WriteString(", top phrases: ")
		summary.WriteString(strings.Join(topPhrases[:min(3, len(topPhrases))], ", "))
	}

	return &Summary{
		Text:       summary.String(),
		Lines:      lineCount,
		Warnings:   warnings,
		TopPhrases: topPhrases,
		Size:       len(body),
	}
}

// detectWarnings looks for common warning/error patterns.
func (s *Summarizer) detectWarnings(lines []string) []string {
	warningPatterns := []string{
		"warning:",
		"WARNING:",
		"Error:",
		"ERROR:",
		"error:",
		"FAILED",
		"failed",
		"panic:",
		"exception:",
		"SyntaxError",
		"TypeError",
		"ReferenceError",
	}

	var warnings []string
	seen := make(map[string]bool)

	for _, line := range lines {
		for _, pattern := range warningPatterns {
			if strings.Contains(line, pattern) && !seen[line] {
				warnings = append(warnings, strings.TrimSpace(line))
				seen[line] = true
				if len(warnings) >= 5 { // Cap at 5 warnings
					break
				}
			}
		}
		if len(warnings) >= 5 {
			break
		}
	}

	return warnings
}

// extractTopPhrases extracts distinctive phrases from content.
func (s *Summarizer) extractTopPhrases(body string) []string {
	// Tokenize and count
	tokens := core.TokenizeFull(body, nil)

	// Filter short tokens and stopwords
	stopwords := map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "is": {}, "are": {},
		"in": {}, "on": {}, "at": {}, "to": {}, "for": {},
		"of": {}, "and": {}, "or": {}, "but": {}, "if": {},
		"it": {}, "this": {}, "that": {}, "with": {},
	}

	var significant []string
	for _, t := range tokens {
		if len(t) > 4 && t != "" {
			if _, ok := stopwords[t]; !ok {
				significant = append(significant, t)
			}
		}
	}

	// Count frequency
	freq := make(map[string]int)
	for _, t := range significant {
		freq[t]++
	}

	// Sort by frequency
	type tokenFreq struct {
		token string
		freq  int
	}
	var sorted []tokenFreq
	for t, f := range freq {
		sorted = append(sorted, tokenFreq{t, f})
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].freq > sorted[i].freq {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Return top phrases
	result := make([]string, min(5, len(sorted)))
	for i, tf := range sorted[:min(5, len(sorted))] {
		result[i] = tf.token
	}

	return result
}

func intToStr(n int) string {
	return strconv.Itoa(n)
}

