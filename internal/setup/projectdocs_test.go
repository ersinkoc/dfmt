package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpsertDFMTBlock_CreatesNewFile pins the "fresh project" path:
// running `dfmt init` in a directory without an existing CLAUDE.md must
// create one containing exactly one marker-delimited block.
func TestUpsertDFMTBlock_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	if err := UpsertDFMTBlock(target, "hello body\n"); err != nil {
		t.Fatalf("UpsertDFMTBlock: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}

	if !bytes.Contains(got, []byte(dfmtBlockBegin)) {
		t.Errorf("output missing begin marker:\n%s", got)
	}
	if !bytes.Contains(got, []byte(dfmtBlockEnd)) {
		t.Errorf("output missing end marker:\n%s", got)
	}
	if !bytes.Contains(got, []byte("hello body")) {
		t.Errorf("output missing body:\n%s", got)
	}
	if c := bytes.Count(got, []byte(dfmtBlockBegin)); c != 1 {
		t.Errorf("begin marker count = %d, want 1", c)
	}
}

// TestUpsertDFMTBlock_AppendsToExisting covers the "user already has a
// CLAUDE.md" case: their content must be preserved and our block appended
// with a clean separator.
func TestUpsertDFMTBlock_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")
	preExisting := "# My project\n\nUser notes here.\n"
	if err := os.WriteFile(target, []byte(preExisting), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UpsertDFMTBlock(target, "dfmt body\n"); err != nil {
		t.Fatalf("UpsertDFMTBlock: %v", err)
	}

	got, _ := os.ReadFile(target)
	if !bytes.HasPrefix(got, []byte(preExisting)) {
		t.Errorf("user content not preserved at head:\n%s", got)
	}
	if !bytes.Contains(got, []byte("dfmt body")) {
		t.Errorf("dfmt body missing:\n%s", got)
	}
	if !bytes.Contains(got, []byte(dfmtBlockBegin)) || !bytes.Contains(got, []byte(dfmtBlockEnd)) {
		t.Errorf("markers missing:\n%s", got)
	}
}

// TestUpsertDFMTBlock_ReplacesExistingBlock proves idempotency: the second
// call must NOT duplicate the block, it must replace the previous one.
// Without this, every `dfmt init` would grow the file unboundedly.
func TestUpsertDFMTBlock_ReplacesExistingBlock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	if err := UpsertDFMTBlock(target, "first body\n"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := UpsertDFMTBlock(target, "second body\n"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, _ := os.ReadFile(target)
	if c := bytes.Count(got, []byte(dfmtBlockBegin)); c != 1 {
		t.Errorf("begin marker count = %d after upsert, want 1", c)
	}
	if c := bytes.Count(got, []byte(dfmtBlockEnd)); c != 1 {
		t.Errorf("end marker count = %d after upsert, want 1", c)
	}
	if bytes.Contains(got, []byte("first body")) {
		t.Errorf("old body still present after replacement:\n%s", got)
	}
	if !bytes.Contains(got, []byte("second body")) {
		t.Errorf("new body missing:\n%s", got)
	}
}

// TestUpsertDFMTBlock_PreservesSurroundingContent — user has notes both
// before AND after our block. After upsert, both halves must be intact.
func TestUpsertDFMTBlock_PreservesSurroundingContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	mixed := "# Header\n\nbefore\n\n" +
		dfmtBlockBegin + "\noriginal body\n" + dfmtBlockEnd + "\n\nafter\n"
	if err := os.WriteFile(target, []byte(mixed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UpsertDFMTBlock(target, "replacement body\n"); err != nil {
		t.Fatalf("UpsertDFMTBlock: %v", err)
	}

	got, _ := os.ReadFile(target)
	gs := string(got)
	if !strings.Contains(gs, "# Header") {
		t.Errorf("header lost:\n%s", gs)
	}
	if !strings.Contains(gs, "before") {
		t.Errorf("before-text lost:\n%s", gs)
	}
	if !strings.Contains(gs, "after") {
		t.Errorf("after-text lost:\n%s", gs)
	}
	if strings.Contains(gs, "original body") {
		t.Errorf("original block body still present:\n%s", gs)
	}
	if !strings.Contains(gs, "replacement body") {
		t.Errorf("replacement body missing:\n%s", gs)
	}
}

// TestUpsertDFMTBlock_RefusesMalformed: if the file has a begin marker
// but no matching end, refuse rather than nest a fresh block inside the
// orphan or silently corrupt the user's content. This is rare but real
// (manual edits gone wrong).
func TestUpsertDFMTBlock_RefusesMalformed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")
	malformed := "user note\n" + dfmtBlockBegin + "\nstuff but no end\n"
	if err := os.WriteFile(target, []byte(malformed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := UpsertDFMTBlock(target, "anything\n")
	if err == nil {
		t.Fatal("expected error on malformed file, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error text = %q; want substring %q", err.Error(), "malformed")
	}
}

// TestUpsertDFMTBlock_RejectsBodyWithMarkers: defense-in-depth — if a
// caller hands us body text that itself contains the markers, we'd
// generate ambiguous content (multiple begin tags). Refuse explicitly.
func TestUpsertDFMTBlock_RejectsBodyWithMarkers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	bad := "before\n" + dfmtBlockBegin + "\nnested\n" + dfmtBlockEnd + "\nafter\n"
	err := UpsertDFMTBlock(target, bad)
	if err == nil {
		t.Fatal("expected error on body containing markers, got nil")
	}
}

// TestUpsertDFMTBlock_IsByteIdempotent — running upsert twice with the
// same body must produce byte-identical output. Without this guarantee
// the file would churn whitespace on every init.
func TestUpsertDFMTBlock_IsByteIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")
	body := "stable body\n"

	if err := UpsertDFMTBlock(target, body); err != nil {
		t.Fatalf("first: %v", err)
	}
	first, _ := os.ReadFile(target)
	if err := UpsertDFMTBlock(target, body); err != nil {
		t.Fatalf("second: %v", err)
	}
	second, _ := os.ReadFile(target)
	if !bytes.Equal(first, second) {
		t.Errorf("non-idempotent — bytes differ\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestStripDFMTBlock_NoOpOnAbsent — strip must not error when the file
// has no marker (uninstall-after-uninstall case).
func TestStripDFMTBlock_NoOpOnAbsent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")
	original := "# clean file\nnothing of ours here\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := StripDFMTBlock(target); err != nil {
		t.Fatalf("StripDFMTBlock: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, []byte(original)) {
		t.Errorf("file mutated by no-op strip:\nbefore:\n%s\nafter:\n%s", original, got)
	}
}

// TestStripDFMTBlock_RemovesBlock — install then strip, surrounding
// content survives, our block is gone.
func TestStripDFMTBlock_RemovesBlock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(target, []byte("# header\n\nuser\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := UpsertDFMTBlock(target, "dfmt content\n"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := StripDFMTBlock(target); err != nil {
		t.Fatalf("strip: %v", err)
	}
	got, _ := os.ReadFile(target)
	gs := string(got)
	if strings.Contains(gs, dfmtBlockBegin) || strings.Contains(gs, dfmtBlockEnd) {
		t.Errorf("markers still present after strip:\n%s", gs)
	}
	if strings.Contains(gs, "dfmt content") {
		t.Errorf("dfmt content still present after strip:\n%s", gs)
	}
	if !strings.Contains(gs, "# header") || !strings.Contains(gs, "user") {
		t.Errorf("user content lost:\n%s", gs)
	}
}

// TestStripDFMTBlock_RemovesEmptyFile — when DFMT created the file
// fresh and is now stripping its own contribution, nothing remains;
// don't leave a 0-byte CLAUDE.md the user didn't ask for.
func TestStripDFMTBlock_RemovesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")
	if err := UpsertDFMTBlock(target, "only us\n"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := StripDFMTBlock(target); err != nil {
		t.Fatalf("strip: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected file removed; stat err = %v", err)
	}
}

// TestProjectInstructionPath maps agent IDs to their canonical
// instruction file. Unknown agents return ("", false) so callers can
// skip silently.
func TestProjectInstructionPath(t *testing.T) {
	dir := filepath.Clean("/proj")

	if p, ok := ProjectInstructionPath(dir, "claude-code"); !ok || filepath.Base(p) != "CLAUDE.md" {
		t.Errorf("claude-code -> %q ok=%v; want .../CLAUDE.md ok=true", p, ok)
	}
	if _, ok := ProjectInstructionPath(dir, "no-such-agent"); ok {
		t.Errorf("unknown agent should return ok=false")
	}
}

// TestUpsertProjectInstructions_ClaudeCode is the integration check:
// pass a projectDir + agentID, end up with a CLAUDE.md containing the
// canonical Claude Code body.
func TestUpsertProjectInstructions_ClaudeCode(t *testing.T) {
	dir := t.TempDir()

	path, err := UpsertProjectInstructions(dir, "claude-code")
	if err != nil {
		t.Fatalf("UpsertProjectInstructions: %v", err)
	}
	if filepath.Base(path) != "CLAUDE.md" {
		t.Errorf("path = %q, want .../CLAUDE.md", path)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	gs := string(got)
	for _, want := range []string{
		dfmtBlockBegin,
		dfmtBlockEnd,
		"Context Discipline",
		"dfmt_exec",
		"dfmt_remember",
		"`intent`",
	} {
		if !strings.Contains(gs, want) {
			t.Errorf("CLAUDE.md missing %q\n--- file ---\n%s", want, gs)
		}
	}
}

// TestUpsertProjectInstructions_UnknownAgent — silent no-op, no file
// created. Callers iterating over Detect() shouldn't have to filter.
func TestUpsertProjectInstructions_UnknownAgent(t *testing.T) {
	dir := t.TempDir()

	path, err := UpsertProjectInstructions(dir, "no-such-agent")
	if err != nil {
		t.Fatalf("err = %v, want nil for unknown agent", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty for unknown agent", path)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("dir not empty after unknown-agent upsert: %v", entries)
	}
}

// TestProjectInstructionPath_AllWiredAgents pins every agent ID we
// currently route. A regression that drops one of these mappings would
// silently turn `dfmt init` into a no-op for that agent.
func TestProjectInstructionPath_AllWiredAgents(t *testing.T) {
	root := filepath.Clean("/proj")
	cases := []struct {
		agent    string
		wantTail string
	}{
		{"claude-code", "CLAUDE.md"},
		{"gemini", "GEMINI.md"},
		{"vscode", filepath.Join(".github", "copilot-instructions.md")},
		{"codex", "AGENTS.md"},
		{"opencode", "AGENTS.md"},
		{"zed", "AGENTS.md"},
		{"cursor", ".cursorrules"},
		{"windsurf", ".windsurfrules"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			p, ok := ProjectInstructionPath(root, tc.agent)
			if !ok {
				t.Fatalf("agent %q: ProjectInstructionPath returned ok=false", tc.agent)
			}
			if !strings.HasSuffix(p, tc.wantTail) {
				t.Errorf("agent %q: path = %q, want suffix %q", tc.agent, p, tc.wantTail)
			}
		})
	}
}

// TestUpsertProjectInstructions_AGENTSmdShared verifies that codex,
// opencode, and zed all upsert the same AGENTS.md content. The single-
// file-many-agents pattern depends on bytes-equal output regardless of
// which agent runs first; otherwise re-running init with a different
// agent set would churn the block content unnecessarily.
func TestUpsertProjectInstructions_AGENTSmdShared(t *testing.T) {
	for _, agent := range []string{"codex", "opencode", "zed"} {
		t.Run(agent, func(t *testing.T) {
			dir := t.TempDir()
			path, err := UpsertProjectInstructions(dir, agent)
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if filepath.Base(path) != "AGENTS.md" {
				t.Fatalf("path = %q, want .../AGENTS.md", path)
			}
			got, _ := os.ReadFile(path)
			gs := string(got)
			for _, want := range []string{
				dfmtBlockBeginMD,
				dfmtBlockEndMD,
				"REQUIRED",
				"Compliance is your responsibility",
				"dfmt_remember",
			} {
				if !strings.Contains(gs, want) {
					t.Errorf("AGENTS.md (%s) missing %q\n--- file ---\n%s", agent, want, gs)
				}
			}
		})
	}
}

// TestUpsertProjectInstructions_ContinueIsSilentNoOp pins the
// design choice that Continue.dev has no project-root injection
// point — its rules live in user-scope ~/.continue/config.yaml.
// A regression that wires Continue to AGENTS.md or similar would
// silently overwrite user files; verify the no-op holds.
func TestUpsertProjectInstructions_ContinueIsSilentNoOp(t *testing.T) {
	dir := t.TempDir()
	path, err := UpsertProjectInstructions(dir, "continue")
	if err != nil {
		t.Fatalf("err = %v, want nil for Continue (no-op)", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty (Continue has no project file)", path)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("dir not empty after Continue upsert: %v", entries)
	}
}

// TestUpsertProjectInstructions_WindsurfMarkerStyle proves Windsurf
// gets the cursor-style plain-text markers, not HTML. Windsurf parses
// .windsurfrules line by line just like Cursor parses .cursorrules.
func TestUpsertProjectInstructions_WindsurfMarkerStyle(t *testing.T) {
	dir := t.TempDir()
	path, err := UpsertProjectInstructions(dir, "windsurf")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if filepath.Base(path) != ".windsurfrules" {
		t.Fatalf("path = %q, want .../.windsurfrules", path)
	}
	got, _ := os.ReadFile(path)
	gs := string(got)
	if !strings.Contains(gs, dfmtBlockBeginCursor) {
		t.Errorf(".windsurfrules missing cursor-style begin marker:\n%s", gs)
	}
	if strings.Contains(gs, dfmtBlockBeginMD) {
		t.Errorf(".windsurfrules contains markdown-style marker (wrong style):\n%s", gs)
	}
}

// TestUpsertProjectInstructions_CursorrulesMarkerStyle proves the
// .cursorrules path uses plain-text `#`-prefixed markers, not HTML
// comments. Cursor parses the file line by line and would surface
// `<!-- ... -->` as raw text.
func TestUpsertProjectInstructions_CursorrulesMarkerStyle(t *testing.T) {
	dir := t.TempDir()
	path, err := UpsertProjectInstructions(dir, "cursor")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if filepath.Base(path) != ".cursorrules" {
		t.Fatalf("path = %q, want .../.cursorrules", path)
	}
	got, _ := os.ReadFile(path)
	gs := string(got)
	if !strings.Contains(gs, dfmtBlockBeginCursor) {
		t.Errorf(".cursorrules missing cursor-style begin marker %q\n%s", dfmtBlockBeginCursor, gs)
	}
	if !strings.Contains(gs, dfmtBlockEndCursor) {
		t.Errorf(".cursorrules missing cursor-style end marker %q\n%s", dfmtBlockEndCursor, gs)
	}
	if strings.Contains(gs, dfmtBlockBeginMD) {
		t.Errorf(".cursorrules contains markdown-style marker (wrong style):\n%s", gs)
	}
}

// TestUpsertDFMTBlock_CursorStyleReplaces verifies the cursor-style
// markers participate in the replace path the same way markdown-style
// markers do — without this, a second `dfmt init` would append a
// second cursor block instead of upserting.
func TestUpsertDFMTBlock_CursorStyleReplaces(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".cursorrules")

	if err := UpsertDFMTBlock(target, "first body\n"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := UpsertDFMTBlock(target, "second body\n"); err != nil {
		t.Fatalf("second: %v", err)
	}
	got, _ := os.ReadFile(target)
	if c := bytes.Count(got, []byte(dfmtBlockBeginCursor)); c != 1 {
		t.Errorf("cursor begin marker count = %d, want 1\n%s", c, got)
	}
	if bytes.Contains(got, []byte("first body")) {
		t.Errorf("first body still present after upsert:\n%s", got)
	}
	if !bytes.Contains(got, []byte("second body")) {
		t.Errorf("second body missing:\n%s", got)
	}
}

// TestStripDFMTBlock_CursorStyle exercises strip on .cursorrules to
// confirm the marker-style switch reaches Strip too. Without per-style
// markers in StripDFMTBlock, a `setup --uninstall` against a Cursor
// project would leave the block in place (begin marker not found).
func TestStripDFMTBlock_CursorStyle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".cursorrules")
	if err := UpsertDFMTBlock(target, "to be removed\n"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := StripDFMTBlock(target); err != nil {
		t.Fatalf("strip: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".cursorrules should be removed after strip-of-only-block; stat err = %v", err)
	}
}

// TestManifestPersistsFileKind covers the Kind field roundtrip:
// SaveManifest → LoadManifest must preserve FileKindStrip so uninstall
// knows to call StripDFMTBlock instead of os.Remove. A regression that
// drops the JSON/YAML tag on Kind would leave instruction files orphaned
// after `dfmt setup --uninstall`.
func TestManifestPersistsFileKind(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	original := &Manifest{
		Version: 1,
		Files: []FileEntry{
			{Path: filepath.Join(tmp, "regular.json"), Agent: AgentClaudeCode, Version: "1"},
			{Path: filepath.Join(tmp, "CLAUDE.md"), Agent: AgentClaudeCode, Version: "v1", Kind: FileKindStrip},
			{Path: filepath.Join(tmp, "AGENTS.md"), Agent: AgentCodex, Version: "v1", Kind: FileKindStrip},
		},
	}
	if err := SaveManifest(original); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	got, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(got.Files))
	}

	byName := make(map[string]FileEntry)
	for _, f := range got.Files {
		byName[filepath.Base(f.Path)] = f
	}
	if k := byName["regular.json"].Kind; k != "" {
		t.Errorf("regular.json Kind = %q, want empty (legacy/delete)", k)
	}
	if k := byName["CLAUDE.md"].Kind; k != FileKindStrip {
		t.Errorf("CLAUDE.md Kind = %q, want %q", k, FileKindStrip)
	}
	if k := byName["AGENTS.md"].Kind; k != FileKindStrip {
		t.Errorf("AGENTS.md Kind = %q, want %q", k, FileKindStrip)
	}
}

// TestExtractDFMTBlock_RoundtripsCanonicalBody pins the doctor
// staleness check's contract: ExtractDFMTBlock must return the body
// in a form that compares byte-equal to ProjectBlockBodyForAgent
// after a single TrimRight on both sides. A regression that adds a
// stray newline or whitespace would flip every doctor check to "stale".
func TestExtractDFMTBlock_RoundtripsCanonicalBody(t *testing.T) {
	dir := t.TempDir()
	path, err := UpsertProjectInstructions(dir, "claude-code")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, found, err := ExtractDFMTBlock(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !found {
		t.Fatal("ExtractDFMTBlock found=false on a freshly-upserted file")
	}

	canonical := ProjectBlockBodyForAgent("claude-code")
	gotTrim := strings.TrimRight(got, "\n")
	wantTrim := strings.TrimRight(canonical, "\n")
	if gotTrim != wantTrim {
		t.Errorf("roundtrip mismatch — staleness check would falsely flag fresh files\n--- extracted ---\n%s\n--- canonical ---\n%s\n", got, canonical)
	}
}

// TestExtractDFMTBlock_DetectsDrift covers the actual staleness
// scenario: a CLAUDE.md from an older dfmt has a different body than
// the current canonical. ExtractDFMTBlock returns the old body;
// caller compares to canonical and detects drift.
func TestExtractDFMTBlock_DetectsDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	// Hand-write an "old version" block — the marker says v1 but the
	// body is fictional.
	oldContent := dfmtBlockBeginMD + "\nold body from v0.1.0\n" + dfmtBlockEndMD + "\n"
	if err := os.WriteFile(path, []byte(oldContent), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, found, err := ExtractDFMTBlock(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !found {
		t.Fatal("ExtractDFMTBlock found=false on file with valid markers")
	}
	if got != "old body from v0.1.0" {
		t.Errorf("got body = %q, want %q", got, "old body from v0.1.0")
	}

	canonical := ProjectBlockBodyForAgent("claude-code")
	if strings.TrimRight(got, "\n") == strings.TrimRight(canonical, "\n") {
		t.Error("old body matched current canonical — drift detection would not fire")
	}
}

// TestExtractDFMTBlock_NoMarkersIsNotFound — a clean user CLAUDE.md
// with no DFMT injection must return found=false, no error. Doctor
// uses this to skip files DFMT never touched.
func TestExtractDFMTBlock_NoMarkersIsNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("# user notes\nno dfmt here\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, found, err := ExtractDFMTBlock(path)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if found {
		t.Errorf("found = true, want false (no markers in file)")
	}
	if got != "" {
		t.Errorf("got = %q, want empty", got)
	}
}

// TestStyleBodiesDoNotContainOwnMarkers — defense-in-depth. If someone
// edits a body and accidentally includes the marker text, UpsertDFMTBlock
// would refuse with the body-contains-markers error. Catch that at test
// time, not first-call-from-init.
func TestStyleBodiesDoNotContainOwnMarkers(t *testing.T) {
	cases := []struct {
		name string
		body string
		m    markerStyle
	}{
		{"markdown", markdownProjectBlockBody, markerStyle{dfmtBlockBeginMD, dfmtBlockEndMD}},
		{"agentsmd", agentsMdProjectBlockBody, markerStyle{dfmtBlockBeginMD, dfmtBlockEndMD}},
		{"cursorrules", cursorrulesProjectBlockBody, markerStyle{dfmtBlockBeginCursor, dfmtBlockEndCursor}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.body, tc.m.begin) {
				t.Errorf("body contains its own begin marker %q", tc.m.begin)
			}
			if strings.Contains(tc.body, tc.m.end) {
				t.Errorf("body contains its own end marker %q", tc.m.end)
			}
		})
	}
}
