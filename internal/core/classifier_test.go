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
