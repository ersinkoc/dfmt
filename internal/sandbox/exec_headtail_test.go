package sandbox

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestHeadTailBufferKeepsBothEnds(t *testing.T) {
	var stream bytes.Buffer
	for i := 0; i < 1000; i++ {
		stream.WriteString("0123456789")
	}
	src := stream.Bytes()

	// Write in uneven chunks so the ring wraps mid-write.
	b := newHeadTailBuffer(25, 40)
	for off := 0; off < len(src); {
		n := min(7+off%13, len(src)-off)
		if k, err := b.Write(src[off : off+n]); k != n || err != nil {
			t.Fatalf("Write = %d, %v; want %d, nil", k, err, n)
		}
		off += n
	}
	if got, want := string(b.Head()), string(src[:25]); got != want {
		t.Errorf("Head = %q, want %q", got, want)
	}
	if got, want := string(b.Tail()), string(src[len(src)-40:]); got != want {
		t.Errorf("Tail = %q, want %q", got, want)
	}
	if b.Total() != int64(len(src)) || b.Omitted() != int64(len(src)-65) {
		t.Errorf("Total/Omitted = %d/%d, want %d/%d", b.Total(), b.Omitted(), len(src), len(src)-65)
	}
}

func TestHeadTailBufferShortStreamIsVerbatim(t *testing.T) {
	b := newHeadTailBuffer(8, 8)
	_, _ = b.Write([]byte("hello, wor"))
	_, _ = b.Write([]byte("ld"))
	out, omitted := joinHeadTail(b)
	if out != "hello, world" || omitted != 0 {
		t.Errorf("joinHeadTail = %q, %d; want verbatim, 0", out, omitted)
	}
}

// TestExecKeepsTailOfLargeStdout: stdout over MaxRawBytes used to keep only
// the head, silently — a test run's final verdict line was the first thing
// lost.
func TestExecKeepsTailOfLargeStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell loop")
	}
	sb := NewSandbox(t.TempDir())
	code := `i=0; while [ $i -lt 12000 ]; do echo "line $i padding-padding-padding-pad"; i=$((i+1)); done; ` +
		`echo FINAL-VERDICT-OK`
	resp, err := sb.Exec(context.Background(), ExecReq{Code: code, Lang: "bash", Return: "raw"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(resp.RawStdout, "FINAL-VERDICT-OK") {
		t.Errorf("tail lost: RawStdout ends %q", resp.RawStdout[max(0, len(resp.RawStdout)-200):])
	}
	if !strings.Contains(resp.RawStdout, "line 0 ") {
		t.Error("head lost")
	}
	if !strings.Contains(resp.RawStdout, "bytes omitted") {
		t.Error("no in-band marker where stdout was cut")
	}
	if !strings.Contains(resp.Stderr, "omitted from the middle") {
		t.Errorf("stderr does not report the cut: %q", resp.Stderr)
	}
}
