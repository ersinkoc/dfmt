package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/transport"
)

func runRemember(verb string, args []string) int {
	var typ, source string
	var actor string
	var dataJSON string
	var inputTokens, outputTokens, cachedTokens int
	var model string

	// Use the invocation verb ("remember" or "note") so `dfmt note --help`
	// prints "Usage of note:" rather than the misleading "Usage of remember:".
	if verb == "" {
		verb = "remember"
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.StringVar(&typ, "type", "note", "Event type")
	fs.StringVar(&source, "source", "cli", "Event source")
	fs.StringVar(&actor, "actor", "", "Actor")
	fs.StringVar(&dataJSON, "data", "", "JSON data")
	fs.IntVar(&inputTokens, "input-tokens", 0, "LLM input tokens")
	fs.IntVar(&outputTokens, "output-tokens", 0, "LLM output tokens")
	fs.IntVar(&cachedTokens, "cached-tokens", 0, "Cached tokens (savings)")
	fs.StringVar(&model, "model", "", "LLM model name")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	var data map[string]any
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			fmt.Fprintf(os.Stderr, "error: --data is not valid JSON: %v\n", err)
			return 1
		}
	}

	// Add token data if provided
	if inputTokens > 0 || outputTokens > 0 || cachedTokens > 0 || model != "" {
		if data == nil {
			data = make(map[string]any)
		}
		if inputTokens > 0 {
			data["input_tokens"] = inputTokens
		}
		if outputTokens > 0 {
			data["output_tokens"] = outputTokens
		}
		if cachedTokens > 0 {
			data["cached_tokens"] = cachedTokens
		}
		if model != "" {
			data["model"] = model
		}
	}

	params := transport.RememberParams{
		Type:   typ,
		Source: source,
		Actor:  actor,
		Data:   data,
		Tags:   fs.Args(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)
	params.ProjectID = proj

	resp, err := backend.Remember(ctx, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(resp))
	} else {
		fmt.Printf("Recorded: %s at %s\n", resp.ID, resp.TS)
	}

	return 0
}

func runSearch(args []string) int {
	var limit int
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.IntVar(&limit, "limit", 10, "Max results")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	query := fs.Arg(0)
	if query == "" {
		fmt.Fprintf(os.Stderr, "error: query required\n")
		return 1
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	resp, err := backend.Search(ctx, transport.SearchParams{
		Query:     query,
		Limit:     limit,
		ProjectID: proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(resp))
	} else {
		for _, hit := range resp.Results {
			fmt.Printf("[%.2f] %s\n", hit.Score, hit.ID)
		}
	}

	return 0
}

func runRecall(args []string) int {
	var budget int
	var format string
	var save bool

	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.IntVar(&budget, "budget", 4096, "Byte budget")
	fs.StringVar(&format, "format", "md", "Output format (md, json, xml)")
	fs.BoolVar(&save, "save", false, "Write snapshot to .dfmt/last-recall.md (0600) instead of stdout")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	resp, err := backend.Recall(ctx, transport.RecallParams{
		Budget:    budget,
		Format:    format,
		ProjectID: proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if save {
		// Write via Go so permissions are enforced (0600) and we don't rely on
		// shell redirect portability. PreCompact hooks call this.
		outPath := filepath.Join(proj, ".dfmt", "last-recall.md")
		if err := os.WriteFile(outPath, []byte(resp.Snapshot), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", outPath, err)
			return 1
		}
		return 0
	}

	fmt.Println(resp.Snapshot)
	return 0
}

func runTask(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: dfmt task <subject> | dfmt task done <id>\n")
		return 1
	}
	// Help short-circuit BEFORE we treat args[0] as the task subject. Pre-fix
	// `dfmt task --help` happily journaled a task whose subject was the
	// literal string "--help" — exactly the state-mutating UX bug the audit
	// surfaced. Same defense covers `dfmt task done --help`.
	if helpRequested(args) {
		fmt.Println("usage: dfmt task <subject> | dfmt task done <id>")
		return 0
	}

	if args[0] == "done" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: dfmt task done <id>\n")
			return 1
		}
		return runRemember("remember", []string{"-type", string(core.EvtTaskDone), "-data", fmt.Sprintf(`{"id":%q}`, args[1])})
	}

	// Flags must precede the trailing positional arg; otherwise flag.Parse
	// stops at the first non-flag and -type is dropped, silently journaling
	// the task as type "note".
	return runRemember("remember", []string{"-type", string(core.EvtTaskCreate), "-data", fmt.Sprintf(`{"subject":%q}`, args[0])})
}

func runStats(args []string) int {
	// Reject unknown flags and route --help to FlagSet's usage output.
	// Pre-fix the function silently ignored args, so `dfmt stats --help`
	// printed stats instead of help.
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: stats takes no positional arguments\n")
		return 2
	}
	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// v0.6.0: connect to existing daemon, or promote self in-process.
	// No exec.Command spawning. Returns a non-nil *Daemon when this
	// process owns it; the deferred shutdown waits for signal/idle.
	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	// CLI bypasses the daemon-side stats cache: a human running `dfmt
	// stats` twice in a row interprets identical numbers as "DFMT
	// stopped recording", but the cache TTL would hold the same value
	// for 5 seconds. Dashboard polling still gets the cached path
	// because it sets NoCache=false (default).
	resp, err := backend.Stats(ctx, transport.StatsParams{NoCache: true, ProjectID: proj})
	if err != nil {
		if flagJSON {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Println("Could not fetch stats from daemon.")
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(resp))
	} else {
		fmt.Println("DFMT Session Statistics")
		fmt.Println("========================")
		fmt.Printf("Total Events: %d\n\n", resp.EventsTotal)

		if len(resp.EventsByType) > 0 {
			fmt.Println("Events by Type:")
			for t, c := range resp.EventsByType {
				fmt.Printf("  %s: %d\n", t, c)
			}
			fmt.Println()
		}

		if len(resp.EventsByPriority) > 0 {
			fmt.Println("Events by Priority:")
			for p, c := range resp.EventsByPriority {
				fmt.Printf("  %s: %d\n", p, c)
			}
			fmt.Println()
		}

		// LLM token metrics — only meaningful when callers wire usage through dfmt_remember.
		if resp.TotalInputTokens > 0 || resp.TotalOutputTokens > 0 || resp.TokenSavings > 0 {
			fmt.Println("LLM Token Metrics (from dfmt_remember):")
			fmt.Printf("  Input Tokens:  %d\n", resp.TotalInputTokens)
			fmt.Printf("  Output Tokens: %d\n", resp.TotalOutputTokens)
			fmt.Printf("  Cache Savings: %d\n", resp.TokenSavings)
			if resp.CacheHitRate > 0 {
				fmt.Printf("  Cache Hit Rate: %.1f%%\n", resp.CacheHitRate)
			}
			fmt.Println()
		} else if resp.EventsTotal > 0 {
			// Surface why the LLM block is missing instead of letting users
			// guess. The fields are not auto-tracked; the host harness has to
			// pipe usage through dfmt_remember on its side. Closes a recurring
			// "are these stats broken?" question raised during system audits.
			fmt.Println("LLM Token Metrics: not tracked")
			fmt.Println("  Input/output/cache tokens are opt-in: pass input_tokens, output_tokens, and")
			fmt.Println("  cached_tokens to dfmt_remember from the agent harness when it finishes a turn.")
			fmt.Println("  Without that the MCP layer cannot observe API usage on its own — only the byte")
			fmt.Println("  savings below are computed automatically.")
			fmt.Println()
		}

		// MCP byte savings — automatic from sandbox tool calls.
		if resp.TotalRawBytes > 0 {
			fmt.Println("MCP Byte Savings (from intent-filtered tool calls):")
			fmt.Printf("  Raw Bytes:        %d\n", resp.TotalRawBytes)
			fmt.Printf("  Returned Bytes:   %d\n", resp.TotalReturnedBytes)
			fmt.Printf("  Bytes Saved:      %d\n", resp.BytesSaved)
			fmt.Printf("  Compression:      %.1f%%\n", resp.CompressionRatio*100)
			fmt.Println()
		}

		if resp.SessionStart != "" && resp.SessionEnd != "" {
			fmt.Printf("Session: %s -> %s\n", resp.SessionStart, resp.SessionEnd)
		}

		if resp.EventsTotal == 0 {
			fmt.Println("")
			fmt.Println("No events recorded yet. Start using dfmt to record your work.")
		} else {
			fmt.Println()
			fmt.Println("Tip: `dfmt dashboard` opens a live web view of these stats.")
		}
	}

	return 0
}

func runTail(args []string) int {
	var follow bool
	var from string
	var limit int
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	fs.BoolVar(&follow, "follow", false, "Follow new events (Ctrl+C to stop)")
	fs.StringVar(&from, "from", "", "Cursor to stream from (empty = all events)")
	fs.IntVar(&limit, "limit", 0, "Max events to print (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx := context.Background()
	if follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}
	ctx = transport.WithProjectID(ctx, proj)

	events, err := backend.StreamEvents(ctx, from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	count := 0
	for e := range events {
		dataStr := ""
		if e.Data != nil {
			if path, ok := e.Data["path"].(string); ok {
				dataStr = " " + path
			}
		}
		tags := ""
		if len(e.Tags) > 0 {
			tags = fmt.Sprintf(" #%s", strings.Join(e.Tags, " #"))
		}
		fmt.Printf("[%s] %s%s%s\n", e.Priority, e.TS.Format(time.RFC3339), tags, dataStr)
		count++
		if limit > 0 && count >= limit {
			return 0
		}
		if !follow {
			// Non-follow: drain and return.
			continue
		}
	}
	return 0
}
