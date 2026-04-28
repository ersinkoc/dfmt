package sandbox

import (
	"encoding/json"
	"strings"
)

// structuredNoiseFields names keys whose values are nearly always wire bloat
// for an LLM consumer: timestamps, ETags, hypermedia links, opaque IDs. The
// list is conservative — keys here must be (a) common across cloud-CLI JSON
// outputs (gh, kubectl, aws, docker) and (b) rarely needed for the kind of
// reasoning an agent does over the response. See ADR-0010 for the
// alternatives considered (per-shape projections, schema-driven compaction)
// and why a flat blocklist won.
//
// The `id` key is intentionally absent — numeric IDs are sometimes the only
// stable handle for an object, and silently dropping them changes the
// semantics of the response. A future env knob could opt in.
var structuredNoiseFields = map[string]struct{}{
	"created_at": {},
	"updated_at": {},
	"etag":       {},
	"_links":     {},
	"node_id":    {},
	"url":        {},
	"html_url":   {},
}

// structuredNoiseSuffix is matched against any key not already in the field
// set. Cloud REST APIs sprinkle dozens of `*_url` fields per object
// (events_url, labels_url, comments_url, repository_url, ...) — enumerating
// them by hand would miss the next one Github adds. Suffix matching catches
// the family.
const structuredNoiseSuffix = "_url"

// CompactStructured detects JSON-shaped input and removes noise fields
// recursively. Returns input unchanged when:
//   - input is not valid JSON,
//   - input does not begin with `{` or `[`,
//   - the compacted form is not strictly smaller than the input (cap
//     regression guard — pathological cases must not increase wire bytes).
//
// The function is pure (no I/O, no mutex) and safe for concurrent use.
func CompactStructured(s string) string {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if trimmed == "" {
		return s
	}
	first := trimmed[0]
	if first != '{' && first != '[' {
		return s
	}
	if !json.Valid([]byte(trimmed)) {
		return s
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return s
	}
	walked := walkDropNoise(v)
	out, err := json.Marshal(walked)
	if err != nil {
		return s
	}
	if len(out) >= len(s) {
		// Compaction failed to shrink (e.g. JSON of only drop-list keys
		// produced "{}", but the input was already small). Returning the
		// original keeps the contract that NormalizeOutput is monotone.
		return s
	}
	return string(out)
}

// walkDropNoise recurses into v, dropping keys named in structuredNoiseFields
// or matching structuredNoiseSuffix. Scalars and nil pass through unchanged.
// Arrays preserve order. Map iteration order is non-deterministic but we
// re-marshal via json.Marshal which sorts keys alphabetically — so the
// output is stable regardless of input map ordering.
func walkDropNoise(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if _, drop := structuredNoiseFields[k]; drop {
				continue
			}
			if strings.HasSuffix(k, structuredNoiseSuffix) {
				continue
			}
			out[k] = walkDropNoise(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = walkDropNoise(val)
		}
		return out
	default:
		return v
	}
}
