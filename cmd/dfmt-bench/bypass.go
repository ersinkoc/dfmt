package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// nativeToDFMT maps the host's native tools to the DFMT tool that replaces
// each one (the table in AGENTS.md).
var nativeToDFMT = map[string]string{
	"Bash":     "mcp__dfmt__dfmt_exec",
	"Read":     "mcp__dfmt__dfmt_read",
	"Grep":     "mcp__dfmt__dfmt_grep",
	"Glob":     "mcp__dfmt__dfmt_glob",
	"Edit":     "mcp__dfmt__dfmt_edit",
	"Write":    "mcp__dfmt__dfmt_write",
	"WebFetch": "mcp__dfmt__dfmt_fetch",
}

// bypassRow counts one native tool and its DFMT replacement.
type bypassRow struct {
	native, viaDFMT int
	// Result tokens of the native calls whose result was seen, and how many
	// of those were above the inline threshold (where DFMT would filter).
	results, tokens, large int
}

// runBypass reads an agent transcript and reports how often the agent used a
// native tool where a DFMT one exists, and how large the native results were.
// It is a command the user runs on purpose: the daemon never reads agent
// transcripts. Only tool names and result sizes are kept; no content is.
func runBypass(args []string) {
	paths, err := transcriptPaths(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bypass: %v\n", err)
		os.Exit(1)
	}
	if err := bypassFiles(paths); err != nil {
		fmt.Fprintf(os.Stderr, "bypass: %v\n", err)
		os.Exit(1)
	}
}

// bypassFiles exists so the files' deferred Close runs before runBypass exits.
func bypassFiles(paths []string) error {
	var files []io.Reader
	var opened []*os.File
	defer func() {
		for _, f := range opened {
			_ = f.Close() // read-only: a Close error loses nothing
		}
	}()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		opened = append(opened, f)
		files = append(files, f)
	}
	return bypassReport(files, os.Stdout)
}

// transcriptPaths resolves the argument: a file, a directory of *.jsonl, or —
// with no argument — Claude Code's transcript directory for the cwd.
func transcriptPaths(args []string) ([]string, error) {
	dir := ""
	if len(args) > 0 {
		dir = args[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		slug := strings.NewReplacer("/", "-", ".", "-", "\\", "-", ":", "-").Replace(cwd)
		dir = filepath.Join(home, ".claude", "projects", slug)
	}
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%v (pass a transcript .jsonl or a directory of them)", err)
	}
	if !st.IsDir() {
		return []string{dir}, nil
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("no .jsonl transcripts in %s", dir)
	}
	return paths, nil
}

func bypassReport(files []io.Reader, w io.Writer) error {
	ew := &errWriter{w: w}
	rows := map[string]*bypassRow{}
	row := func(native string) *bypassRow {
		if rows[native] == nil {
			rows[native] = &bypassRow{}
		}
		return rows[native]
	}
	dfmtOf := map[string]string{}
	for n, d := range nativeToDFMT {
		dfmtOf[d] = n
	}

	for _, r := range files {
		pending := map[string]string{} // tool_use id -> native tool name
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var line struct {
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil || len(line.Message.Content) == 0 || line.Message.Content[0] != '[' {
				continue
			}
			var blocks []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			}
			if json.Unmarshal(line.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					if _, ok := nativeToDFMT[b.Name]; ok {
						row(b.Name).native++
						pending[b.ID] = b.Name
					} else if n, ok := dfmtOf[b.Name]; ok {
						row(n).viaDFMT++
					}
				case "tool_result":
					n, ok := pending[b.ToolUseID]
					if !ok {
						continue
					}
					t := sandbox.ApproxTokens(resultText(b.Content))
					rr := row(n)
					rr.results++
					rr.tokens += t
					if t > sandbox.InlineTokenThreshold {
						rr.large++
					}
				}
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(rows))
	for n := range rows {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return rows[names[i]].tokens > rows[names[j]].tokens })

	ew.println("\n=== Native-tool bypass (from the agent transcript) ===")
	ew.printf("%-9s %7s %7s %7s %11s %8s\n", "Native", "Native", "DFMT", "DFMT %", "Native tok", ">1k tok")
	ew.println(strings.Repeat("-", 56))
	var tn, td, tt, tl int
	for _, n := range names {
		r := rows[n]
		ew.printf("%-9s %7d %7d %6.0f%% %11d %8d\n", n, r.native, r.viaDFMT,
			share(r.viaDFMT, r.native+r.viaDFMT), r.tokens, r.large)
		tn += r.native
		td += r.viaDFMT
		tt += r.tokens
		tl += r.large
	}
	ew.println(strings.Repeat("-", 56))
	ew.printf("%-9s %7d %7d %6.0f%% %11d %8d\n", "TOTAL", tn, td, share(td, tn+td), tt, tl)
	ew.println("\nNative tok = approximate tokens of the native results seen. Only the >1k column is output")
	ew.println("DFMT would have filtered; the rest passes through its inline tier unchanged either way.")
	return ew.err
}

// resultText flattens a tool_result's content: a string, or a list of text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

func share(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(part) / float64(total)
}
