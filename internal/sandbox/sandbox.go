package sandbox

import (
	"context"
	"time"
)

// Sandbox runs operations on behalf of agents.
type Sandbox interface {
	Exec(ctx context.Context, req ExecReq) (ExecResp, error)
	Read(ctx context.Context, req ReadReq) (ReadResp, error)
	Fetch(ctx context.Context, req FetchReq) (FetchResp, error)
	BatchExec(ctx context.Context, items []any) ([]any, error)
}

// ExecReq is a request to execute code.
type ExecReq struct {
	Code    string            // Code to execute
	Lang    string            // "bash" | "sh" | "node" | "python" | "go" | ...
	Intent  string            // Intent for content filtering
	Timeout time.Duration     // Execution timeout
	Env     map[string]string // Additional environment variables
	Return  string            // "auto" | "raw" | "summary" | "search"
}

// ExecResp is the response from an exec operation.
type ExecResp struct {
	Exit       int            // Exit code
	Stdout     string         // Inline output if small
	Stderr     string         // Inline error output if small
	ChunkSet   string         // Chunk set ID if output was chunked
	Summary    string         // Human-readable summary
	Matches    []ContentMatch // Intent-matched excerpts
	Vocabulary []string       // Distinctive terms
	DurationMs int            // Execution duration in milliseconds
	TimedOut   bool           // True if execution timed out
}

// ReadReq is a request to read a file.
type ReadReq struct {
	Path   string // File path
	Intent string // Intent for content filtering
	Offset int64  // Byte offset to start reading
	Limit  int64  // Maximum bytes to read
	Return string // "auto" | "raw" | "summary" | "search"
}

// ReadResp is the response from a read operation.
type ReadResp struct {
	Content   string         // Inline content if small
	ChunkSet  string         // Chunk set ID if content was chunked
	Summary   string         // Human-readable summary
	Matches   []ContentMatch // Intent-matched excerpts
	Size      int64          // Total file size
	ReadBytes int64          // Bytes actually read
}

// FetchReq is a request to fetch a URL.
type FetchReq struct {
	URL     string // URL to fetch
	Intent  string // Intent for content filtering
	Method  string // HTTP method
	Headers map[string]string
	Body    string
	Return  string // "auto" | "raw" | "summary" | "search"
	Timeout time.Duration
}

// FetchResp is the response from a fetch operation.
type FetchResp struct {
	Status     int // HTTP status code
	Headers    map[string]string
	Body       string         // Inline body if small
	ChunkSet   string         // Chunk set ID if body was chunked
	Summary    string         // Human-readable summary
	Matches    []ContentMatch // Intent-matched excerpts
	Vocabulary []string       // Distinctive terms
	TimedOut   bool           // True if fetch timed out
}

// ContentMatch represents an intent-matched excerpt.
type ContentMatch struct {
	Text   string  // Matched text excerpt
	Score  float64 // Relevance score
	Source string  // Source file or URL
	Line   int     // Line number (for files)
}

// DefaultExecTimeout is the default execution timeout.
const DefaultExecTimeout = 60 * time.Second

// MaxExecTimeout is the maximum allowed execution timeout.
const MaxExecTimeout = 300 * time.Second

// InlineThreshold is the max size for inline return (4 KB).
const InlineThreshold = 4 * 1024

// MediumThreshold is the max size for summary return (64 KB).
const MediumThreshold = 64 * 1024

// MaxRawBytes is the maximum size for raw return (256 KB).
const MaxRawBytes = 256 * 1024
