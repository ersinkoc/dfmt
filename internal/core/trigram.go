package core

import (
	"slices"
)

// TrigramIndex provides fast substring search via trigram inverted index.
type TrigramIndex struct {
	postings map[string][]string // trigram -> document IDs
}

// NewTrigramIndex creates a new trigram index.
func NewTrigramIndex() *TrigramIndex {
	return &TrigramIndex{
		postings: make(map[string][]string),
	}
}

// Add indexes a document's tokens for trigram search.
func (ti *TrigramIndex) Add(id string, text string) {
	tokens := TokenizeFull(text, nil)
	for _, tok := range tokens {
		if len(tok) >= 3 {
			trigram := tok[:3]
			ti.postings[trigram] = append(ti.postings[trigram], id)
		}
	}
}

// Search finds document IDs that might contain the given substring.
func (ti *TrigramIndex) Search(substring string) []string {
	substr := substring
	if len(substr) < 3 {
		// For very short substrings, scan all
		var all []string
		seen := make(map[string]bool)
		for _, ids := range ti.postings {
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					all = append(all, id)
				}
			}
		}
		return all
	}

	// Get trigrams from substring
	var trigrams []string
	for i := 0; i <= len(substr)-3; i++ {
		trigrams = append(trigrams, substr[i:i+3])
	}

	if len(trigrams) == 0 {
		return nil
	}

	// Find intersection of posting lists (documents containing all trigrams)
	var result []string
	for _, tg := range trigrams {
		ids := ti.postings[tg]
		if result == nil {
			result = ids
		} else {
			result = intersection(result, ids)
		}
		if len(result) == 0 {
			return nil
		}
	}

	return result
}

// intersection returns the sorted intersection of two string slices.
func intersection(a, b []string) []string {
	// Sort if not already sorted
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)

	var result []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			result = append(result, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return result
}
