package core

import (
	"testing"
)

func TestMatchRuleMessageRegexInvalid(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{MessageRegex: "[invalid"},
		Priority: PriP1,
	})

	// Invalid regex should not match (err != nil branch)
	e := Event{Type: EvtError, Data: map[string]any{"message": "ERROR: something"}}
	if c.Classify(e) != PriP2 {
		t.Errorf("Invalid regex should fall through to default P2, got %s", c.Classify(e))
	}
}

func TestMatchRuleMessageRegexNoMatch(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{MessageRegex: "^ERROR:"},
		Priority: PriP1,
	})

	// Valid regex but message doesn't match
	e := Event{Type: EvtError, Data: map[string]any{"message": "Just a warning"}}
	if c.Classify(e) != PriP2 {
		t.Errorf("Non-matching message should use default P2, got %s", c.Classify(e))
	}
}

func TestMatchRuleMessageRegexMatch(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{MessageRegex: "^ERROR:"},
		Priority: PriP1,
	})

	// Matching message
	e := Event{Type: EvtError, Data: map[string]any{"message": "ERROR: something failed"}}
	if c.Classify(e) != PriP1 {
		t.Errorf("Should match regex with P1, got %s", c.Classify(e))
	}
}

func TestMatchRuleMessageNotString(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{MessageRegex: "^ERROR:"},
		Priority: PriP1,
	})

	// Message is not a string
	e := Event{Type: EvtError, Data: map[string]any{"message": 123}}
	if c.Classify(e) != PriP2 {
		t.Errorf("Non-string message should use default P2, got %s", c.Classify(e))
	}
}

func TestMatchRuleMultipleConditions(t *testing.T) {
	c := NewClassifier()

	// Rule with both type and regex
	c.AddRule(Rule{
		Match:    RuleMatch{Type: EvtError, MessageRegex: "timeout"},
		Priority: PriP1,
	})

	// Matching type but non-matching message
	e := Event{Type: EvtError, Data: map[string]any{"message": "ERROR: success"}}
	if c.Classify(e) != PriP2 {
		t.Errorf("Type match but regex no match should use default P2, got %s", c.Classify(e))
	}

	// Matching both
	e2 := Event{Type: EvtError, Data: map[string]any{"message": "timeout occurred"}}
	if c.Classify(e2) != PriP1 {
		t.Errorf("Both type and regex match should use P1, got %s", c.Classify(e2))
	}
}

func TestMatchRulePathGlobNotString(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{PathGlob: "*.go"},
		Priority: PriP1,
	})

	// Path is not a string
	e := Event{Type: EvtFileEdit, Data: map[string]any{"path": 123}}
	if c.Classify(e) != PriP3 {
		t.Errorf("Non-string path should use default P3, got %s", c.Classify(e))
	}
}

func TestMatchRulePathGlobInvalidPattern(t *testing.T) {
	c := NewClassifier()

	// Invalid glob pattern - this actually should work with filepath.Match
	c.AddRule(Rule{
		Match:    RuleMatch{PathGlob: "*.go"},
		Priority: PriP1,
	})

	e := Event{Type: EvtFileEdit, Data: map[string]any{"path": "/tmp/main.txt"}}
	if c.Classify(e) != PriP3 {
		t.Errorf("Non-matching glob should use default P3, got %s", c.Classify(e))
	}
}

func TestMatchRulePathGlobMatch(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{PathGlob: "*.go"},
		Priority: PriP1,
	})

	e := Event{Type: EvtFileEdit, Data: map[string]any{"path": "main.go"}}
	if c.Classify(e) != PriP1 {
		t.Errorf("Matching glob should use P1, got %s", c.Classify(e))
	}
}

func TestMatchRuleTypeMismatch(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{Type: EvtFileEdit},
		Priority: PriP1,
	})

	e := Event{Type: EvtNote}
	if c.Classify(e) != PriP4 {
		t.Errorf("Type mismatch should use default P4, got %s", c.Classify(e))
	}
}

func TestMatchRuleTypeMatch(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{Type: EvtNote},
		Priority: PriP2,
	})

	e := Event{Type: EvtNote}
	if c.Classify(e) != PriP2 {
		t.Errorf("Type match should use rule P2, got %s", c.Classify(e))
	}
}

func TestMatchRuleDataNil(t *testing.T) {
	c := NewClassifier()

	c.AddRule(Rule{
		Match:    RuleMatch{PathGlob: "*.go"},
		Priority: PriP1,
	})

	e := Event{Type: EvtFileEdit, Data: nil}
	if c.Classify(e) != PriP3 {
		t.Errorf("Nil Data should use default P3, got %s", c.Classify(e))
	}
}

func TestMatchRuleMessageRegexCompileError(t *testing.T) {
	c := NewClassifier()

	// Test that invalid regex pattern causes matchRule to return false
	// so classification falls through to default
	c.AddRule(Rule{
		Match:    RuleMatch{MessageRegex: "[invalid"},
		Priority: PriP1,
	})

	e := Event{Type: EvtNote, Data: map[string]any{"message": "test"}}
	result := c.Classify(e)
	// Should not be PriP1 since invalid regex doesn't match
	if result == PriP1 {
		t.Error("Invalid regex should not match, expected different priority")
	}
}

// TestNoteElevation_P2Tags pins the dogfood-discovered defect closure:
// notes carrying summary/decision/strengths/ledger tags must classify
// at P2 so a tight-budget recall keeps them above tool calls. Without
// this, the entire audit-trail value of dfmt_remember collapses —
// every recorded note ranked equal to a routine tool.read event.
func TestNoteElevation_P2Tags(t *testing.T) {
	c := NewClassifier()

	cases := []string{"summary", "decision", "strengths", "ledger"}
	for _, tag := range cases {
		e := Event{
			Type: EvtNote,
			Data: map[string]any{"message": "session summary"},
			Tags: []string{tag, "extra-tag"},
		}
		if got := c.Classify(e); got != PriP2 {
			t.Errorf("note with tag %q should classify P2, got %s", tag, got)
		}
	}
}

// TestNoteElevation_P3Tags pins individual-finding notes at P3 — above
// routine tool calls but below session-level summaries.
func TestNoteElevation_P3Tags(t *testing.T) {
	c := NewClassifier()

	cases := []string{"audit", "finding", "followup", "preserve"}
	for _, tag := range cases {
		e := Event{
			Type: EvtNote,
			Data: map[string]any{"message": "F-G-LOW-1 closure"},
			Tags: []string{tag},
		}
		if got := c.Classify(e); got != PriP3 {
			t.Errorf("note with tag %q should classify P3, got %s", tag, got)
		}
	}
}

// TestNoteElevation_P2OverridesP3 ensures the seeded rules fire in the
// declared order — a note tagged with both summary (P2) and audit (P3)
// must land at P2, not P3, because the P2 rule is registered first.
// Confirms the "first-matching-rule wins" contract holds for the seeded
// defaults.
func TestNoteElevation_P2OverridesP3(t *testing.T) {
	c := NewClassifier()

	e := Event{
		Type: EvtNote,
		Data: map[string]any{"message": "session-level audit summary"},
		Tags: []string{"audit", "summary"},
	}
	if got := c.Classify(e); got != PriP2 {
		t.Errorf("note with both P2 and P3 tags should classify P2, got %s", got)
	}
}

// TestNoteElevation_NoTagsFallsThroughToDefault confirms the existing
// P4 default still applies for plain (untagged or routine-tagged) notes.
// Regression guard: don't accidentally promote ALL notes — only those
// whose tags signal session-spanning value.
func TestNoteElevation_NoTagsFallsThroughToDefault(t *testing.T) {
	c := NewClassifier()

	e := Event{
		Type: EvtNote,
		Data: map[string]any{"message": "incidental observation"},
		// No tags AT ALL.
	}
	if got := c.Classify(e); got != PriP4 {
		t.Errorf("untagged note should keep default P4, got %s", got)
	}

	// Routine tag that isn't on the elevation lists must also stay P4.
	e2 := Event{
		Type: EvtNote,
		Data: map[string]any{"message": "incidental observation"},
		Tags: []string{"misc"},
	}
	if got := c.Classify(e2); got != PriP4 {
		t.Errorf("note with non-elevating tag %q should keep default P4, got %s", "misc", got)
	}
}

// TestNoteElevation_TypeGuard pins that the elevation only applies to
// EvtNote — a hypothetical EvtMCPCall with the same tags should NOT be
// promoted (those are tool-call records, not deliberate session notes).
func TestNoteElevation_TypeGuard(t *testing.T) {
	c := NewClassifier()

	e := Event{
		Type: EvtMCPCall,
		Data: map[string]any{"message": "tool.read"},
		Tags: []string{"summary"}, // would elevate IF it were a note
	}
	if got := c.Classify(e); got != PriP4 {
		t.Errorf("non-note event with elevating tag should keep its type default, got %s", got)
	}
}

// TestTagAnyMatch covers the new RuleMatch.TagAny field directly so
// future tag-based rule additions (yaml-driven custom config) have a
// fixture they can reuse.
func TestTagAnyMatch(t *testing.T) {
	c := NewClassifier()
	c.AddRule(Rule{
		Match:    RuleMatch{TagAny: []string{"hot", "incident"}},
		Priority: PriP1,
	})

	hit := Event{
		Type: EvtFileEdit, // wouldn't otherwise hit P1
		Data: map[string]any{"message": "irrelevant"},
		Tags: []string{"unrelated", "incident"},
	}
	if got := c.Classify(hit); got != PriP1 {
		t.Errorf("event with matching TagAny should classify P1, got %s", got)
	}

	miss := Event{
		Type: EvtFileEdit,
		Data: map[string]any{"message": "irrelevant"},
		Tags: []string{"unrelated"},
	}
	// Falls through to seeded note rules (no match, type guard) and then
	// the defaultPriorities entry for EvtFileEdit, which is P3.
	if got := c.Classify(miss); got != PriP3 {
		t.Errorf("event with no matching TagAny should fall through to default P3, got %s", got)
	}
}

// TestAnyTagMatchesEdgeCases pins the helper directly. Empty inputs
// must NOT match (mirrors the absence-of-clause vs. unsatisfied-clause
// convention in matchRule).
func TestAnyTagMatchesEdgeCases(t *testing.T) {
	if anyTagMatches(nil, []string{"a"}) {
		t.Error("nil event tags must not match anything")
	}
	if anyTagMatches([]string{"a"}, nil) {
		t.Error("nil rule tags must not match anything")
	}
	if !anyTagMatches([]string{"a", "b"}, []string{"x", "b", "y"}) {
		t.Error("intersection at b must match")
	}
	if anyTagMatches([]string{"a"}, []string{"b"}) {
		t.Error("disjoint sets must not match")
	}
}
