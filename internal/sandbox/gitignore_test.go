package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompileIgnoreLineSemantics(t *testing.T) {
	cases := []struct {
		line  string
		path  string
		isDir bool
		want  bool
	}{
		{"*.log", "a.log", false, true},
		{"*.log", "deep/dir/a.log", false, true}, // unanchored: any depth
		{"*.log", "a.logx", false, false},
		{"/top.txt", "top.txt", false, true},
		{"/top.txt", "sub/top.txt", false, false}, // leading slash anchors
		{"doc/frotz", "doc/frotz", false, true},
		{"doc/frotz", "a/doc/frotz", false, false}, // inner slash anchors too
		{"build/", "build", true, true},
		{"build/", "build", false, false}, // dir-only
		{"build/", "src/build", true, true},
		{"**/foo", "a/b/foo", false, true},
		{"**/foo", "foo", false, true},
		{"abc/**", "abc/x/y", false, true},
		{"a/**/b", "a/b", false, true},
		{"a/**/b", "a/x/y/b", false, true},
		{"?.go", "a.go", false, true},
		{"?.go", "ab.go", false, false},
		{"[ab].txt", "b.txt", false, true},
		{"[!ab].txt", "c.txt", false, true},
		{"[!ab].txt", "a.txt", false, false},
		{`\#hash`, "#hash", false, true},
		{"gen", "gen/x.go", false, true}, // a matched dir covers its contents
	}
	for _, tc := range cases {
		r, ok := compileIgnoreLine(tc.line, "")
		if !ok {
			t.Errorf("%q did not compile", tc.line)
			continue
		}
		g := &gitIgnore{rules: []ignoreRule{r}}
		if got := g.ignored(tc.path, tc.isDir); got != tc.want {
			t.Errorf("%q vs %q (dir=%v) = %v, want %v", tc.line, tc.path, tc.isDir, got, tc.want)
		}
	}
	for _, skip := range []string{"", "   ", "# comment", "!"} {
		if _, ok := compileIgnoreLine(skip, ""); ok {
			t.Errorf("%q produced a rule", skip)
		}
	}
}

func TestGitIgnoreNegationAndNesting(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "*.log\n!keep.log\n")
	write("sub/.gitignore", "local.txt\n")
	write(".git/info/exclude", "secret.env\n")

	g := newGitIgnore(root, filepath.Join(root, "sub"))
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"a.log", true},
		{"keep.log", false}, // later negation wins
		{"sub/local.txt", true},
		{"local.txt", false}, // nested rule applies only beneath its dir
		{"secret.env", true}, // .git/info/exclude
		{"main.go", false},
	} {
		if got := g.ignored(tc.path, false); got != tc.want {
			t.Errorf("ignored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func gitignoredTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		".gitignore":         "generated/\n*.min.js\n!keep.min.js\n",
		"src/app.go":         "package src // needle\n",
		"generated/api.go":   "package gen // needle\n",
		"web/bundle.min.js":  "var x='needle'\n",
		"web/keep.min.js":    "var y='needle'\n",
		"web/sub/.gitignore": "local.go\n",
		"web/sub/local.go":   "package sub // needle\n",
		"web/sub/tracked.go": "package sub // needle\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The walkers honor .gitignore (ADR-0031): ignored files and directories
// are skipped and counted, a negated pattern re-includes, nested
// .gitignore files apply beneath their directory.
func TestGrepAndGlobHonorGitignore(t *testing.T) {
	root := gitignoredTree(t)
	sb := NewSandbox(root)
	ctx := context.Background()

	resp, err := sb.Grep(ctx, GrepReq{Pattern: "needle"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	got := map[string]bool{}
	for _, m := range resp.Matches {
		got[filepath.ToSlash(m.File)] = true
	}
	want := map[string]bool{"src/app.go": true, "web/keep.min.js": true, "web/sub/tracked.go": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grep files = %v, want %v", got, want)
	}
	if !strings.Contains(resp.Summary, "gitignored") {
		t.Errorf("summary does not report gitignored skips: %q", resp.Summary)
	}

	g, err := sb.Glob(ctx, GlobReq{Pattern: "**/*.go"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	files := map[string]bool{}
	for _, f := range g.Files {
		files[filepath.ToSlash(f)] = true
	}
	if files["generated/api.go"] || files["web/sub/local.go"] || !files["src/app.go"] || !files["web/sub/tracked.go"] {
		t.Errorf("glob files = %v", g.Files)
	}
}

// Asking for a gitignored directory by path is a request for it.
func TestGrepExplicitGitignoredRootIsSearched(t *testing.T) {
	root := gitignoredTree(t)
	resp, err := NewSandbox(root).Grep(context.Background(), GrepReq{Pattern: "needle", Path: "generated"})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Errorf("explicit gitignored root: %+v", resp.Matches)
	}
}
