package sandbox

import (
	"bytes"
	"strings"
	"sync"
)

// builderPool is a sync.Pool for strings.Builder instances reused across
// NormalizeOutput pipeline stages. Reusing builders amortizes allocation
// and reduces GC pressure under high throughput.
//
// Each call to Get returns a builder with a fresh underlying buffer;
// callers must call b.Reset() before reuse. Put returns the builder to
// the pool. The zero value of strings.Builder is ready to use after a
// Reset; no explicit initialization needed.
var builderPool = sync.Pool{
	New: func() any {
		return new(strings.Builder)
	},
}

// GetBuilder borrows a strings.Builder from the pool. The caller MUST
// call b.Reset() before using it and MUST call PoolBuilder when done.
func GetBuilder() *strings.Builder {
	return builderPool.Get().(*strings.Builder)
}

// PoolBuilder returns b to the builder pool. b.Reset() is called
// automatically so the caller does not need to reset before returning.
func PoolBuilderReturn(b *strings.Builder) {
	b.Reset()
	builderPool.Put(b)
}

// bufferPool is a sync.Pool for bytes.Buffer instances reused across
// NormalizeOutput pipeline stages that need io.Writer targets (e.g.,
// YAML encoding in CompactYAML). Reusing buffers reduces allocation
// pressure during large document compaction.
var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// GetBuffer borrows a bytes.Buffer from the pool. The caller MUST call
// b.Reset() before using it and MUST call PoolBufferReturn when done.
func GetBuffer() *bytes.Buffer {
	return bufferPool.Get().(*bytes.Buffer)
}

// PoolBufferReturn returns buf to the buffer pool. b.Reset() is called
// automatically so the caller does not need to reset before returning.
func PoolBufferReturn(buf *bytes.Buffer) {
	buf.Reset()
	bufferPool.Put(buf)
}

// cappedBuffer is an io.Writer that retains at most `limit` bytes and
// silently discards the rest, reporting every write as fully consumed.
//
// It exists so `cmd.Stderr` can be captured without handing an unbounded
// buffer to a subprocess the agent chose. A `go build ./...` across a broken
// monorepo, a test runner in verbose mode, or any tool stuck in a retry loop
// can emit tens of megabytes on stderr; the daemon is long-lived and serves
// every project on the host, so an unbounded sink there is a heap-growth
// vector rather than a hypothetical.
//
// Reporting short writes instead would make exec.Cmd's internal copier
// abandon the stream with ErrShortWrite and surface a spurious failure on
// what is really just a chatty-but-successful command — so the contract here
// is deliberately "absorb and drop", matching the io.Discard-after-cap
// behavior the stdout path gets from io.LimitReader.
//
// Truncation is observable via Truncated(); callers append the
// "(truncated)" marker rather than silently presenting a partial tail as
// complete output.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

// Write implements io.Writer. Always returns len(p), nil — see the type
// comment for why a short write would be wrong here.
func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// String returns the retained bytes.
func (c *cappedBuffer) String() string { return c.buf.String() }

// Len returns the number of retained bytes (never more than limit).
func (c *cappedBuffer) Len() int { return c.buf.Len() }

// Truncated reports whether any bytes were dropped at the cap.
func (c *cappedBuffer) Truncated() bool { return c.truncated }

// headTailBuffer keeps the first headLimit and the last tailLimit bytes of
// a stream and counts what fell between them. Like cappedBuffer it absorbs
// every write.
//
// It replaced a LimitReader on exec stdout, which kept the HEAD and dropped
// the rest without a word: `go test -v ./...` or a long build lost the
// PASS/FAIL verdict printed at the very end, and the return policy's
// tail-bias then presented the end of the truncated buffer as if it were
// the end of the output. The tail is where a command's verdict lives.
type headTailBuffer struct {
	head      []byte
	headLimit int
	tail      []byte // ring buffer once full; tailPos is the oldest byte
	tailLimit int
	tailPos   int
	total     int64
}

func newHeadTailBuffer(headLimit, tailLimit int) *headTailBuffer {
	return &headTailBuffer{headLimit: headLimit, tailLimit: tailLimit}
}

// Write implements io.Writer. Always returns len(p), nil.
func (b *headTailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if room := b.headLimit - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	if b.tailLimit <= 0 || len(p) == 0 {
		return n, nil
	}
	if len(p) >= b.tailLimit {
		b.tail = append(b.tail[:0], p[len(p)-b.tailLimit:]...)
		b.tailPos = 0
		return n, nil
	}
	for len(p) > 0 {
		if len(b.tail) < b.tailLimit {
			k := min(b.tailLimit-len(b.tail), len(p))
			b.tail = append(b.tail, p[:k]...)
			p = p[k:]
			continue
		}
		k := copy(b.tail[b.tailPos:], p)
		b.tailPos = (b.tailPos + k) % b.tailLimit
		p = p[k:]
	}
	return n, nil
}

// Head returns the retained leading bytes.
func (b *headTailBuffer) Head() []byte { return b.head }

// Tail returns the retained trailing bytes in stream order.
func (b *headTailBuffer) Tail() []byte {
	if b.tailPos == 0 {
		return b.tail
	}
	out := make([]byte, 0, len(b.tail))
	out = append(out, b.tail[b.tailPos:]...)
	return append(out, b.tail[:b.tailPos]...)
}

// Total is the number of bytes written, retained or not.
func (b *headTailBuffer) Total() int64 { return b.total }

// Omitted is the number of bytes dropped between head and tail.
func (b *headTailBuffer) Omitted() int64 {
	return b.total - int64(len(b.head)) - int64(len(b.tail))
}
