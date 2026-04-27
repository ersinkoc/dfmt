package sandbox

import (
	"regexp"
	"strings"
)

// SignalScore is the synthetic relevance score we assign to a kind-aware
// signal line. It outranks anything MatchContent produces for ordinary lines
// (which top out near ~3.6 in scoreLine) so verdict markers always sort
// above noisy keyword hits when the two are merged.
const SignalScore = 100.0

// signalCap bounds the number of signal lines we'll surface from one body.
// Beyond this, we fall back to ordinary matches/tail — a runaway stack
// trace or a 10k-line panic shouldn't crowd out everything else the
// excerpt machinery has to say.
const signalCap = 8

// signalPattern is a kind-aware verdict-shaped line: panics, FAIL summaries,
// language-specific exception headers, error: prefixes, stack-trace frames.
// All patterns are anchored on the trimmed line so the matcher doesn't
// fire on a substring buried in normal output ("the panicked banana" is
// not a signal). Stack-trace patterns are intentionally narrow — broad
// "matches anything indented" patterns would treat normal source listings
// as signals.
type signalPattern struct {
	name string
	re   *regexp.Regexp
}

// signalPatterns is the priority-ordered set we scan against. Order doesn't
// affect correctness — extractSignalLines records every match — but groups
// the patterns by domain for readability and future tuning.
var signalPatterns = []signalPattern{
	// Go: test runner verdicts and runtime panics.
	{name: "go.fail", re: regexp.MustCompile(`^---\s+FAIL:\s`)},
	{name: "go.summary", re: regexp.MustCompile(`^FAIL\s+\S`)}, // "FAIL\tpkg/path\t1.234s"
	{name: "go.panic", re: regexp.MustCompile(`^panic:\s`)},
	{name: "go.fatal", re: regexp.MustCompile(`^fatal error:\s`)},
	{name: "go.race", re: regexp.MustCompile(`^WARNING:\s+DATA\s+RACE`)},

	// Rust: cargo/rustc errors and test verdicts.
	{name: "rust.error", re: regexp.MustCompile(`^error(?:\[E\d+\])?:\s`)},
	{name: "rust.test", re: regexp.MustCompile(`^test\s+\S+\s+\.\.\.\s+FAILED`)},
	{name: "rust.fail", re: regexp.MustCompile(`^failures:\s*$`)},

	// Python: tracebacks and assertion failures.
	{name: "py.traceback", re: regexp.MustCompile(`^Traceback\s+\(most recent call last\):`)},
	{name: "py.exception", re: regexp.MustCompile(`^\w*(?:Error|Exception|Warning):\s`)},
	{name: "py.pytest.fail", re: regexp.MustCompile(`^FAILED\s+\S`)},
	{name: "py.pytest.E", re: regexp.MustCompile(`^E\s{2,}\S`)}, // pytest "E   AssertionError"

	// Node / JS: explicit error names anywhere in the line are caught
	// here; the leading-position variants above already handle the
	// header form.
	{name: "js.error", re: regexp.MustCompile(`^(?:Uncaught\s+)?(?:Type|Reference|Syntax|Range|URI)Error:\s`)},

	// Java / JVM: exception class headers and "Caused by:".
	{name: "jvm.exception", re: regexp.MustCompile(`^(?:Exception\s+in\s+thread\b|Caused\s+by:\s)`)},

	// Generic CI: explicit FATAL / process exit messages.
	{name: "ci.fatal", re: regexp.MustCompile(`^FATAL:?\s`)},
	{name: "ci.exit", re: regexp.MustCompile(`^Process\s+exited\s+with\s+code\s+\d`)},
}

// extractSignalLines walks content and returns up to signalCap "verdict-
// shaped" lines as ContentMatch entries with SignalScore. Lines are
// returned in source order so the agent can read them top-to-bottom and
// reconstruct the failure timeline. Empty bodies and bodies that contain
// no signals return nil — the empty-slice vs nil distinction matters for
// JSON omitempty.
func extractSignalLines(content string) []ContentMatch {
	if content == "" {
		return nil
	}
	var out []ContentMatch
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, p := range signalPatterns {
			if p.re.MatchString(trimmed) {
				out = append(out, ContentMatch{
					Text:  trimmed,
					Score: SignalScore,
					Line:  i + 1,
				})
				break // one signal hit per line is enough
			}
		}
		if len(out) >= signalCap {
			break
		}
	}
	return out
}

// mergeSignalsIntoMatches prepends signals to matches, dedups by line
// number (so a signal that ALSO happens to be a top keyword hit is only
// emitted once), and caps the merged slice at maxOut to respect the
// tier-based size budget. Signals always come first because their
// SignalScore dominates; the cap drops trailing keyword matches first
// when budget is tight.
func mergeSignalsIntoMatches(signals, matches []ContentMatch, maxOut int) []ContentMatch {
	if len(signals) == 0 {
		return matches
	}
	if maxOut <= 0 {
		maxOut = len(signals) + len(matches)
	}
	seen := make(map[int]struct{}, len(signals))
	merged := make([]ContentMatch, 0, len(signals)+len(matches))
	for _, s := range signals {
		seen[s.Line] = struct{}{}
		merged = append(merged, s)
		if len(merged) >= maxOut {
			return merged
		}
	}
	for _, m := range matches {
		if _, dup := seen[m.Line]; dup {
			continue
		}
		merged = append(merged, m)
		if len(merged) >= maxOut {
			break
		}
	}
	return merged
}
