package core

import (
	"container/heap"
	"encoding/json"
	"os"
	"strings"
	"sync"
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
}

// NewIndex creates a new Index.
func NewIndex() *Index {
	return &Index{
		stemPL:    make(map[string]*PostingList),
		trigramPL: make(map[string]*PostingList),
		docLen:    make(map[string]int),
	}
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

	ix.stemPL = j.StemPL
	ix.trigramPL = j.TrigramPL
	ix.docLen = j.DocLen
	ix.avgDocLen = j.AvgDocLen
	ix.totalDocs = j.TotalDocs
	return nil
}

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

// SearchBM25 searches the index using BM25.
func (ix *Index) SearchBM25(query string, limit int) []ScoredHit {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	tokens := TokenizeFull(query, nil)
	if len(tokens) == 0 {
		return nil
	}

	// Find smallest posting list to iterate
	var smallest *PostingList
	for _, tok := range tokens {
		stem := Stem(tok)
		if pl, ok := ix.stemPL[stem]; ok {
			if smallest == nil || len(pl.IDs) < len(smallest.IDs) {
				smallest = pl
			}
		}
	}

	if smallest == nil {
		return nil
	}

	// Score documents
	scores := make(map[string]float64)
	bm := NewBM25Okapi()

	for i, docID := range smallest.IDs {
		tf := int(smallest.TFs[i])
		docLen := ix.docLen[docID]
		df := len(smallest.IDs)
		score := bm.Score(tf, docLen, ix.avgDocLen, df, ix.totalDocs)
		scores[docID] = score
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

	if _, ok := ix.docLen[id]; !ok {
		// No-op: unknown id. Prevents totalDocs from going negative on
		// double-remove.
		return
	}
	delete(ix.docLen, id)
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

// Persist saves the index to a file using JSON serialization.
func (ix *Index) Persist(path string) error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	return enc.Encode(ix)
}

// LoadIndex loads an index from a file using JSON deserialization.
func LoadIndex(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var ix Index
	dec := json.NewDecoder(f)
	if err := dec.Decode(&ix); err != nil {
		return nil, err
	}
	return &ix, nil
}
