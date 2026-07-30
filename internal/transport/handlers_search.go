package transport

import (
	"context"
	"fmt"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

type RememberParams struct {
	// ProjectID, when set, routes the call to the per-project resources
	// (journal/index) for that project root instead of the per-handler
	// default. Optional for backward compat with pre-Phase-2 callers.
	ProjectID string         `json:"project_id,omitempty"`
	Type      string         `json:"type"`
	Priority  string         `json:"priority"`
	Source    string         `json:"source"`
	Actor     string         `json:"actor,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Refs      []string       `json:"refs,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	// Direct token fields for MCP tools
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	CachedTokens int    `json:"cached_tokens,omitempty"`
	Model        string `json:"model,omitempty"`
	Message      string `json:"message,omitempty"`
}

// RememberResponse is the response for the Remember method.
type RememberResponse struct {
	ID string `json:"id"`
	TS string `json:"ts"`
}

// Remember adds an event to the journal and index.
func (h *Handlers) Remember(ctx context.Context, params RememberParams) (_ *RememberResponse, err error) {
	// Every other tool records its call; Remember was the one that did not,
	// so the duration histograms and error counters (ADR-0018) had a hole
	// exactly where session memory is written.
	defer recordToolCall("remember", ctx, &err, time.Now())
	h.touch()
	bundle, err := h.resolveBundle(ctx)
	if err != nil {
		return nil, err
	}
	if bundle.Journal == nil {
		return nil, errNoProject
	}
	// Handle direct token fields and the Message field — both are
	// advertised as MCP `dfmt_remember` parameters but the previous
	// version of this handler silently dropped Message. Result: the
	// recall snapshot showed a tag-only line ("[p3] #audit #preserve")
	// and dfmt_search could not find any word from the message body —
	// only tags. Closes the audit-discovered defect "Remember.Message
	// silently dropped from indexed event."
	data := params.Data
	hasTokenFields := params.InputTokens > 0 || params.OutputTokens > 0 || params.CachedTokens > 0 || params.Model != ""
	if hasTokenFields || params.Message != "" {
		if data == nil {
			data = make(map[string]any)
		}
		if params.InputTokens > 0 {
			data[core.KeyInputTokens] = params.InputTokens
		}
		if params.OutputTokens > 0 {
			data[core.KeyOutputTokens] = params.OutputTokens
		}
		if params.CachedTokens > 0 {
			data[core.KeyCachedTokens] = params.CachedTokens
		}
		if params.Model != "" {
			data[core.KeyModel] = params.Model
		}
		if params.Message != "" {
			// Use the literal "message" key — that is the convention
			// the recall renderer (`internal/retrieve`) and the test
			// fixtures already follow.
			data["message"] = params.Message
		}
	}

	// Redact sensitive strings before the event is persisted or indexed.
	redactedTags := params.Tags
	if h.redactorFor(ctx) != nil && len(redactedTags) > 0 {
		redactedTags = make([]string, len(params.Tags))
		for i, t := range params.Tags {
			redactedTags[i] = h.redactString(ctx, t)
		}
	}

	// F-21: server-side override of Source and Priority. Both are
	// agent-controllable on the wire; without this the agent (or a prompt-
	// injected loop running through it) could write events claiming source
	// "githook" or "fswatch" and priority "p1" — exactly the bands `dfmt
	// recall` keeps under tight budget. The agent IS calling via MCP, so
	// Source is a fact, not a parameter. p1 is reserved for non-agent
	// paths (decisions logged by the operator, incident events). Anything
	// outside the agent-allowed band is coerced to p3.
	priority := core.Priority(params.Priority)
	switch priority {
	case core.PriP2, core.PriP3, core.PriP4:
		// keep
	default:
		priority = core.PriP3
	}

	// Create event
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  h.getProjectFor(ctx),
		Type:     core.EventType(params.Type),
		Priority: priority,
		Source:   core.SrcMCP,
		Actor:    params.Actor,
		Data:     h.redactData(ctx, data),
		Refs:     params.Refs,
		Tags:     redactedTags,
	}
	e.Sig = e.ComputeSig()

	// Append to journal
	if err := bundle.Journal.Append(ctx, e); err != nil {
		return nil, fmt.Errorf("journal append: %w", err)
	}

	// Add to index (index is paired with journal — both nil in degraded mode,
	// but the journal nil-guard above already short-circuits that path; this
	// guard only matters for the daemon test seam where index is omitted).
	if bundle.Index != nil {
		bundle.Index.Add(e)
	}

	return &RememberResponse{
		ID: e.ID,
		TS: e.TS.Format(time.RFC3339Nano),
	}, nil
}

// SearchParams are the parameters for the Search method.
type SearchParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Query     string `json:"query"`
	Limit     int    `json:"limit,omitempty"`
	Layer     string `json:"layer,omitempty"` // "bm25", "trigram", "fuzzy"
	// Type restricts results to one event type ("note" for things recorded
	// with dfmt_remember, "tool.exec" for command history, …).
	//
	// It exists because a journal is overwhelmingly automatic: 193 of 202
	// events on this repo were tool.* calls, each a short document that
	// BM25's length normalization favours over a long, information-dense
	// note. Measured before this filter existed: a query for "timeout"
	// against a journal whose single note discussed timeouts at length
	// returned six tool.grep/tool.exec hits and not the note. The scoring
	// is not wrong — the corpus is lopsided — so the fix is to let the
	// caller say which corpus it means rather than to invent a weight.
	Type string `json:"type,omitempty"`
}

// SearchResponse is the response for the Search method.
type SearchResponse struct {
	Results []SearchHit `json:"results"`
	Layer   string      `json:"layer"`
}

// SearchHit represents a single search result. Excerpt carries a short
// content snippet (≤80 bytes, rune-aligned) so agents can decide
// whether to drill into the hit without a follow-up dfmt_recall round
// trip — net wire saving even after the per-hit byte cost. Empty when
// the indexed event predates the excerpt feature.
type SearchHit struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Layer   int     `json:"layer"`
	Type    string  `json:"type,omitempty"`
	Source  string  `json:"source,omitempty"`
	Excerpt string  `json:"excerpt,omitempty"`
}

// Search queries the index.
func (h *Handlers) Search(ctx context.Context, params SearchParams) (_ *SearchResponse, err error) {
	defer recordToolCall("search", ctx, &err, time.Now())
	h.touch()
	if params.Limit == 0 {
		params.Limit = 10
	}

	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Index == nil {
		return &SearchResponse{Results: nil, Layer: params.Layer}, nil
	}

	var hits []core.ScoredHit
	resolvedLayer := params.Layer

	// With a type filter, ask the index for more candidates than the caller
	// wants and drop the ones that do not match. Filtering after scoring
	// keeps BM25 intact (relative ranking within the type is unchanged) at
	// the cost of over-fetching. The multiplier is bounded so a filter that
	// matches nothing cannot turn into a full-index scan.
	fetch := params.Limit
	if params.Type != "" {
		fetch = min(params.Limit*searchFilterOverFetch, searchFilterMaxCandidates)
	}

	switch params.Layer {
	case "trigram":
		hits = bundle.Index.SearchTrigram(params.Query, fetch)
	case "fuzzy":
		// Fuzzy search would go here
		hits = nil
	default:
		// Default: BM25 first, then trigram fallback when BM25 returns
		// nothing. BM25 misses synthetic markers (AUDIT_PROBE_XJ7Q3),
		// UUIDs, and other tokens that the Porter stemmer drops or
		// splits awkwardly; trigram match restores them. Reporting the
		// resolved layer back lets clients distinguish a true miss
		// from a fallback hit.
		hits = bundle.Index.SearchBM25(params.Query, fetch)
		if len(hits) > 0 {
			resolvedLayer = "bm25"
		} else {
			hits = bundle.Index.SearchTrigram(params.Query, fetch)
			if len(hits) > 0 {
				resolvedLayer = "trigram"
			}
		}
	}

	results := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		meta := bundle.Index.Meta(hit.ID)
		if params.Type != "" && meta.Type != params.Type {
			continue
		}
		results = append(results, SearchHit{
			ID:    hit.ID,
			Score: hit.Score,
			Layer: hit.Layer,
			// Type and Source were declared on this struct from the start
			// and never assigned, so every hit arrived unlabelled and a
			// deliberately recorded note looked exactly like the tool call
			// next to it. They come from the index's per-document metadata.
			Type:    meta.Type,
			Source:  meta.Source,
			Excerpt: bundle.Index.Excerpt(hit.ID),
		})
		if len(results) >= params.Limit {
			break
		}
	}

	return &SearchResponse{
		Results: results,
		Layer:   resolvedLayer,
	}, nil
}

// searchFilterOverFetch is how many candidates per requested result the
// index is asked for when a type filter is active, and
// searchFilterMaxCandidates is the ceiling on that expansion. Together they
// bound the cost of a filter that matches little or nothing: a query for
// type "note" against a journal of 100k tool events scans at most
// searchFilterMaxCandidates scored hits rather than every document.
const (
	searchFilterOverFetch     = 20
	searchFilterMaxCandidates = 500
)

// RecallParams are the parameters for the Recall method.
