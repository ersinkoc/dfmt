package project

import "bytes"

// IsDfmtIgnored reports whether content (a .gitignore file body) contains an
// effective rule that ignores the .dfmt/ directory.
//
// The previous implementation used `bytes.Contains(content, []byte(".dfmt/"))`
// which produces both false positives (a `# tracks .dfmt/ elsewhere` comment
// makes the check think the directory is already ignored) and false negatives
// for entries written without the trailing slash. This matcher walks
// .gitignore line-by-line, skips comments and blank lines, accepts the four
// canonical spellings (`.dfmt`, `.dfmt/`, `/.dfmt`, `/.dfmt/`), and honors
// a leading `!` negation per .gitignore last-match-wins semantics.
//
// See V-6 in security-report/.
func IsDfmtIgnored(content []byte) bool {
	ignored := false
	for _, raw := range bytes.Split(content, []byte("\n")) {
		line := bytes.TrimRight(bytes.TrimSpace(raw), "\r")
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		negated := false
		if line[0] == '!' {
			negated = true
			line = bytes.TrimSpace(line[1:])
		}
		switch string(line) {
		case ".dfmt", ".dfmt/", "/.dfmt", "/.dfmt/":
			ignored = !negated
		}
	}
	return ignored
}
