package sandbox

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Recursive glob expansion for the dfmt_glob tool.
//
// Why this file exists: Glob delegated to filepath.Glob, and Go's stdlib
// glob has no `**`. filepath.Match documents `*` as "any sequence of
// non-Separator characters", and a second `*` adds nothing — so `**/*.go`
// compiles to the same thing as `*/*.go` and matches files at exactly one
// level of depth. Measured on this repo: `**/*.go` returned 2 of 278 Go
// files (the two in scripts/), and the MCP schema advertises that very
// pattern as the example. The failure is silent — a short list of real
// paths reads like a small tree, not like a broken matcher.
//
// The project already had two `**`-aware matchers (the policy engine's
// globToRegex and the fs watcher's matchIgnorePattern), but neither could be
// reused as-is: the policy variant is load-bearing for deny rules and
// compiles a leading `**/` to "at least one directory", which is the wrong
// reading for a file search (`**/*.go` must also match a file at the root).
// This one is scoped to path expansion and free to use doublestar semantics.

// maxGlobWalkMatches bounds how many paths an expansion collects before it
// stops walking. The caller separately truncates the inline list to
// maxGlobInlineFiles; this larger cap exists so `**/*` on a monorepo cannot
// build an unbounded slice in the daemon before that truncation happens.
const maxGlobWalkMatches = 5000

// hasDoublestar reports whether a pattern needs the recursive walker.
// Patterns without `**` keep going through filepath.Glob: it is cheaper
// (one readdir per matched directory instead of a subtree walk) and its
// semantics — including character classes — are what existing callers and
// tests already depend on.
func hasDoublestar(slashPattern string) bool {
	return strings.Contains(slashPattern, "**")
}

// expandDoublestarGlob walks absWd and returns the absolute paths of files
// matching relPattern (a wd-relative, forward-slash pattern containing at
// least one `**`).
//
// Directory pruning uses the same exclusion set as Grep, with the same
// escape hatch: a directory the pattern names literally (`dist/**/*.map`)
// is still traversed, because naming it is how the caller asks for it.
// Pruned subtrees are counted, never silently dropped — see walkSkipStats.
func expandDoublestarGlob(absWd, relPattern string) ([]string, walkSkipStats, error) {
	var skipped walkSkipStats

	re, err := globPatternRegex(relPattern)
	if err != nil {
		return nil, skipped, err
	}

	// Root the walk at the deepest wildcard-free prefix. `internal/**/*.go`
	// walks internal/, not the whole project — on a repo with a large
	// sibling directory that is the difference between a fast search and a
	// full-tree traversal the pattern could never have matched anyway.
	root := filepath.Join(absWd, filepath.FromSlash(staticPrefix(relPattern)))

	var matches []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory (permissions, a race with a delete):
			// skip that subtree rather than abandoning the whole search.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Never prune the root itself: the caller walked here on purpose.
			if path == root {
				return nil
			}
			if shouldSkipDir(d.Name()) && !patternNamesSegment(relPattern, d.Name()) {
				skipped.dirs++
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(absWd, path)
		if rerr != nil {
			return nil
		}
		if !re.MatchString(filepath.ToSlash(rel)) {
			return nil
		}
		matches = append(matches, path)
		if len(matches) >= maxGlobWalkMatches {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		// A missing root is "no matches", not an error: `docs/**/*.md` on a
		// project without docs/ should return an empty list the same way
		// filepath.Glob does.
		if os.IsNotExist(walkErr) {
			return nil, skipped, nil
		}
		return nil, skipped, walkErr
	}
	return matches, skipped, nil
}

// staticPrefix returns the leading path segments of a pattern that contain
// no wildcard, so the walk can start below them.
func staticPrefix(slashPattern string) string {
	segs := strings.Split(slashPattern, "/")
	var static []string
	for _, seg := range segs {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		static = append(static, seg)
	}
	// The last static segment may be the filename itself (a pattern with no
	// wildcards at all never reaches this function), so every segment kept
	// here is a directory.
	if len(static) == len(segs) {
		static = static[:len(static)-1]
	}
	return strings.Join(static, "/")
}

// globPatternRegex compiles a forward-slash glob into an anchored regexp
// with doublestar semantics:
//
//	**/     zero or more path segments  (so `**/*.go` matches `main.go` too)
//	**      anything, separators included
//	*       anything within one segment
//	?       one character within one segment
//	[a-z]   character class, `[!…]` accepted as `[^…]`
//
// The "zero or more" reading of a leading `**/` is the one every tool an
// agent has used elsewhere (git, ripgrep, bash globstar, VS Code) implements,
// and it is the difference between finding a root-level file and silently
// omitting it.
func globPatternRegex(slashPattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(slashPattern); {
		switch {
		case strings.HasPrefix(slashPattern[i:], "**/"):
			b.WriteString("(?:[^/]*/)*")
			i += 3
		case strings.HasPrefix(slashPattern[i:], "**"):
			b.WriteString(".*")
			i += 2
		case slashPattern[i] == '*':
			b.WriteString("[^/]*")
			i++
		case slashPattern[i] == '?':
			b.WriteString("[^/]")
			i++
		case slashPattern[i] == '[':
			end := strings.IndexByte(slashPattern[i:], ']')
			if end < 0 {
				// Unterminated class: treat as a literal bracket rather than
				// failing the whole call.
				b.WriteString(regexp.QuoteMeta("["))
				i++
				continue
			}
			class := slashPattern[i : i+end+1]
			if strings.HasPrefix(class, "[!") {
				class = "[^" + class[2:]
			}
			b.WriteString(class)
			i += end + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(slashPattern[i])))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
