package transport

import (
	"encoding/json"

	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// leanWireMaxTokens is the size ceiling for the lean wire shape (ADR-0033):
// the same 1k-token threshold at which ApplyReturnPolicy stops inlining a
// body whole. Below it the response is the payload and nothing else.
const leanWireMaxTokens = 1024

// leanEligible reports whether a response may drop its bookkeeping fields.
// All of these must hold:
//
//   - the body reached the agent whole (not an excerpt/summary/matches reply),
//   - no summary, matches or vocabulary rode along (those ARE the answer),
//   - the payload is small, so the fixed envelope is a visible fraction of it.
//
// A response that is not eligible keeps today's full shape. Dedup
// ("(unchanged; same content_id)") replies never pass through here: they
// exist to carry the content_id.
func leanEligible(excerpted bool, summary string, nMatches, nVocab int, parts ...string) bool {
	if excerpted || summary != "" || nMatches > 0 || nVocab > 0 {
		return false
	}
	tokens := 0
	for _, p := range parts {
		tokens += sandbox.ApproxTokens(p)
		if tokens > leanWireMaxTokens {
			return false
		}
	}
	return true
}

// leanRead strips a ReadResponse down to its content. Size/read_bytes/
// content_id go always; the line window goes only when it is the whole file,
// because for a partial window it is what lets the agent cite file:line and
// page forward. An empty body keeps its metadata: `{}` would be
// indistinguishable from a failed call.
func leanRead(r *ReadResponse, params ReadParams, excerpted bool) *ReadResponse {
	if r.Content == "" || !leanEligible(excerpted, r.Summary, len(r.Matches), 0, r.Content) {
		return r
	}
	r.Size, r.ReadBytes, r.ContentID = 0, 0, ""
	whole := params.Offset <= 1 && params.Limit == 0 && r.TotalLines > 0 && r.EndLine == r.TotalLines
	if whole {
		r.StartLine, r.EndLine, r.TotalLines = 0, 0, 0
	}
	return r
}

// leanExec drops content_id and duration_ms. `exit` is never dropped: it is
// the only thing separating a command that succeeded silently from a call
// that did nothing. timed_out survives when true (omitempty hides false).
func leanExec(r *ExecResponse, excerpted bool) *ExecResponse {
	if !leanEligible(excerpted, r.Summary, len(r.Matches), len(r.Vocabulary), r.Stdout, r.Stderr) {
		return r
	}
	r.ContentID = ""
	r.DurationMs = 0
	return r
}

// leanFetch drops content_id. Status and headers stay: they are HTTP
// semantics, not bookkeeping.
func leanFetch(r *FetchResponse, excerpted bool) *FetchResponse {
	if !leanEligible(excerpted, r.Summary, len(r.Matches), len(r.Vocabulary), r.Body) {
		return r
	}
	r.ContentID = ""
	return r
}

// MarshalJSON keeps `size`/`read_bytes` on a response that carries no
// content at all (an empty file, a dedup acknowledgement is unaffected since
// it has a summary). Both fields are omitempty so a lean read can drop them,
// but an empty file would then serialize to `{}`, indistinguishable from a
// call that did nothing.
func (r ReadResponse) MarshalJSON() ([]byte, error) {
	type plain ReadResponse
	if r.Content != "" || r.Summary != "" || len(r.Matches) > 0 {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Size      int64 `json:"size"`
		ReadBytes int64 `json:"read_bytes"`
	}{plain(r), r.Size, r.ReadBytes})
}
