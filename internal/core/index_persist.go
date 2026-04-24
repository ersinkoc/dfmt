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

// PersistIndex saves the index and cursor to disk atomically. Each file is
// written to a sibling <name>.tmp, fsynced, and renamed into place. A crash
// mid-write leaves the previous complete file intact instead of a truncated
// stub that would force a full rebuild.
func PersistIndex(index *Index, path string, hiULID string) error {
	if err := writeJSONAtomic(path, index); err != nil {
		return err
	}

	cursorPath := filepath.Join(filepath.Dir(path), "index.cursor")
	cursor := IndexCursor{
		HiULID:    hiULID,
		TokenVer:  TokenizerVersion,
		TotalDocs: index.totalDocs,
		AvgDocLen: index.avgDocLen,
	}
	return writeJSONAtomic(cursorPath, cursor)
}

// writeJSONAtomic encodes v as JSON, then delegates to writeRawAtomic.
func writeJSONAtomic(path string, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeRawAtomic(path, buf)
}

// writeRawAtomic writes buf to a temp file in the same directory, fsyncs,
// then renames onto path. Mode is 0600 since index and cursor contain
// indexed event data that may include sensitive strings. A crash mid-write
// leaves the previous complete file intact rather than a truncated stub.
func writeRawAtomic(path string, buf []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	success = true
	return nil
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
