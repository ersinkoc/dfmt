package core

import (
	"time"

	"github.com/ersinkoc/dfmt/internal/version"
)

// Version is the DFMT version. Mirrors internal/version.Current — the
// single build-time-injected source of truth. Kept as a re-export to
// preserve the historical core.Version reference.
var Version = version.Current

const (
	// ULIDLen is the length of a ULID string.
	ULIDLen = 26

	// MaxEventSize is the maximum size of an event in bytes.
	MaxEventSize = 1024 * 1024 // 1 MB

	// DefaultBudget is the default recall budget in bytes.
	DefaultBudget = 4096

	// MaxBudget is the maximum recall budget.
	MaxBudget = 1024 * 1024 // 1 MB
)

// DefaultDurability is the default journal durability mode.
const DefaultDurability = "batched"

// Priority tiers.
const (
	PriorityP1 Priority = "p1" // Critical: decisions, task outcomes
	PriorityP2 Priority = "p2" // Important: file edits, git operations
	PriorityP3 Priority = "p3" // Normal: shell commands, searches
	PriorityP4 Priority = "p4" // Low: reads, minor events
)

// Source types.
const (
	SourceCLI   Source = "cli"
	SourceMCP   Source = "mcp"
	SourceHook  Source = "hook"
	SourceFS    Source = "fs"
	SourceShell Source = "shell"
	SourceGit   Source = "git"
)

// Default index constants.
const (
	DefaultBM25K1       = 1.2
	DefaultBM25B        = 0.75
	DefaultHeadingBoost = 5.0
)

// Default stopwords for English.
var EnglishStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {},
	"be": {}, "been": {}, "being": {}, "but": {}, "by": {},
	"can": {}, "could": {}, "did": {}, "do": {}, "does": {},
	"doing": {}, "done": {}, "for": {}, "from": {},
	"had": {}, "has": {}, "have": {}, "having": {},
	"he": {}, "her": {}, "here": {}, "him": {}, "his": {},
	"how": {}, "i": {}, "if": {}, "in": {}, "into": {},
	"is": {}, "it": {}, "its": {}, "just": {},
	"me": {}, "my": {},
	"no": {}, "not": {}, "of": {}, "on": {}, "or": {},
	"our": {}, "out": {},
	"said": {}, "she": {}, "so": {}, "some": {},
	"that": {}, "the": {}, "their": {}, "them": {}, "then": {},
	"there": {}, "these": {}, "they": {}, "this": {}, "those": {},
	"to": {}, "too": {},
	"us": {}, "was": {}, "we": {}, "were": {}, "what": {},
	"when": {}, "where": {}, "which": {}, "while": {},
	"who": {}, "will": {}, "with": {}, "would": {},
	"you": {}, "your": {},
}

// Turkish stopwords.
var TurkishStopwords = map[string]struct{}{
	"bir": {}, "bu": {}, "da": {}, "de": {}, "daha": {},
	"ile": {}, "için": {}, "kadar": {}, "ne": {}, "oysa": {},
	"ve": {}, "ya": {}, "yani": {}, "zaten": {},
}

// Now returns the current time.
var Now = time.Now
