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
