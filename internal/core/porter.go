package core

import (
	"strings"
)

// Porter stemmer implementation (1980 algorithm).
// Based on the original Porter algorithm.

func isConsonant(s string, i int) bool {
	switch s[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		if i == 0 {
			return true
		}
		return !isConsonant(s, i-1)
	}
	return true
}

func measure(s string) int {
	if len(s) == 0 {
		return 0
	}
	// Count VC patterns
	var count int
	var i int
	for i < len(s) && !isConsonant(s, i) {
		i++
	}
	for i < len(s) {
		for i < len(s) && isConsonant(s, i) {
			i++
		}
		if i < len(s) {
			count++
		}
		for i < len(s) && !isConsonant(s, i) {
			i++
		}
	}
	return count
}

func hasSuffix(s, suffix string) bool {
	return strings.HasSuffix(s, suffix) && len(s) > len(suffix)
}

func removeSuffix(s, suffix string) string {
	return s[:len(s)-len(suffix)]
}

// Stem returns the Porter stem of a word. Porter's vowel/consonant rules
// are defined over ASCII letters only — running it byte-wise on a UTF-8
// string like "çalışma" treats every continuation byte as a consonant and
// produces nonsense stems. For any non-ASCII input we skip stemming and
// return the lowercased word unchanged; a dedicated Turkish stemmer would
// be needed to do better.
func Stem(word string) string {
	s := strings.ToLower(word)
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return s
		}
	}

	// Step 1a
	if strings.HasSuffix(s, "sses") {
		return s[:len(s)-2]
	}
	if strings.HasSuffix(s, "ies") {
		return s[:len(s)-2]
	}
	if strings.HasSuffix(s, "ss") {
		return s
	}
	if strings.HasSuffix(s, "s") {
		return s[:len(s)-1]
	}

	// Step 1b: Porter spec is EED → EE when measure(stem) > 0.
	// "agreed" (m=1) → "agree", not "agre".
	if strings.HasSuffix(s, "eed") {
		stem := removeSuffix(s, "eed")
		if measure(stem) > 0 {
			return stem + "ee"
		}
		return s
	}

	if strings.HasSuffix(s, "ed") {
		stem := removeSuffix(s, "ed")
		if containsVowel(stem) {
			s = stem
			// Step 1b* continued
			if strings.HasSuffix(s, "at") || strings.HasSuffix(s, "bl") || strings.HasSuffix(s, "iz") {
				return s + "e"
			}
			if isDoubleConsonant(s) && !isShort(s) {
				return s[:len(s)-1]
			}
			if measure(s) == 1 && isShort(s) {
				return s + "e"
			}
			return s
		}
		return s
	}

	if strings.HasSuffix(s, "ing") {
		stem := removeSuffix(s, "ing")
		if containsVowel(stem) {
			s = stem
			if strings.HasSuffix(s, "at") || strings.HasSuffix(s, "bl") || strings.HasSuffix(s, "iz") {
				return s + "e"
			}
			if isDoubleConsonant(s) && !isShort(s) {
				return s[:len(s)-1]
			}
			if measure(s) == 1 && isShort(s) {
				return s + "e"
			}
			return s
		}
		return s
	}

	// Step 1c
	if strings.HasSuffix(s, "y") {
		stem := removeSuffix(s, "y")
		if containsVowel(stem) {
			return stem + "i"
		}
	}

	return s
}

func containsVowel(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isConsonant(s, i) {
			return true
		}
	}
	return false
}

func isDoubleConsonant(s string) bool {
	if len(s) < 2 {
		return false
	}
	return s[len(s)-1] == s[len(s)-2] && isConsonant(s, len(s)-1)
}

func isShort(s string) bool {
	return measure(s) == 1 && isConsonant(s, len(s)-1) && !isConsonant(s, len(s)-2)
}
