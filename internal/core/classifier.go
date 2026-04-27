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
//
// All present clauses must match (AND semantics). TagAny is the one
// disjunctive clause: the event matches if ANY of the listed tags is
// present in Event.Tags. The previous schema (Type + PathGlob +
// MessageRegex only) had no way to elevate priority based on tags, so
// callers using dfmt_remember with tags like "audit" or "decision"
// landed at the default P4 priority — same as routine tool calls —
// and got dropped first under a tight byte-budget recall.
type RuleMatch struct {
	Type         EventType `yaml:"type"`
	PathGlob     string    `yaml:"path_glob,omitempty"`
	MessageRegex string    `yaml:"message_regex,omitempty"`
	TagAny       []string  `yaml:"tag_any,omitempty"`
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

// noteElevateP2Tags lists tag values that elevate a note-typed event to
// P2 (just under decisions/task-done). These tags signal session-spanning
// context the recall snapshot must keep even under a tight byte budget:
// audit summaries, design decisions, design-strength records that future
// agents must NOT regress, and ledger entries pointing at commits.
var noteElevateP2Tags = []string{
	"summary",
	"decision",
	"strengths",
	"ledger",
}

// noteElevateP3Tags lists tag values that elevate a note-typed event to
// P3 (above routine tool calls). Individual audit findings and follow-up
// tasks live here — they are valuable but more numerous, so the byte
// budget can drop them before the P2-tagged top-level context.
var noteElevateP3Tags = []string{
	"audit",
	"finding",
	"followup",
	"preserve",
}

// NewClassifier creates a new Classifier with default priorities and
// seeded note-elevation rules. The seeded rules let dfmt_remember callers
// raise a note above the default P4 by attaching the right tag — without
// the seed, every manually-recorded note ranked equal to a tool.read
// event in the recall budget pass.
func NewClassifier() *Classifier {
	defaults := make(map[EventType]Priority)
	for k, v := range defaultPriorities {
		defaults[k] = v
	}
	c := &Classifier{
		defaults: defaults,
		rules:    []compiledRule{},
	}
	// Seed in priority order so the first-matching-rule wins logic in
	// Classify picks P2 over P3 when a note carries both kinds of tags.
	c.AddRule(Rule{
		Match:    RuleMatch{Type: EvtNote, TagAny: append([]string(nil), noteElevateP2Tags...)},
		Priority: PriP2,
	})
	c.AddRule(Rule{
		Match:    RuleMatch{Type: EvtNote, TagAny: append([]string(nil), noteElevateP3Tags...)},
		Priority: PriP3,
	})
	return c
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

	// Check tag membership. TagAny matches if ANY tag in the rule appears
	// in Event.Tags. An empty TagAny is a no-op (clause is absent), not a
	// "match nothing" — same convention as the empty Type / PathGlob /
	// MessageRegex clauses above.
	if len(m.TagAny) > 0 {
		if !anyTagMatches(e.Tags, m.TagAny) {
			return false
		}
	}

	return true
}

// anyTagMatches returns true if eventTags and ruleTags share at least one
// element. Both slices are typically short (<= 8 entries), so a nested
// linear scan is cheaper than building a map. Empty inputs return false —
// callers must guard `len(ruleTags) > 0` before calling if absence of the
// clause should mean "match".
func anyTagMatches(eventTags, ruleTags []string) bool {
	if len(eventTags) == 0 || len(ruleTags) == 0 {
		return false
	}
	for _, rt := range ruleTags {
		for _, et := range eventTags {
			if et == rt {
				return true
			}
		}
	}
	return false
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
