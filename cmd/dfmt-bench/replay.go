package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// replayRow is one tool's totals in tokens, accumulated from journal events.
type replayRow struct {
	calls, raw, returned int
	// calls by raw size: under the inline threshold, an output is passed
	// through whole and cannot be compressed by construction.
	small, large int
}

// runReplay prints what a real journal says about token savings: per-tool
// totals plus how many calls were too small to filter at all. The synthetic
// `tokensaving` scenarios show what the pipeline can do; this shows what it
// did.
func runReplay(args []string) {
	path := ".dfmt/journal.jsonl"
	if len(args) > 0 {
		path = args[0]
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	if err := replayJournal(f, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		os.Exit(1)
	}
}

func replayJournal(r io.Reader, w io.Writer) error {
	rows := map[string]*replayRow{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || !strings.HasPrefix(e.Type, "tool.") {
			continue
		}
		raw, okR := num(e.Data["raw_bytes"])
		ret, okT := num(e.Data["returned_bytes"])
		if !okR || !okT {
			continue
		}
		row := rows[e.Type]
		if row == nil {
			row = &replayRow{}
			rows[e.Type] = row
		}
		// Prefer the tokens recorded at event time; bytes/4 for older events.
		rawTok, retTok := tok(raw), tok(ret)
		if v, ok := num(e.Data["raw_tokens"]); ok {
			rawTok = v
		}
		if v, ok := num(e.Data["returned_tokens"]); ok {
			retTok = v
		}
		row.calls++
		row.raw += rawTok
		row.returned += retTok
		if rawTok <= sandbox.InlineTokenThreshold {
			row.small++
		} else {
			row.large++
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no byte-counted tool events in this journal")
		return nil
	}

	names := make([]string, 0, len(rows))
	for n := range rows {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return rows[names[i]].raw > rows[names[j]].raw })

	fmt.Fprintln(w, "\n=== Journal Replay (measured, not simulated) ===")
	fmt.Fprintf(w, "%-12s %7s %10s %10s %8s %8s %8s\n",
		"Tool", "Calls", "Raw tok", "Ret tok", "Saved", "<=1k tok", ">1k tok")
	fmt.Fprintln(w, strings.Repeat("-", 70))
	var t replayRow
	for _, n := range names {
		r := rows[n]
		fmt.Fprintf(w, "%-12s %7d %10d %10d %7.1f%% %8d %8d\n", strings.TrimPrefix(n, "tool."),
			r.calls, r.raw, r.returned, pct(r.raw, r.returned), r.small, r.large)
		t.calls += r.calls
		t.raw += r.raw
		t.returned += r.returned
		t.small += r.small
		t.large += r.large
	}
	fmt.Fprintln(w, strings.Repeat("-", 70))
	fmt.Fprintf(w, "%-12s %7d %10d %10d %7.1f%% %8d %8d\n", "TOTAL",
		t.calls, t.raw, t.returned, pct(t.raw, t.returned), t.small, t.large)
	fmt.Fprintf(w, "\n%.0f%% of calls were small enough to pass through unfiltered (tokens recorded per event; bytes/4 for older events).\n",
		100*float64(t.small)/float64(t.calls))
	return nil
}

func num(v any) (int, bool) {
	f, ok := v.(float64)
	return int(f), ok
}

func tok(b int) int { return (b + 3) / 4 }

func pct(raw, ret int) float64 {
	if raw <= 0 || ret >= raw {
		return 0
	}
	return 100 * float64(raw-ret) / float64(raw)
}
