package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpusFixture describes a golden-corpus HTML fixture and the
// expectations its conversion must satisfy. Kept declarative so adding
// a new fixture is a single struct entry; the test loop does the rest.
type corpusFixture struct {
	file        string   // basename under testdata/
	wantContent []string // substrings the markdown form MUST contain
	dropTags    []string // tag literals that MUST NOT appear in output
}

// htmlCorpusFixtures models real-world page shapes — documentation,
// blog posts, issue trackers — without depending on live URLs. Each
// fixture is hand-written to exercise the walker on the elements
// agents actually fetch.
var htmlCorpusFixtures = []corpusFixture{
	{
		file: "html-doc-page.html",
		wantContent: []string{
			"# parseConfig(input, options)",
			"## Parameters",
			"## Returns",
			"## Errors",
			"```go",   // language hint preserved on code block
			"loadConfig",
		},
		dropTags: []string{"<script", "<style", "<nav", "<footer", "<aside", "<head>"},
	},
	{
		file: "html-blog-post.html",
		wantContent: []string{
			"# Why we rewrote our parser",
			"## The original problem",
			"streaming tokenizer",
			"![Parser data flow diagram]", // image alt+src preserved
		},
		dropTags: []string{"<script", "<style", "<aside", "<footer"},
	},
	{
		file: "html-issue-page.html",
		wantContent: []string{
			"# Bug: parseConfig crashes on empty input",
			"## Description",
			"```go",
			"@bob",
		},
		dropTags: []string{"<script", "<style", "<nav", "<footer"},
	},
}

// TestConvertHTML_Corpus runs the full HTML→markdown pipeline against
// each fixture and asserts:
//   - Conversion completes without panic.
//   - Output bytes are strictly smaller than input bytes (the
//     cap-regression guard would otherwise reroute through the lite
//     path; a corpus fixture failing this check would mean the walker
//     made the page bigger, which is a bug).
//   - Content-bearing keywords survive.
//   - Drop-set boilerplate tags are gone.
func TestConvertHTML_Corpus(t *testing.T) {
	for _, fx := range htmlCorpusFixtures {
		t.Run(fx.file, func(t *testing.T) {
			path := filepath.Join("testdata", fx.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			input := string(data)
			out := ConvertHTML(input)
			if out == "" {
				t.Fatal("ConvertHTML returned empty string")
			}
			if len(out) >= len(input) {
				t.Errorf("walker did not shrink the page: input=%d, output=%d",
					len(input), len(out))
			}
			for _, want := range fx.wantContent {
				if !strings.Contains(out, want) {
					t.Errorf("expected %q in markdown output but missing", want)
				}
			}
			for _, banned := range fx.dropTags {
				if strings.Contains(out, banned) {
					t.Errorf("drop-set tag literal %q leaked into output", banned)
				}
			}
		})
	}
}

// TestConvertHTML_Corpus_NoBoilerplate is the universal negative check:
// for every fixture, no <script>/<style>/<nav>/<footer>/<aside>
// substring (any case) appears in the output. Catches the regression
// where a future tokenizer change emits a tag that should have been
// dropped.
func TestConvertHTML_Corpus_NoBoilerplate(t *testing.T) {
	bannedSubstrings := []string{
		"<script", "</script>", "<style", "</style>",
		"<nav", "</nav>", "<footer", "</footer>",
		"<aside", "</aside>", "<head>", "</head>",
	}
	for _, fx := range htmlCorpusFixtures {
		t.Run(fx.file, func(t *testing.T) {
			path := filepath.Join("testdata", fx.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			out := strings.ToLower(ConvertHTML(string(data)))
			for _, banned := range bannedSubstrings {
				if strings.Contains(out, banned) {
					t.Errorf("boilerplate tag %q present in output", banned)
				}
			}
		})
	}
}
