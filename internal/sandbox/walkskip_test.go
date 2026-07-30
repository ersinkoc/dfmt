package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mkfile writes content at dir/rel, creating parents.
func mkfile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

func grepFiles(resp GrepResp) []string {
	var out []string
	for _, m := range resp.Matches {
		out = append(out, filepath.ToSlash(m.File))
	}
	return out
}

func hasFileMatch(resp GrepResp, want string) bool {
	return slices.Contains(grepFiles(resp), want)
}

// TestGrepPrunesExcludedDirs: the core regression. Build output, VCS
// internals and dfmt's own state must not be searched.
func TestGrepPrunesExcludedDirs(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "internal/app.go", "package app // NEEDLE here\n")
	mkfile(t, dir, "dist/bundle.txt", "NEEDLE in build output\n")
	mkfile(t, dir, ".git/COMMIT_EDITMSG", "NEEDLE in git internals\n")
	mkfile(t, dir, "node_modules/dep/index.js", "NEEDLE in a dependency\n")
	mkfile(t, dir, ".dfmt/journal.jsonl", `{"msg":"NEEDLE in dfmt's own journal"}`+"\n")

	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "NEEDLE"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	got := grepFiles(resp)
	if len(got) != 1 || got[0] != "internal/app.go" {
		t.Errorf("matches=%v, want only internal/app.go", got)
	}
	// The exclusions must be reported, not silent: an unreported skip
	// is indistinguishable from "no matches there".
	if !strings.Contains(resp.Summary, "skipped") {
		t.Errorf("Summary=%q, want a skipped-dirs note", resp.Summary)
	}
}

// TestGrepExplicitPathIntoExcludedDirWins: the exclusions are a default,
// not a prohibition. An agent that names dist/ gets dist/ — the walker
// never prunes its own search root.
func TestGrepExplicitPathIntoExcludedDirWins(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "dist/notes.txt", "NEEDLE in build output\n")

	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{
		Pattern: "NEEDLE",
		Path:    "dist",
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !hasFileMatch(resp, "dist/notes.txt") {
		t.Errorf("matches=%v, want dist/notes.txt when path names dist explicitly", grepFiles(resp))
	}
}

// TestGrepSkipsBinaryFiles: a match inside an executable's string table
// is unusable to an agent and costs ~2.5x a source line in tokens
// (ApproxTokens charges one token per non-ASCII rune).
func TestGrepSkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "real.go", "// NEEDLE\n")
	// PE/COFF header — the shape of dist/dfmt.exe.
	mkfile(t, dir, "prog.exe", "MZ\x90\x00\x03\x00\x00\x00NEEDLE\x00\x00binary junk\n")
	// No magic number, but NUL bytes inside the sniff window: the shape
	// of a git packfile.
	mkfile(t, dir, "objects.pack", "PACK\x00\x00\x00\x02NEEDLE\x00\x00\x00\n")

	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "NEEDLE"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	got := grepFiles(resp)
	if len(got) != 1 || got[0] != "real.go" {
		t.Errorf("matches=%v, want only real.go", got)
	}
	if !strings.Contains(resp.Summary, "binary") {
		t.Errorf("Summary=%q, want a binary-skip note", resp.Summary)
	}
}

// TestGrepSkipsBinaryEvenUnderExplicitPath: unlike directory pruning,
// binary exclusion is unconditional. Dumping an executable's string
// table serves nobody, however explicitly it was requested.
func TestGrepSkipsBinaryEvenUnderExplicitPath(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "dist/prog.exe", "MZ\x90\x00NEEDLE\x00junk\n")
	mkfile(t, dir, "dist/readme.txt", "NEEDLE in text\n")

	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "NEEDLE", Path: "dist"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	got := grepFiles(resp)
	if len(got) != 1 || got[0] != "dist/readme.txt" {
		t.Errorf("matches=%v, want only dist/readme.txt", got)
	}
}

// TestGrepSkipsOversizedFiles guards the read cost for large *text*
// (logs, generated dumps) that binary sniffing would pass.
func TestGrepSkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "small.txt", "NEEDLE\n")
	big := strings.Repeat("filler line that is plain ascii text\n", 40)
	mkfile(t, dir, "huge.log", strings.Repeat(big, (maxGrepFileBytes/len(big))+2)+"NEEDLE\n")

	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "NEEDLE"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	got := grepFiles(resp)
	if len(got) != 1 || got[0] != "small.txt" {
		t.Errorf("matches=%v, want only small.txt", got)
	}
	if !strings.Contains(resp.Summary, "oversized") {
		t.Errorf("Summary=%q, want an oversized-skip note", resp.Summary)
	}
}

// TestGrepExcludedDirCannotStarveTheMatchCap is the precise failure that
// was observed on dfmt's own repo: WalkDir is lexical, so `dist` is
// reached before `internal`, and 58 of the 100 match slots went to
// dist/dfmt.exe while source returned nothing. Pruning has to happen
// before the cap is consumed, not after.
func TestGrepExcludedDirCannotStarveTheMatchCap(t *testing.T) {
	dir := t.TempDir()
	// 400 matches in an excluded dir that sorts before "zsrc".
	var buf strings.Builder
	for range 400 {
		buf.WriteString("NEEDLE line\n")
	}
	mkfile(t, dir, "dist/generated.txt", buf.String())
	mkfile(t, dir, "zsrc/app.go", "// NEEDLE in real source\n")

	resp, err := NewSandbox(dir).Grep(context.Background(), GrepReq{Pattern: "NEEDLE"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !hasFileMatch(resp, "zsrc/app.go") {
		t.Errorf("source match starved by excluded dir; matches=%v", grepFiles(resp))
	}
}

func TestShouldSkipDir(t *testing.T) {
	for _, name := range []string{".git", "dist", "node_modules", ".dfmt", "vendor", "__pycache__"} {
		if !shouldSkipDir(name) {
			t.Errorf("shouldSkipDir(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"internal", "cmd", "docs", "src", "distribution", "gitlab"} {
		if shouldSkipDir(name) {
			t.Errorf("shouldSkipDir(%q) = true, want false", name)
		}
	}
}

func TestPathHasSkippedDir(t *testing.T) {
	tests := []struct {
		path, pattern string
		want          bool
	}{
		{"dist/app.js", "**/*", true},
		{"internal/app.go", "**/*", false},
		// The caller named the directory: honor it.
		{"dist/app.js", "dist/*", false},
		{"a/node_modules/x.js", "**/node_modules/*.js", false},
		// A wildcard component is not a literal mention.
		{"dist/app.js", "di*/*", true},
		// Windows-shaped path, forward-slash pattern.
		{`dist\release\app.exe`, "**/*", true},
		// Substring collisions must not trigger.
		{"distribution/app.go", "**/*", false},
	}
	for _, tc := range tests {
		if got := pathHasSkippedDir(tc.path, tc.pattern); got != tc.want {
			t.Errorf("pathHasSkippedDir(%q, %q) = %v, want %v", tc.path, tc.pattern, got, tc.want)
		}
	}
}

func TestIsBinaryPath(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, body string
		want       bool
	}{
		{"text.go", "package main\n", false},
		{"empty.txt", "", false},
		{"utf8.md", "# Başlık — açıklama\n", false},
		{"pe.exe", "MZ\x90\x00\x03\x00", true},
		{"elf.bin", "\x7fELF\x02\x01\x01", true},
		{"png.png", "\x89PNG\r\n\x1a\n", true},
		{"nul.dat", "text\x00more", true},
	}
	for _, tc := range cases {
		p := mkfile(t, dir, tc.name, tc.body)
		if got := isBinaryPath(p); got != tc.want {
			t.Errorf("isBinaryPath(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if isBinaryPath(filepath.Join(dir, "does-not-exist")) {
		t.Error("isBinaryPath on a missing file = true, want false")
	}
}

// TestGlobExcludesBuildOutput: Glob has its own path (filepath.Glob plus
// a filter, no WalkDir), so it needed the exclusions wired separately —
// otherwise a `**/*` spends its 500-path cap on node_modules.
func TestGlobExcludesBuildOutput(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "src/app.go", "package app\n")
	mkfile(t, dir, "dist/app.js", "built\n")
	mkfile(t, dir, "node_modules/dep/i.js", "dep\n")

	resp, err := NewSandbox(dir).Glob(context.Background(), GlobReq{Pattern: "*/*"})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range resp.Files {
		slash := filepath.ToSlash(f)
		if strings.HasPrefix(slash, "dist/") || strings.HasPrefix(slash, "node_modules/") {
			t.Errorf("glob returned excluded path %q (all: %v)", f, resp.Files)
		}
	}
	found := false
	for _, f := range resp.Files {
		if filepath.ToSlash(f) == "src/app.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("glob lost the real source file; files=%v", resp.Files)
	}
}

// TestGlobExplicitPatternIntoExcludedDirWins mirrors Grep's escape
// hatch: naming the directory in the pattern keeps its contents.
func TestGlobExplicitPatternIntoExcludedDirWins(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "dist/app.js", "built\n")

	resp, err := NewSandbox(dir).Glob(context.Background(), GlobReq{Pattern: "dist/*"})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(resp.Files) != 1 || filepath.ToSlash(resp.Files[0]) != "dist/app.js" {
		t.Errorf("files=%v, want dist/app.js when the pattern names dist", resp.Files)
	}
}

func TestWalkSkipStatsNote(t *testing.T) {
	if got := (walkSkipStats{}).note(); got != "" {
		t.Errorf("empty stats note = %q, want empty (must cost no tokens)", got)
	}
	if got := (walkSkipStats{dirs: 1}).note(); !strings.Contains(got, "1 ignored dir") || strings.Contains(got, "dirs") {
		t.Errorf("singular note = %q", got)
	}
	got := (walkSkipStats{dirs: 3, binary: 2, large: 1}).note()
	for _, want := range []string{"3 ignored dirs", "2 binary", "1 oversized"} {
		if !strings.Contains(got, want) {
			t.Errorf("note = %q, missing %q", got, want)
		}
	}
}
