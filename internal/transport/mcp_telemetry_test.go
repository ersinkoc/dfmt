package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// telemetryEvent constructs a tool.* journal event with the byte-savings
// fields the self-tuning telemetry reads. Tests use it to seed a mock
// journal at a precise compression ratio.
func telemetryEvent(typ core.EventType, raw, returned int) core.Event {
	return core.Event{
		ID:   "01" + strings.Repeat("X", 24),
		Type: typ,
		Data: map[string]any{
			core.KeyRawBytes:      raw,
			core.KeyReturnedBytes: returned,
		},
		TS: time.Now(),
	}
}

// TestToolStatsBlurb_AppendsWhenSamplesSufficient verifies the dynamic
// suffix lands on the description once enough tool.exec events are in the
// journal at meaningful compression.
func TestToolStatsBlurb_AppendsWhenSamplesSufficient(t *testing.T) {
	j := &mockJournal{}
	for i := 0; i < toolStatsMinSamples+5; i++ {
		j.events = append(j.events, telemetryEvent("tool.exec", 1000, 100)) // 90% savings
	}
	h := NewHandlers(core.NewIndex(), j, &stubSandbox{})
	m := NewMCPProtocol(h)

	resp, err := m.handleToolsList(&MCPRequest{ID: 1})
	if err != nil {
		t.Fatalf("handleToolsList: %v", err)
	}
	tools := resp.Result.(map[string]any)["tools"].([]MCPTool)
	var execDesc string
	for _, tool := range tools {
		if tool.Name == mcpToolExec {
			execDesc = tool.Description
			break
		}
	}
	if execDesc == "" {
		t.Fatal("dfmt_exec tool not found in tools/list response")
	}
	if !strings.Contains(execDesc, "Recent:") {
		t.Errorf("description must include the 'Recent:' blurb when samples are sufficient; got %q", execDesc)
	}
	if !strings.Contains(execDesc, "90%") {
		t.Errorf("description must reflect the observed compression ratio; got %q", execDesc)
	}
}

// TestToolStatsBlurb_SuppressedOnFewSamples verifies the blurb is omitted
// when the journal hasn't yet seen enough calls — the description stays
// honest on a fresh project instead of advertising "saves 0% over 1 call".
func TestToolStatsBlurb_SuppressedOnFewSamples(t *testing.T) {
	j := &mockJournal{events: []core.Event{
		telemetryEvent("tool.exec", 1000, 100),
		telemetryEvent("tool.exec", 1000, 100),
	}}
	h := NewHandlers(core.NewIndex(), j, &stubSandbox{})
	m := NewMCPProtocol(h)

	resp, _ := m.handleToolsList(&MCPRequest{ID: 1})
	tools := resp.Result.(map[string]any)["tools"].([]MCPTool)
	for _, tool := range tools {
		if tool.Name == mcpToolExec && strings.Contains(tool.Description, "Recent:") {
			t.Errorf("blurb must be suppressed below toolStatsMinSamples; got %q", tool.Description)
		}
	}
}

// TestToolStatsBlurb_SuppressedOnLowSavings verifies the threshold filter:
// when an agent is mostly using return=raw or hitting only inline-tier
// outputs, the savings approach 0% and the description should NOT
// advertise that as a feature.
func TestToolStatsBlurb_SuppressedOnLowSavings(t *testing.T) {
	j := &mockJournal{}
	for i := 0; i < toolStatsMinSamples+5; i++ {
		// raw == returned: zero savings.
		j.events = append(j.events, telemetryEvent("tool.exec", 500, 500))
	}
	h := NewHandlers(core.NewIndex(), j, &stubSandbox{})
	m := NewMCPProtocol(h)

	resp, _ := m.handleToolsList(&MCPRequest{ID: 1})
	tools := resp.Result.(map[string]any)["tools"].([]MCPTool)
	for _, tool := range tools {
		if tool.Name == mcpToolExec && strings.Contains(tool.Description, "Recent:") {
			t.Errorf("blurb must be suppressed when savings are sub-5%%; got %q", tool.Description)
		}
	}
}

// TestToolStatsBlurb_PerEventTypeIsolation verifies per-tool stats don't
// bleed: a journal with great exec compression but terrible read
// compression must not advertise the exec ratio on the read description.
func TestToolStatsBlurb_PerEventTypeIsolation(t *testing.T) {
	j := &mockJournal{}
	for i := 0; i < toolStatsMinSamples+5; i++ {
		j.events = append(j.events, telemetryEvent("tool.exec", 1000, 100)) // 90%
	}
	for i := 0; i < toolStatsMinSamples+5; i++ {
		j.events = append(j.events, telemetryEvent("tool.read", 1000, 999)) // 0.1%
	}
	h := NewHandlers(core.NewIndex(), j, &stubSandbox{})
	m := NewMCPProtocol(h)

	resp, _ := m.handleToolsList(&MCPRequest{ID: 1})
	tools := resp.Result.(map[string]any)["tools"].([]MCPTool)
	var readDesc, execDesc string
	for _, tool := range tools {
		switch tool.Name {
		case mcpToolRead:
			readDesc = tool.Description
		case mcpToolExec:
			execDesc = tool.Description
		}
	}
	if !strings.Contains(execDesc, "Recent:") {
		t.Errorf("exec must carry the blurb; got %q", execDesc)
	}
	if strings.Contains(readDesc, "Recent:") {
		t.Errorf("read must NOT inherit exec's stats; got %q", readDesc)
	}
}

// TestCompressionStats_Cached verifies the journal is streamed at most once
// per toolStatsTTL window — multiple tools/list calls share one scan.
func TestCompressionStats_Cached(t *testing.T) {
	j := &countingJournal{}
	for i := 0; i < toolStatsMinSamples+1; i++ {
		j.events = append(j.events, telemetryEvent("tool.exec", 1000, 100))
	}
	h := NewHandlers(core.NewIndex(), j, &stubSandbox{})
	m := NewMCPProtocol(h)

	for i := 0; i < 3; i++ {
		_, _ = m.handleToolsList(&MCPRequest{ID: 1})
	}
	if j.streamCalls > 1 {
		t.Errorf("journal Stream must be called once per TTL window; got %d", j.streamCalls)
	}
}

// TestCompressionStats_NoJournalIsSafe verifies a Handlers with a nil
// journal (degraded "no project" mode) still produces a working tools/list,
// just without the blurb.
func TestCompressionStats_NoJournalIsSafe(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, &stubSandbox{})
	m := NewMCPProtocol(h)

	resp, err := m.handleToolsList(&MCPRequest{ID: 1})
	if err != nil {
		t.Fatalf("handleToolsList must succeed without a journal; got %v", err)
	}
	tools := resp.Result.(map[string]any)["tools"].([]MCPTool)
	for _, tool := range tools {
		if strings.Contains(tool.Description, "Recent:") {
			t.Errorf("no-journal mode must not produce a blurb; got %q on %s", tool.Description, tool.Name)
		}
	}
}

// countingJournal wraps mockJournal to count Stream calls so the cache test
// can assert at-most-once-per-TTL semantics.
type countingJournal struct {
	mockJournal
	streamCalls int
}

func (j *countingJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	j.streamCalls++
	return j.mockJournal.Stream(ctx, from)
}

// Force compile-time confirmation that countingJournal satisfies core.Journal.
var _ core.Journal = (*countingJournal)(nil)

// Quiet unused "time" import on builds that elide some helpers.
var _ = time.Second
