package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The regression these tests cover: Glob used filepath.Glob, which has no
// `**`, so a recursive pattern matched only at one fixed depth and said
// nothing about it. On this repo `**/*.go` returned 2 of 278 Go files.

// globTestTree builds a small nested project and returns its root.
func globTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"main.go",
		"cmd/app/main.go",
		"internal/core/index.go",
		"internal/core/deep/nested/very/far.go",
		"internal/core/index_test.go",
		"docs/readme.md",
		"node_modules/pkg/index.go",
		"dist/generated.go",
	}
	for _, f := range files {
		path := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", f, err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return root
}

// globFiles runs the Glob tool and returns slash-form relative paths.
func globFiles(t *testing.T, root, pattern string) []string {
	t.Helper()
	sb := NewSandbox(root)
	resp, err := sb.Glob(context.Background(), GlobReq{Pattern: pattern})
	if err != nil {
		t.Fatalf("Glob(%q): %v", pattern, err)
	}
	out := make([]string, 0, len(resp.Files))
	for _, f := range resp.Files {
		out = append(out, filepath.ToSlash(f))
	}
	slices.Sort(out)
	return out
}

func TestGlobDoublestarRecursesToEveryDepth(t *testing.T) {
	root := globTestTree(t)

	got := globFiles(t, root, "**/*.go")

	// Depth 0 through 5, all of which filepath.Glob's `**`-as-`*` missed
	// except the one at depth 1.
	want := []string{
		"cmd/app/main.go",
		"internal/core/deep/nested/very/far.go",
		"internal/core/index.go",
		"internal/core/index_test.go",
		"main.go",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("**/*.go missed %s\ngot: %v", w, got)
		}
	}
	// Build output and dependencies stay excluded unless named.
	for _, unwanted := range []string{"node_modules/pkg/index.go", "dist/generated.go"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("**/*.go returned %s, which the traversal exclusions should prune", unwanted)
		}
	}
}

// TestGlobDoublestarMatchesRootLevelFile pins the semantic that separates a
// file-search glob from the policy engine's rule glob: a leading `**/` means
// "zero or more directories", so a file at the root is a match.
func TestGlobDoublestarMatchesRootLevelFile(t *testing.T) {
	root := globTestTree(t)

	got := globFiles(t, root, "**/main.go")

	if !slices.Contains(got, "main.go") {
		t.Errorf("**/main.go did not match the root-level main.go; got %v", got)
	}
	if !slices.Contains(got, "cmd/app/main.go") {
		t.Errorf("**/main.go did not match the nested cmd/app/main.go; got %v", got)
	}
}

// TestGlobDoublestarUnderPrefixWalksOnlyThatSubtree keeps the optimization
// honest: rooting the walk at the static prefix must not change results.
func TestGlobDoublestarUnderPrefixWalksOnlyThatSubtree(t *testing.T) {
	root := globTestTree(t)

	got := globFiles(t, root, "internal/**/*.go")

	want := []string{
		"internal/core/deep/nested/very/far.go",
		"internal/core/index.go",
		"internal/core/index_test.go",
	}
	if !slices.Equal(got, want) {
		t.Errorf("internal/**/*.go = %v, want %v", got, want)
	}
}

// TestGlobDoublestarNamedExcludedDirIsTraversed mirrors the Grep rule: an
// exclusion is a default, not a prohibition. Naming the directory opts in.
func TestGlobDoublestarNamedExcludedDirIsTraversed(t *testing.T) {
	root := globTestTree(t)

	got := globFiles(t, root, "dist/**/*.go")

	if !slices.Contains(got, "dist/generated.go") {
		t.Errorf("dist/**/*.go = %v, want it to include dist/generated.go — naming a "+
			"pruned directory is how the caller asks for it", got)
	}
}

// TestGlobWithoutDoublestarUnchanged guards the fast path: single-star
// patterns must keep filepath.Glob's exact behavior (one level, no recursion).
func TestGlobWithoutDoublestarUnchanged(t *testing.T) {
	root := globTestTree(t)

	got := globFiles(t, root, "*.go")

	if !slices.Equal(got, []string{"main.go"}) {
		t.Errorf("*.go = %v, want only the root-level main.go", got)
	}
}

func TestGlobPatternRegexSemantics(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "a/b/c.txt", false},
		{"**/*_test.go", "internal/core/index_test.go", true},
		{"internal/**/*.go", "internal/core/index.go", true},
		{"internal/**/*.go", "cmd/app/main.go", false},
		{"**", "anything/at/all.txt", true},
		{"src/**", "src/a/b.ts", true},
		{"src/**", "other/a/b.ts", false},
		{"*.go", "main.go", true},
		{"*.go", "a/main.go", false}, // single star never crosses a separator
		{"a?c.txt", "abc.txt", true},
		{"a?c.txt", "a/c.txt", false},
		{"[ab].txt", "a.txt", true},
		{"[!ab].txt", "c.txt", true},
		{"[!ab].txt", "a.txt", false},
	}
	for _, tc := range cases {
		re, err := globPatternRegex(tc.pattern)
		if err != nil {
			t.Fatalf("globPatternRegex(%q): %v", tc.pattern, err)
		}
		if got := re.MatchString(tc.path); got != tc.want {
			t.Errorf("pattern %q vs path %q = %v, want %v (regex %s)",
				tc.pattern, tc.path, got, tc.want, re)
		}
	}
}

func TestStaticPrefix(t *testing.T) {
	cases := map[string]string{
		"**/*.go":            "",
		"internal/**/*.go":   "internal",
		"a/b/c/**/*.md":      "a/b/c",
		"src/*/index.ts":     "src",
		"docs/**":            "docs",
		"internal/core/*.go": "internal/core",
	}
	for pattern, want := range cases {
		if got := staticPrefix(pattern); got != want {
			t.Errorf("staticPrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
}
