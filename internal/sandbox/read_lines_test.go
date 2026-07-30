package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dfmt_read's offset/limit were BYTES, and nothing in the response said which
// lines came back. An agent that knew a function started "around line 400"
// had to guess a byte offset, and could not cite file:line from what it got —
// which is why code work kept falling back to the native Read tool.

func lineFile(t *testing.T, lines int, trailingNewline bool) (*SandboxImpl, string) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString(fmt.Sprintf("line %d", i))
		if i < lines || trailingNewline {
			b.WriteString("\n")
		}
	}
	name := "sample.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	return NewSandbox(dir), name
}

func TestReadWindowsByLine(t *testing.T) {
	sb, name := lineFile(t, 100, true)

	resp, err := sb.Read(context.Background(), ReadReq{Path: name, Offset: 40, Limit: 3, Return: "raw"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if resp.Content != "line 40\nline 41\nline 42\n" {
		t.Errorf("Content = %q, want lines 40-42", resp.Content)
	}
	if resp.StartLine != 40 || resp.EndLine != 42 {
		t.Errorf("window = %d-%d, want 40-42", resp.StartLine, resp.EndLine)
	}
}

func TestReadReportsTotalLinesWhenFullyScanned(t *testing.T) {
	sb, name := lineFile(t, 25, true)

	resp, err := sb.Read(context.Background(), ReadReq{Path: name, Return: "raw"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if resp.TotalLines != 25 {
		t.Errorf("TotalLines = %d, want 25", resp.TotalLines)
	}
	if resp.StartLine != 1 || resp.EndLine != 25 {
		t.Errorf("window = %d-%d, want 1-25", resp.StartLine, resp.EndLine)
	}
}

// A partial read must not claim a total it never established — reporting the
// lines it happened to see as "the file's length" is worse than saying
// nothing, because the caller cannot tell the difference.
func TestReadOmitsTotalLinesOnPartialRead(t *testing.T) {
	sb, name := lineFile(t, 100, true)

	resp, err := sb.Read(context.Background(), ReadReq{Path: name, Limit: 5, Return: "raw"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if resp.TotalLines != 0 {
		t.Errorf("TotalLines = %d, want 0 (unknown) after a limited read", resp.TotalLines)
	}
	if resp.EndLine != 5 {
		t.Errorf("EndLine = %d, want 5", resp.EndLine)
	}
}

// Full reads must round-trip byte-for-byte: an anchor an agent lifts out of a
// read has to match the file when it comes back through dfmt_edit.
func TestReadRoundTripsExactBytes(t *testing.T) {
	for _, trailing := range []bool{true, false} {
		sb, name := lineFile(t, 12, trailing)
		onDisk, err := os.ReadFile(filepath.Join(sb.wd, name))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		resp, err := sb.Read(context.Background(), ReadReq{Path: name, Return: "raw"})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if resp.RawContent != string(onDisk) {
			t.Errorf("trailingNewline=%v: content did not round-trip\n got %q\nwant %q",
				trailing, resp.RawContent, string(onDisk))
		}
	}
}

// An offset past the end is a coherent question with an empty answer.
func TestReadOffsetPastEndOfFile(t *testing.T) {
	sb, name := lineFile(t, 10, true)

	resp, err := sb.Read(context.Background(), ReadReq{Path: name, Offset: 500, Return: "raw"})
	if err != nil {
		t.Fatalf("Read past EOF should not error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, want empty", resp.Content)
	}
	if resp.StartLine != 0 || resp.EndLine != 0 {
		t.Errorf("window = %d-%d, want 0-0 for an empty result", resp.StartLine, resp.EndLine)
	}
	if resp.TotalLines != 10 {
		t.Errorf("TotalLines = %d, want 10 — the scan reached the end", resp.TotalLines)
	}
}

// Offset 0 and 1 both mean "from the top"; an agent should not have to know
// which convention this tool picked.
func TestReadOffsetZeroAndOneAgree(t *testing.T) {
	sb, name := lineFile(t, 8, true)
	ctx := context.Background()

	zero, err := sb.Read(ctx, ReadReq{Path: name, Offset: 0, Limit: 2, Return: "raw"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	one, err := sb.Read(ctx, ReadReq{Path: name, Offset: 1, Limit: 2, Return: "raw"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if zero.Content != one.Content || zero.Content != "line 1\nline 2\n" {
		t.Errorf("offset 0 = %q, offset 1 = %q, want both to start at line 1", zero.Content, one.Content)
	}
}

// Deep windows must stay reachable in files larger than the byte ceiling:
// the scan skips without buffering, so line 30 000 is available even though
// the whole file would not fit in MaxSandboxReadBytes.
func TestReadReachesDeepLinesInLargeFile(t *testing.T) {
	dir := t.TempDir()
	name := "big.log"
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// ~200 bytes per line × 40 000 lines ≈ 8 MiB, twice MaxSandboxReadBytes.
	filler := strings.Repeat("x", 190)
	for i := 1; i <= 40000; i++ {
		if _, err := fmt.Fprintf(f, "line %d %s\n", i, filler); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = f.Close()

	sb := NewSandbox(dir)
	resp, err := sb.Read(context.Background(), ReadReq{Path: name, Offset: 30000, Limit: 1, Return: "raw"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.HasPrefix(resp.Content, "line 30000 ") {
		t.Errorf("Content starts %q, want line 30000 — a deep window must stay reachable "+
			"in a file bigger than the byte ceiling", truncate(resp.Content, 40))
	}
	if resp.StartLine != 30000 || resp.EndLine != 30000 {
		t.Errorf("window = %d-%d, want 30000-30000", resp.StartLine, resp.EndLine)
	}
}
