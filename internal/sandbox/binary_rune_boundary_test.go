package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectBinaryToleratesRuneCutAtSampleEdge: detectBinary sniffs a
// fixed-size prefix, and a multi-byte rune straddling the cut made valid
// text look like invalid UTF-8. Observed: dfmt_read returned "(binary; …)"
// for 25 lines of journal.go whose em dash sat on bytes 510-512.
func TestDetectBinaryToleratesRuneCutAtSampleEdge(t *testing.T) {
	for _, r := range []string{"—", "ğ", "€", "😀"} {
		for pad := 0; pad < len(r); pad++ {
			text := strings.Repeat("a", binaryDetectSampleBytes-pad-1) + r + strings.Repeat("b", 600)
			if _, bin := detectBinary(text); bin {
				t.Errorf("rune %q cut at offset %d flagged as binary", r, pad)
			}
		}
	}
	// Genuinely invalid bytes are still binary.
	if _, bin := detectBinary("abc\xff\xfedef"); !bin {
		t.Error("invalid UTF-8 no longer detected")
	}
}

func TestGrepFindsFileWithRuneOnSniffBoundary(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", binarySniffBytes-1) + "ş\nneedle\n"
	if err := os.WriteFile(filepath.Join(dir, "tr.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "needle"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Errorf("file skipped as binary: %+v (summary %q)", resp.Matches, resp.Summary)
	}
}
