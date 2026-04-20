package core

// Levenshtein computes the Levenshtein (edit) distance between two strings.
func Levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	// Create matrix
	m := make([][]int, len(a)+1)
	for i := range m {
		m[i] = make([]int, len(b)+1)
	}

	// Initialize
	for i := 0; i <= len(a); i++ {
		m[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		m[0][j] = j
	}

	// Fill matrix
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			m[i][j] = min(
				m[i-1][j]+1,   // deletion
				m[i][j-1]+1,   // insertion
				m[i-1][j-1]+cost, // substitution
			)
		}
	}

	return m[len(a)][len(b)]
}

// FuzzyMatch returns true if the edit distance is within the threshold.
func FuzzyMatch(a, b string, threshold int) bool {
	return Levenshtein(a, b) <= threshold
}
