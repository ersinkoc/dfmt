package core

import (
	"path/filepath"
	"regexp"
)

// Default priority table for event types.
var defaultPriorities = map[EventType]Priority{
	EvtDecision:    PriP1,
	EvtTaskDone:    PriP1,
	EvtGitCommit:   PriP2,
	EvtGitPush:     PriP2,
	EvtGitCheckout: PriP2,
	EvtFileEdit:    PriP3,
	EvtFileCreate:  PriP3,
	EvtTaskCreate: PriP3,
	EvtError:       PriP2,
	EvtMCPCall:     PriP4,
	EvtNote:        PriP4,
	EvtPrompt:      PriP4,
}

// RuleMatch defines matching criteria for a classification rule.
type RuleMatch struct {
	Type         EventType  `yaml:"type"`
	PathGlob     string     `yaml:"path_glob,omitempty"`
	MessageRegex string     `yaml:"message_regex,omitempty"`
}

// Rule defines a classification rule.
type Rule struct {
	Match    RuleMatch `yaml:"match"`
	Priority Priority  `yaml:"priority"`
}

// Classifier assigns priority tiers to events.
type Classifier struct {
	defaults map[EventType]Priority
	rules    []Rule
}

// NewClassifier creates a new Classifier with default priorities.
func NewClassifier() *Classifier {
	defaults := make(map[EventType]Priority)
	for k, v := range defaultPriorities {
		defaults[k] = v
	}
	return &Classifier{
		defaults: defaults,
		rules:    []Rule{},
	}
}

// Classify returns the priority for an event.
func (c *Classifier) Classify(e Event) Priority {
	// Check rules first (first matching rule wins)
	for _, rule := range c.rules {
		if c.matchRule(e, rule.Match) {
			return rule.Priority
		}
	}

	// Fall through to defaults
	if pri, ok := c.defaults[e.Type]; ok {
		return pri
	}

	// Default to P4 if unknown type
	return PriP4
}

// matchRule checks if an event matches a rule.
func (c *Classifier) matchRule(e Event, m RuleMatch) bool {
	// Check type
	if m.Type != "" && m.Type != e.Type {
		return false
	}

	// Check path glob
	if m.PathGlob != "" {
		if e.Data == nil {
			return false
		}
		path, ok := e.Data["path"].(string)
		if !ok {
			return false
		}
		matched, err := filepath.Match(m.PathGlob, filepath.Base(path))
		if err != nil || !matched {
			return false
		}
	}

	// Check message regex
	if m.MessageRegex != "" {
		if e.Data == nil {
			return false
		}
		msg, ok := e.Data["message"].(string)
		if !ok {
			return false
		}
		re, err := regexp.Compile(m.MessageRegex)
		if err != nil {
			return false
		}
		if !re.MatchString(msg) {
			return false
		}
	}

	return true
}

// AddRule adds a classification rule.
func (c *Classifier) AddRule(rule Rule) {
	c.rules = append(c.rules, rule)
}

// SetDefault sets the default priority for an event type.
func (c *Classifier) SetDefault(typ EventType, pri Priority) {
	c.defaults[typ] = pri
}
