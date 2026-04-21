package core

import (
	"testing"
)

func TestTrigramIndexAddAndSearch(t *testing.T) {
	ti := NewTrigramIndex()

	// Search for trigrams that exist in the content
	// "hello world" contains trigrams: "hel", "ell", "llo", "lo ", "o w", " wo", "wor", "orl", "rld"
	ti.Add("doc1", "hello world")
	ti.Add("doc2", "goodbye world")

	// Search for "wor" which is a trigram in "world"
	results := ti.Search("wor")
	if len(results) == 0 {
		t.Error("Search should find doc1 containing 'wor' from 'world'")
	}
}

func TestTrigramIndexSearchShortSubstring(t *testing.T) {
	ti := NewTrigramIndex()

	ti.Add("doc1", "test content")

	// Short substrings (< 3 chars) return all docs (scan all)
	results := ti.Search("te")
	if results == nil {
		t.Error("Short search should return all docs (nil means no results, not empty)")
	}
}

func TestTrigramIndexSearchNoMatchAtAll(t *testing.T) {
	ti := NewTrigramIndex()

	ti.Add("doc1", "hello world")

	// Search for trigrams that don't exist in any document
	results := ti.Search("xyz")
	if len(results) != 0 {
		t.Error("Search for nonexistent trigram should return empty")
	}
}

func TestTrigramIntersection(t *testing.T) {
	// Test the intersection function directly
	result := intersection([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	if len(result) != 2 {
		t.Errorf("intersection should return 2 elements, got %d", len(result))
	}
}

func TestTrigramIntersectionEmpty(t *testing.T) {
	result := intersection([]string{"a", "b"}, []string{"c", "d"})
	if len(result) != 0 {
		t.Errorf("intersection of disjoint sets should be empty, got %d", len(result))
	}
}

func TestTrigramIntersectionOneEmpty(t *testing.T) {
	result := intersection([]string{"a", "b"}, []string{})
	if len(result) != 0 {
		t.Errorf("intersection with empty should be empty, got %d", len(result))
	}
}

func TestTrigramIntersectionDuplicate(t *testing.T) {
	result := intersection([]string{"a", "b", "b"}, []string{"b", "b", "c"})
	// Intersection preserves duplicates from input slices
	// After sorting: ["a","b","b"] and ["b","b","c"]
	// Both "b" elements match, giving ["b","b"]
	if len(result) != 2 {
		t.Errorf("intersection should return 2 elements, got %d", len(result))
	}
}

func TestTrigramIndexSearchSingleDoc(t *testing.T) {
	ti := NewTrigramIndex()

	ti.Add("doc1", "testing the trigram index")

	// Search for trigram "tri" which is the first 3 chars of "trigram"
	results := ti.Search("tri")
	if len(results) == 0 {
		t.Error("Should find doc1 containing 'tri' from 'trigram'")
	}
}

func TestTrigramIndexSearchExactMatch(t *testing.T) {
	ti := NewTrigramIndex()

	ti.Add("doc1", "abc")
	ti.Add("doc2", "abd")
	ti.Add("doc3", "xyz")

	// Search for "abc" - only the first 3 chars are stored
	// Only doc1 contains "abc"
	results := ti.Search("abc")
	if len(results) == 0 {
		t.Error("Should find doc1 containing 'abc'")
	}
}
