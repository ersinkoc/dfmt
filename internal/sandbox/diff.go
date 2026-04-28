package sandbox

import (
	"regexp"
	"strings"
)

// Git unified-diff compaction.
//
// `git diff` output for every changed file emits four header lines:
//
//	diff --git a/path b/path
//	index abc1234..def5678 100644
//	--- a/path
//	+++ b/path
//
// The `index` line carries pre/post blob hashes plus the file mode.
// For an LLM agent reasoning over the diff, the hashes are opaque
// and the mode (`100644`) is rarely meaningful — the surrounding
// `--- a/` / `+++ b/` headers already identify the file. Dropping the
// `index` line saves ~40-60 bytes per modified file with zero loss
// of agent-relevant signal.
//
// Detection is conservative: we only fire when the body opens with
// `diff --git ` so plain text containing the literal "index abc..def"
// (a code review comment, a Stack Overflow snippet) stays untouched.

// gitDiffPrefix anchors detection at the start of the body. We
// require the `diff --git ` prefix verbatim — git's output is
// machine-stable, so a strict match avoids false positives on prose
// that happens to contain similar fragments.
var gitDiffPrefix = regexp.MustCompile(`(?m)^diff --git `)

// gitDiffIndexLine matches a `git diff` index header. Format:
//
//	index <pre-hash>..<post-hash> <mode>
//	index <pre-hash>..<post-hash>           (mode optional for new/deleted files)
//
// Hashes are 7-40 hex chars (git's abbrev_length is configurable).
// Mode is six octal digits (100644, 100755, etc.) or absent.
var gitDiffIndexLine = regexp.MustCompile(`(?m)^index [0-9a-f]+\.\.[0-9a-f]+(?: [0-7]{6})?\s*$\n?`)

// CompactGitDiff drops the `index` header line from every file block
// in a unified-diff body. Returns input unchanged when:
//   - input doesn't start with `diff --git `,
//   - dropping the index lines would not strictly shrink the body
//     (impossible in practice, but the cap-regression contract is
//     uniform across all NormalizeOutput compactors).
//
// The function is pure and safe for concurrent use.
func CompactGitDiff(s string) string {
	if s == "" {
		return s
	}
	if !gitDiffPrefix.MatchString(s) {
		return s
	}
	out := gitDiffIndexLine.ReplaceAllString(s, "")
	if len(out) >= len(s) {
		return s
	}
	// Defensive: only drop the trailing newline of the matched block,
	// not surrounding whitespace. ReplaceAllString already handles
	// the inline anchor; nothing more to do.
	return strings.TrimRight(out, "")
}
