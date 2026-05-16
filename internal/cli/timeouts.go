package cli

import "time"

// Named context timeouts used by CLI subcommands. Pre-consolidation
// these were inline literals — `5 * time.Second` appeared at 5+ call
// sites, `30 * time.Second` at another 5. Naming them puts the
// semantic bucket (short RPC vs long tool call) at the call site and
// makes a future single-source adjustment land in one place.
//
// Add a new constant here when a new timeout starts showing up at
// more than one call site. One-off timings (e.g., probe windows in
// doctor.go) can stay inline — naming a 3-second timer that's used
// exactly once is more noise than signal.
const (
	// rpcTimeout caps short metadata calls (stats, list, recall, daemon
	// teardown). These are not user-visible long-runners; if the
	// daemon doesn't respond in 5 s something is wrong and the caller
	// should surface the error rather than wait.
	rpcTimeout = 5 * time.Second

	// toolTimeout caps interactive tool calls (exec, read, fetch, edit,
	// write, glob, grep, remember). The sandbox itself enforces a
	// per-call deadline via cfg.Exec.Timeout; this is the outer cap
	// the dispatch layer applies so a runaway tool call doesn't hang
	// the CLI past 30 s even if the daemon-side deadline is misconfigured.
	toolTimeout = 30 * time.Second
)
