//go:build ignore

// coverage-gate.go — package-level coverage threshold check.
//
// Reads a Go coverage profile, aggregates raw statement counts per package,
// and compares each tracked package against the threshold documented in
// CLAUDE.md / AGENTS.md.
//
// Usage:
//
//	go run scripts/coverage-gate.go [profile] [--strict]
//
//	profile    Path to the coverage profile (default: cover.out).
//	--strict   Exit 1 when any tracked package is below target. Without this
//	           flag the tool is informational — prints the table and exits 0.
//	           CI starts informational; flip to --strict once thresholds met.
//
// Why parse the raw profile instead of `go tool cover -func`:
//
//	The -func report rounds per-function percentages and aggregating those
//	loses precision. The raw profile gives integer statement counts, which
//	sum cleanly into a per-package ratio.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// target lists the (package, min-coverage-%) pairs we gate on. Sub-packages
// (e.g. internal/transport/sub) roll up into their tracked parent. Order is
// preserved in the output for deterministic diffs across CI runs.
type target struct {
	pkg string
	min float64
}

var targets = []target{
	{"internal/core", 90},
	{"internal/transport", 85},
	{"internal/daemon", 80},
	{"internal/cli", 75},
}

type stats struct{ covered, total int }

func main() {
	profile := "cover.out"
	strict := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--strict":
			strict = true
		case "-h", "--help":
			fmt.Println("usage: coverage-gate [profile] [--strict]")
			return
		default:
			if !strings.HasPrefix(a, "-") {
				profile = a
			}
		}
	}

	f, err := os.Open(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coverage-gate: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()

	pkgStats, err := parse(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coverage-gate: parse: %v\n", err)
		os.Exit(2)
	}

	rows := make([]row, 0, len(targets))
	fail := false
	for _, t := range targets {
		s := pkgStats[t.pkg]
		var actual float64
		if s.total > 0 {
			actual = float64(s.covered) * 100.0 / float64(s.total)
		}
		status := "OK"
		switch {
		case s.total == 0:
			status = "NO DATA"
		case actual < t.min:
			status = "BELOW"
			fail = true
		}
		rows = append(rows, row{
			pkg: t.pkg, covered: s.covered, total: s.total,
			actual: actual, target: t.min, status: status,
		})
	}

	printTable(os.Stdout, rows)
	writeStepSummary(rows)

	if fail && strict {
		fmt.Fprintln(os.Stderr, "\ncoverage-gate: one or more packages below target (strict mode)")
		os.Exit(1)
	}
	if fail {
		fmt.Println("\ncoverage-gate: informational — not failing the build.")
		fmt.Println("Flip to --strict once the targets are met.")
	}
}

type row struct {
	pkg            string
	covered, total int
	actual, target float64
	status         string
}

// parse reads a Go coverage profile and aggregates per tracked package.
//
// Profile line format (one per line after the "mode:" header):
//
//	<import-path>/<file>:<startLine.startCol>,<endLine.endCol> <numStmts> <hitCount>
func parse(r io.Reader) (map[string]*stats, error) {
	out := make(map[string]*stats, len(targets))
	for _, t := range targets {
		out[t.pkg] = &stats{}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			// "mode: atomic" — skip.
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		col := strings.IndexByte(fields[0], ':')
		if col < 0 {
			continue
		}
		rel := stripModulePrefix(fields[0][:col])
		pkg := path.Dir(rel)

		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		hits, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}

		for _, t := range targets {
			if pkg == t.pkg || strings.HasPrefix(pkg, t.pkg+"/") {
				out[t.pkg].total += stmts
				if hits > 0 {
					out[t.pkg].covered += stmts
				}
				break
			}
		}
	}
	return out, sc.Err()
}

// stripModulePrefix turns "github.com/ersinkoc/dfmt/internal/core/file.go"
// into "internal/core/file.go". Three path components (host, org, repo) are
// the standard Go module shape and what this repo uses; we don't try to
// generalize because the script only ever runs against this module's profile.
func stripModulePrefix(p string) string {
	parts := strings.SplitN(p, "/", 4)
	if len(parts) < 4 {
		return p
	}
	return parts[3]
}

func printTable(w io.Writer, rows []row) {
	fmt.Fprintf(w, "%-22s %10s %10s %9s %8s  %s\n",
		"PACKAGE", "COVERED", "TOTAL", "ACTUAL", "TARGET", "STATUS")
	fmt.Fprintf(w, "%-22s %10s %10s %9s %8s  %s\n",
		"-------", "-------", "-----", "------", "------", "------")
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].pkg < rows[j].pkg })
	for _, r := range rows {
		actual := "—"
		if r.total > 0 {
			actual = fmt.Sprintf("%.2f%%", r.actual)
		}
		fmt.Fprintf(w, "%-22s %10d %10d %9s %7.0f%%  %s\n",
			r.pkg, r.covered, r.total, actual, r.target, r.status)
	}
}

// writeStepSummary appends a Markdown table to GitHub Actions' step-summary
// file when running under Actions. Outside CI it is a silent no-op.
func writeStepSummary(rows []row) {
	dst := os.Getenv("GITHUB_STEP_SUMMARY")
	if dst == "" {
		return
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	fmt.Fprintln(f, "### Coverage gate")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| Package | Covered | Total | Actual | Target | Status |")
	fmt.Fprintln(f, "|---|---:|---:|---:|---:|---|")
	for _, r := range rows {
		actual := "—"
		if r.total > 0 {
			actual = fmt.Sprintf("%.2f%%", r.actual)
		}
		mark := "✅ ok"
		if r.status == "BELOW" {
			mark = "❌ below"
		} else if r.status == "NO DATA" {
			mark = "⚠️ no data"
		}
		fmt.Fprintf(f, "| `%s` | %d | %d | %s | %.0f%% | %s |\n",
			r.pkg, r.covered, r.total, actual, r.target, mark)
	}
}
