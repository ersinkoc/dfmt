package sandbox

import (
	"strings"
	"testing"
)

// TestCompactGitDiff_DropsIndexLines: a typical multi-file diff loses
// every `index <hash>..<hash> <mode>` line; everything else stays
// verbatim. Hunk headers, file headers, and content survive.
func TestCompactGitDiff_DropsIndexLines(t *testing.T) {
	in := `diff --git a/foo.go b/foo.go
index abc1234..def5678 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,3 @@
 line1
-old
+new
diff --git a/bar.go b/bar.go
index 1234567..89abcde 100644
--- a/bar.go
+++ b/bar.go
@@ -10,3 +10,3 @@
 line10
-old10
+new10
`
	out := CompactGitDiff(in)
	if out == in {
		t.Fatal("expected diff compaction; got input unchanged")
	}
	if strings.Contains(out, "index abc1234") || strings.Contains(out, "index 1234567") {
		t.Errorf("index line not dropped: %s", out)
	}
	// Everything else preserved.
	for _, want := range []string{
		"diff --git a/foo.go b/foo.go",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,3 @@",
		"-old",
		"+new",
		"diff --git a/bar.go b/bar.go",
		"@@ -10,3 +10,3 @@",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preserved line %q lost: %s", want, out)
		}
	}
}

// TestCompactGitDiff_NoModeIndexLine: new-file diffs sometimes emit
// an index line without a trailing mode (`index 0000000..abc`).
// Detection must catch that shape too.
func TestCompactGitDiff_NoModeIndexLine(t *testing.T) {
	in := `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+package main
`
	out := CompactGitDiff(in)
	if strings.Contains(out, "index 0000000") {
		t.Errorf("mode-less index line not dropped: %s", out)
	}
	if !strings.Contains(out, "new file mode 100644") {
		t.Errorf("`new file mode` header should survive: %s", out)
	}
}

// TestCompactGitDiff_NotADiffPassesThrough: text without the
// `diff --git ` prefix stays untouched. Plain prose with the literal
// fragment "index abc..def" must not be mangled.
func TestCompactGitDiff_NotADiffPassesThrough(t *testing.T) {
	cases := []string{
		"plain text\nindex abc1234..def5678 100644\nmore text",
		`{"json":"value"}`,
		"",
		"<!doctype html><html></html>",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := CompactGitDiff(in); got != in {
				t.Errorf("non-diff input must pass through; got %q", got)
			}
		})
	}
}

// TestCompactGitDiff_NormalizeOutputIntegration: pipeline-level wiring.
func TestCompactGitDiff_NormalizeOutputIntegration(t *testing.T) {
	in := `diff --git a/x.go b/x.go
index aaa..bbb 100644
--- a/x.go
+++ b/x.go
@@ -1,1 +1,1 @@
-x
+y
`
	out := NormalizeOutput(in)
	if strings.Contains(out, "index aaa") {
		t.Errorf("NormalizeOutput must invoke CompactGitDiff: %s", out)
	}
}
