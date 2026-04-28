package sandbox

import (
	"regexp"
	"strings"
)

// Stack-trace path-repetition collapsing.
//
// Recursive code in any language produces stack traces where the same
// source file appears repeatedly across frames. A 30-frame Python
// traceback through `/long/path/to/module.py` ships ~1500 bytes of
// path repetition with zero new signal — every frame's file is the
// same as the one above it.
//
// We collapse those runs conservatively: only when the same path
// appears in ≥3 consecutive frames do we replace the path on the
// 2nd-and-later occurrences with a continuation marker. Line numbers
// and function names stay verbatim — those are the bits the agent
// actually needs to localize the bug.
//
// The transform is idempotent and shape-preserving: it never changes
// the line count, never reorders frames, never drops information that
// isn't redundant. An agent unfamiliar with the marker can still see
// "this frame uses the same file as the one above" from context.

// pythonFrameRegex matches a Python traceback frame's file line:
//
//	  File "/path/to/foo.py", line 42, in funcname
//
// Capture groups: indent, path, line+function tail. The path is what
// we collapse on consecutive matches.
var pythonFrameRegex = regexp.MustCompile(`^(\s*)File "([^"]+)"(.*)$`)

// goFrameRegex matches a Go runtime stack frame's file:line line:
//
//	/path/to/file.go:42 +0x123
//
// Go runtime traces are denser than Python — file path lives on its
// own line below the function name. Capture: leading whitespace, path,
// rest of line.
var goFrameRegex = regexp.MustCompile(`^(\s+)([/A-Za-z][^:\s]*\.go):(\d+)(.*)$`)

// stackPathContMarker is what we emit on collapsed continuation
// frames. Three dots signal repetition without claiming a specific
// file — agents reading "..." in a frame's path slot understand
// "same as previous frame". Quotes preserve the syntactic shape so a
// trace remains visually a trace.
const stackPathContMarker = `"…"`

// stackPathRunMin is the minimum number of consecutive same-path
// frames before we start collapsing. Three is conservative: it leaves
// flat traces (1-2 frames per file) untouched and only kicks in for
// genuinely recursive shapes where the win is meaningful.
const stackPathRunMin = 3

// CompactStackTracePaths walks s line-by-line; when a run of ≥
// stackPathRunMin consecutive Python or Go frames share the same file
// path, replaces the path on continuation frames with a short marker.
// Returns input unchanged when no run crosses the threshold.
//
// The function is pure and safe for concurrent use.
func CompactStackTracePaths(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	// Walk the line slice tracking current path-run. The run extends
	// across consecutive frame lines that all match the same regex
	// AND share the same path. Non-frame lines reset the run.
	type frameMatch struct {
		idx     int    // line index
		path    string // path captured by the regex
		isFrame bool   // matched a frame regex
	}
	matches := make([]frameMatch, len(lines))
	for i, ln := range lines {
		if m := pythonFrameRegex.FindStringSubmatch(ln); m != nil {
			matches[i] = frameMatch{idx: i, path: m[2], isFrame: true}
			continue
		}
		if m := goFrameRegex.FindStringSubmatch(ln); m != nil {
			matches[i] = frameMatch{idx: i, path: m[2], isFrame: true}
			continue
		}
	}

	changed := false
	i := 0
	for i < len(matches) {
		if !matches[i].isFrame {
			i++
			continue
		}
		// Find the run of same-path frames, allowing exactly one
		// non-frame gap between adjacent frames. Python tracebacks
		// place a source-code excerpt line between each `File "…"`
		// line; without the gap-tolerance the run terminates after
		// every frame and the threshold never fires.
		runIdx := []int{i}
		j := i + 1
		for j < len(matches) {
			if matches[j].isFrame {
				if matches[j].path == matches[i].path {
					runIdx = append(runIdx, j)
					j++
					continue
				}
				break
			}
			// Non-frame line at j. Peek j+1: if it's a same-path
			// frame, swallow the gap and continue the run.
			if j+1 < len(matches) && matches[j+1].isFrame && matches[j+1].path == matches[i].path {
				runIdx = append(runIdx, j+1)
				j += 2
				continue
			}
			break
		}
		if len(runIdx) >= stackPathRunMin {
			// Collapse continuation frames (every index in runIdx
			// except the first). The first frame keeps the full
			// path so the agent has at least one explicit anchor.
			for _, k := range runIdx[1:] {
				lines[k] = collapseFramePath(lines[k], matches[k].path)
			}
			changed = true
		}
		i = j
	}

	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

// collapseFramePath replaces the path inside a single frame line with
// the continuation marker. Preserves indent + line+function tail.
// Falls back to the original line if neither regex matches (defensive
// guard against the matches table going stale).
func collapseFramePath(line, path string) string {
	if m := pythonFrameRegex.FindStringSubmatch(line); m != nil {
		// m[1]=indent, m[2]=path, m[3]=tail (", line N, in func")
		return m[1] + "File " + stackPathContMarker + m[3]
	}
	if m := goFrameRegex.FindStringSubmatch(line); m != nil {
		// m[1]=indent, m[2]=path, m[3]=line, m[4]=tail
		return m[1] + stackPathContMarker + ":" + m[3] + m[4]
	}
	return line
}
