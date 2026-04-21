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
