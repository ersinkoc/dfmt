package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

func (s *SandboxImpl) Glob(ctx context.Context, req GlobReq) (GlobResp, error) {
	// Resolve working directory
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return GlobResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Clean and resolve the glob pattern
	pattern := req.Pattern
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(absWd, pattern)
	}
	pattern = filepath.Clean(pattern)

	// Verify the pattern stays within working directory
	rel, err := filepath.Rel(absWd, pattern)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return GlobResp{}, fmt.Errorf("pattern outside working directory: %s", req.Pattern)
	}

	// Policy check on the directory
	if err := s.PolicyCheck("read", absWd); err != nil {
		return GlobResp{}, err
	}

	// Execute glob
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return GlobResp{}, fmt.Errorf("glob pattern: %w", err)
	}

	// Filter to only files (not directories), drop anything the read policy
	// denies (F-02: a deny rule like `read **/.env*` must keep dfmt_glob from
	// surfacing the file's existence — otherwise the agent learns the path
	// even when the eventual Read would refuse), and make paths relative.
	// Filtering is silent: callers are not told *which* paths were withheld,
	// only that the result list is shorter than a raw glob would produce.
	var files []string
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			continue
		}
		if perr := s.PolicyCheck("read", m); perr != nil {
			continue
		}
		relPath, _ := filepath.Rel(absWd, m)
		files = append(files, relPath)
	}

	// Intent-based filtering
	var contentMatches []ContentMatch
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 && len(files) > 0 {
		// Read first few files to find intent matches
		for _, f := range files {
			if len(contentMatches) >= 10 {
				break
			}
			fullPath := filepath.Join(absWd, f)
			// V-01: refuse to read through a symlink leaf whose target
			// escapes wd. Lexical containment above only proves the link
			// path is in-bounds; os.ReadFile would follow it to wherever
			// the link points. Read does the equivalent EvalSymlinks
			// check inline; safefs.EnsureResolvedUnder keeps Glob's
			// intent-content path on the same boundary.
			if _, sym := safefs.EnsureResolvedUnder(fullPath, absWd); sym != nil {
				continue
			}
			data, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			content := string(data)
			lineMatches := MatchContent(content, keywords, 3)
			for _, m := range lineMatches {
				contentMatches = append(contentMatches, ContentMatch{
					Text:   m.Text,
					Score:  m.Score,
					Source: f,
					Line:   m.Line,
				})
			}
		}
	}

	// Cap inline file list. A glob like "**/*" on a large repo can return tens
	// of thousands of paths — inlining that defeats the project's purpose.
	// Beyond the cap, agents must narrow the pattern or use Grep with intent.
	const maxGlobInlineFiles = 500
	totalFiles := len(files)
	if len(files) > maxGlobInlineFiles {
		files = files[:maxGlobInlineFiles]
	}

	resp := GlobResp{
		Files:   files,
		Matches: contentMatches,
	}
	if totalFiles > maxGlobInlineFiles {
		// Use Matches as a side channel to surface the truncation; consumers
		// already display Matches in their summary.
		resp.Matches = append(resp.Matches, ContentMatch{
			Text: fmt.Sprintf("(truncated: %d more files not shown; refine pattern)", totalFiles-maxGlobInlineFiles),
		})
	}
	return resp, nil
}

// compileGrepPattern validates the user-supplied pattern (size +
// complexity budget — RE2 is linear-time but pathological patterns can
// still hit compile cost) and returns the compiled regexp. The
// case-insensitive flag is folded into the pattern via (?i) rather
// than passed to a separate compile path so all callers see one error
// shape.
func compileGrepPattern(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	if err := validateGrepPattern(pattern); err != nil {
		return nil, err
	}
	if caseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	return re, nil
}

// resolveGrepSearchRoot turns the optional req.Path into an absolute
// directory or file under absWd that the walk should descend from.
// Untrusted input → clean + absolutize + lexical containment check
// against absWd; a path that escapes (Rel returns ".." or starts
// with ".."+sep) is refused. A missing target Stat is also refused
// so the agent gets a clear error rather than an empty walk.
// Returns absWd when reqPath is empty.
func resolveGrepSearchRoot(absWd, reqPath string) (string, error) {
	if reqPath == "" {
		return absWd, nil
	}
	p := filepath.Clean(reqPath)
	if !filepath.IsAbs(p) {
		p = filepath.Join(absWd, p)
	}
	rel, err := filepath.Rel(absWd, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("grep path escapes working directory: %s", pathHint(reqPath))
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("grep path: %w", err)
	}
	return p, nil
}

// grepFileLines reads path, applies pattern to each line, and returns
// the matches with File set to rel. ok=false signals the file should be
// skipped (unreadable / policy-denied / symlink-escapes-wd). The match
// cap is enforced by the caller, but we stop early when the caller's
// pre-call accounting plus our local hits would cross 100 so we don't
// read past the budget.
func grepFileLines(s *SandboxImpl, pattern *regexp.Regexp, path, rel, absWd string, remainingBudget int) (matches []GrepMatch, ok bool) {
	// F-02: per-file deny-rule enforcement. The directory-level
	// PolicyCheck above only blocks if the wd itself is denied;
	// per-file rules like `read **/.env*` need to be applied here,
	// before reading. Without this, dfmt_grep "API_KEY" --files "*"
	// would surface secrets that direct dfmt_read of .env refuses.
	if err := s.PolicyCheck("read", path); err != nil {
		return nil, false
	}
	// V-01: refuse to read through a symlink leaf whose target escapes
	// wd. The lexical Rel check upstream is purely textual — a symlink
	// `notes.txt -> /etc/passwd` looks contained but os.ReadFile follows
	// it. EnsureResolvedUnder centralizes the pattern.
	if _, sym := safefs.EnsureResolvedUnder(path, absWd); sym != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	lines := strings.Split(string(data), "\n")
	for lineNum, line := range lines {
		if pattern.MatchString(line) {
			matches = append(matches, GrepMatch{
				File:    rel,
				Line:    lineNum + 1,
				Content: line,
			})
			if len(matches) >= remainingBudget {
				break
			}
		}
	}
	return matches, true
}

// filterMatchesByIntent applies the intent-keyword filter to matches. A
// non-zero number of keyword-hit matches replaces the input slice and
// the returned summarySuffix records the filter count; otherwise the
// input passes through unchanged. Trimming the per-line Content cap is
// folded in here since both steps mutate the same slice in lockstep.
const maxGrepLineBytes = 200

func filterMatchesByIntent(matches []GrepMatch, intent string) (out []GrepMatch, summarySuffix string) {
	out = matches
	keywords := ExtractKeywords(intent)
	if len(keywords) > 0 {
		var filtered []GrepMatch
		for _, m := range matches {
			if MatchContent(m.Content, keywords, 1) != nil {
				filtered = append(filtered, m)
			}
		}
		if len(filtered) > 0 {
			summarySuffix = fmt.Sprintf(" (filtered by intent: %d matches)", len(filtered))
			out = filtered
		}
	}
	// Trim per-line content so a regex matching very long lines
	// (minified JS, log dumps) doesn't push the response into the
	// megabytes — grep is meant for navigation, not bulk extraction.
	for i := range out {
		if len(out[i].Content) > maxGrepLineBytes {
			out[i].Content = truncate(out[i].Content, maxGrepLineBytes)
		}
	}
	return out, summarySuffix
}

// Grep implements the Sandbox interface.
func (s *SandboxImpl) Grep(ctx context.Context, req GrepReq) (GrepResp, error) {
	wd := s.wd
	if wd == "" {
		wd = "."
	}
	absWd, err := filepath.Abs(wd)
	if err != nil {
		return GrepResp{}, fmt.Errorf("resolve working directory: %w", err)
	}
	if err := s.PolicyCheck("read", absWd); err != nil {
		return GrepResp{}, err
	}

	pattern, err := compileGrepPattern(req.Pattern, req.CaseInsensitive)
	if err != nil {
		return GrepResp{}, err
	}

	searchRoot, err := resolveGrepSearchRoot(absWd, req.Path)
	if err != nil {
		return GrepResp{}, err
	}

	// Walk recursively from searchRoot. The previous implementation used
	// filepath.Glob(absWd + "/**/*") which silently degraded to a
	// non-recursive depth-2 match — Go's stdlib Glob does NOT treat **
	// as a recursive wildcard, so matches in nested dirs (the typical
	// case) were missing. WalkDir walks the actual tree.
	const grepMatchCap = 100
	var grepMatches []GrepMatch
	totalFiles := 0
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if len(grepMatches) >= grepMatchCap {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		// Lexical containment: refuse paths whose textual form escapes
		// absWd (e.g. `..\..\etc\passwd` after a buggy join). This is a
		// fast pre-filter; the symlink-target check inside grepFileLines
		// is what actually closes the renamed-symlink-leaf bypass.
		rel, rerr := filepath.Rel(absWd, path)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		// Files glob: basename-only by design — agents asking for "*.go"
		// want every .go file under the search root, not just the top
		// level. Empty pattern matches everything.
		if req.Files != "" {
			if ok, _ := filepath.Match(req.Files, d.Name()); !ok {
				return nil
			}
		}
		fileMatches, ok := grepFileLines(s, pattern, path, rel, absWd, grepMatchCap-len(grepMatches))
		if !ok {
			return nil
		}
		totalFiles++
		grepMatches = append(grepMatches, fileMatches...)
		if len(grepMatches) >= grepMatchCap {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return GrepResp{}, fmt.Errorf("grep walk: %w", walkErr)
	}

	summary := fmt.Sprintf("Found %d matches in %d files", len(grepMatches), totalFiles)
	grepMatches, suffix := filterMatchesByIntent(grepMatches, req.Intent)
	summary += suffix
	return GrepResp{Matches: grepMatches, Summary: summary}, nil
}

func validateGrepPattern(pattern string) error {
	if len(pattern) > maxGrepPatternBytes {
		return fmt.Errorf("grep pattern too large: %d bytes exceeds %d", len(pattern), maxGrepPatternBytes)
	}

	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("invalid pattern: %w", err)
	}

	nodes, repeatDepth := grepPatternComplexity(re, 0)
	if nodes > maxGrepPatternNodes {
		return fmt.Errorf("grep pattern too complex: %d nodes exceeds %d", nodes, maxGrepPatternNodes)
	}
	if repeatDepth > maxGrepRepeatNesting {
		return fmt.Errorf("grep pattern repeat nesting too deep: %d exceeds %d", repeatDepth, maxGrepRepeatNesting)
	}
	return nil
}

func grepPatternComplexity(re *syntax.Regexp, repeatDepth int) (nodes int, maxRepeatDepth int) {
	if re == nil {
		return 0, repeatDepth
	}
	nodes = 1
	childRepeatDepth := repeatDepth
	switch re.Op {
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		childRepeatDepth++
	}
	maxRepeatDepth = childRepeatDepth
	for _, sub := range re.Sub {
		subNodes, subRepeatDepth := grepPatternComplexity(sub, childRepeatDepth)
		nodes += subNodes
		if subRepeatDepth > maxRepeatDepth {
			maxRepeatDepth = subRepeatDepth
		}
	}
	return nodes, maxRepeatDepth
}

// Edit implements the Sandbox interface.
