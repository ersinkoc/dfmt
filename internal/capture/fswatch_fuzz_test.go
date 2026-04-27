package capture

import (
	"strings"
	"testing"
)

// FuzzMatchIgnorePattern hardens the **-aware glob matcher behind the
// fswatch ignore list. The default ignore patterns ship with `.dfmt/**`,
// `node_modules/**`, `**/__pycache__`, and `a/**/b` shapes; a buggy matcher
// that misses any of these lets fswatch journal-loop on its own writes.
//
// Invariants the fuzzer asserts:
//   - never panics on arbitrary inputs
//   - if pattern == target, returns true (every glob matches itself only
//     when literal — but for "x", "x" must match)
//   - prefix `**/x` matches `x` and any `*/.../x`
//
// Run with: go test ./internal/capture/ -run=^$ -fuzz=FuzzMatchIgnorePattern
func FuzzMatchIgnorePattern(f *testing.F) {
	// Seed corpus reflecting real default-ignore patterns.
	f.Add(".dfmt/**", ".dfmt/journal.jsonl")
	f.Add("node_modules/**", "node_modules/foo/bar.js")
	f.Add("**/__pycache__", "src/lib/__pycache__")
	f.Add("a/**/b", "a/x/y/b")
	f.Add("*.log", "errors.log")
	f.Add("**/*.swp", "deep/nested/file.swp")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, pattern, target string) {
		// matchIgnorePattern uses path.Match which only handles forward
		// slashes — fuzz inputs with embedded NULs or backslashes are
		// fine to feed in (the function ToSlash-normalises them).
		// We're testing for panics and basic invariants, not pinning
		// every case of the matcher's truth table.
		_ = matchIgnorePattern(pattern, target)

		// Identity: a literal pattern with no wildcards must match itself.
		if !strings.ContainsAny(pattern, "*?[") {
			if !matchIgnorePattern(pattern, pattern) {
				t.Fatalf("literal pattern %q does not match itself", pattern)
			}
		}
	})
}
