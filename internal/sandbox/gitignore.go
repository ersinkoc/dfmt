package sandbox

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// .gitignore support for the recursive Grep/Glob walkers (ADR-0031).
//
// defaultSkipDirs is a hand-kept list of directory basenames that guesses
// at what is "not source". The repository already says exactly that in
// its .gitignore files, and the guess drifted from it both ways: a
// project's real build directory (`bin/`, `generated/`, `*.min.js`) was
// searched, while `out/` or `build/` holding real source was not. The
// walkers now also honor .gitignore — the root's, .git/info/exclude, and
// every nested .gitignore the walk enters — with stdlib-only matching.
// The default list stays: it covers VCS internals and projects with no
// .gitignore at all.

// ignoreRule is one compiled .gitignore line.
type ignoreRule struct {
	base    string // directory the rule's file lives in, slash-separated, relative to the walk root ("" = root)
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// gitIgnore holds the rules loaded so far, in precedence order (later
// wins). Rules from a nested .gitignore apply only beneath its directory.
type gitIgnore struct {
	root   string // absolute walk-root (project) directory
	rules  []ignoreRule
	loaded map[string]bool
}

// newGitIgnore loads <root>/.git/info/exclude and the .gitignore of the
// root and of every directory from root down to start (exclusive of
// deeper ones, which the walk loads as it enters them).
func newGitIgnore(root, start string) *gitIgnore {
	g := &gitIgnore{root: root, loaded: map[string]bool{}}
	g.loadFile(filepath.Join(root, ".git", "info", "exclude"), "")
	g.loadDir(root)
	rel, err := filepath.Rel(root, start)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return g
	}
	dir := root
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		dir = filepath.Join(dir, seg)
		g.loadDir(dir)
	}
	return g
}

// loadDir reads dir/.gitignore once.
func (g *gitIgnore) loadDir(dir string) {
	if g == nil || g.loaded[dir] {
		return
	}
	g.loaded[dir] = true
	rel, err := filepath.Rel(g.root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}
	base := filepath.ToSlash(rel)
	if base == "." {
		base = ""
	}
	g.loadFile(filepath.Join(dir, ".gitignore"), base)
}

func (g *gitIgnore) loadFile(path, base string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if r, ok := compileIgnoreLine(sc.Text(), base); ok {
			g.rules = append(g.rules, r)
		}
	}
}

// ignored reports whether rel (slash-separated, relative to the root)
// is excluded. The last matching rule decides, as in git.
func (g *gitIgnore) ignored(rel string, isDir bool) bool {
	if g == nil || len(g.rules) == 0 {
		return false
	}
	rel = filepath.ToSlash(rel)
	out := false
	for _, r := range g.rules {
		sub := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			sub = rel[len(r.base)+1:]
		}
		if r.dirOnly && !isDir {
			// "build/" names directories; a file matches only by lying
			// beneath one, so test its ancestor directories.
			if matchesAncestor(r.re, sub) {
				out = !r.negate
			}
			continue
		}
		if r.re.MatchString(sub) {
			out = !r.negate
		}
	}
	return out
}

// matchesAncestor reports whether re matches any proper ancestor
// directory of the slash-separated path p.
func matchesAncestor(re *regexp.Regexp, p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] == '/' && re.MatchString(p[:i]) {
			return true
		}
	}
	return false
}

// compileIgnoreLine turns one .gitignore line into a rule, following
// gitignore(5): blank lines and #comments are skipped; a leading "!"
// negates; a trailing "/" restricts to directories; a "/" anywhere else
// anchors the pattern to the .gitignore's directory, otherwise it matches
// at any depth; "**" spans directories; "*", "?" and "[...]" never cross
// a "/".
func compileIgnoreLine(line, base string) (ignoreRule, bool) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasSuffix(line, `\ `) {
		line = strings.TrimRight(line, " ")
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	r := ignoreRule{base: base}
	switch {
	case strings.HasPrefix(line, "!"):
		r.negate = true
		line = line[1:]
	case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return ignoreRule{}, false
	}
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '*' && i+1 < len(line) && line[i+1] == '*':
			// "**" — at the start ("**/x"), middle ("a/**/b") or end ("a/**").
			atSegStart := i == 0 || line[i-1] == '/'
			atSegEnd := i+2 == len(line) || line[i+2] == '/'
			if atSegStart && atSegEnd {
				if i+2 == len(line) {
					b.WriteString(".*")
				} else {
					b.WriteString("(?:.*/)?")
					i++ // consume the "/" after "**"
				}
				i++
				continue
			}
			b.WriteString("[^/]*") // "**" inside a segment is just "*"
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(line[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := line[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(line):
			i++
			b.WriteString(regexp.QuoteMeta(string(line[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	// A matched directory excludes everything beneath it.
	b.WriteString("(?:/.*)?$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return ignoreRule{}, false
	}
	r.re = re
	return r, true
}

// walkGitIgnore returns the matcher for a walk of start inside project
// root, or nil when start is itself ignored: an agent that searches
// path:"dist" — gitignored — asked for build output on purpose, the same
// escape hatch the basename list gives a search root.
func walkGitIgnore(root, start string) *gitIgnore {
	g := newGitIgnore(root, root)
	if rel, err := filepath.Rel(root, start); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		if g.ignored(rel, true) {
			return nil
		}
	}
	return newGitIgnore(root, start)
}
