package core

import (
	"errors"
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

// ErrIndexVersionMismatch is returned by UnmarshalJSON when the persisted
// index format version does not match the current indexJSONVersion. Callers
// (LoadIndexWithCursor) should treat this as needsRebuild=true (CORE-7).
var ErrIndexVersionMismatch = errors.New("index format version mismatch")

// Priority tiers.
//
// Deprecated aliases for the canonical set in event.go. New code should
// use PriP1..PriP4 directly. These exist only so historical imports
// compile without churn (CORE-8).
const (
	PriorityP1 = PriP1
	PriorityP2 = PriP2
	PriorityP3 = PriP3
	PriorityP4 = PriP4
)

// Source types.
//
// Deprecated aliases for the canonical set in event.go. New code should
// use SrcCLI, SrcMCP, SrcShell, SrcFSWatch, SrcGitHook directly.
// Note: the values here intentionally differ from the canonical names —
// SourceHook="hook" vs SrcGitHook="githook", SourceFS="fs" vs
// SrcFSWatch="fswatch". The canonical event.go values are what the
// journal writes; these aliases are kept for backward compatibility
// but should not be used for new event creation.
const (
	SourceCLI          = SrcCLI
	SourceMCP          = SrcMCP
	SourceShell        = SrcShell
	SourceHook  Source = "hook"
	SourceFS    Source = "fs"
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
