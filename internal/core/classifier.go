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
	EvtTaskCreate:  PriP3,
	EvtError:       PriP2,
	EvtMCPCall:     PriP4,
	EvtNote:        PriP4,
	EvtPrompt:      PriP4,
}

// RuleMatch defines matching criteria for a classification rule.
type RuleMatch struct {
	Type         EventType `yaml:"type"`
	PathGlob     string    `yaml:"path_glob,omitempty"`
	MessageRegex string    `yaml:"message_regex,omitempty"`
}

// Rule defines a classification rule.
type Rule struct {
	Match    RuleMatch `yaml:"match"`
	Priority Priority  `yaml:"priority"`
}

// compiledRule wraps a Rule with its precompiled MessageRegex so Classify
// doesn't recompile on every event — prior implementation ran regexp.Compile
// inside matchRule, making Stats/Recall O(events × rules) compiles.
// reInvalid tracks MessageRegex that was provided but failed to compile, so
// matchRule can fail such rules (preserving the pre-cache behavior where an
// invalid regex meant the rule never matched).
type compiledRule struct {
	rule      Rule
	re        *regexp.Regexp
	reInvalid bool
}

// Classifier assigns priority tiers to events.
type Classifier struct {
	defaults map[EventType]Priority
	rules    []compiledRule
}

// NewClassifier creates a new Classifier with default priorities.
func NewClassifier() *Classifier {
	defaults := make(map[EventType]Priority)
	for k, v := range defaultPriorities {
		defaults[k] = v
	}
	return &Classifier{
		defaults: defaults,
		rules:    []compiledRule{},
	}
}

// Classify returns the priority for an event.
func (c *Classifier) Classify(e Event) Priority {
	// Check rules first (first matching rule wins)
	for _, cr := range c.rules {
		if c.matchRule(e, cr) {
			return cr.rule.Priority
		}
	}

	// Fall through to defaults
	if pri, ok := c.defaults[e.Type]; ok {
		return pri
	}

	// Default to P4 if unknown type
	return PriP4
}

// matchRule checks if an event matches a compiled rule.
func (c *Classifier) matchRule(e Event, cr compiledRule) bool {
	m := cr.rule.Match
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

	// Check message regex against the precompiled form. An invalid regex
	// (reInvalid) never matches — mirrors the pre-cache err != nil branch.
	if cr.reInvalid {
		return false
	}
	if cr.re != nil {
		if e.Data == nil {
			return false
		}
		msg, ok := e.Data["message"].(string)
		if !ok {
			return false
		}
		if !cr.re.MatchString(msg) {
			return false
		}
	}

	return true
}

// AddRule adds a classification rule. The MessageRegex is compiled once
// here; an invalid regex is logged as a silent skip (matcher treats the
// rule's regex clause as always-unmet) rather than failing AddRule, so a
// malformed user rule doesn't prevent the classifier from being built.
func (c *Classifier) AddRule(rule Rule) {
	cr := compiledRule{rule: rule}
	if rule.Match.MessageRegex != "" {
		if re, err := regexp.Compile(rule.Match.MessageRegex); err == nil {
			cr.re = re
		} else {
			cr.reInvalid = true
		}
	}
	c.rules = append(c.rules, cr)
}

// SetDefault sets the default priority for an event type.
func (c *Classifier) SetDefault(typ EventType, pri Priority) {
	c.defaults[typ] = pri
}
