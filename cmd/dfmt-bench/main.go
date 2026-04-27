package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// Benchmark result
type Result struct {
	Name      string
	OpsPerSec float64
	Duration  time.Duration
	Bytes     int
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--help" {
		fmt.Println(`dfmt bench - Benchmark DFMT operations

Usage: dfmt bench [operation]

Operations:
  tokenize     Benchmark tokenization
  index        Benchmark indexing
  search       Benchmark search
  exec         Benchmark sandbox execution
  tokensaving  Measure end-to-end token-savings vs legacy pipeline
  all          Run all benchmarks (default)`)
		return
	}

	op := "all"
	if len(os.Args) > 1 {
		op = os.Args[1]
	}

	if op == "tokensaving" {
		runTokenSavingReport()
		return
	}

	var results []Result

	switch op {
	case "all":
		results = append(results, benchTokenize()...)
		results = append(results, benchIndex()...)
		results = append(results, benchSearch()...)
		results = append(results, benchExec()...)
		// Token-savings report writes its own table; trigger after the
		// numeric results above have printed so users see both views.
		defer runTokenSavingReport()
	case "tokenize":
		results = append(results, benchTokenize()...)
	case "index":
		results = append(results, benchIndex()...)
	case "search":
		results = append(results, benchSearch()...)
	case "exec":
		results = append(results, benchExec()...)
	default:
		fmt.Printf("unknown operation: %s\n", op)
		os.Exit(1)
	}

	// Print results
	fmt.Println("\n=== Benchmark Results ===")
	fmt.Printf("%-20s %15s %12s %10s\n", "Operation", "Ops/sec", "Duration", "Bytes")
	fmt.Println(strings.Repeat("-", 60))
	for _, r := range results {
		fmt.Printf("%-20s %15.2f %12s %10d\n", r.Name, r.OpsPerSec, r.Duration.Round(time.Millisecond), r.Bytes)
	}
}

func benchTokenize() []Result {
	text := `The quick brown fox jumps over the lazy dog. This is a sample text
for benchmarking tokenization performance. The tokenize function should
handle multiple languages, punctuation, and various text formats efficiently.`

	var results []Result

	// Small text
	start := time.Now()
	iterations := 10000
	for range iterations {
		core.Tokenize(text)
	}
	duration := time.Since(start)
	results = append(results, Result{
		Name:      "tokenize/small",
		OpsPerSec: float64(iterations) / duration.Seconds(),
		Duration:  duration,
		Bytes:     len(text),
	})

	// Large text (10x)
	largeText := strings.Repeat(text+" ", 10)
	start = time.Now()
	iterations = 1000
	for range iterations {
		core.Tokenize(largeText)
	}
	duration = time.Since(start)
	results = append(results, Result{
		Name:      "tokenize/large",
		OpsPerSec: float64(iterations) / duration.Seconds(),
		Duration:  duration,
		Bytes:     len(largeText),
	})

	return results
}

func benchIndex() []Result {
	idx := core.NewIndex()

	// Add events
	eventCount := 100
	for i := range eventCount {
		e := core.Event{
			ID:   string(core.NewULID(time.Now())),
			TS:   time.Now(),
			Type: core.EvtFileEdit,
			Data: map[string]any{
				"message": fmt.Sprintf("edited file%d.go: added feature %d", i%10, i),
			},
		}
		idx.Add(e)
	}

	var results []Result

	// Index add
	start := time.Now()
	iterations := 1000
	for range iterations {
		e := core.Event{
			ID:   string(core.NewULID(time.Now())),
			TS:   time.Now(),
			Type: core.EvtNote,
			Data: map[string]any{"message": "benchmark note"},
		}
		idx.Add(e)
	}
	duration := time.Since(start)
	results = append(results, Result{
		Name:      "index/add",
		OpsPerSec: float64(iterations) / duration.Seconds(),
		Duration:  duration,
		Bytes:     eventCount,
	})

	return results
}

func benchSearch() []Result {
	idx := core.NewIndex()

	// Index some events
	terms := []string{"error", "file", "edit", "commit", "push", "task", "note", "search"}
	for i := range 50 {
		e := core.Event{
			ID:   string(core.NewULID(time.Now())),
			TS:   time.Now(),
			Type: core.EvtNote,
			Data: map[string]any{
				"message": fmt.Sprintf(
					"%s %d: testing search performance for %s operations",
					terms[i%len(terms)], i, terms[i%len(terms)],
				),
			},
		}
		idx.Add(e)
	}

	var results []Result

	// BM25 search
	start := time.Now()
	iterations := 500
	for range iterations {
		idx.SearchBM25("file edit commit", 10)
	}
	duration := time.Since(start)
	results = append(results, Result{
		Name:      "search/bm25",
		OpsPerSec: float64(iterations) / duration.Seconds(),
		Duration:  duration,
		Bytes:     50,
	})

	return results
}

func benchExec() []Result {
	wd, _ := os.Getwd()
	sb := sandbox.NewSandbox(wd)

	ctx := context.Background()

	var results []Result

	// Small exec (echo)
	start := time.Now()
	iterations := 50
	for range iterations {
		sb.Exec(ctx, sandbox.ExecReq{
			Code: "echo 'hello'",
			Lang: "bash",
		})
	}
	duration := time.Since(start)
	results = append(results, Result{
		Name:      "exec/echo",
		OpsPerSec: float64(iterations) / duration.Seconds(),
		Duration:  duration,
		Bytes:     6,
	})

	// Medium exec (ls)
	start = time.Now()
	iterations = 20
	for range iterations {
		sb.Exec(ctx, sandbox.ExecReq{
			Code: "ls -la /tmp",
			Lang: "bash",
		})
	}
	duration = time.Since(start)
	results = append(results, Result{
		Name:      "exec/ls",
		OpsPerSec: float64(iterations) / duration.Seconds(),
		Duration:  duration,
		Bytes:     0,
	})

	_ = bytes.Buffer{}
	return results
}
