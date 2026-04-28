package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStatsResponse_ZeroValuesOmitted: a fresh project's stats response
// must not ship zero-valued numeric/string/map fields. Dashboard reads
// each field with a `|| 0` fallback (dashboard.go), so omission is
// observationally identical to "explicit zero" but ~150 bytes smaller
// per call. Cumulative win on the polling loop.
func TestStatsResponse_ZeroValuesOmitted(t *testing.T) {
	resp := &StatsResponse{
		EventsTotal: 0,
		// All other fields zero-valued.
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)
	// EventsTotal stays — it's the "stats populated" sentinel.
	if !strings.Contains(got, `"events_total":0`) {
		t.Errorf("events_total must always ship; got %s", got)
	}
	// Every omittable field must be absent.
	bannedKeys := []string{
		`"events_by_type"`,
		`"events_by_priority"`,
		`"session_start"`,
		`"session_end"`,
		`"total_input_tokens"`,
		`"total_output_tokens"`,
		`"total_cached_tokens"`,
		`"token_savings"`,
		`"cache_hit_rate"`,
		`"total_raw_bytes"`,
		`"total_returned_bytes"`,
		`"bytes_saved"`,
		`"compression_ratio"`,
		`"dedup_hits"`,
		`"native_tool_calls"`,
		`"mcp_tool_calls"`,
		`"native_tool_bypass_rate"`,
	}
	for _, k := range bannedKeys {
		if strings.Contains(got, k) {
			t.Errorf("zero-value field %s leaked: %s", k, got)
		}
	}
}

// TestStatsResponse_PopulatedFieldsShip: when a field is set, it ships.
// Pinning the inverse so a future "let's just always omit" change
// can't silently drop populated metrics.
func TestStatsResponse_PopulatedFieldsShip(t *testing.T) {
	resp := &StatsResponse{
		EventsTotal:        42,
		TotalRawBytes:      1000,
		TotalReturnedBytes: 200,
		BytesSaved:         800,
		CompressionRatio:   0.8,
		DedupHits:          5,
	}
	body, _ := json.Marshal(resp)
	got := string(body)
	for _, want := range []string{
		`"events_total":42`,
		`"total_raw_bytes":1000`,
		`"bytes_saved":800`,
		`"compression_ratio":0.8`,
		`"dedup_hits":5`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("populated field %q lost: %s", want, got)
		}
	}
}
