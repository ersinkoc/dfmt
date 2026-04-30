package core

import (
	"fmt"
	"strings"
	"testing"
)

// BM25 search benchmarks at three corpus sizes. The Index walks the term
// dictionary once per query to compute IDF, then walks the matched
// posting lists for scoring; both costs scale with corpus size, so
// keeping a baseline at 100 / 1k / 10k events flags either regression
// (stem dictionary blowup, posting-list growth) or wins (better
// short-circuit on top-K).
//
// `make bench` picks these up; CI does not run benchmarks.

// seedIndex builds an Index with `n` synthetic events whose Data
// contains a small vocabulary plus a per-event variant. The vocabulary
// gives BM25 something to score against; the variant prevents every
// event from being indistinguishable.
func seedIndex(b *testing.B, n int) *Index {
	b.Helper()
	commonTerms := []string{
		"deploy", "rollback", "test", "build", "lint", "review",
		"merge", "branch", "config", "permission", "sandbox", "redact",
		"journal", "index", "search", "recall",
	}
	ix := NewIndex()
	for i := 0; i < n; i++ {
		// Each event mixes 3 common terms plus a unique-ish suffix so
		// the posting lists have realistic per-term selectivity.
		t1 := commonTerms[i%len(commonTerms)]
		t2 := commonTerms[(i*3)%len(commonTerms)]
		t3 := commonTerms[(i*7)%len(commonTerms)]
		ix.Add(Event{
			ID:   fmt.Sprintf("ev%07d", i),
			Type: "tool.exec",
			Tags: []string{t1, t2},
			Data: map[string]any{
				"message": fmt.Sprintf("%s and %s with %s detail-%d", t1, t2, t3, i),
			},
		})
	}
	return ix
}

func BenchmarkSearchBM25_100(b *testing.B) {
	ix := seedIndex(b, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ix.SearchBM25("deploy build review", 10)
	}
}

func BenchmarkSearchBM25_1k(b *testing.B) {
	ix := seedIndex(b, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ix.SearchBM25("deploy build review", 10)
	}
}

func BenchmarkSearchBM25_10k(b *testing.B) {
	ix := seedIndex(b, 10000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ix.SearchBM25("deploy build review", 10)
	}
}

// BenchmarkIndexAdd captures the journal-replay cost on daemon start.
// Each Add tokenizes, stems, and updates the inverted index; this is
// what `LoadIndexWithCursor` runs in a hot loop when the cursor is
// stale or the journal grew between sessions.
func BenchmarkIndexAdd_1k(b *testing.B) {
	commonTerms := []string{"deploy", "rollback", "test", "build", "lint"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Re-create the index every iteration so we measure cold-add
		// cost (matches the journal-replay startup path) rather than
		// the steady-state amortized cost.
		ix := NewIndex()
		for j := 0; j < 1000; j++ {
			t1 := commonTerms[j%len(commonTerms)]
			t2 := commonTerms[(j*3)%len(commonTerms)]
			ix.Add(Event{
				ID:   fmt.Sprintf("ev%07d", j),
				Type: "tool.exec",
				Tags: []string{t1, t2},
				Data: map[string]any{
					"message": strings.Repeat(t1+" "+t2+" ", 10),
				},
			})
		}
	}
}
