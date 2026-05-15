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

// Grep implements the Sandbox interface.
func (s *SandboxImpl) Grep(ctx context.Context, req GrepReq) (GrepResp, error) {
	// Resolve working directory
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return GrepResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Policy check on the directory
	if err := s.PolicyCheck("read", absWd); err != nil {
		return GrepResp{}, err
	}

	if err := validateGrepPattern(req.Pattern); err != nil {
		return GrepResp{}, err
	}

	// Compile regex pattern. Go's regexp engine is RE2-based and linear-time;
	// the validator above bounds compile/match resource use for very large or
	// deeply repetitive user-provided patterns.
	var pattern *regexp.Regexp
	if req.CaseInsensitive {
		pattern, err = regexp.Compile("(?i)" + req.Pattern)
	} else {
		pattern, err = regexp.Compile(req.Pattern)
	}
	if err != nil {
		return GrepResp{}, fmt.Errorf("invalid pattern: %w", err)
	}

	// Resolve the walk root. req.Path scopes the search to a subtree (or
	// a single file); without it the walk descends from absWd. The path
	// is untrusted input, so we clean + absolutize + reject anything
	// that escapes absWd. Per-file PolicyCheck below is the second line
	// of defense, so even a symlink that points outside absWd cannot
	// leak content.
	searchRoot := absWd
	if req.Path != "" {
		p := filepath.Clean(req.Path)
		if !filepath.IsAbs(p) {
			p = filepath.Join(absWd, p)
		}
		rel, rerr := filepath.Rel(absWd, p)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return GrepResp{}, fmt.Errorf("grep path escapes working directory: %s", pathHint(req.Path))
		}
		if _, serr := os.Stat(p); serr != nil {
			return GrepResp{}, fmt.Errorf("grep path: %w", serr)
		}
		searchRoot = p
	}

	// Walk recursively from searchRoot. The previous implementation used
	// filepath.Glob(absWd + "/**/*") which silently degraded to a
	// non-recursive depth-2 match — Go's stdlib Glob does NOT treat **
	// as a recursive wildcard, so matches in nested dirs (the typical
	// case) were missing. WalkDir walks the actual tree.
	var grepMatches []GrepMatch
	totalFiles := 0
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// Unreadable dir/file — skip and continue.
			return nil
		}
		if len(grepMatches) >= 100 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}

		// Lexical containment: refuse paths whose textual form escapes
		// absWd (e.g. `..\..\etc\passwd` after a buggy join). This is a
		// fast pre-filter; the symlink-target check below is what
		// actually closes the renamed-symlink-leaf bypass.
		rel, rerr := filepath.Rel(absWd, path)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}

		// Files glob: a basename pattern (e.g. "*.go") applied per file.
		// Empty pattern matches everything. Matches are basename-only by
		// design — agents asking for "*.go" want every .go file under
		// the search root, not just the top level.
		if req.Files != "" {
			if ok, _ := filepath.Match(req.Files, d.Name()); !ok {
				return nil
			}
		}

		// F-02: per-file deny-rule enforcement. The directory-level
		// PolicyCheck above only blocks if the wd itself is denied;
		// per-file rules like `read **/.env*` need to be applied here,
		// before reading. Without this, dfmt_grep "API_KEY" --files "*"
		// would surface secrets that direct dfmt_read of .env refuses.
		if perr := s.PolicyCheck("read", path); perr != nil {
			return nil
		}

		// V-01: refuse to read through a symlink leaf whose target
		// escapes wd. The lexical Rel check above is purely textual —
		// a symlink `notes.txt -> /etc/passwd` looks contained but
		// os.ReadFile follows it. Read does the equivalent inline;
		// EnsureResolvedUnder centralizes the pattern.
		if _, sym := safefs.EnsureResolvedUnder(path, absWd); sym != nil {
			return nil
		}

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		totalFiles++

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if pattern.MatchString(line) {
				grepMatches = append(grepMatches, GrepMatch{
					File:    rel,
					Line:    lineNum + 1,
					Content: line,
				})
				if len(grepMatches) >= 100 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return GrepResp{}, fmt.Errorf("grep walk: %w", walkErr)
	}

	// Generate summary
	summary := fmt.Sprintf("Found %d matches in %d files", len(grepMatches), totalFiles)

	// Intent-based filtering on matches
	var filteredMatches []GrepMatch
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 {
		for _, m := range grepMatches {
			if MatchContent(m.Content, keywords, 1) != nil {
				filteredMatches = append(filteredMatches, m)
			}
		}
		if len(filteredMatches) > 0 {
			summary += fmt.Sprintf(" (filtered by intent: %d matches)", len(filteredMatches))
			grepMatches = filteredMatches
		}
	}

	// Trim per-line content so a regex matching very long lines (minified JS,
	// log dumps) doesn't push the response into the megabytes — grep is meant
	// for navigation, not bulk extraction.
	const maxGrepLineBytes = 200
	for i := range grepMatches {
		if len(grepMatches[i].Content) > maxGrepLineBytes {
			grepMatches[i].Content = truncate(grepMatches[i].Content, maxGrepLineBytes)
		}
	}

	return GrepResp{
		Matches: grepMatches,
		Summary: summary,
	}, nil
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
