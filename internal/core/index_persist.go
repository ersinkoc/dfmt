package core

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TokenizerVersion tracks changes to the tokenizer that require rebuild.
const TokenizerVersion = 1

// IndexCursor tracks the state needed to resume indexing.
type IndexCursor struct {
	HiULID    string  `json:"hi_ulid"`
	TokenVer  int     `json:"token_ver"`
	TotalDocs int     `json:"total_docs"`
	AvgDocLen float64 `json:"avg_doc_len"`
}

// PersistIndex saves the index and cursor to disk using JSON.
func PersistIndex(index *Index, path string, hiULID string) error {
	// Write index data
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if err := enc.Encode(index); err != nil {
		return err
	}

	// Write cursor
	cursorPath := filepath.Join(filepath.Dir(path), "index.cursor")
	cursor := IndexCursor{
		HiULID:    hiULID,
		TokenVer:  TokenizerVersion,
		TotalDocs: index.totalDocs,
		AvgDocLen: index.avgDocLen,
	}

	cf, err := os.Create(cursorPath)
	if err != nil {
		return err
	}
	defer cf.Close()

	enc = json.NewEncoder(cf)
	return enc.Encode(cursor)
}

// LoadIndexWithCursor loads an index and cursor, returns whether rebuild needed.
func LoadIndexWithCursor(indexPath, cursorPath string) (*Index, *IndexCursor, bool, error) {
	// Load cursor first
	cursor, err := loadCursor(cursorPath)
	if err != nil {
		// No cursor = need full rebuild
		return nil, nil, true, nil
	}

	// Check tokenizer version
	if cursor.TokenVer != TokenizerVersion {
		return nil, nil, true, nil
	}

	// Load index
	index, err := LoadIndex(indexPath)
	if err != nil {
		return nil, nil, true, nil
	}

	return index, cursor, false, nil
}

func loadCursor(path string) (*IndexCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cursor IndexCursor
	dec := json.NewDecoder(f)
	if err := dec.Decode(&cursor); err != nil {
		return nil, err
	}
	return &cursor, nil
}
