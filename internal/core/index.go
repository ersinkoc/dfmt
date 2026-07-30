package core

import (
	"container/heap"
	"encoding/json"
	"io"
	"os"
	"sort"
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

	// meta maps event ID to the few fields a search result needs in order
	// to be actionable: what KIND of event this is and where it came from.
	//
	// Without it, SearchHit.Type and SearchHit.Source were declared on the
	// wire struct and never populated, so every hit looked alike. That
	// matters more than it sounds: a journal is overwhelmingly automatic
	// tool events (193 of 202 on this repo at the time of writing), so a
	// result list with no type is a list in which the one note the agent
	// deliberately recorded is indistinguishable from the noise around it.
	// Same non-persistence rationale as excerpts.
	meta map[string]DocMeta

	// docStems / docTrigrams are the reverse index: which terms a
	// document contributed to. Without them, removing one document meant
	// walking EVERY posting list in the index — and since eviction runs on
	// every Add once the cap is reached, that turned a full index into a
	// per-insert full scan. With them, removal touches only the terms the
	// document actually has.
	docStems    map[string][]string
	docTrigrams map[string][]string

	// docLenSum is the running total of docLen values, so avgDocLen is
	// arithmetic rather than a fresh walk over every document on each
	// removal.
	docLenSum int

	// evictHeaps holds one min-heap of document IDs per priority tier
	// (index 0 = P1 … 3 = P4). Eviction drains the lowest-priority
	// non-empty tier first, so a routine tool call is dropped before a
	// decision note.
	//
	// A single age-ordered heap — what this was — evicted strictly by ULID,
	// which meant the OLDEST note went before the NEWEST tool call. On a
	// journal that is overwhelmingly automatic (193 of 202 events here),
	// that is precisely backwards: the note stayed in the journal, so
	// recall could still see it, while the index silently forgot it and
	// dfmt_search could not.
	//
	// Entries are removed lazily, so a heap may hold IDs that are already
	// gone; evictLowestPriorityLocked skips them.
	evictHeaps [4]docIDHeap

	// classifier assigns the tier used above. It is the SAME classifier
	// Recall uses, so the tag vocabulary (summary/decision → P2,
	// audit/finding → P3) governs what survives in the index exactly as it
	// governs what survives a recall budget. Two different answers to
	// "how important is this event" would be worse than either.
	classifier *Classifier

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

// docIDHeap is a min-heap of document IDs (ULID strings). ULIDs encode
// time as their prefix, so the lexicographically smallest ID is the oldest.
// heap.Interface requires a concrete type, not a pointer receiver.
type docIDHeap []string

func (h docIDHeap) Len() int           { return len(h) }
func (h docIDHeap) Less(i, j int) bool { return h[i] < h[j] } // min-heap: smallest = oldest
func (h docIDHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *docIDHeap) Push(x any)        { *h = append(*h, x.(string)) }
func (h *docIDHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

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
		meta:         make(map[string]DocMeta),
		docStems:     make(map[string][]string),
		docTrigrams:  make(map[string][]string),
		classifier:   NewClassifier(),
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
	if ix.meta == nil {
		ix.meta = make(map[string]DocMeta)
	}
	// The reverse maps and the classifier are not persisted either. Nil maps
	// would make the first Add panic on assignment, and a nil classifier
	// would make tierFor fall back to the stored priority — which for agent
	// events is always p3 (F-21), erasing exactly the tag-driven distinction
	// eviction depends on.
	if ix.docStems == nil {
		ix.docStems = make(map[string][]string)
	}
	if ix.docTrigrams == nil {
		ix.docTrigrams = make(map[string][]string)
	}
	if ix.classifier == nil {
		ix.classifier = NewClassifier()
	}
	ix.avgDocLen = j.AvgDocLen
	ix.totalDocs = j.TotalDocs

	// Rebuild the eviction state a loaded index would otherwise lack.
	// docLenSum keeps avgDocLen arithmetic; the heaps decide what gets
	// dropped at the cap. Loaded documents have no recorded tier — their
	// metadata is rebuilt only when the journal is replayed over them — so
	// they go in the lowest tier: they are both the oldest and the least
	// identifiable, and leaving the heaps empty would send every eviction
	// through the O(N) fallback scan instead.
	ix.docLenSum = 0
	for id, l := range ix.docLen {
		ix.docLenSum += l
		ix.evictHeaps[3] = append(ix.evictHeaps[3], id)
	}
	heap.Init(&ix.evictHeaps[3])
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
		ix.evictLowestPriorityLocked()
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
	if ix.meta == nil {
		ix.meta = make(map[string]DocMeta)
	}
	tier := ix.tierFor(e)
	ix.meta[id] = DocMeta{Type: string(e.Type), Source: string(e.Source), Priority: priorityForTier(tier)}

	// Extract searchable text from event
	text := ix.eventText(e)
	tokens := TokenizeFull(text, nil)

	docLen := len(tokens)
	ix.docLen[id] = docLen
	ix.totalDocs++
	ix.docLenSum += docLen
	ix.avgDocLen = float64(ix.docLenSum) / float64(ix.totalDocs)

	// Count term frequencies
	tf := make(map[string]int)
	for _, tok := range tokens {
		stem := Stem(tok)
		tf[stem]++
	}

	// Update posting lists, recording which stems this document touched so
	// removal does not have to rediscover them by scanning every term.
	stems := make([]string, 0, len(tf))
	for stem, freq := range tf {
		pl, ok := ix.stemPL[stem]
		if !ok {
			pl = &PostingList{}
			ix.stemPL[stem] = pl
		}
		pl.IDs = append(pl.IDs, id)
		pl.TFs = append(pl.TFs, uint32(freq))
		stems = append(stems, stem)
	}
	if ix.docStems == nil {
		ix.docStems = make(map[string][]string)
	}
	ix.docStems[id] = stems

	// Also index for trigram search
	ti := NewTrigramIndex()
	ti.Add(id, text)
	// Merge trigram postings into the main index, again recording the
	// reverse mapping for removal.
	trigrams := make([]string, 0, len(ti.postings))
	for tg, ids := range ti.postings {
		pl, ok := ix.trigramPL[tg]
		if !ok {
			pl = &PostingList{}
			ix.trigramPL[tg] = pl
		}
		pl.IDs = append(pl.IDs, ids...)
		trigrams = append(trigrams, tg)
	}
	if ix.docTrigrams == nil {
		ix.docTrigrams = make(map[string][]string)
	}
	ix.docTrigrams[id] = trigrams

	// Push onto this document's tier heap. Stale entries (documents already
	// removed) are skipped at eviction time rather than deleted here —
	// finding an arbitrary element in a heap is O(N), and the eviction path
	// has to pop anyway.
	heap.Push(&ix.evictHeaps[tier], id)
}

// DocMeta carries the per-document facts a search result needs beyond its
// score: the event type and the source that produced it. Kept deliberately
// tiny — this is held for every indexed document (up to MaxIndexDocs).
type DocMeta struct {
	Type   string
	Source string
	// Priority is the classified tier, not the value stored on the event:
	// dfmt_remember coerces agent-written events to p3 and the classifier
	// re-elevates from tags, so this is the one that decides retention.
	Priority Priority
}

// Meta returns the type/source recorded for docID at index time. The zero
// value means the document predates this feature or was never indexed.
// Safe for concurrent reads.
func (ix *Index) Meta(docID string) DocMeta {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.meta[docID]
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
		// The field that identifies a tool event is whatever it acted on.
		// Only "path" was consulted before, so every tool.exec, tool.grep,
		// and tool.glob hit came back with an excerpt of just its own type
		// ("tool.grep") — a search result carrying no information beyond
		// what the type field already says. The command, pattern, or URL is
		// the one thing that tells the agent whether the hit is the call it
		// was looking for.
		for _, key := range []string{"path", "code", "pattern", "query", "url", "subject"} {
			if v, ok := e.Data[key].(string); ok && v != "" {
				return excerptTruncate(string(e.Type)+" "+v, excerptMaxBytes)
			}
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
//
// Cost note: this used to walk every entry in stemPL and every entry in
// trigramPL, rebuilding each posting list, and then recompute avgDocLen
// across all documents. At MaxIndexDocs that ran on EVERY Add — a
// per-insert scan of the whole index. It now visits only the terms the
// document actually contributed to (docStems / docTrigrams) and keeps
// avgDocLen arithmetic via docLenSum.
func (ix *Index) removeLocked(id string) {
	docLen, ok := ix.docLen[id]
	if !ok {
		// No-op: unknown id. Prevents totalDocs from going negative on
		// double-remove.
		return
	}
	delete(ix.docLen, id)
	delete(ix.excerpts, id)
	delete(ix.meta, id)
	ix.docLenSum -= docLen
	if ix.docLenSum < 0 {
		ix.docLenSum = 0
	}
	if ix.totalDocs > 0 {
		ix.totalDocs--
	}

	// Capture the reverse mappings BEFORE deleting them: whether this
	// document HAD them decides whether the slow legacy path below is
	// needed, and reading that back after the delete always answers "no"
	// — which silently reinstated the full-index sweep this function
	// exists to avoid. Caught by profiling, not by a test: every result
	// stayed correct, only the cost changed (a linear scan of every
	// posting list per removal, 2.5 ms/insert at a 20k cap).
	stems, hadStems := ix.docStems[id]
	trigrams, hadTrigrams := ix.docTrigrams[id]

	for _, stem := range stems {
		pl, ok := ix.stemPL[stem]
		if !ok {
			continue
		}
		if removeFromPostingList(pl, id) && len(pl.IDs) == 0 {
			delete(ix.stemPL, stem)
		}
	}
	delete(ix.docStems, id)

	for _, tg := range trigrams {
		pl, ok := ix.trigramPL[tg]
		if !ok {
			continue
		}
		if removeFromPostingList(pl, id) && len(pl.IDs) == 0 {
			delete(ix.trigramPL, tg)
		}
	}
	delete(ix.docTrigrams, id)

	// Legacy path: a document indexed before the reverse maps existed (an
	// index deserialized from disk carries posting lists but no docStems).
	// Without this its ID would linger in the posting lists forever and
	// keep scoring as a hit for a document that is gone.
	if !hadStems && !hadTrigrams {
		ix.purgeFromAllPostings(id)
	}

	if ix.totalDocs == 0 {
		ix.avgDocLen = 0
	} else {
		ix.avgDocLen = float64(ix.docLenSum) / float64(ix.totalDocs)
	}
}

// purgeFromAllPostings is the slow path: a full sweep of every posting
// list. Only reachable for legacy documents (see removeLocked) — the
// reverse-mapped path never calls it.
func (ix *Index) purgeFromAllPostings(id string) {
	for stem, pl := range ix.stemPL {
		if removeFromPostingList(pl, id) && len(pl.IDs) == 0 {
			delete(ix.stemPL, stem)
		}
	}
	for tg, pl := range ix.trigramPL {
		if removeFromPostingList(pl, id) && len(pl.IDs) == 0 {
			delete(ix.trigramPL, tg)
		}
	}
}

// removeFromPostingList drops every occurrence of id from pl, keeping IDs
// and TFs parallel. Reports whether anything was removed.
//
// Posting lists are appended in insertion order, which is ULID order, so
// they are sorted in practice and the common case — evicting the OLDEST
// document — hits the front element. A binary search finds it; if the list
// turns out not to be sorted (two events minted in the same millisecond can
// interleave), the search falls back to a linear scan rather than silently
// leaving the entry behind.
func removeFromPostingList(pl *PostingList, id string) bool {
	idx := sort.SearchStrings(pl.IDs, id)
	if idx >= len(pl.IDs) || pl.IDs[idx] != id {
		idx = -1
		for i, docID := range pl.IDs {
			if docID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return false
		}
	}
	if idx == 0 {
		// Front deletion, which is the case eviction always hits: the
		// document being dropped is the oldest, and posting lists are in
		// insertion (ULID) order, so its entry is the first one. Re-slicing
		// is O(1); the copy-shift below is O(len) and, on a posting list for
		// a common term in a full index, that memmove was the entire cost of
		// an eviction — measured at 3.0 ms/insert with a 20k-document cap
		// versus 0.22 ms at 2k, i.e. still scaling with index size after the
		// reverse maps had removed the term-dictionary scan.
		//
		// The dropped head stays alive in the backing array until the next
		// append reallocates it, which is the standard cost of this trick and
		// is bounded by the array's own size.
		pl.IDs = pl.IDs[1:]
		if len(pl.TFs) > 0 {
			pl.TFs = pl.TFs[1:]
		}
		return true
	}
	pl.IDs = append(pl.IDs[:idx], pl.IDs[idx+1:]...)
	if idx < len(pl.TFs) {
		pl.TFs = append(pl.TFs[:idx], pl.TFs[idx+1:]...)
	}
	return true
}

// evictLowestPriorityLocked removes one document to make room, choosing the
// oldest member of the LOWEST-priority non-empty tier. The caller MUST hold
// ix.mu (write lock).
//
// Age alone was the previous rule, and on a real journal it evicted exactly
// the wrong thing: automatic tool events outnumber deliberate notes roughly
// 200:1, so "oldest first" quietly retired the earliest decisions — the ones
// most likely to explain why the code looks the way it does — while keeping
// every recent `tool.read`. The note survived in the journal, so `dfmt_recall`
// could still show it, but `dfmt_search` could not find it: memory decaying
// from the wrong end.
//
// Within a tier, oldest still goes first. Smallest ULID == earliest timestamp
// because ULIDs encode time in their lexicographically-comparable prefix.
func (ix *Index) evictLowestPriorityLocked() {
	if len(ix.docLen) == 0 {
		return
	}
	// Walk tiers from P4 (index 3) up to P1 (index 0).
	for tier := len(ix.evictHeaps) - 1; tier >= 0; tier-- {
		for len(ix.evictHeaps[tier]) > 0 {
			candidate := heap.Pop(&ix.evictHeaps[tier]).(string)
			if _, live := ix.docLen[candidate]; live {
				ix.removeLocked(candidate)
				return
			}
			// Stale entry (already removed) — keep popping.
		}
	}
	// Heaps exhausted but documents remain: an index deserialized from disk
	// has posting lists and docLen but no tier information. Fall back to the
	// old rule for those rather than refusing to evict, which would let the
	// cap be exceeded indefinitely.
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

// tierFor returns the evictHeaps index for an event: 0 = P1 … 3 = P4.
func (ix *Index) tierFor(e Event) int {
	pri := e.Priority
	if ix.classifier != nil {
		pri = ix.classifier.Classify(e)
	}
	switch pri {
	case PriP1:
		return 0
	case PriP2:
		return 1
	case PriP3:
		return 2
	default:
		return 3
	}
}

// priorityForTier is the inverse of tierFor, for reporting.
func priorityForTier(tier int) Priority {
	switch tier {
	case 0:
		return PriP1
	case 1:
		return PriP2
	case 2:
		return PriP3
	default:
		return PriP4
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
