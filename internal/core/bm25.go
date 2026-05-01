package core

import "math"

// BM25Okapi implements the Okapi BM25 ranking function.
type BM25Okapi struct {
	k1 float64 // term frequency saturation parameter
	b  float64 // length normalization parameter
}

// NewBM25Okapi creates a BM25 scorer with package-default parameters
// (k1=DefaultBM25K1=1.2, b=DefaultBM25B=0.75).
func NewBM25Okapi() *BM25Okapi {
	return &BM25Okapi{k1: DefaultBM25K1, b: DefaultBM25B}
}

// NewBM25OkapiWithParams creates a scorer with operator-configured
// parameters. Zero or negative inputs fall back to the package
// defaults — defense-in-depth for the load path where Index.UnmarshalJSON
// leaves the per-Index k1/b at zero until the daemon calls SetParams.
// Production configs are gated by config.Validate so reaching the
// fallback usually means a freshly-deserialized index whose owner
// hasn't called SetParams yet.
func NewBM25OkapiWithParams(k1, b float64) *BM25Okapi {
	if k1 <= 0 {
		k1 = DefaultBM25K1
	}
	if b <= 0 || b > 1 {
		b = DefaultBM25B
	}
	return &BM25Okapi{k1: k1, b: b}
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
	// avgDocLen <= 0 happens when the index has posting lists but hasn't yet
	// recomputed the average (e.g. during a rebuild). Dividing by it below
	// would yield NaN/+Inf and sort garbage rows to the top. Fall back to
	// IDF-only scoring, which is a correct lower bound.
	if avgDocLen <= 0 {
		return IDF(df, N)
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
