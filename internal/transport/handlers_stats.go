package transport

import (
	"context"
	"fmt"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

type StatsParams struct {
	ProjectID string `json:"project_id,omitempty"`
	// NoCache bypasses the TTL-based memoisation. Human-driven CLI
	// callers (`dfmt stats`) set this so successive runs reflect the
	// current journal instead of a 5-second-stale snapshot. Dashboard
	// HTTP polling leaves this false to keep its high-frequency loop
	// from re-streaming the journal on every poll.
	NoCache bool `json:"no_cache,omitempty"`
}

// StatsResponse is the response for the Stats method.
//
// Most numeric fields use `omitempty` so a fresh project (zero events,
// zero token reports, zero MCP traffic) returns a small payload. The
// dashboard reads each field with a `|| 0` fallback (see
// internal/transport/dashboard.go) so an absent field reads as zero
// — same as the explicit zero would. EventsTotal stays without
// `omitempty` because consumers use its presence as a "stats are
// populated" sentinel.
type StatsResponse struct {
	EventsTotal      int            `json:"events_total"`
	EventsByType     map[string]int `json:"events_by_type,omitempty"`
	EventsByPriority map[string]int `json:"events_by_priority,omitempty"`
	SessionStart     string         `json:"session_start,omitempty"`
	SessionEnd       string         `json:"session_end,omitempty"`
	// LLM token metrics — populated only when callers pass input_tokens /
	// output_tokens / cached_tokens to dfmt_remember (the MCP layer cannot
	// observe API usage on its own).
	TotalInputTokens  int     `json:"total_input_tokens,omitempty"`
	TotalOutputTokens int     `json:"total_output_tokens,omitempty"`
	TotalCachedTokens int     `json:"total_cached_tokens,omitempty"`
	TokenSavings      int     `json:"token_savings,omitempty"` // alias of TotalCachedTokens
	CacheHitRate      float64 `json:"cache_hit_rate,omitempty"`
	// MCP byte savings — populated automatically by sandbox tool calls.
	// raw = pre-filter (post-redact) bytes; returned = bytes actually sent back.
	TotalRawBytes      int     `json:"total_raw_bytes,omitempty"`
	TotalReturnedBytes int     `json:"total_returned_bytes,omitempty"`
	BytesSaved         int     `json:"bytes_saved,omitempty"`       // raw - returned
	CompressionRatio   float64 `json:"compression_ratio,omitempty"` // saved / raw, 0..1
	// DedupHits is the number of times stashContent collapsed an
	// identical (kind, source, body) tuple to an existing chunk-set ID
	// instead of writing a new one. Process-lifetime counter — survives
	// idle restarts via re-warming, not via persistence.
	DedupHits int `json:"dedup_hits,omitempty"`
	// Native tool awareness: how many tool calls bypassed dfmt MCP
	NativeToolCalls      map[string]int `json:"native_tool_calls,omitempty"`       // count by tool name (Bash, Read, Glob, etc.)
	MCPToolCalls         map[string]int `json:"mcp_tool_calls,omitempty"`          // count by MCP tool name (dfmt.exec, dfmt.read, etc.)
	NativeToolBypassRate float64        `json:"native_tool_bypass_rate,omitempty"` // % of tool calls that used native tools
}

// knownNativeTools is the set of Claude Code built-in tool names captured
// by the PreToolUse hook and logged as note events with the tool name as tag.
var knownNativeTools = map[string]struct{}{
	"Bash": {}, "Read": {}, "Edit": {}, "Write": {}, "Glob": {},
	"Grep": {}, "WebFetch": {}, "WebSearch": {}, "TaskCreate": {},
	"TaskUpdate": {}, "TaskDone": {}, "Agent": {},
}

// Stats returns aggregated statistics from the journal.
// Aggregates as events stream in — O(|event-types| + |priorities|) memory,
// not O(|journal|). The previous implementation buffered every event into a
// slice, which grew unbounded on long-running projects.
func (h *Handlers) Stats(ctx context.Context, params StatsParams) (*StatsResponse, error) {
	h.touch()
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Journal == nil {
		return nil, errNoProject
	}

	// TTL cache: the dashboard polls /api/stats and re-streaming the full
	// journal every poll burned hundreds of ms on busy projects. Cache hits
	// return a defensive copy so callers can't mutate shared maps. Cache
	// misses (re)compute and store the fresh result — invalidation is
	// purely TTL-based; a freshly-appended event becomes visible at the
	// next refresh, which is well within human-readable polling rates.
	//
	// Bypassed entirely when params.NoCache is set so that `dfmt stats`
	// from the CLI shows the current journal state — humans interpret
	// "the number didn't change" as "DFMT is broken", and the 5-second
	// staleness window makes that interpretation easy to fall into.
	if !params.NoCache {
		h.statsCacheMu.RLock()
		if h.statsCache != nil && time.Since(h.statsCachedAt) < statsTTL {
			cached := h.statsCache
			h.statsCacheMu.RUnlock()
			return cloneStatsResponse(cached), nil
		}
		h.statsCacheMu.RUnlock()
	}

	stream, err := bundle.Journal.Stream(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("stream journal: %w", err)
	}

	resp := &StatsResponse{
		EventsByType:     make(map[string]int),
		EventsByPriority: make(map[string]int),
		NativeToolCalls:  make(map[string]int),
		MCPToolCalls:     make(map[string]int),
	}

	classifier := core.NewClassifier()
	var earliest, latest time.Time
	var totalInput, totalOutput, totalCached int
	var totalRawBytes, totalReturnedBytes int
	total := 0

	for e := range stream {
		total++
		resp.EventsByType[string(e.Type)]++
		resp.EventsByPriority[string(classifier.Classify(e))]++

		if inputTokens, ok := getInt(e.Data, core.KeyInputTokens); ok {
			totalInput += inputTokens
		}
		if outputTokens, ok := getInt(e.Data, core.KeyOutputTokens); ok {
			totalOutput += outputTokens
		}
		if cachedTokens, ok := getInt(e.Data, core.KeyCachedTokens); ok {
			totalCached += cachedTokens
		}
		if rawBytes, ok := getInt(e.Data, core.KeyRawBytes); ok {
			totalRawBytes += rawBytes
		}
		if returnedBytes, ok := getInt(e.Data, core.KeyReturnedBytes); ok {
			totalReturnedBytes += returnedBytes
		}

		// Track native tool calls via PreToolUse hook (note events with tool tag)
		if e.Type == core.EvtNote && len(e.Tags) > 0 {
			if _, ok := knownNativeTools[e.Tags[0]]; ok {
				resp.NativeToolCalls[e.Tags[0]]++
			}
		}
		// Track dfmt MCP tool calls (tool.exec, tool.read, tool.fetch, tool.glob, tool.grep, tool.edit, tool.write)
		if e.Type == "tool.exec" || e.Type == "tool.read" || e.Type == "tool.fetch" ||
			e.Type == "tool.glob" || e.Type == "tool.grep" || e.Type == "tool.edit" || e.Type == "tool.write" {
			resp.MCPToolCalls[string(e.Type)]++
		}

		if earliest.IsZero() || e.TS.Before(earliest) {
			earliest = e.TS
		}
		if latest.IsZero() || e.TS.After(latest) {
			latest = e.TS
		}
	}

	resp.EventsTotal = total
	resp.TotalInputTokens = totalInput
	resp.TotalOutputTokens = totalOutput
	resp.TotalCachedTokens = totalCached
	resp.TokenSavings = totalCached
	if totalInput > 0 {
		resp.CacheHitRate = float64(totalCached) / float64(totalInput) * 100
	}

	resp.TotalRawBytes = totalRawBytes
	resp.TotalReturnedBytes = totalReturnedBytes
	if totalRawBytes > totalReturnedBytes {
		resp.BytesSaved = totalRawBytes - totalReturnedBytes
	}
	if totalRawBytes > 0 {
		resp.CompressionRatio = float64(resp.BytesSaved) / float64(totalRawBytes)
	}
	resp.DedupHits = int(h.dedupHits.Load())

	// Compute native tool bypass rate
	var nativeTotal, mcpTotal int
	for _, n := range resp.NativeToolCalls {
		nativeTotal += n
	}
	for _, n := range resp.MCPToolCalls {
		mcpTotal += n
	}
	totalToolCalls := nativeTotal + mcpTotal
	if totalToolCalls > 0 {
		resp.NativeToolBypassRate = float64(nativeTotal) / float64(totalToolCalls) * 100
	}

	if !earliest.IsZero() {
		resp.SessionStart = earliest.Format(time.RFC3339)
	}
	if !latest.IsZero() {
		resp.SessionEnd = latest.Format(time.RFC3339)
	}

	h.statsCacheMu.Lock()
	h.statsCache = resp
	h.statsCachedAt = time.Now()
	h.statsCacheMu.Unlock()

	return cloneStatsResponse(resp), nil
}

// cloneStatsResponse produces a defensive copy. The cache is shared across
// concurrent callers; without copying, the dashboard's mutating its own map
// (e.g., adding derived keys) would corrupt the cached value for every
// future caller. Maps are duplicated; primitive fields are value-copied.
func cloneStatsResponse(src *StatsResponse) *StatsResponse {
	if src == nil {
		return nil
	}
	out := *src
	out.EventsByType = cloneIntMap(src.EventsByType)
	out.EventsByPriority = cloneIntMap(src.EventsByPriority)
	out.NativeToolCalls = cloneIntMap(src.NativeToolCalls)
	out.MCPToolCalls = cloneIntMap(src.MCPToolCalls)
	return &out
}

func cloneIntMap(src map[string]int) map[string]int {
	if src == nil {
		return nil
	}
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
