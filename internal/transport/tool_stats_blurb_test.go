package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestToolStatsBlurbFromDaemonStats: the tools/list savings blurb used to
// stream the whole journal to every proxy at session start; it now reads
// per-tool totals from Stats. It must still appear with enough samples and
// stay silent below the floor.
func TestToolStatsBlurbFromDaemonStats(t *testing.T) {
	journal := &mockJournal{}
	base := time.Now()
	for i := 0; i < toolStatsMinSamples; i++ {
		journal.events = append(journal.events, core.Event{
			ID: string(core.NewULID(base.Add(time.Duration(i) * time.Millisecond))), TS: base,
			Type: "tool.exec",
			Data: map[string]any{core.KeyRawBytes: 1000, core.KeyReturnedBytes: 100},
		})
	}
	journal.events = append(journal.events, core.Event{
		ID: string(core.NewULID(base.Add(time.Second))), TS: base, Type: "tool.read",
		Data: map[string]any{core.KeyRawBytes: 1000, core.KeyReturnedBytes: 100},
	})
	m := NewMCPProtocol(NewHandlers(core.NewIndex(), journal, nil))

	if got := m.toolStatsBlurb("tool.exec"); !strings.Contains(got, "90%") ||
		!strings.Contains(got, "5 calls") {
		t.Errorf("exec blurb = %q, want ~90%% over 5 calls", got)
	}
	if got := m.toolStatsBlurb("tool.read"); got != "" {
		t.Errorf("read blurb with 1 sample = %q, want none", got)
	}

	st, err := NewHandlers(core.NewIndex(), journal, nil).Stats(context.Background(), StatsParams{NoCache: true})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if tb := st.ToolBytes["tool.exec"]; tb.Calls != 5 || tb.RawBytes != 5000 || tb.ReturnedBytes != 500 {
		t.Errorf("ToolBytes[tool.exec] = %+v", tb)
	}
}
