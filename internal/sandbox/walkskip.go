package sandbox

import (
	"io"
	"os"
	"slices"
)

// Traversal exclusions for the recursive Grep/Glob walkers.
//
// The walker used to be a bare filepath.WalkDir with no exclusions at
// all, which inverted the point of the whole project. On dfmt's own
// repo — 130 MB, of which 98 MB is `dist/` (eight cross-compiled
// binaries) and 14 MB is `.git` — grepping "runtime" spent 58 of its
// 100 match slots inside `dist/dfmt.exe` and 2 more inside
// `.git/objects/pack/*.pack`, and returned nothing at all from
// `internal/` or `cmd/`. WalkDir is lexical, so `dist` is reached
// before `docs` and `internal`: build output claimed the budget before
// the walker ever saw source.
//
// That cost the agent twice. Directly, because a matched line from an
// executable's string table is high-entropy — measured at ~55
// ApproxTokens per line against ~22 for a source line, since
// ApproxTokens charges one token per non-ASCII rune. And indirectly,
// far worse, because the agent got zero usable signal and had to fall
// back to native grep, paying for the same search a second time.
//
// Two mechanisms, both cheap:
//
//  1. Directory pruning by basename. Returning fs.SkipDir prunes the
//     whole subtree, so `dist/` costs one comparison instead of eight
//     multi-megabyte reads. Basename matching (not path globbing) is
//     what WalkDir wants and keeps the check O(1) per directory.
//  2. Per-file binary detection from a 512-byte prefix, which also
//     removes the pathological read: grepFileLines slurps whole files,
//     so a 13 MB executable meant a 13 MB allocation plus a
//     strings.Split over it.
//
// The list mirrors the ignore set already shipped in config.yaml for
// the fs watcher (capture.fs.ignore) — the same "don't look at this"
// knowledge, which existed but had never been wired to the walkers.
// It is deliberately basename-only and deliberately not user-facing
// config yet; see the ROADMAP note about honoring .gitignore, which
// is the principled version and needs its own ADR.
var defaultSkipDirs = map[string]bool{
	// dfmt's own state. WITHOUT this, grep matches its own journal —
	// observed live: a search for a nonexistent token returned exactly
	// one hit, the previous grep's query, recorded in
	// .dfmt/journal.jsonl a moment earlier. The journal grows with
	// every call, so this is a feedback loop, not a one-off.
	".dfmt": true,

	// Version control. Packfiles are binary and .git/hooks ships
	// sample scripts that match ordinary English words — a search for
	// "error" returned 10 hits from fsmonitor-watchman.sample alone.
	".git": true, ".hg": true, ".svn": true, ".bzr": true,

	// Build output.
	"dist": true, "build": true, "target": true, "out": true,
	"_build": true, "obj": true, "coverage": true,

	// Dependencies.
	"node_modules": true, "vendor": true, "bower_components": true,
	".venv": true, "venv": true, "__pycache__": true,
	"site-packages": true, ".tox": true,

	// Tool caches.
	".cache": true, ".next": true, ".nuxt": true, ".turbo": true,
	".parcel-cache": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".gradle": true, ".terraform": true,

	// Editor / IDE state.
	".idea": true, ".vscode": true,
}

// shouldSkipDir reports whether a directory basename is pruned during
// recursive traversal.
//
// Note what this deliberately does NOT do: it never applies to the
// search root itself. An agent that passes path:"dist" has explicitly
// asked for build output, and the walker starts there, so the root is
// never a candidate for pruning — only its descendants are. That keeps
// the exclusions a default rather than a prohibition.
func shouldSkipDir(name string) bool { return defaultSkipDirs[name] }

// pathHasSkippedDir reports whether relPath sits under an excluded
// directory, honoring the same "explicitly asked for it" escape hatch
// that the Grep walker gets from never pruning its search root.
//
// Glob cannot use fs.SkipDir — it delegates to filepath.Glob and
// filters the result list — so the opt-out has to be recovered from the
// caller's pattern instead: if the pattern names the directory as a
// literal component (`dist/*`, `**/node_modules/*.json`), matches under
// it are kept. Wildcard components never count as naming it, which is
// what keeps a broad `**/*` from dragging in build output.
func pathHasSkippedDir(relPath, pattern string) bool {
	for _, seg := range splitPathSegments(relPath) {
		if !defaultSkipDirs[seg] {
			continue
		}
		if patternNamesSegment(pattern, seg) {
			continue // caller asked for this directory by name
		}
		return true
	}
	return false
}

// patternNamesSegment reports whether pattern contains seg as a literal
// path component. A component carrying a wildcard is not a literal
// mention even if seg is a substring of it.
func patternNamesSegment(pattern, seg string) bool {
	return slices.Contains(splitPathSegments(pattern), seg)
}

// splitPathSegments splits on both separators so a Windows-shaped path
// and a forward-slash pattern segment the same way. Empty and "."
// segments are dropped.
func splitPathSegments(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' || p[i] == '\\' {
			if seg := p[start:i]; seg != "" && seg != "." {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	return out
}

// maxGrepFileBytes caps the size of a single file the grep walker will
// read. Binaries are already excluded by content sniffing, so this
// exists for large *text*: multi-gigabyte logs, generated CSV dumps,
// minified bundles. Emitted lines are separately trimmed to
// maxGrepLineBytes, so the token cost of a match is already bounded —
// this bounds the read cost.
const maxGrepFileBytes = 8 << 20 // 8 MiB

// binarySniffBytes is how much of a file's head is read to classify it.
// Matches binaryDetectSampleBytes so a file and an in-memory body are
// judged by the same evidence.
const binarySniffBytes = binaryDetectSampleBytes

// isBinaryPath reports whether path looks like binary content, reading
// at most binarySniffBytes from the head rather than the whole file.
//
// Reuses detectBinary, so the magic-number table and the NUL /
// invalid-UTF-8 fallbacks are shared with the output pipeline's binary
// refusal — one definition of "binary", two call sites. The magic table
// already covers the cases that motivated this (PE/`MZ` for
// dist/dfmt.exe, ELF for the linux/freebsd release artifacts); git
// packfiles have no entry but fail the NUL check inside 512 bytes.
//
// An unreadable file reports false: the caller's own read will fail and
// skip it, and guessing "binary" here would misreport the reason.
func isBinaryPath(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, binarySniffBytes)
	n, err := io.ReadFull(f, buf)
	if n == 0 {
		return false // empty, or unreadable past open
	}
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false
	}
	_, isBin := detectBinary(string(buf[:n]))
	return isBin
}

// walkSkipStats accumulates what a traversal declined to look at, so
// the caller can say so instead of silently returning a short list. A
// skip the agent cannot see is indistinguishable from an absence of
// matches, which is the failure mode that let build output masquerade
// as "no results in source" for so long.
type walkSkipStats struct {
	dirs   int // directories pruned by name
	binary int // files skipped by content sniff
	large  int // files skipped by maxGrepFileBytes
}

func (w walkSkipStats) any() bool { return w.dirs+w.binary+w.large > 0 }

// note renders a compact, human-readable suffix for a result summary.
// Empty when nothing was skipped, so the common case adds no tokens.
func (w walkSkipStats) note() string {
	if !w.any() {
		return ""
	}
	out := " (skipped"
	sep := " "
	if w.dirs > 0 {
		out += sep + itoa(w.dirs) + " ignored dir"
		if w.dirs != 1 {
			out += "s"
		}
		sep = ", "
	}
	if w.binary > 0 {
		out += sep + itoa(w.binary) + " binary"
		sep = ", "
	}
	if w.large > 0 {
		out += sep + itoa(w.large) + " oversized"
	}
	return out + ")"
}

// itoa avoids pulling strconv into this file for one call; the counts
// are small and non-negative by construction.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
