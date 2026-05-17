package timeouts

import "time"

// Named context timeouts used across the DFMT codebase. Pre-consolidation
// these were scattered across packages — each name appearing at two or
// more call sites is a candidate for this file.
//
// One-off timings (e.g., probe windows in doctor.go) stay inline.

const (
	// RPC timeout caps short metadata calls (stats, list, recall, daemon
	// teardown). If the daemon doesn't respond in 5s something is wrong.
	RPC = 5 * time.Second

	// ToolTimeout caps interactive tool calls (exec, read, fetch, edit,
	// write, glob, grep, remember). The sandbox enforces a per-call
	// deadline; this is the outer cap so a runaway call doesn't hang
	// the CLI past 30s even if the daemon-side deadline is misconfigured.
	Tool = 30 * time.Second

	// PerProjectShutdown caps time spent winding down a single project's
	// resources (fswatch Stop, journal Checkpoint, index Persist). "5s
	// matches closeExtraProjects" — per projectres.go:108 comment.
	PerProjectShutdown = 5 * time.Second

	// StatsTTL is how long Stats() returns the memoised result before
	// re-streaming the journal. 5s keeps the dashboard's poll loop cheap.
	StatsTTL = 5 * time.Second

	// DedupTTL is the window in which a re-stash of identical bytes returns
	// the existing chunk-set ID instead of writing a fresh copy.
	DedupTTL = 30 * time.Second

	// StopDrainTimeout caps how long Stop() waits for in-flight connections
	// to finish. Past this, Stop returns and the OS unwinds the rest.
	StopDrain = 5 * time.Second

	// SocketReadIdleTimeout is the deadline applied to each socket read
	// operation. If a connected peer sends nothing within this window the
	// connection is torn down.
	SocketReadIdle = 60 * time.Second
)