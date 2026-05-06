// Package safejson wraps encoding/json with a depth check that runs before
// the actual decode.
//
// The threat model is V-10 from the security audit: encoding/json recurses
// without bound on nested arrays/objects, and a 1 MiB body of `[[[…` blows
// the stack on any decoder in the daemon (HTTP, socket, MCP stdio, journal
// replay, index load). The standard library has no built-in depth limit;
// this package is a stdlib-only replacement for the call sites where
// attacker-controlled bytes meet the JSON parser.
//
// CheckDepth is a single-pass, string-aware byte scan. It does not parse
// the document — it only counts `{`/`[` openers minus closers, while
// tracking quote state so that JSON-encoded strings containing brace
// characters don't trip the counter. This is intentionally simpler (and
// faster) than driving json.Decoder.Token, and the depth bound is the only
// thing we care about.
package safejson

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MaxJSONDepth is the maximum nesting depth permitted on any decode that
// uses this package. Picked to comfortably cover legitimate JSON-RPC and
// event payloads (top-level Response → Result → nested Data with a few
// levels of structure → arrays of those) while still bounding the recursion
// the standard library performs at decode time.
const MaxJSONDepth = 64

// ErrMaxDepthExceeded is returned by CheckDepth (and Unmarshal) when a
// document's nesting depth exceeds MaxJSONDepth.
var ErrMaxDepthExceeded = errors.New("safejson: max nesting depth exceeded")

// CheckDepth scans data and reports an error if the JSON nesting depth
// exceeds MaxJSONDepth. The scan is string-aware: brace/bracket characters
// inside a quoted string are not counted as openers/closers. Backslash
// escapes inside strings are honored so `"\""` does not prematurely close
// the string.
//
// CheckDepth does not validate that data is well-formed JSON. Mismatched
// braces, unterminated strings, and other malformed input are accepted as
// long as the depth bound is respected — the subsequent json.Unmarshal
// call rejects malformed bytes with its own parse error.
func CheckDepth(data []byte) error {
	var (
		depth    int
		inString bool
		escape   bool
	)
	for _, b := range data {
		if escape {
			escape = false
			continue
		}
		if inString {
			switch b {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > MaxJSONDepth {
				return fmt.Errorf("%w (limit=%d)", ErrMaxDepthExceeded, MaxJSONDepth)
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

// Unmarshal is a drop-in replacement for json.Unmarshal that runs
// CheckDepth on the input first. Use this at every decode site where the
// bytes can be agent-controlled (HTTP body, socket line, MCP stdio frame,
// journal line, persisted index). Sub-decodes inside an outer call are
// bounded by the top-level check, so wrapping the entry point is enough.
func Unmarshal(data []byte, v any) error {
	if err := CheckDepth(data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
