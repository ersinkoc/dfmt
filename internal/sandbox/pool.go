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