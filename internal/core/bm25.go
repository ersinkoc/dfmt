package core

import "math"

// BM25Okapi implements the Okapi BM25 ranking function.
type BM25Okapi struct {
	k1 float64 // term frequency saturation parameter
	b  float64 // length normalization parameter
}

// NewBM25Okapi creates a BM25 scorer with default parameters (k1=1.2, b=0.75).
func NewBM25Okapi() *BM25Okapi {
	return &BM25Okapi{k1: 1.2, b: 0.75}
}

// IDF computes inverse document frequency.
// Uses the smoothed formula to avoid negative values.
func IDF(df, N int) float64 {
	// Smoothed IDF: log((N - df + 0.5) / (df + 0.5)) + 1
	return math.Log(float64(N-df+1)/float64(df+1)) + 1
}

// Score computes the BM25 score for a single term.
func (bm *BM25Okapi) Score(tf int, docLen int, avgDocLen float64, df, N int) float64 {
	if tf == 0 || df == 0 {
		return 0
	}

	idf := IDF(df, N)

	// Normalized term frequency
	tfNorm := float64(tf) * (bm.k1 + 1) / (float64(tf) + bm.k1*(1-bm.b+bm.b*float64(docLen)/avgDocLen))

	return idf * tfNorm
}

// BM25Result holds a scored document result.
type BM25Result struct {
	ID    string
	Score float64
}
