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
	Glob(ctx context.Context, req GlobReq) (GlobResp, error)
	Grep(ctx context.Context, req GrepReq) (GrepResp, error)
	Edit(ctx context.Context, req EditReq) (EditResp, error)
	Write(ctx context.Context, req WriteReq) (WriteResp, error)
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
//
// Stdout is what the agent should see (after the return-policy filter has
// optionally replaced large output with summary+matches). RawStdout is the
// full pre-filter output the daemon stashes into the content store so the
// agent can fetch the raw bytes later via the chunk-set ID. The two fields
// were previously the same; that meant when the policy dropped Stdout for a
// large output, the content store ended up storing nothing — the chunk set
// ID was a pointer to empty bytes.
type ExecResp struct {
	Exit       int            // Exit code
	Stdout     string         // Filtered output for the client (may be empty when policy excludes it)
	RawStdout  string         // Full pre-filter output for content stashing; never sent to the client
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

// ReadResp is the response from a read operation. Content is filtered for
// the client; RawContent is the full pre-filter content for stashing. See
// ExecResp for the rationale.
type ReadResp struct {
	Content    string         // Filtered content for the client
	RawContent string         // Full pre-filter content for stashing; never sent to the client
	ChunkSet   string         // Chunk set ID if content was chunked
	Summary    string         // Human-readable summary
	Matches    []ContentMatch // Intent-matched excerpts
	Size       int64          // Total file size
	ReadBytes  int64          // Bytes actually read
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

// FetchResp is the response from a fetch operation. Body is filtered for the
// client; RawBody is the full pre-filter body for stashing. See ExecResp for
// the rationale.
type FetchResp struct {
	Status     int // HTTP status code
	Headers    map[string]string
	Body       string         // Filtered body for the client
	RawBody    string         // Full pre-filter body for stashing; never sent to the client
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

// GlobReq is a request to glob files.
type GlobReq struct {
	Pattern string // Glob pattern (e.g., "**/*.go")
	Intent  string // Intent for filtering results
}

// GlobResp is the response from a glob operation.
type GlobResp struct {
	Files   []string       `json:"files"`   // Matched file paths
	Matches []ContentMatch `json:"matches"` // Intent-matched excerpts
}

// GrepReq is a request to grep files.
type GrepReq struct {
	Pattern         string `json:"pattern"` // Search pattern (regex)
	Files           string `json:"files"`   // File pattern (e.g., "*.go")
	Intent          string `json:"intent"`  // Intent for filtering results
	CaseInsensitive bool   `json:"case_insensitive"`
	Context         int    `json:"context"` // Lines of context around matches
}

// GrepResp is the response from a grep operation.
type GrepResp struct {
	Matches []GrepMatch `json:"matches"` // Matches with line numbers
	Summary string      `json:"summary"`
}

// GrepMatch represents a single grep match.
type GrepMatch struct {
	File    string `json:"file"`    // File path
	Line    int    `json:"line"`    // Line number
	Content string `json:"content"` // Line content
}

// EditReq is a request to edit a file.
type EditReq struct {
	Path      string `json:"path"`       // File path
	OldString string `json:"old_string"` // String to replace
	NewString string `json:"new_string"` // Replacement string
}

// EditResp is the response from an edit operation.
type EditResp struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// WriteReq is a request to write a file.
type WriteReq struct {
	Path    string `json:"path"`    // File path
	Content string `json:"content"` // Content to write
}

// WriteResp is the response from a write operation.
type WriteResp struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
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
