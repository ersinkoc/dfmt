package transport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// Handlers implements the business logic for all transport layers.
type Handlers struct {
	index  *core.Index
	journal core.Journal
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(index *core.Index, journal core.Journal) *Handlers {
	return &Handlers{
		index:   index,
		journal: journal,
	}
}

// RememberParams are the parameters for the Remember method.
type RememberParams struct {
	Type     string            `json:"type"`
	Priority string            `json:"priority"`
	Source   string            `json:"source"`
	Actor    string            `json:"actor,omitempty"`
	Data     map[string]any    `json:"data,omitempty"`
	Refs     []string          `json:"refs,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
}

// RememberResponse is the response for the Remember method.
type RememberResponse struct {
	ID  string `json:"id"`
	TS  string `json:"ts"`
}

// Remember adds an event to the journal and index.
func (h *Handlers) Remember(ctx context.Context, params RememberParams) (*RememberResponse, error) {
	// Create event
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EventType(params.Type),
		Priority: core.Priority(params.Priority),
		Source:   core.Source(params.Source),
		Actor:    params.Actor,
		Data:     params.Data,
		Refs:     params.Refs,
		Tags:     params.Tags,
	}
	e.Sig = e.ComputeSig()

	// Append to journal
	if err := h.journal.Append(ctx, e); err != nil {
		return nil, fmt.Errorf("journal append: %w", err)
	}

	// Add to index
	h.index.Add(e)

	return &RememberResponse{
		ID: e.ID,
		TS: e.TS.Format(time.RFC3339Nano),
	}, nil
}

// SearchParams are the parameters for the Search method.
type SearchParams struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Layer  string `json:"layer,omitempty"` // "bm25", "trigram", "fuzzy"
}

// SearchResponse is the response for the Search method.
type SearchResponse struct {
	Results []SearchHit `json:"results"`
	Layer   string      `json:"layer"`
}

// SearchHit represents a single search result.
type SearchHit struct {
	ID     string  `json:"id"`
	Score  float64 `json:"score"`
	Layer  int     `json:"layer"`
	Type   string  `json:"type,omitempty"`
	Source string  `json:"source,omitempty"`
}

// Search queries the index.
func (h *Handlers) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	if params.Limit == 0 {
		params.Limit = 10
	}

	var hits []core.ScoredHit

	switch params.Layer {
	case "trigram":
		// Trigram search would go here
		hits = nil
	case "fuzzy":
		// Fuzzy search would go here
		hits = nil
	default:
		// Default to BM25
		hits = h.index.SearchBM25(params.Query, params.Limit)
	}

	results := make([]SearchHit, len(hits))
	for i, hit := range hits {
		results[i] = SearchHit{
			ID:    hit.ID,
			Score: hit.Score,
			Layer: hit.Layer,
		}
	}

	return &SearchResponse{
		Results: results,
		Layer:   params.Layer,
	}, nil
}

// RecallParams are the parameters for the Recall method.
type RecallParams struct {
	Budget int    `json:"budget,omitempty"`
	Format string `json:"format,omitempty"`
}

// RecallResponse is the response for the Recall method.
type RecallResponse struct {
	Snapshot string `json:"snapshot"`
	Format   string `json:"format"`
}

// Recall builds a session snapshot with tier-ordered greedy fill.
func (h *Handlers) Recall(ctx context.Context, params RecallParams) (*RecallResponse, error) {
	budget := params.Budget
	if budget == 0 {
		budget = 4096
	}
	format := params.Format
	if format == "" {
		format = "md"
	}

	// Get all events from journal
	var events []core.Event
	stream, err := h.journal.Stream(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("stream journal: %w", err)
	}
	for e := range stream {
		events = append(events, e)
	}

	// Classify and sort by priority (P1 first), then by timestamp descending
	classifier := core.NewClassifier()
	sorted := make([]core.Event, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		pi := classifier.Classify(sorted[i])
		pj := classifier.Classify(sorted[j])
		if pi != pj {
			return pi < pj // P1 < P2 < P3 < P4
		}
		return sorted[i].TS.After(sorted[j].TS)
	})

	// Greedy fill with budget
	var used int
	var lines []string
	lines = append(lines, "# Session Snapshot\n")

	for _, e := range sorted {
		// Estimate size: event as markdown line
		prefix := fmt.Sprintf("- [%s] %s", e.Priority, e.Type)
		var dataStr string
		if e.Data != nil {
			dataStr = fmt.Sprintf(" %v", e.Data)
		}
		lineSize := len(prefix) + len(dataStr) + len(e.Tags)*10 + 50 // rough estimate

		if used+lineSize > budget {
			continue
		}

		ts := e.TS.Format("15:04:05")
		actor := ""
		if e.Actor != "" {
			actor = fmt.Sprintf(" @%s", e.Actor)
		}
		tags := ""
		if len(e.Tags) > 0 {
			tags = fmt.Sprintf(" #%s", strings.Join(e.Tags, " #"))
		}
		line := fmt.Sprintf("- [%s] %s%s%s%s", e.Priority, ts, actor, tags, dataStr)
		lines = append(lines, line)
		used += lineSize
	}

	if len(lines) == 1 {
		lines = append(lines, "_No events in session_")
	}

	return &RecallResponse{
		Snapshot: strings.Join(lines, "\n"),
		Format:   format,
	}, nil
}

// StreamParams are the parameters for the Stream method.
type StreamParams struct {
	From string `json:"from,omitempty"`
}

// Stream streams events from the journal.
func (h *Handlers) Stream(ctx context.Context, params StreamParams) (<-chan core.Event, error) {
	return h.journal.Stream(ctx, params.From)
}
