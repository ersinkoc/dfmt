package sandbox

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// englishStopwords for intent parsing.
var englishStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "has": {}, "he": {}, "in": {}, "is": {}, "it": {},
	"its": {}, "of": {}, "on": {}, "that": {}, "the": {}, "to": {}, "was": {},
	"were": {}, "will": {}, "with": {}, "i": {}, "want": {}, "find": {}, "show": {},
	"get": {}, "list": {}, "look": {}, "see": {}, "need": {}, "this": {}, "these": {},
	"those": {}, "my": {}, "me": {}, "we": {}, "our": {}, "us": {}, "you": {},
	"your": {}, "have": {}, "had": {}, "do": {}, "does": {}, "did": {}, "can": {},
	"could": {}, "would": {}, "should": {}, "may": {}, "might": {}, "must": {},
	"shall": {}, "am": {}, "been": {}, "being": {},
}

// ExtractKeywords parses an intent string into searchable keywords.
func ExtractKeywords(intent string) []string {
	if intent == "" {
		return nil
	}

	var tokens []string
	var current strings.Builder

	for _, r := range strings.ToLower(intent) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 2 {
				tok := current.String()
				if _, ok := englishStopwords[tok]; !ok {
					tokens = append(tokens, tok)
				}
			}
			current.Reset()
		}
	}

	if current.Len() >= 2 {
		tok := current.String()
		if _, ok := englishStopwords[tok]; !ok {
			tokens = append(tokens, tok)
		}
	}

	// Deduplicate while preserving order
	seen := make(map[string]struct{}, len(tokens))
	unique := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			unique = append(unique, t)
		}
	}

	return unique
}

// Excerpt represents a scored text excerpt.
type Excerpt struct {
	Text  string
	Score float64
	Line  int
}

// MatchContent scores lines against keywords and returns top matches.
func MatchContent(content string, keywords []string, maxMatches int) []ContentMatch {
	if len(keywords) == 0 || content == "" {
		return nil
	}

	lines := strings.Split(content, "\n")
	excerpts := make([]Excerpt, 0, len(lines))

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		score := scoreLine(line, keywords)
		if score > 0 {
			excerpts = append(excerpts, Excerpt{
				Text:  line,
				Score: score,
				Line:  i + 1,
			})
		}
	}

	// Sort by score descending
	sort.Slice(excerpts, func(i, j int) bool {
		return excerpts[i].Score > excerpts[j].Score
	})

	// Take top N
	if maxMatches <= 0 {
		maxMatches = 10
	}
	if len(excerpts) > maxMatches {
		excerpts = excerpts[:maxMatches]
	}

	matches := make([]ContentMatch, len(excerpts))
	for i, e := range excerpts {
		matches[i] = ContentMatch{
			Text:  e.Text,
			Score: e.Score,
			Line:  e.Line,
		}
	}

	return matches
}

// scoreLine scores a single line against keywords.
func scoreLine(line string, keywords []string) float64 {
	lower := strings.ToLower(line)
	score := 0.0
	matches := 0

	for _, kw := range keywords {
		// Exact match bonus
		if strings.Contains(lower, kw) {
			score += 1.0
			matches++
			// Prefix/suffix matching for hyphenated/compound
			if strings.Contains(lower, " "+kw) || strings.Contains(lower, kw+" ") {
				score += 0.5
			}
		}
	}

	// Bonus for matching multiple keywords
	if matches > 1 {
		score *= (1.0 + 0.2*float64(matches-1))
	}

	// Penalize very long lines slightly (they're less focused)
	if len(line) > 200 {
		score *= 0.8
	}

	return score
}

// GenerateSummary creates a short summary of content.
func GenerateSummary(content string, keywords []string) string {
	lines := strings.Split(content, "\n")
	var nonEmpty []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}

	total := len(nonEmpty)
	if total == 0 {
		return "(empty)"
	}

	if len(keywords) > 0 {
		matches := MatchContent(content, keywords, 5)
		if len(matches) > 0 {
			var parts []string
			for _, m := range matches {
				parts = append(parts, fmt.Sprintf("L%d: %s", m.Line, truncate(m.Text, 80)))
			}
			return fmt.Sprintf("Found %d matching lines out of %d total. Top matches:\n%s",
				len(matches), total, strings.Join(parts, "\n"))
		}
		return fmt.Sprintf("No matches found for intent. Total lines: %d", total)
	}

	return fmt.Sprintf("Total lines: %d", total)
}

// ExtractVocabulary returns distinctive terms from content.
func ExtractVocabulary(content string) []string {
	freq := make(map[string]int)
	var current strings.Builder

	for _, r := range strings.ToLower(content) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 3 && current.Len() <= 32 {
				tok := current.String()
				if _, ok := englishStopwords[tok]; !ok {
					freq[tok]++
				}
			}
			current.Reset()
		}
	}

	if current.Len() >= 3 && current.Len() <= 32 {
		tok := current.String()
		if _, ok := englishStopwords[tok]; !ok {
			freq[tok]++
		}
	}

	// Score by TF-IDF-like metric: frequent but not too frequent
	type termScore struct {
		term  string
		score float64
	}
	var scores []termScore

	totalTokens := 0
	for _, c := range freq {
		totalTokens += c
	}

	for term, count := range freq {
		if count < 2 {
			continue
		}
		tf := float64(count) / float64(totalTokens)
		// Penalize extremely common terms
		idf := math.Log(float64(totalTokens) / float64(count))
		scores = append(scores, termScore{term: term, score: tf * idf})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	var vocab []string
	for i := 0; i < len(scores) && i < 20; i++ {
		vocab = append(vocab, scores[i].term)
	}

	return vocab
}

// truncate shortens s to at most maxLen bytes, appending "..." when it had
// to cut. The cut is backed up to a rune boundary so non-ASCII summaries
// (Turkish, CJK, accented Latin) don't end mid-rune — encoding/json would
// otherwise substitute U+FFFD for the invalid UTF-8 tail.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// For maxLen < 3 there's no room for the ellipsis; just return a valid
	// rune-aligned prefix.
	cut := maxLen - 3
	if cut <= 0 {
		cut = maxLen
	}
	if cut > len(s) {
		cut = len(s)
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut >= maxLen {
		return s[:cut]
	}
	return s[:cut] + "..."
}
