package sandbox

import (
	"strings"
	"testing"
)

// TestCompactStackTracePaths_PythonRecursion: a 5-frame Python
// traceback through the same file collapses continuation paths to
// the marker; the first frame keeps its full path as anchor.
func TestCompactStackTracePaths_PythonRecursion(t *testing.T) {
	in := `Traceback (most recent call last):
  File "/long/path/to/recurse.py", line 42, in main
    bar()
  File "/long/path/to/recurse.py", line 38, in bar
    baz()
  File "/long/path/to/recurse.py", line 30, in baz
    qux()
  File "/long/path/to/recurse.py", line 22, in qux
    raise ValueError
ValueError: oops
`
	out := CompactStackTracePaths(in)
	if out == in {
		t.Fatal("expected collapse on 4 same-path frames")
	}
	// First occurrence stays.
	if strings.Count(out, `"/long/path/to/recurse.py"`) != 1 {
		t.Errorf("first frame should keep full path; got %d occurrences",
			strings.Count(out, `"/long/path/to/recurse.py"`))
	}
	// Continuation frames have the marker.
	if !strings.Contains(out, `File "…"`) {
		t.Errorf("continuation marker missing: %s", out)
	}
	// Line numbers preserved verbatim — these are the bits the agent needs.
	for _, want := range []string{"line 42", "line 38", "line 30", "line 22"} {
		if !strings.Contains(out, want) {
			t.Errorf("line number %q lost: %s", want, out)
		}
	}
	// Function names preserved.
	for _, want := range []string{"in main", "in bar", "in baz", "in qux"} {
		if !strings.Contains(out, want) {
			t.Errorf("function %q lost: %s", want, out)
		}
	}
}

// TestCompactStackTracePaths_BelowThreshold: 2 same-path frames must
// stay verbatim. Threshold is 3 — flat traces aren't worth the
// cognitive overhead of the marker.
func TestCompactStackTracePaths_BelowThreshold(t *testing.T) {
	in := `Traceback (most recent call last):
  File "/path/a.py", line 1, in foo
  File "/path/a.py", line 2, in bar
SomeError: x
`
	out := CompactStackTracePaths(in)
	if out != in {
		t.Errorf("2-frame run should not collapse; got %q", out)
	}
}

// TestCompactStackTracePaths_DifferentPathsStaySplit: when adjacent
// frames have DIFFERENT paths, no collapse — each frame stays full.
// This is the typical multi-module trace shape.
func TestCompactStackTracePaths_DifferentPathsStaySplit(t *testing.T) {
	in := `Traceback (most recent call last):
  File "/a/main.py", line 1, in main
  File "/b/lib.py", line 22, in run
  File "/c/util.py", line 7, in step
Error
`
	out := CompactStackTracePaths(in)
	if out != in {
		t.Errorf("distinct paths should not collapse; got %q", out)
	}
}

// TestCompactStackTracePaths_NotATracePassesThrough: random text
// without frame-shaped lines is unchanged.
func TestCompactStackTracePaths_NotATracePassesThrough(t *testing.T) {
	cases := []string{
		"plain log line\nanother line",
		"",
		"# Markdown\n\nbody",
		`{"json":true}`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := CompactStackTracePaths(in); got != in {
				t.Errorf("non-trace must pass through; got %q", got)
			}
		})
	}
}

// TestCompactStackTracePaths_GoRuntimeRecursion: Go's runtime stack
// has a different shape (path:line on its own line below the
// function). Same threshold and marker applied.
func TestCompactStackTracePaths_GoRuntimeRecursion(t *testing.T) {
	in := `goroutine 1 [running]:
main.recurse(...)
	/repo/cmd/app/main.go:42 +0x123
main.recurse(...)
	/repo/cmd/app/main.go:42 +0x123
main.recurse(...)
	/repo/cmd/app/main.go:42 +0x123
main.recurse(...)
	/repo/cmd/app/main.go:42 +0x123
`
	out := CompactStackTracePaths(in)
	if out == in {
		// Go traces with identical adjacent lines also fall into RLE
		// territory — but we run before RLE in NormalizeOutput, so
		// stack-trace collapse should fire first. If lines are
		// byte-identical the run length matters more than the path
		// match; check at least one collapse occurred.
		t.Fatal("expected Go-stack collapse")
	}
	if !strings.Contains(out, `"…":42`) {
		t.Errorf("continuation marker missing: %s", out)
	}
}

// TestCompactStackTracePaths_NormalizeOutputIntegration: pipeline
// wiring — stack-trace compaction runs before structured/HTML
// transforms.
func TestCompactStackTracePaths_NormalizeOutputIntegration(t *testing.T) {
	in := strings.Repeat(`  File "/x/y.py", line `, 1) + "1, in a\n" +
		`  File "/x/y.py", line 2, in b` + "\n" +
		`  File "/x/y.py", line 3, in c` + "\n" +
		`  File "/x/y.py", line 4, in d` + "\n"
	out := NormalizeOutput(in)
	if !strings.Contains(out, `File "…"`) {
		t.Errorf("NormalizeOutput must invoke CompactStackTracePaths: %s", out)
	}
}
