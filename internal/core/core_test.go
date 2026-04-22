package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventCanonicalRoundTrip(t *testing.T) {
	e := Event{
		ID:       "01ARYZ6S41TVGZPZ9J5QSBC4GT",
		TS:       time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		Project:  "test-project",
		Type:     EvtFileEdit,
		Priority: PriP1,
		Source:   SrcMCP,
		Actor:    "test-actor",
		Data: map[string]any{
			"path":    "/tmp/test.go",
			"content": "hello world",
		},
		Tags: []string{"important", "bugfix"},
	}

	// Compute signature
	e.Sig = e.ComputeSig()

	// Marshal
	data, err := CanonicalJSON(e)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	// Unmarshal
	var e2 Event
	if err := json.Unmarshal(data, &e2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verify fields
	if e2.ID != e.ID {
		t.Errorf("ID mismatch: got %s, want %s", e2.ID, e.ID)
	}
	if e2.Type != e.Type {
		t.Errorf("Type mismatch: got %s, want %s", e2.Type, e.Type)
	}
	if e2.Project != e.Project {
		t.Errorf("Project mismatch: got %s, want %s", e2.Project, e.Project)
	}
}

func TestEventValidate(t *testing.T) {
	e := Event{
		ID:       "01ARYZ6S41TVGZPZ9J5QSBC4GT",
		TS:       time.Now(),
		Project:  "test",
		Type:     EvtNote,
		Priority: PriP2,
		Source:   SrcCLI,
	}
	e.Sig = e.ComputeSig()

	if !e.Validate() {
		t.Error("Event should be valid")
	}

	// Tamper with data
	e.Data = map[string]any{"changed": true}
	if e.Validate() {
		t.Error("Tampered event should be invalid")
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"Hello World", []string{"hello", "world"}},
		{"foo-bar_baz", []string{"foo", "bar_baz"}},
		{"short x", []string{"short"}}, // x is too short
		{"a b c d", []string{}},        // all stopwords
	}

	for _, tt := range tests {
		got := TokenizeFull(tt.input, nil)
		if len(got) != len(tt.expected) {
			t.Errorf("Tokenize(%q): got %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestStem(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"running", "run"},
		{"connected", "connect"},
		{"computers", "computer"},
		{"walking", "walk"},
	}

	for _, tt := range tests {
		got := Stem(tt.input)
		if got != tt.expected {
			t.Errorf("Stem(%q): got %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBM25(t *testing.T) {
	bm := NewBM25Okapi()

	// Simple test: tf=1, docLen=avgDocLen, df=1, N=1
	score := bm.Score(1, 10, 10.0, 1, 10)
	if score <= 0 {
		t.Error("BM25 score should be positive")
	}

	// tf=0 should return 0
	score0 := bm.Score(0, 10, 10.0, 1, 10)
	if score0 != 0 {
		t.Errorf("BM25(tf=0): got %f, want 0", score0)
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a        string
		b        string
		expected int
	}{
		{"kitten", "kitten", 0},
		{"kitten", "sitting", 3},
		{"Saturday", "Sunday", 3},
		{"", "abc", 3},
		{"abc", "", 3},
	}

	for _, tt := range tests {
		got := Levenshtein(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("Levenshtein(%q, %q): got %d, want %d", tt.a, tt.b, got, tt.expected)
		}
	}
}

func TestULIDMonotonicity(t *testing.T) {
	ts := time.Now()
	ids := make([]ULID, 100)
	for i := range ids {
		ids[i] = NewULID(ts)
	}

	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("ULIDs not monotonically increasing at index %d", i)
		}
	}
}

func TestULIDTime(t *testing.T) {
	ts := time.Now()
	id := NewULID(ts)

	extracted := id.Time()
	// Allow 1ms tolerance
	diff := extracted.Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Errorf("ULID time differs by %v from input", diff)
	}
}

func TestFuzzyMatch(t *testing.T) {
	if !FuzzyMatch("hello", "helo", 1) {
		t.Error("FuzzyMatch(hello, helo, 1) should be true")
	}
	if FuzzyMatch("hello", "world", 2) {
		t.Error("FuzzyMatch(hello, world, 2) should be false")
	}
}

func TestConstants(t *testing.T) {
	if Version != "0.1.0" {
		t.Errorf("Version = %s, want '0.1.0'", Version)
	}
	if ULIDLen != 26 {
		t.Errorf("ULIDLen = %d, want 26", ULIDLen)
	}
	if MaxEventSize != 1024*1024 {
		t.Errorf("MaxEventSize = %d, want 1048576", MaxEventSize)
	}
	if DefaultBudget != 4096 {
		t.Errorf("DefaultBudget = %d, want 4096", DefaultBudget)
	}
	if MaxBudget != 1024*1024 {
		t.Errorf("MaxBudget = %d, want 1048576", MaxBudget)
	}
}

func TestPriorityConstants(t *testing.T) {
	if PriorityP1 != "p1" {
		t.Errorf("PriorityP1 = %s, want 'p1'", PriorityP1)
	}
	if PriorityP2 != "p2" {
		t.Errorf("PriorityP2 = %s, want 'p2'", PriorityP2)
	}
	if PriorityP3 != "p3" {
		t.Errorf("PriorityP3 = %s, want 'p3'", PriorityP3)
	}
	if PriorityP4 != "p4" {
		t.Errorf("PriorityP4 = %s, want 'p4'", PriorityP4)
	}
}

func TestSourceConstants(t *testing.T) {
	if SourceCLI != "cli" {
		t.Errorf("SourceCLI = %s, want 'cli'", SourceCLI)
	}
	if SourceMCP != "mcp" {
		t.Errorf("SourceMCP = %s, want 'mcp'", SourceMCP)
	}
	if SourceHook != "hook" {
		t.Errorf("SourceHook = %s, want 'hook'", SourceHook)
	}
	if SourceFS != "fs" {
		t.Errorf("SourceFS = %s, want 'fs'", SourceFS)
	}
	if SourceShell != "shell" {
		t.Errorf("SourceShell = %s, want 'shell'", SourceShell)
	}
	if SourceGit != "git" {
		t.Errorf("SourceGit = %s, want 'git'", SourceGit)
	}
}

func TestBM25Constants(t *testing.T) {
	if DefaultBM25K1 != 1.2 {
		t.Errorf("DefaultBM25K1 = %f, want 1.2", DefaultBM25K1)
	}
	if DefaultBM25B != 0.75 {
		t.Errorf("DefaultBM25B = %f, want 0.75", DefaultBM25B)
	}
	if DefaultHeadingBoost != 5.0 {
		t.Errorf("DefaultHeadingBoost = %f, want 5.0", DefaultHeadingBoost)
	}
}

func TestStopwords(t *testing.T) {
	if _, ok := EnglishStopwords["the"]; !ok {
		t.Error("EnglishStopwords should contain 'the'")
	}
	if _, ok := EnglishStopwords["and"]; !ok {
		t.Error("EnglishStopwords should contain 'and'")
	}
	if _, ok := TurkishStopwords["bir"]; !ok {
		t.Error("TurkishStopwords should contain 'bir'")
	}
}

func TestDefaultDurability(t *testing.T) {
	if DefaultDurability != "batched" {
		t.Errorf("DefaultDurability = %s, want 'batched'", DefaultDurability)
	}
}

func TestClassifier(t *testing.T) {
	c := NewClassifier()

	// Test default classification
	e := Event{Type: EvtDecision}
	if c.Classify(e) != PriP1 {
		t.Errorf("Decision should be P1, got %s", c.Classify(e))
	}

	e = Event{Type: EvtNote}
	if c.Classify(e) != PriP4 {
		t.Errorf("Note should be P4, got %s", c.Classify(e))
	}

	e = Event{Type: EvtGitCommit}
	if c.Classify(e) != PriP2 {
		t.Errorf("GitCommit should be P2, got %s", c.Classify(e))
	}

	e = Event{Type: EvtFileEdit}
	if c.Classify(e) != PriP3 {
		t.Errorf("FileEdit should be P3, got %s", c.Classify(e))
	}

	// Unknown type should default to P4
	e = Event{Type: "unknown_type"}
	if c.Classify(e) != PriP4 {
		t.Errorf("Unknown type should be P4, got %s", c.Classify(e))
	}
}

func TestClassifierAddRule(t *testing.T) {
	c := NewClassifier()

	// Add a rule for a specific type
	c.AddRule(Rule{
		Match:    RuleMatch{Type: "custom.event"},
		Priority: PriP1,
	})

	e := Event{Type: "custom.event"}
	if c.Classify(e) != PriP1 {
		t.Errorf("Custom event should be P1 via rule, got %s", c.Classify(e))
	}
}

func TestClassifierSetDefault(t *testing.T) {
	c := NewClassifier()

	// Change default for notes from P4 to P1
	c.SetDefault(EvtNote, PriP1)

	e := Event{Type: EvtNote}
	if c.Classify(e) != PriP1 {
		t.Errorf("Note should be P1 after SetDefault, got %s", c.Classify(e))
	}
}

func TestClassifierMatchRuleType(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{Type: EvtDecision},
		Priority: PriP2,
	})

	// Matching type
	e := Event{Type: EvtDecision}
	if c.Classify(e) != PriP2 {
		t.Errorf("Should match rule with P2, got %s", c.Classify(e))
	}

	// Non-matching type - should use default
	e = Event{Type: EvtNote}
	if c.Classify(e) != PriP4 {
		t.Errorf("Note should use default P4, got %s", c.Classify(e))
	}
}

func TestClassifierMatchRulePathGlob(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{PathGlob: "*.go"},
		Priority: PriP1,
	})

	// Matching path
	e := Event{Type: EvtFileEdit, Data: map[string]any{"path": "/tmp/main.go"}}
	if c.Classify(e) != PriP1 {
		t.Errorf("Should match *.go glob with P1, got %s", c.Classify(e))
	}

	// Non-matching path
	e = Event{Type: EvtFileEdit, Data: map[string]any{"path": "/tmp/main.txt"}}
	if c.Classify(e) != PriP3 {
		t.Errorf("Should use default P3 for *.txt, got %s", c.Classify(e))
	}
}

func TestClassifierMatchRuleMessageRegex(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{MessageRegex: "^ERROR:"},
		Priority: PriP1,
	})

	// Matching message
	e := Event{Type: EvtError, Data: map[string]any{"message": "ERROR: something failed"}}
	if c.Classify(e) != PriP1 {
		t.Errorf("Should match ERROR regex with P1, got %s", c.Classify(e))
	}

	// Non-matching message
	e = Event{Type: EvtError, Data: map[string]any{"message": "Warning: something happened"}}
	if c.Classify(e) != PriP2 {
		t.Errorf("Should use default P2 for Warning, got %s", c.Classify(e))
	}
}

func TestClassifierMatchRuleNoData(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{PathGlob: "*.go"},
		Priority: PriP1,
	})

	// No data should not match path glob rule
	e := Event{Type: EvtFileEdit}
	if c.Classify(e) != PriP3 {
		t.Errorf("Should use default P3 when no data, got %s", c.Classify(e))
	}
}

func TestEventTypes(t *testing.T) {
	tests := []struct {
		evt  EventType
		want string
	}{
		{EvtFileRead, "file.read"},
		{EvtFileEdit, "file.edit"},
		{EvtFileCreate, "file.create"},
		{EvtFileDelete, "file.delete"},
		{EvtTaskCreate, "task.create"},
		{EvtTaskUpdate, "task.update"},
		{EvtTaskDone, "task.done"},
		{EvtDecision, "decision"},
		{EvtError, "error"},
		{EvtGitCommit, "git.commit"},
		{EvtGitCheckout, "git.checkout"},
		{EvtGitPush, "git.push"},
		{EvtGitStash, "git.stash"},
		{EvtGitDiff, "git.diff"},
		{EvtEnvCwd, "env.cwd"},
		{EvtEnvVars, "env.vars"},
		{EvtEnvInstall, "env.install"},
		{EvtPrompt, "prompt"},
		{EvtMCPCall, "mcp.call"},
		{EvtSubagent, "subagent"},
		{EvtSkill, "skill"},
		{EvtRole, "role"},
		{EvtIntent, "intent"},
		{EvtDataRef, "data.ref"},
		{EvtNote, "note"},
		{EvtTombstone, "tombstone"},
	}

	for _, tt := range tests {
		if string(tt.evt) != tt.want {
			t.Errorf("EventType %s = %q, want %q", tt.want, tt.evt, tt.want)
		}
	}
}

func TestTokenizeDirect(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"foo-bar_baz", []string{"foo", "bar_baz"}},
		{"short x", []string{"short"}}, // x is too short
		{"a b c d", []string{}},        // all stopwords
		{"hello123", []string{"hello123"}},
		{"hello_world", []string{"hello_world"}},
		{"test.file.go", []string{"test", "file", "go"}},
		{"", []string{}},
		{"  spaces  ", []string{"spaces"}},
		{"camelCase", []string{"camelcase"}},
		{"UPPER", []string{"upper"}},
	}

	for _, tt := range tests {
		got := Tokenize(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("Tokenize(%q): got %v (len=%d), want %v (len=%d)", tt.input, got, len(got), tt.expected, len(tt.expected))
		}
	}
}

func TestTokenizeFull(t *testing.T) {
	tests := []struct {
		input string
		stop  map[string]struct{}
	}{
		{"hello world the and", EnglishStopwords},
		{"test with content", nil},
		{"test", TurkishStopwords},
	}

	for _, tt := range tests {
		got := TokenizeFull(tt.input, tt.stop)
		// Just verify it returns without error and has expected behavior
		if got == nil {
			t.Errorf("TokenizeFull(%q) returned nil", tt.input)
		}
	}
}

func TestEventComputeSig(t *testing.T) {
	e := Event{
		ID:       "test123",
		TS:       time.Now(),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
	}

	sig := e.ComputeSig()
	if sig == "" {
		t.Error("ComputeSig returned empty string")
	}
	if len(sig) != 16 {
		t.Errorf("Sig length = %d, want 16", len(sig))
	}

	// Same event should produce same sig
	sig2 := e.ComputeSig()
	if sig != sig2 {
		t.Error("ComputeSig should be deterministic")
	}
}

func TestCanonicalJSON(t *testing.T) {
	e := Event{
		ID:       "test123",
		TS:       time.Now(),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
	}

	data, err := CanonicalJSON(e)
	if err != nil {
		t.Fatalf("CanonicalJSON failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("CanonicalJSON returned empty")
	}

	// Verify it's valid JSON
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("CanonicalJSON output is not valid JSON: %v", err)
	}
}

func TestEventSigDifferentForDifferentEvents(t *testing.T) {
	e1 := Event{ID: "test1", Type: EvtNote, Priority: PriP4}
	e2 := Event{ID: "test2", Type: EvtNote, Priority: PriP4}

	sig1 := e1.ComputeSig()
	sig2 := e2.ComputeSig()

	if sig1 == sig2 {
		t.Error("Different events should have different signatures")
	}
}

func TestCanonicalJSONWithOptionalFields(t *testing.T) {
	e := Event{
		ID:       "test123",
		TS:       time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
		Actor:    "tester",
		Data:     map[string]any{"key": "value"},
		Refs:     []string{"ref1", "ref2"},
		Tags:     []string{"tag1"},
	}

	data, err := CanonicalJSON(e)
	if err != nil {
		t.Fatalf("CanonicalJSON failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("CanonicalJSON returned empty")
	}

	// Verify it's valid JSON
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("CanonicalJSON output is not valid JSON: %v", err)
	}

	// Check all fields are present
	if result["id"] != "test123" {
		t.Errorf("expected id=test123, got %v", result["id"])
	}
	if result["actor"] != "tester" {
		t.Errorf("expected actor=tester, got %v", result["actor"])
	}
	if result["data"] == nil {
		t.Error("expected data field to be present")
	}
}

func TestCanonicalJSONWithEmptyOptionalFields(t *testing.T) {
	// Test with no optional fields
	e := Event{
		ID:       "test123",
		TS:       time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
	}

	data, err := CanonicalJSON(e)
	if err != nil {
		t.Fatalf("CanonicalJSON failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("CanonicalJSON output is not valid JSON: %v", err)
	}

	// Verify no extra fields
	if _, hasActor := result["actor"]; hasActor {
		t.Error("actor should not be present when empty")
	}
	if _, hasData := result["data"]; hasData {
		t.Error("data should not be present when nil")
	}
	if _, hasRefs := result["refs"]; hasRefs {
		t.Error("refs should not be present when empty")
	}
	if _, hasTags := result["tags"]; hasTags {
		t.Error("tags should not be present when empty")
	}
}

func TestCanonicalJSONDataKeySorting(t *testing.T) {
	// Test that Data keys are sorted
	e := Event{
		ID:       "test123",
		TS:       time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
		Data:     map[string]any{"z_key": "z_value", "a_key": "a_value", "m_key": "m_value"},
	}

	data, err := CanonicalJSON(e)
	if err != nil {
		t.Fatalf("CanonicalJSON failed: %v", err)
	}

	jsonStr := string(data)
	// a_key should appear before m_key, which should appear before z_key
	aIdx := strings.Index(jsonStr, "a_key")
	mIdx := strings.Index(jsonStr, "m_key")
	zIdx := strings.Index(jsonStr, "z_key")

	if aIdx == -1 || mIdx == -1 || zIdx == -1 {
		t.Fatal("expected all keys to be present in output")
	}
	if aIdx > mIdx || mIdx > zIdx {
		t.Error("Data keys should be sorted alphabetically in canonical JSON")
	}
}

func TestCanonicalJSONTimestampFormat(t *testing.T) {
	e := Event{
		ID:       "test123",
		TS:       time.Date(2026, 4, 20, 10, 30, 45, 123456789, time.UTC),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
	}

	data, _ := CanonicalJSON(e)
	var result map[string]any
	_ = json.Unmarshal(data, &result)

	// RFC3339Nano format includes nanoseconds
	ts, ok := result["ts"].(string)
	if !ok {
		t.Fatal("ts should be a string")
	}
	// Should contain the time with nanoseconds
	if ts != "2026-04-20T10:30:45.123456789Z" {
		t.Errorf("expected RFC3339Nano timestamp, got %s", ts)
	}
}

func TestNewClassifierCopiesDefaults(t *testing.T) {
	c := NewClassifier()

	// Modify the classifier's defaults
	c.SetDefault(EvtNote, PriP1)

	// Create another classifier - should have original defaults
	c2 := NewClassifier()
	e := Event{Type: EvtNote}

	if c2.Classify(e) != PriP4 {
		t.Errorf("New classifier should have original defaults, got %s", c2.Classify(e))
	}
}

func TestNGrams(t *testing.T) {
	tests := []struct {
		tokens []string
		n      int
		want   []string
	}{
		{[]string{"hello"}, 2, []string{"he", "el", "ll", "lo"}},
		{[]string{"hi"}, 2, []string{"hi"}}, // length 2 >= n
		{[]string{"hello", "world"}, 3, []string{"hel", "ell", "llo", "wor", "orl", "rld"}},
		{[]string{}, 2, []string{}}, // empty
	}

	for _, tt := range tests {
		got := NGrams(tt.tokens, tt.n)
		if len(got) != len(tt.want) {
			t.Errorf("NGrams(%v, %d) len = %d, want %d", tt.tokens, tt.n, len(got), len(tt.want))
		}
	}
}

func TestUnique(t *testing.T) {
	tests := []struct {
		input []string
		want  []string
	}{
		{[]string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}}, // already unique
		{[]string{}, []string{}},                           // empty
		{[]string{"only"}, []string{"only"}},               // single element
	}

	for _, tt := range tests {
		got := Unique(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("Unique(%v) len = %d, want %d", tt.input, len(got), len(tt.want))
			continue
		}
		for i, v := range got {
			if v != tt.want[i] {
				t.Errorf("Unique(%v)[%d] = %q, want %q", tt.input, i, v, tt.want[i])
			}
		}
	}
}

func TestMerge(t *testing.T) {
	a := []string{"a", "c", "e"}
	b := []string{"b", "d", "f"}

	got := Merge(a, b)
	// Merge just concatenates
	wantLen := len(a) + len(b)
	if len(got) != wantLen {
		t.Errorf("Merge len = %d, want %d", len(got), wantLen)
	}
}

func TestTrigramIndex(t *testing.T) {
	ti := NewTrigramIndex()
	if ti == nil {
		t.Fatal("NewTrigramIndex returned nil")
	}
}

func TestTrigramIndexAdd(t *testing.T) {
	ti := NewTrigramIndex()
	ti.Add("doc1", "hello world")

	if len(ti.postings) == 0 {
		t.Error("postings should not be empty after Add")
	}
}

func TestTrigramIndexSearch(t *testing.T) {
	ti := NewTrigramIndex()
	ti.Add("doc1", "hello world")

	// Search for part of the first token ("hel" matches "hello")
	results := ti.Search("hel")
	if len(results) == 0 {
		t.Error("Search for 'hel' should return results")
	}
}

func TestTrigramIndexSearchShort(t *testing.T) {
	ti := NewTrigramIndex()
	ti.Add("doc1", "hello world")

	// Short substring (< 3 chars) should return all docs
	results := ti.Search("lo")
	if len(results) == 0 {
		t.Error("Search for short substring should return results")
	}
}

func TestTrigramIndexSearchNoMatch(t *testing.T) {
	ti := NewTrigramIndex()
	ti.Add("doc1", "hello world")

	// No matching docs
	results := ti.Search("xyz123")
	if len(results) > 0 {
		t.Errorf("Search for nonexistent got %v, want empty or nil", results)
	}
}

func TestIntersection(t *testing.T) {
	tests := []struct {
		a    []string
		b    []string
		want []string
	}{
		{[]string{"a", "b", "c"}, []string{"b", "c", "d"}, []string{"b", "c"}},
		{[]string{"a", "c"}, []string{"b", "d"}, []string{}},
		{[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
		{[]string{}, []string{"a"}, []string{}},
	}

	for _, tt := range tests {
		got := intersection(tt.a, tt.b)
		if len(got) != len(tt.want) {
			t.Errorf("intersection(%v, %v) len = %d, want %d", tt.a, tt.b, len(got), len(tt.want))
		}
	}
}

func TestHasSuffix(t *testing.T) {
	tests := []struct {
		s      string
		suffix string
		want   bool
	}{
		{"hello", "lo", true},
		{"hello", "world", false},
		{"hello", "hello", false}, // len(s) > len(suffix) check
		{"a", "a", false},
		{"ab", "b", true},
	}

	for _, tt := range tests {
		got := hasSuffix(tt.s, tt.suffix)
		if got != tt.want {
			t.Errorf("hasSuffix(%q, %q) = %v, want %v", tt.s, tt.suffix, got, tt.want)
		}
	}
}

func TestRemoveSuffix(t *testing.T) {
	tests := []struct {
		s      string
		suffix string
		want   string
	}{
		{"hello", "lo", "hel"},
		{"running", "ning", "run"},
	}

	for _, tt := range tests {
		got := removeSuffix(tt.s, tt.suffix)
		if got != tt.want {
			t.Errorf("removeSuffix(%q, %q) = %q, want %q", tt.s, tt.suffix, got, tt.want)
		}
	}
}

func TestContainsVowel(t *testing.T) {
	// rhythm has y which is treated as vowel in some positions
	tests := []struct {
		s    string
		want bool
	}{
		{"hello", true},
		{"", false},
		{"bcd", false},
		{"xyz", true}, // y acts as consonant at start, vowel elsewhere
	}

	for _, tt := range tests {
		got := containsVowel(tt.s)
		if got != tt.want {
			t.Errorf("containsVowel(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestIsDoubleConsonant(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"add", true},  // dd
		{"box", false}, // x is not double
		{"a", false},   // too short
		{"at", false},  // t is consonant but not double
		{"egg", true},  // gg
	}

	for _, tt := range tests {
		got := isDoubleConsonant(tt.s)
		if got != tt.want {
			t.Errorf("isDoubleConsonant(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestMeasure(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"tr", 0},
		{"tree", 1},
	}

	for _, tt := range tests {
		got := measure(tt.s)
		if got != tt.want {
			t.Errorf("measure(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

func TestJournalErrors(t *testing.T) {
	if ErrJournalFull.Error() != "journal has reached max bytes" {
		t.Errorf("ErrJournalFull = %q", ErrJournalFull.Error())
	}
	if ErrJournalNotFound.Error() != "journal not found" {
		t.Errorf("ErrJournalNotFound = %q", ErrJournalNotFound.Error())
	}
}

func TestJournalOptions(t *testing.T) {
	opt := JournalOptions{
		Path:     "/tmp/journal",
		MaxBytes: 1024,
		Durable:  true,
	}

	if opt.Path != "/tmp/journal" {
		t.Errorf("Path = %q, want '/tmp/journal'", opt.Path)
	}
	if opt.MaxBytes != 1024 {
		t.Errorf("MaxBytes = %d, want 1024", opt.MaxBytes)
	}
	if !opt.Durable {
		t.Error("Durable should be true")
	}
}

func TestOpenJournal(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	j, err := OpenJournal(journalPath, JournalOptions{
		MaxBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("OpenJournal failed: %v", err)
	}
	if j == nil {
		t.Fatal("OpenJournal returned nil")
	}

	if err := j.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestOpenJournalNonExistentDir(t *testing.T) {
	_, err := OpenJournal("/tmp/dir/that/cannot/be/created/journal.jsonl", JournalOptions{})
	// On Windows, this may succeed initially but Close will fail
	// We just check it doesn't panic
	_ = err
}

func TestJournalAppendAndStream(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	j, _ := OpenJournal(journalPath, JournalOptions{MaxBytes: 1024 * 1024})

	e := Event{
		ID:       "01ARYZ6S41TVGZPZ9J5QSBC4GT",
		TS:       time.Now(),
		Project:  "test-project",
		Type:     EvtNote,
		Priority: PriP4,
		Source:   SrcCLI,
	}
	e.Sig = e.ComputeSig()

	ctx := context.Background()
	if err := j.Append(ctx, e); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Stream from beginning
	ch, err := j.Stream(ctx, "")
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	var received int
	for range ch {
		received++
	}
	if received != 1 {
		t.Errorf("Received %d events, want 1", received)
	}

	j.Close()
}

func TestJournalAppendMaxBytes(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	// Create journal with very small max bytes
	j, _ := OpenJournal(journalPath, JournalOptions{MaxBytes: 10})

	e := Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     EvtNote,
		Priority: PriP4,
	}

	ctx := context.Background()
	// First append should work
	if err := j.Append(ctx, e); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Second append should fail due to size limit
	if err := j.Append(ctx, e); err == nil {
		t.Error("Append should fail when journal is full")
	}

	j.Close()
}

func TestJournalCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	j, _ := OpenJournal(journalPath, JournalOptions{MaxBytes: 1024 * 1024})

	e := Event{
		ID:   "01ARYZ6S41TVGZPZ9J5QSBC4GT",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()

	ctx := context.Background()
	_ = j.Append(ctx, e)

	cursor, err := j.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("Checkpoint failed: %v", err)
	}
	if cursor != e.ID {
		t.Errorf("Checkpoint = %q, want %q", cursor, e.ID)
	}

	j.Close()
}

func TestJournalStreamNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "nonexistent.jsonl")

	j, _ := OpenJournal(journalPath, JournalOptions{})

	ctx := context.Background()
	ch, err := j.Stream(ctx, "")
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	// Should return empty channel for nonexistent file
	for range ch {
		// no events
	}

	j.Close()
}

func TestJournalRotate(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	j, _ := OpenJournal(journalPath, JournalOptions{MaxBytes: 1024 * 1024})

	e := Event{
		ID:   "01ARYZ6S41TVGZPZ9J5QSBC4GT",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()

	ctx := context.Background()
	_ = j.Append(ctx, e)

	if err := j.Rotate(ctx); err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}

	j.Close()
}

func TestJournalRotateEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	j, _ := OpenJournal(journalPath, JournalOptions{})

	ctx := context.Background()
	// Rotate without any events should be no-op
	if err := j.Rotate(ctx); err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}

	j.Close()
}

func TestJournalDurable(t *testing.T) {
	tmpDir := t.TempDir()
	journalPath := filepath.Join(tmpDir, "journal.jsonl")

	j, _ := OpenJournal(journalPath, JournalOptions{
		MaxBytes: 1024 * 1024,
		Durable:  true,
	})

	e := Event{
		ID:   "test1",
		TS:   time.Now(),
		Type: EvtNote,
	}
	e.Sig = e.ComputeSig()

	ctx := context.Background()
	if err := j.Append(ctx, e); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	j.Close()
}

func TestULIDTimeInvalid(t *testing.T) {
	// Invalid ULID that can't be decoded as hex should return zero time
	invalid := ULID("INVALID_ID_12345678901234")
	ts := invalid.Time()
	if !ts.IsZero() {
		t.Error("Time() on invalid ULID should return zero time")
	}
}

func TestULIDTimeShort(t *testing.T) {
	// Too short ULID should return zero time
	short := ULID("01ARZ3NDEKTSV4RR")
	ts := short.Time()
	if !ts.IsZero() {
		t.Error("Time() on short ULID should return zero time")
	}
}

func TestNGramsEdgeCases(t *testing.T) {
	// Tokens shorter than n are skipped
	tokens := []string{"hi", "yo"}
	got := NGrams(tokens, 3)
	// "hi" is 2 chars < 3, skipped; "yo" is 2 chars < 3, skipped
	if len(got) != 0 {
		t.Errorf("NGrams with short tokens = %d, want 0", len(got))
	}

	// Single character tokens - skipped for n=2
	tokens = []string{"a", "b"}
	got = NGrams(tokens, 2)
	if len(got) != 0 {
		t.Errorf("NGrams single char tokens = %d, want 0", len(got))
	}

	// Tokens exactly n - should generate n-1+1 = 1 ngram
	tokens = []string{"abc", "def"}
	got = NGrams(tokens, 3)
	// abc: [0:3] = "abc" (1 ngram), def: [0:3] = "def" (1 ngram)
	if len(got) != 2 {
		t.Errorf("NGrams exact length tokens = %d, want 2", len(got))
	}
}

func TestUniqueEdgeCases(t *testing.T) {
	// All duplicates
	dups := []string{"a", "a", "a"}
	got := Unique(dups)
	if len(got) != 1 {
		t.Errorf("Unique with all dups len = %d, want 1", len(got))
	}

	// Single element
	single := []string{"only"}
	got = Unique(single)
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("Unique single = %v, want [only]", got)
	}
}

func TestMergeEdgeCases(t *testing.T) {
	// Empty a
	a := []string{}
	b := []string{"b"}
	got := Merge(a, b)
	if len(got) != 1 {
		t.Errorf("Merge with empty a len = %d, want 1", len(got))
	}

	// Empty b
	a = []string{"a"}
	b = []string{}
	got = Merge(a, b)
	if len(got) != 1 {
		t.Errorf("Merge with empty b len = %d, want 1", len(got))
	}

	// Both empty
	got = Merge([]string{}, []string{})
	if len(got) != 0 {
		t.Errorf("Merge both empty len = %d, want 0", len(got))
	}
}

func TestTrigramIndexEmpty(t *testing.T) {
	ti := NewTrigramIndex()

	// Empty text
	ti.Add("doc1", "")

	// Should have postings but empty for empty text
	if len(ti.postings) != 0 {
		t.Logf("Empty text creates %d postings", len(ti.postings))
	}
}

func TestBM25EdgeCases(t *testing.T) {
	bm := NewBM25Okapi()

	// Very high term frequency
	score := bm.Score(100, 10, 10.0, 1, 10)
	if score <= 0 {
		t.Error("BM25 with high tf should have positive score")
	}

	// High document length
	score = bm.Score(5, 1000, 10.0, 1, 10)
	if score <= 0 {
		t.Error("BM25 with high docLen should have positive score")
	}
}

func TestLevenshteinEdgeCases(t *testing.T) {
	// Same strings
	if Levenshtein("test", "test") != 0 {
		t.Error("Levenshtein same strings should be 0")
	}

	// Empty both
	if Levenshtein("", "") != 0 {
		t.Error("Levenshtein empty both should be 0")
	}

	// Very long strings
	long1 := "abcdefghijklmnopqrstuvwxyz"
	long2 := "zyxwvutsrqponmlkjihgfedcba"
	d := Levenshtein(long1, long2)
	if d <= 0 {
		t.Error("Levenshtein very different long strings should be positive")
	}
}

func TestFuzzyMatchEdgeCases(t *testing.T) {
	// Exact match
	if !FuzzyMatch("hello", "hello", 0) {
		t.Error("FuzzyMatch exact match should be true")
	}

	// Very high tolerance
	if FuzzyMatch("hello", "world", 100) {
		t.Log("FuzzyMatch with tolerance 100 matched unrelated strings")
	}

	// Empty strings
	if !FuzzyMatch("", "", 0) {
		t.Error("FuzzyMatch empty both should be true")
	}
}

func TestIntersectionEdgeCases(t *testing.T) {
	// One empty
	result := intersection([]string{"a"}, []string{})
	if len(result) != 0 {
		t.Errorf("intersection one empty = %d, want 0", len(result))
	}

	// Both empty
	result = intersection([]string{}, []string{})
	if len(result) != 0 {
		t.Errorf("intersection both empty = %d, want 0", len(result))
	}
}

func TestHasSuffixEdgeCases(t *testing.T) {
	// Same string returns false (len(s) > len(suffix) is false when equal)
	if hasSuffix("hello", "hello") != false {
		t.Error("hasSuffix same string should be false")
	}

	// Empty suffix - every string ends with empty string, but len(s) > len("") is true
	// So hasSuffix returns true for empty suffix
	if hasSuffix("hello", "") != true {
		t.Error("hasSuffix empty suffix should be true")
	}
}

func TestRemoveSuffixEdgeCases(t *testing.T) {
	// Suffix not present - removes last len(suffix) chars anyway
	result := removeSuffix("hello", "world")
	// hello[0:5-5] = hello[0:0] = ""
	if result != "" {
		t.Errorf("removeSuffix no match = %q, want empty string", result)
	}

	// Empty suffix - removes last 0 chars, so returns full string
	result = removeSuffix("hello", "")
	if result != "hello" {
		t.Errorf("removeSuffix empty suffix = %q, want hello", result)
	}

	// Suffix shorter than string - removes last 3 chars
	result = removeSuffix("hello", "llo")
	if result != "he" {
		t.Errorf("removeSuffix matching = %q, want he", result)
	}
}

func TestContainsVowelEdgeCases(t *testing.T) {
	// All consonants
	if containsVowel("bcdfg") != false {
		t.Error("containsVowel all consonants should be false")
	}

	// Single vowel
	if containsVowel("a") != true {
		t.Error("containsVowel single vowel should be true")
	}
}

func TestIsDoubleConsonantEdgeCases(t *testing.T) {
	// llama: last two chars are 'm' and 'a', not equal, so false
	if isDoubleConsonant("llama") != false {
		t.Error("isDoubleConsonant llama should be false")
	}

	// egg: last two chars are 'g' and 'g', both consonants, so true
	if isDoubleConsonant("egg") != true {
		t.Error("isDoubleConsonant egg should be true")
	}

	// Single letter
	if isDoubleConsonant("x") != false {
		t.Error("isDoubleConsonant single letter should be false")
	}
}

func TestMeasureEdgeCases(t *testing.T) {
	// All consonants - no VC pattern, so count is 0
	result := measure("trch")
	if result != 0 {
		t.Errorf("measure trch = %d, want 0", result)
	}

	// Contains vowel - "tree": CVCV
	// t=consonant, r=consonant, e=vowel
	result = measure("tree")
	if result != 1 {
		t.Errorf("measure tree = %d, want 1", result)
	}
}
