package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode"
)

// EventType represents the type of an event.
type EventType string

const (
	EvtFileRead    EventType = "file.read"
	EvtFileEdit    EventType = "file.edit"
	EvtFileCreate  EventType = "file.create"
	EvtFileDelete  EventType = "file.delete"
	EvtTaskCreate  EventType = "task.create"
	EvtTaskUpdate  EventType = "task.update"
	EvtTaskDone    EventType = "task.done"
	EvtDecision    EventType = "decision"
	EvtError       EventType = "error"
	EvtGitCommit   EventType = "git.commit"
	EvtGitCheckout EventType = "git.checkout"
	EvtGitPush     EventType = "git.push"
	EvtGitStash    EventType = "git.stash"
	EvtGitDiff     EventType = "git.diff"
	EvtEnvCwd      EventType = "env.cwd"
	EvtEnvVars     EventType = "env.vars"
	EvtEnvInstall  EventType = "env.install"
	EvtPrompt      EventType = "prompt"
	EvtMCPCall     EventType = "mcp.call"
	EvtSubagent    EventType = "subagent"
	EvtSkill       EventType = "skill"
	EvtRole        EventType = "role"
	EvtIntent      EventType = "intent"
	EvtDataRef     EventType = "data.ref"
	EvtNote        EventType = "note"
	EvtTombstone   EventType = "tombstone"
)

// Priority represents the priority tier of an event.
type Priority string

const (
	PriP1 Priority = "p1"
	PriP2 Priority = "p2"
	PriP3 Priority = "p3"
	PriP4 Priority = "p4"
)

// Source represents the source of an event.
type Source string

const (
	SrcMCP     Source = "mcp"
	SrcFSWatch Source = "fswatch"
	SrcGitHook Source = "githook"
	SrcShell   Source = "shell"
	SrcCLI     Source = "cli"
)

// Token data keys for LLM event tracking.
const (
	KeyInputTokens  = "input_tokens"  // int - LLM input token count
	KeyOutputTokens = "output_tokens" // int - LLM output token count
	KeyCachedTokens = "cached_tokens" // int - prompt cache savings
	KeyModel        = "model"         // string - model name
	KeyCacheHit     = "cache_hit"     // bool - cache hit occurred
)

// Event represents a single event in the journal.
type Event struct {
	ID       string         `json:"id"`
	TS       time.Time      `json:"ts"`
	Project  string         `json:"project"`
	Type     EventType      `json:"type"`
	Priority Priority       `json:"priority"`
	Source   Source         `json:"source"`
	Actor    string         `json:"actor,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Refs     []string       `json:"refs,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	Sig      string         `json:"sig"`
}

// ComputeSig computes the signature of the event.
// It uses the first 16 hex chars of SHA-256 of the canonical JSON.
func (e *Event) ComputeSig() string {
	// Create a copy without the Sig field for hashing
	e2 := *e
	e2.Sig = ""

	// Use custom canonical marshaler
	data, _ := CanonicalJSON(e2)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8]) // first 16 hex chars (8 bytes)
}

// CanonicalJSON returns canonical JSON bytes for an event.
func CanonicalJSON(e Event) ([]byte, error) {
	// Use a map to ensure sorted keys
	m := map[string]any{
		"id":       e.ID,
		"ts":       e.TS.Format(time.RFC3339Nano),
		"project":  e.Project,
		"type":     e.Type,
		"priority": e.Priority,
		"source":   e.Source,
	}

	if e.Actor != "" {
		m["actor"] = e.Actor
	}
	if e.Data != nil {
		// Sort Data keys
		keys := make([]string, 0, len(e.Data))
		for k := range e.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sortedData := make(map[string]any, len(e.Data))
		for _, k := range keys {
			sortedData[k] = e.Data[k]
		}
		m["data"] = sortedData
	}
	if len(e.Refs) > 0 {
		m["refs"] = e.Refs
	}
	if len(e.Tags) > 0 {
		m["tags"] = e.Tags
	}

	return json.Marshal(m)
}

// Validate checks if the event's signature is valid.
func (e *Event) Validate() bool {
	return e.Sig == e.ComputeSig()
}

// Tokenize splits text into tokens for indexing.
func Tokenize(s string) []string {
	var tokens []string
	var current strings.Builder

	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 2 && current.Len() <= 64 {
				tokens = append(tokens, current.String())
			}
			current.Reset()
		}
	}

	if current.Len() >= 2 && current.Len() <= 64 {
		tokens = append(tokens, current.String())
	}

	return tokens
}
