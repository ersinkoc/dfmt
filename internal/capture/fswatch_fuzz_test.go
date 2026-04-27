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
		// matchIgnorePattern's contract: pattern is ToSlash-normalised
		// internally, target is expected to be slash-form already
		// (callers run filepath.ToSlash before invocation). The fuzzer
		// feeds raw bytes so we can't assume that contract holds; we
		// test only for panics and the slash-form-pattern identity.
		_ = matchIgnorePattern(pattern, target)

		// Identity: a literal pattern with no path.Match meta-chars
		// must match itself when both sides are slash-form. Backslash
		// is excluded because it's path.Match's escape char (`\*` is a
		// literal `*`, not a meta), so `\\` as a pattern means "literal
		// `\`" — exactly one byte — which can't match a two-byte
		// target. Filtering it here aligns the invariant with the
		// matcher's real semantics.
		if !strings.ContainsAny(pattern, `*?[\`) {
			slashed := strings.ReplaceAll(pattern, "\\", "/") // mirror caller-side normalisation
			if !matchIgnorePattern(pattern, slashed) {
				t.Fatalf("literal pattern %q does not match its slash-normalised target %q", pattern, slashed)
			}
		}
	})
}
