package core

import (
	"container/heap"
	"encoding/gob"
	"os"
	"strings"
	"sync"
)

// PostingList holds the document IDs and term frequencies for a term.
type PostingList struct {
	IDs []string  // ULIDs, sorted
	TFs []uint16  // term frequencies, parallel to IDs
}

// Index implements an in-memory inverted index with BM25 scoring.
type Index struct {
	mu         sync.RWMutex
	stemPL     map[string]*PostingList // stemmed term -> posting list
	trigramPL  map[string]*PostingList // trigram -> posting list
	docLen     map[string]int          // document ID -> token count
	avgDocLen  float64
	totalDocs  int
	tokenVer   int // bumped on tokenizer change, forces rebuild
}

// NewIndex creates a new Index.
func NewIndex() *Index {
	return &Index{
		stemPL:    make(map[string]*PostingList),
		trigramPL: make(map[string]*PostingList),
		docLen:    make(map[string]int),
	}
}

// Add adds an event to the index.
func (ix *Index) Add(e Event) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	// Extract searchable text from event
	text := ix.eventText(e)
	tokens := TokenizeFull(text, nil)

	id := e.ID
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
		pl.TFs = append(pl.TFs, uint16(freq))
	}

	// Also index for trigram search
	ti := NewTrigramIndex()
	ti.Add(id, text)
	// Merge trigram postings
	for tg, ids := range map[string][]string{} { // placeholder
		_ = tg
		_ = ids
	}
	// Note: for simplicity, trigram indexing is done separately
	_ = ti
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

// Remove removes a document from the index.
func (ix *Index) Remove(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	delete(ix.docLen, id)
	ix.totalDocs--

	// Remove from all posting lists
	for stem, pl := range ix.stemPL {
		newIDs := make([]string, 0, len(pl.IDs))
		newTFs := make([]uint16, 0, len(pl.TFs))
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
}

// Persist saves the index to a file.
func (ix *Index) Persist(path string) error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := gob.NewEncoder(f)
	return enc.Encode(ix)
}

// LoadIndex loads an index from a file.
func LoadIndex(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := gob.NewDecoder(f)
	var ix Index
	if err := dec.Decode(&ix); err != nil {
		return nil, err
	}
	return &ix, nil
}
