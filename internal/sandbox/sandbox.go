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
//
// Offset and Limit are LINES, not bytes: Offset is the 1-based first line to
// return (0 and 1 both mean the top of the file) and Limit is how many lines
// to return (0 = as many as fit). They were byte quantities, which is a unit
// nothing else in an agent's world uses — stack traces, editors, review
// comments and Matches[].Line all speak lines.
type ReadReq struct {
	Path   string // File path
	Intent string // Intent for content filtering
	Offset int64  // 1-based first line to return (0 = from the top)
	Limit  int64  // Maximum number of lines to return (0 = unlimited)
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
	// StartLine/EndLine are the 1-based inclusive line range returned, so a
	// caller can cite file:line without counting newlines itself. Both 0
	// when the requested window lies past the end of the file.
	StartLine int
	EndLine   int
	// TotalLines is the file's line count, or 0 when the read stopped early
	// (line limit or byte ceiling) and the true total was never established.
	// Reporting a partial count as the total would be worse than silence.
	TotalLines int
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

// ContentMatch represents an intent-matched excerpt. Score is intentionally
// dropped from the wire — it drives in-memory ranking but agents never read
// it back, so paying the bytes per match is pure waste. Field names are
// lowercased via JSON tags for the same reason: every match line that crosses
// the wire as `"Text"`/`"Source"`/`"Line"` instead of `"text"`/`"source"`/`"line"`
// burns ~12 bytes that the model has to tokenize.
type ContentMatch struct {
	Text   string  `json:"text"`
	Score  float64 `json:"-"`
	Source string  `json:"source,omitempty"`
	Line   int     `json:"line,omitempty"`
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
//
// Path scopes the walk root. Empty means the working directory; a
// relative value resolves against it; an absolute value is rejected
// unless it lies under the working directory. Files is a basename glob
// applied per visited file (e.g. "*.go"). Both can be combined: Path
// narrows the directory, Files filters the basenames within.
type GrepReq struct {
	Pattern         string `json:"pattern"` // Search pattern (regex)
	Path            string `json:"path"`    // Directory or file to scope the walk to (relative to wd or absolute under wd)
	Files           string `json:"files"`   // Basename glob (e.g., "*.go")
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
	// ReplaceAll opts into replacing every occurrence. Without it an
	// OldString that appears more than once is refused — see Edit for why
	// "replace the first one" is the wrong default.
	ReplaceAll bool `json:"replace_all,omitempty"`
}

// EditResp is the response from an edit operation.
type EditResp struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
	// Replacements is how many occurrences were rewritten. Always 1 unless
	// ReplaceAll was set; surfaced so a caller can tell a broad replace_all
	// from the single edit it may have expected.
	Replacements int `json:"replacements,omitempty"`
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
//
// 15 minutes, raised from 5. The ceiling exists to stop a runaway command
// from pinning an exec slot forever, not to define what counts as a
// reasonable job — and at 300 s it was doing the latter: this repository's
// own `go test ./...` takes ~9 minutes, so the tool that exists to replace
// Bash could not run the project's test suite. A build, a test suite, or a
// container image pull is the normal shape of an agent-driven exec, and
// none of them fit in five minutes on a cold cache.
//
// What still bounds a runaway: the deadline is enforced against the whole
// process tree (see execImpl), the caller's own `timeout` argument is
// usually far lower, execSem caps concurrent execs at 4, and a timed-out
// call now returns a clean verdict with partial output rather than hanging.
//
// Deliberately NOT solved here: a job that outlives even this — a long
// migration, a soak test — needs an async job handle (submit, poll, fetch
// output), which is a new MCP tool, a job store, and a cancellation story.
// That is a feature with its own ADR, not a constant.
const MaxExecTimeout = 900 * time.Second

// HardMaxExecTimeout is the absolute ceiling the sandbox enforces, as
// opposed to MaxExecTimeout, which is the policy ceiling the transport
// applies to SYNCHRONOUS calls.
//
// The two differ because the reasons differ. A synchronous call is capped at
// 15 minutes because something is holding an RPC open and an exec slot with
// it. An async job (transport/exec_jobs.go) holds neither — it was submitted
// and answered — so the only thing bounding it is "a job that hangs must not
// live forever". Two hours covers a migration or a soak test.
//
// Without this split the sandbox's own clamp silently capped async jobs at
// the synchronous ceiling: the job context allowed two hours, execImpl cut
// it to 900s, and the feature quietly did not do the one thing it exists for.
const HardMaxExecTimeout = 2 * time.Hour

// execWaitDelay is how long Wait may keep blocking after the deadline has
// fired and the process tree has been killed. It exists for the descendant
// that survives the kill — a process in another session, or one wedged in
// uninterruptible I/O — because such a descendant still holds the inherited
// stdout pipe, and an unbounded Wait would sit on it for as long as that
// process lives. Past this grace period os/exec closes the pipes and Wait
// returns exec.ErrWaitDelay, so the caller gets its partial output and its
// timed_out verdict on schedule.
//
// 2s is generous for "did the kill land": a tree that has been SIGKILLed or
// TerminateProcess'd closes its handles in milliseconds. The cost of the
// window is only paid on the timeout path.
const execWaitDelay = 2 * time.Second

// DefaultFetchTimeout is the default HTTP fetch timeout when the caller
// does not supply one (the inner sandbox Fetch also defaults to 30s).
const DefaultFetchTimeout = 30 * time.Second

// MaxFetchTimeout caps the agent-supplied fetch timeout. Closes V-17:
// without a ceiling, an agent could set Timeout=2147483647 (~68 years)
// and pin a fetch-semaphore slot indefinitely against a slow-loris
// server. Combined with the fetchSem cap of 8, a malicious agent could
// otherwise lock the entire fetch capability for arbitrary duration.
const MaxFetchTimeout = 5 * time.Minute

// InlineThreshold is the legacy byte-based inline cap (4 KB). Retained
// for tests that pin the historical boundary and for the Windows
// PowerShell truncation fallback. The active policy boundary is
// InlineTokenThreshold (ADR-0012).
const InlineThreshold = 4 * 1024

// MediumThreshold is the legacy byte-based summary cap (64 KB).
// Retained alongside InlineThreshold; active policy uses
// MediumTokenThreshold.
const MediumThreshold = 64 * 1024

// MaxRawBytes caps raw-return mode + Windows PowerShell stdout
// truncation (256 KB). Stays byte-based — it protects against
// runaway-output blowup, which is naturally a byte concern.
const MaxRawBytes = 256 * 1024

// InlineTokenThreshold is the policy gate for the inline tier
// (ADR-0012). Bodies under this many approximated tokens ship inline;
// over it, they go through summary+matches. ≈4 KB English text /
// ≈2-3 KB CJK — the latter being exactly the calibration point: a
// 1024-token CJK body costs the same as a 1024-token English body
// even though their byte counts differ by ~2×.
const InlineTokenThreshold = 1024

// MediumTokenThreshold is the policy gate for the medium tier where
// the auto policy starts shipping more matches/vocabulary. ≈64 KB
// English text. ADR-0012.
const MediumTokenThreshold = 16384

// TailTokens is the budget for the "tail" snippet shown when no
// keyword matches landed. ≈2 KB English / ≈30-50 lines of typical
// log output. ADR-0012.
const TailTokens = 512
