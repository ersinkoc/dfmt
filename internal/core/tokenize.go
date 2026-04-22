package core

import (
	"slices"
	"strings"
	"unicode"
)

// Default English stopwords (common words to exclude from indexing).
var englishStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "has": {}, "he": {}, "in": {}, "is": {}, "it": {},
	"its": {}, "of": {}, "on": {}, "that": {}, "the": {}, "to": {}, "was": {},
	"were": {}, "will": {}, "with": {},
}

// Tokenize splits text into lowercase tokens for indexing.
// It drops tokens shorter than 2 or longer than 64 characters,
// and removes stopwords.
func TokenizeFull(s string, stopwords map[string]struct{}) []string {
	if stopwords == nil {
		stopwords = englishStopwords
	}

	var tokens []string
	var current strings.Builder

	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 2 && current.Len() <= 64 {
				tok := current.String()
				if _, ok := stopwords[tok]; !ok {
					tokens = append(tokens, tok)
				}
			}
			current.Reset()
		}
	}

	if current.Len() >= 2 && current.Len() <= 64 {
		tok := current.String()
		if _, ok := stopwords[tok]; !ok {
			tokens = append(tokens, tok)
		}
	}

	return tokens
}

// NGrams generates character n-grams from tokens.
func NGrams(tokens []string, n int) []string {
	var ngrams []string
	for _, tok := range tokens {
		if len(tok) < n {
			continue
		}
		for i := 0; i <= len(tok)-n; i++ {
			ngrams = append(ngrams, tok[i:i+n])
		}
	}
	return ngrams
}

// Unique returns unique tokens preserving order.
func Unique(tokens []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}
	return result
}

// Merge merges two sorted token slices.
func Merge(a, b []string) []string {
	return slices.Concat(a, b)
}
