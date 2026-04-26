package capture

import "testing"

// TestMatchIgnorePattern locks in glob-with-** behavior. Before this matcher
// was added, fswatch used stdlib filepath.Match which treats "**" as two
// literal stars — meaning every default ignore in the shipped config
// (".dfmt/**", "node_modules/**", etc.) silently matched nothing and the
// watcher journaled its own writes back into the journal in an infinite
// loop the moment fs capture was enabled. The cases below are the regression
// test for that fix.
func TestMatchIgnorePattern(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
		desc    string
	}{
		// Prefix form: dir/**
		{".dfmt/**", ".dfmt", true, "prefix matches dir itself"},
		{".dfmt/**", ".dfmt/journal.jsonl", true, "prefix matches direct child"},
		{".dfmt/**", ".dfmt/sub/file.txt", true, "prefix matches deep descendant"},
		{".dfmt/**", "src/.dfmt", false, "prefix anchored to root, not nested"},
		{".dfmt/**", "dfmtish", false, "prefix requires exact dir name"},
		{"node_modules/**", "node_modules/foo/bar.js", true, "node_modules deep child"},
		{"node_modules/**", "node_modules", true, "node_modules dir itself"},
		{"node_modules/**", "src/node_modules/foo.js", false, "nested node_modules NOT matched by anchored pattern"},

		// Suffix form: **/x
		{"**/__pycache__", "__pycache__", true, "suffix matches at root"},
		{"**/__pycache__", "src/x/__pycache__", true, "suffix matches at depth"},
		{"**/__pycache__", "src/__pycache___not_quite", false, "suffix requires exact tail segment"},
		{"**/*.log", "app.log", true, "suffix glob matches at root"},
		{"**/*.log", "var/logs/app.log", true, "suffix glob matches at depth"},

		// Plain (no **)
		{"*.swp", "foo.swp", true, "single-* wildcard at root"},
		{"*.swp", "src/foo.swp", false, "single-* anchored — only matches root via filepath.Match"},
		{"foo", "foo", true, "literal exact"},
		{"foo", "bar", false, "literal mismatch"},

		// Middle form: a/**/b
		{"src/**/test.go", "src/test.go", true, "middle-** matches no extra dirs"},
		{"src/**/test.go", "src/a/test.go", true, "middle-** matches one dir"},
		{"src/**/test.go", "src/a/b/c/test.go", true, "middle-** matches many dirs"},
		{"src/**/test.go", "lib/a/test.go", false, "middle-** requires prefix anchor"},
	}

	for _, tc := range cases {
		got := matchIgnorePattern(tc.pattern, tc.path)
		if got != tc.want {
			t.Errorf("matchIgnorePattern(%q, %q) = %v, want %v — %s",
				tc.pattern, tc.path, got, tc.want, tc.desc)
		}
	}
}
