package core

import (
	"container/heap"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ersinkoc/dfmt/internal/safejson"
)

// PostingList holds the document IDs and term frequencies for a term.
// TFs is uint32: uint16 would overflow on tokens repeated 65 536+ times
// (e.g. a huge log containing the same identifier over and over), silently
// corrupting BM25 scores.
type PostingList struct {
	IDs []string // ULIDs, sorted
	TFs []uint32 // term frequencies, parallel to IDs
}

// Index implements an in-memory inverted index with BM25 scoring.
type Index struct {
	mu        sync.RWMutex
	stemPL    map[string]*PostingList // stemmed term -> posting list
	trigramPL map[string]*PostingList // trigram -> posting list
	docLen    map[string]int          // document ID -> token count
	avgDocLen float64
	totalDocs int
	// excerpts maps event ID to a short content excerpt the search
	// handler attaches to each hit. Lets agents see WHAT each hit
	// contains without a follow-up dfmt_recall round-trip — net wire
	// saving even after the per-hit byte cost. Not persisted: gob
	// format stability beats one-time rebuild on daemon start
	// (Index.Add is called during journal load anyway).
	excerpts map[string]string

	// BM25 / scoring parameters (ADR-0015 v0.4 wire-up).
	//
	// k1 controls term-frequency saturation; b controls length
	// normalization. Stored on the Index so search reads them without
	// a global config lookup. NOT persisted — load paths leave them
	// zero and SearchBM25 falls back to package defaults via
	// NewBM25OkapiWithParams. Daemon-side load flow calls SetParams
	// after LoadIndexWithCursor so the configured values win.
	//
	// headingBoost is reserved: there's no "heading" event-type
	// classification in the search path today, so the value is
	// stored for forward compat but does nothing. Wiring requires a
	// heading-detection ADR.
	k1           float64
	b            float64
	headingBoost float64
}

// IndexParams are the tunable BM25 and scoring parameters. Zero values
// in any field mean "use package defaults" — both the constructor
// (NewIndexWithParams) and the search path (NewBM25OkapiWithParams)
// implement that fallback so a freshly-deserialized Index stays
// scorable until SetParams is called.
type IndexParams struct {
	K1           float64
	B            float64
	HeadingBoost float64
}

// excerptMaxBytes caps each per-event excerpt to keep search responses
// bounded. 80 bytes ≈ a typical truncated title or first sentence —
// enough signal for the agent to decide whether to drill in. Not a
// rune-aligned value because truncate() handles the alignment.
const excerptMaxBytes = 80

// NewIndex creates a new Index with package-default BM25 parameters
// (k1=DefaultBM25K1, b=DefaultBM25B, headingBoost=DefaultHeadingBoost).
// This is the zero-config constructor used by tests and the dfmt-bench
// harness; the daemon production path uses NewIndexWithParams so the
// operator's config.Index.* values flow through.
func NewIndex() *Index {
	return NewIndexWithParams(IndexParams{
		K1:           DefaultBM25K1,
		B:            DefaultBM25B,
		HeadingBoost: DefaultHeadingBoost,
	})
}

// NewIndexWithParams creates an Index with operator-configured BM25
// parameters. Zero fields are treated as "use package defaults" by the
// search path (defense-in-depth), but production callers should pass
// fully-resolved values; Validate already gates input ranges at
// config-load time. ADR-0015 v0.4.
func NewIndexWithParams(p IndexParams) *Index {
	return &Index{
		stemPL:       make(map[string]*PostingList),
		trigramPL:    make(map[string]*PostingList),
		docLen:       make(map[string]int),
		excerpts:     make(map[string]string),
		k1:           p.K1,
		b:            p.B,
		headingBoost: p.HeadingBoost,
	}
}

// SetParams overrides the BM25 / scoring parameters in place. Used by
// the daemon load flow: LoadIndexWithCursor returns an Index whose
// k1/b/headingBoost are zero (not persisted on disk), and the daemon
// follows up with SetParams to apply config-derived values. Tests
// can also use this to exercise a mid-life parameter swap.
func (ix *Index) SetParams(p IndexParams) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.k1 = p.K1
	ix.b = p.B
	ix.headingBoost = p.HeadingBoost
}

// Params returns the currently configured BM25 / scoring parameters
// under read-lock. Used by tests and a future dfmt doctor row that
// surfaces the effective values to operators.
func (ix *Index) Params() IndexParams {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return IndexParams{K1: ix.k1, B: ix.b, HeadingBoost: ix.headingBoost}
}

// TotalDocs returns the number of indexed documents under read-lock.
// Surfaced for observability (the /metrics endpoint publishes it as
// dfmt_index_docs); kept as a method rather than exposing the field so
// the lock contract is enforced at the call site.
func (ix *Index) TotalDocs() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.totalDocs
}

// indexJSON is the JSON-serializable form of Index.
type indexJSON struct {
	StemPL    map[string]*PostingList `json:"stem_pl"`
	TrigramPL map[string]*PostingList `json:"trigram_pl"`
	DocLen    map[string]int          `json:"doc_len"`
	AvgDocLen float64                 `json:"avg_doc_len"`
	TotalDocs int                     `json:"total_docs"`
}

// MarshalJSON implements json.Marshaler for Index.
func (ix *Index) MarshalJSON() ([]byte, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	j := indexJSON{
		StemPL:    ix.stemPL,
		TrigramPL: ix.trigramPL,
		DocLen:    ix.docLen,
		AvgDocLen: ix.avgDocLen,
		TotalDocs: ix.totalDocs,
	}
	return json.Marshal(j)
}

// UnmarshalJSON implements json.Unmarshaler for Index.
func (ix *Index) UnmarshalJSON(data []byte) error {
	var j indexJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	// Unmarshal may leave these nil if the JSON contained `null` for a
	// field (corrupt/partial write or older format) — a subsequent Add()
	// would panic on map assignment. Re-init to keep the index usable.
	ix.stemPL = j.StemPL
	if ix.stemPL == nil {
		ix.stemPL = make(map[string]*PostingList)
	}
	ix.trigramPL = j.TrigramPL
	if ix.trigramPL == nil {
		ix.trigramPL = make(map[string]*PostingList)
	}
	ix.docLen = j.DocLen
	if ix.docLen == nil {
		ix.docLen = make(map[string]int)
	}
	// excerpts is intentionally NOT in the persisted JSON — re-built
	// from the journal stream on load. Init to non-nil so concurrent
	// Search readers don't see a zero-value map.
	if ix.excerpts == nil {
		ix.excerpts = make(map[string]string)
	}
	ix.avgDocLen = j.AvgDocLen
	ix.totalDocs = j.TotalDocs
	return nil
}

// MaxIndexDocs is the V-13 cap on documents held in the in-memory
// index. Without it, an agent that floods dfmt_remember in a tight
// loop with high-entropy unique tokens grows term postings (stemPL,
// trigramPL) unboundedly — RAM that the journal-rotation budget does
// not bound. 100k documents accommodates several years of normal
// development activity (typical projects produce ~10–50 events/day)
// while bounding worst-case memory to ~tens of MB.
//
// At cap, Add evicts the oldest document (smallest ULID, which is
// time-sortable so smallest = earliest TS) before indexing the new
// one. Eviction is amortized: a flooding agent pays per-add eviction
// cost only after the cap is reached, and only for as long as the
// flood continues.
//
// Declared as a var (not const) so tests can temporarily lower the cap
// to drive the eviction path without inserting 100k events. Production
// code never mutates it.
var MaxIndexDocs = 100_000

// Add adds an event to the index. If the event ID was already indexed the
// call is a no-op — this prevents totalDocs drift and duplicate posting-list
// entries when a caller (e.g. a retry path) re-submits the same event.
func (ix *Index) Add(e Event) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	id := e.ID
	if _, exists := ix.docLen[id]; exists {
		return
	}

	// V-13: enforce document cap. When at cap, evict the oldest doc
	// (smallest ULID) before adding the new one. This bounds memory
	// regardless of dfmt_remember flood rate. The 1-step eviction
	// keeps amortized cost low; bulk-evict (e.g. 10% at once) is a
	// future optimization if the per-add eviction cost becomes
	// noticeable on profiling.
	if len(ix.docLen) >= MaxIndexDocs {
		ix.evictOldestLocked()
	}

	// Stash a short content excerpt for search-result enrichment.
	// Done before tokenization because the excerpt comes from raw
	// event fields (message/path/type) before stop-word filtering.
	if ix.excerpts == nil {
		ix.excerpts = make(map[string]string)
	}
	if ex := buildExcerpt(e); ex != "" {
		ix.excerpts[id] = ex
	}

	// Extract searchable text from event
	text := ix.eventText(e)
	tokens := TokenizeFull(text, nil)

	docLen := len(tokens)
	ix.docLen[id] = docLen
	ix.totalDocs++
	ix.avgDocLen = (ix.avgDocLen*float64(ix.totalDocs-1) + float64(docLen)) / float64(ix.totalDocs)

	// Count term frequencies
	tf := make(map[string]int)
	for _, tok := range tokens {
		stem := Stem(tok)
		tf[stem]++
	}

	// Update posting lists
	for stem, freq := range tf {
		pl, ok := ix.stemPL[stem]
		if !ok {
			pl = &PostingList{}
			ix.stemPL[stem] = pl
		}
		pl.IDs = append(pl.IDs, id)
		pl.TFs = append(pl.TFs, uint32(freq))
	}

	// Also index for trigram search
	ti := NewTrigramIndex()
	ti.Add(id, text)
	// Merge trigram postings into the main index
	for tg, ids := range ti.postings {
		pl, ok := ix.trigramPL[tg]
		if !ok {
			pl = &PostingList{}
			ix.trigramPL[tg] = pl
		}
		pl.IDs = append(pl.IDs, ids...)
	}
}

// Excerpt returns the short text snippet attached to docID at index
// time, or "" when no excerpt is recorded (event was indexed before
// the excerpt feature, or never had a message/path field). Safe for
// concurrent reads.
func (ix *Index) Excerpt(docID string) string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.excerpts == nil {
		return ""
	}
	return ix.excerpts[docID]
}

// buildExcerpt picks the most informative ~80-byte snippet for an
// event. Preference order: message → path → type+actor. Rune-aligned
// truncation keeps non-ASCII (Turkish, CJK) snippets valid UTF-8 so
// JSON marshal doesn't substitute U+FFFD.
func buildExcerpt(e Event) string {
	if e.Data != nil {
		if msg, ok := e.Data["message"].(string); ok && msg != "" {
			return excerptTruncate(msg, excerptMaxBytes)
		}
		if path, ok := e.Data["path"].(string); ok && path != "" {
			return excerptTruncate(string(e.Type)+" "+path, excerptMaxBytes)
		}
	}
	if e.Actor != "" {
		return excerptTruncate(string(e.Type)+" by "+e.Actor, excerptMaxBytes)
	}
	return excerptTruncate(string(e.Type), excerptMaxBytes)
}

// excerptTruncate cuts s to at most maxLen bytes at a rune boundary,
// appending "…" when truncation occurred. Local copy of the sandbox
// truncate helper to keep core dep-free.
func excerptTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen - 1
	for cut > 0 {
		b := s[cut]
		if b < 0x80 || b >= 0xC0 {
			break
		}
		cut--
	}
	return s[:cut] + "…"
}

// eventText extracts searchable text from an event.
func (ix *Index) eventText(e Event) string {
	// Combine type, tags, and data values
	var parts []string
	parts = append(parts, string(e.Type))
	parts = append(parts, e.Tags...)
	if e.Data != nil {
		for _, v := range e.Data {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
	}
	return joinStrings(parts, " ")
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		b.WriteString(sep)
		b.WriteString(parts[i])
	}
	return b.String()
}

// SearchBM25 searches the index using BM25. Each query token contributes a
// BM25 partial score for every document that contains it — summed across
// tokens. The prior implementation iterated only the smallest posting list
// and used its own length as the document frequency for every token, which
// effectively collapsed multi-term queries down to single-term behavior.
func (ix *Index) SearchBM25(query string, limit int) []ScoredHit {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	tokens := TokenizeFull(query, nil)
	if len(tokens) == 0 {
		return nil
	}

	// Deduplicate stems — multiple query tokens stemming to the same form
	// would otherwise double-count.
	seenStem := make(map[string]struct{}, len(tokens))
	scores := make(map[string]float64)
	// Use the operator-configured params; NewBM25OkapiWithParams falls
	// back to package defaults if ix.k1/ix.b are zero (deserialized index
	// before SetParams was called). ADR-0015 v0.4.
	bm := NewBM25OkapiWithParams(ix.k1, ix.b)
	for _, tok := range tokens {
		stem := Stem(tok)
		if _, dup := seenStem[stem]; dup {
			continue
		}
		seenStem[stem] = struct{}{}
		pl, ok := ix.stemPL[stem]
		if !ok {
			continue
		}
		df := len(pl.IDs)
		for i, docID := range pl.IDs {
			tf := int(pl.TFs[i])
			docLen := ix.docLen[docID]
			scores[docID] += bm.Score(tf, docLen, ix.avgDocLen, df, ix.totalDocs)
		}
	}

	if len(scores) == 0 {
		return nil
	}

	// Heap for top-K
	h := &hitHeap{}
	for id, score := range scores {
		heap.Push(h, ScoredHit{ID: id, Score: score, Layer: 1})
	}

	var results []ScoredHit
	for h.Len() > 0 && len(results) < limit {
		results = append(results, heap.Pop(h).(ScoredHit))
	}

	return results
}

// SearchTrigram is the substring-match fallback layer. BM25 is the
// primary search path, but it relies on the Porter stemmer and the
// project's stopword list; tokens that the tokenizer drops or splits
// awkwardly (synthetic markers like "AUDIT_PROBE_XJ7Q3", UUID-style
// IDs, mixed-case all-caps acronyms) silently become unsearchable.
// Trigram match restores them: the query is tokenized identically to
// indexed text, every token of length >= 3 contributes its trigrams,
// and a doc is scored by how many query tokens it covers.
//
// Score is the count of matched query tokens (so a doc that matches
// 3 of 4 query tokens outranks one that matches only 1). Layer is set
// to 2 so the response can report which layer produced the hit, and
// the per-tier BM25 layer (1) still wins on direct ties.
func (ix *Index) SearchTrigram(query string, limit int) []ScoredHit {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	tokens := TokenizeFull(query, nil)
	if len(tokens) == 0 {
		return nil
	}

	// hits[docID] = number of distinct query tokens that fully match.
	hits := make(map[string]int)
	for _, tok := range tokens {
		matched := ix.trigramDocsForToken(tok)
		if len(matched) == 0 {
			continue
		}
		seen := make(map[string]struct{}, len(matched))
		for _, id := range matched {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			hits[id]++
		}
	}
	if len(hits) == 0 {
		return nil
	}

	h := &hitHeap{}
	for id, count := range hits {
		heap.Push(h, ScoredHit{ID: id, Score: float64(count), Layer: 2})
	}

	var results []ScoredHit
	for h.Len() > 0 && len(results) < limit {
		results = append(results, heap.Pop(h).(ScoredHit))
	}
	return results
}

// trigramDocsForToken returns the doc IDs whose indexed text contains
// every trigram of the lowercased token. Tokens shorter than 3 chars
// return nil (no trigram coverage possible). A miss on any single
// trigram short-circuits to nil — the document does not contain the
// token as a substring, so further intersections cannot rescue it.
func (ix *Index) trigramDocsForToken(token string) []string {
	if len(token) < 3 {
		return nil
	}
	tok := strings.ToLower(token)
	var result []string
	for i := 0; i <= len(tok)-3; i++ {
		tg := tok[i : i+3]
		pl, ok := ix.trigramPL[tg]
		if !ok {
			return nil
		}
		if i == 0 {
			result = append([]string(nil), pl.IDs...)
		} else {
			result = intersection(result, pl.IDs)
		}
		if len(result) == 0 {
			return nil
		}
	}
	return result
}

// ScoredHit represents a scored search result.
type ScoredHit struct {
	ID    string
	Score float64
	Layer int
}

type hitHeap []ScoredHit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return h[i].Score > h[j].Score } // max heap
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(ScoredHit)) }
func (h *hitHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// Remove removes a document from the index. Cleans up both the stem and
// trigram posting lists and recomputes avgDocLen from scratch so the BM25
// scoring stays consistent after deletes.
func (ix *Index) Remove(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(id)
}

// removeLocked performs the actual removal work. The caller MUST hold
// ix.mu (write lock). Extracted from Remove so the V-13 eviction path
// in Add — which already holds the lock — can reuse the same logic
// without re-entering the mutex.
func (ix *Index) removeLocked(id string) {
	if _, ok := ix.docLen[id]; !ok {
		// No-op: unknown id. Prevents totalDocs from going negative on
		// double-remove.
		return
	}
	delete(ix.docLen, id)
	delete(ix.excerpts, id)
	if ix.totalDocs > 0 {
		ix.totalDocs--
	}

	// Remove from stem posting lists.
	for stem, pl := range ix.stemPL {
		newIDs := make([]string, 0, len(pl.IDs))
		newTFs := make([]uint32, 0, len(pl.TFs))
		for i, docID := range pl.IDs {
			if docID != id {
				newIDs = append(newIDs, docID)
				newTFs = append(newTFs, pl.TFs[i])
			}
		}
		if len(newIDs) == 0 {
			delete(ix.stemPL, stem)
		} else {
			pl.IDs = newIDs
			pl.TFs = newTFs
		}
	}

	// Remove from trigram posting lists.
	for tg, pl := range ix.trigramPL {
		newIDs := make([]string, 0, len(pl.IDs))
		for _, docID := range pl.IDs {
			if docID != id {
				newIDs = append(newIDs, docID)
			}
		}
		if len(newIDs) == 0 {
			delete(ix.trigramPL, tg)
		} else {
			pl.IDs = newIDs
		}
	}

	// Recompute avgDocLen from the surviving docs to avoid drift.
	if ix.totalDocs == 0 {
		ix.avgDocLen = 0
	} else {
		total := 0
		for _, l := range ix.docLen {
			total += l
		}
		ix.avgDocLen = float64(total) / float64(ix.totalDocs)
	}
}

// evictOldestLocked removes the document with the smallest ID. The caller
// MUST hold ix.mu (write lock). Used by Add when totalDocs >= MaxIndexDocs.
//
// Smallest ULID == earliest timestamp because ULIDs encode time in their
// lexicographically-comparable prefix (Crockford base32 of the milliseconds
// since epoch). String comparison is therefore equivalent to time
// comparison without parsing.
//
// Walks docLen once (O(N)) to find the min; then removeLocked walks every
// posting list (O(unique_terms × max_posting_size)) to prune. Eviction
// fires only when at cap so amortized cost per Add is bounded by the
// cap, not by the index size.
func (ix *Index) evictOldestLocked() {
	if len(ix.docLen) == 0 {
		return
	}
	var oldest string
	for id := range ix.docLen {
		if oldest == "" || id < oldest {
			oldest = id
		}
	}
	if oldest != "" {
		ix.removeLocked(oldest)
	}
}

// Persist saves the index to a file using JSON serialization.
func (ix *Index) Persist(path string) error {
	// json.Marshal dispatches to ix.MarshalJSON, which itself takes
	// ix.mu.RLock. Do NOT hold the lock here — Go's RWMutex starves a
	// pending reader behind a pending writer, so re-entering RLock would
	// deadlock the goroutine under write contention. writeRawAtomic then
	// performs tmp+fsync+rename so a crash leaves the prior complete file
	// intact.
	buf, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	return writeRawAtomic(path, buf)
}

// LoadIndex loads an index from a file using JSON deserialization.
//
// V-10: read fully then depth-check before unmarshal. The index file is
// operator-trust-bounded (anyone with .dfmt/ write access can corrupt it),
// but the daemon's New() startup path calls LoadIndex without a recover
// so a poisoned `[[[…` file would otherwise blow the stack on the
// recursive json.Unmarshal and crash the daemon on every launch. The
// async-rebuild path is recover-guarded; LoadIndex on cold start was not.
func LoadIndex(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := safejson.Unmarshal(data, &ix); err != nil {
		return nil, err
	}
	return &ix, nil
}
