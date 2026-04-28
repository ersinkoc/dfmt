package retrieve

import (
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestRenderMD_PathInterning_HotPathGetsToken: when a path appears
// internThreshold or more times, the renderer must emit a `[rNN]` token
// in each event and a single `**Refs:**` line at the top mapping back to
// the full path. The verbatim path appears exactly once in the output —
// in the table.
func TestRenderMD_PathInterning_HotPathGetsToken(t *testing.T) {
	hot := "/very/long/repo/path/internal/sandbox/intent.go"
	events := make([]core.Event, internThreshold+2)
	for i := range events {
		events[i] = core.Event{
			ID:       "e" + itoa(i),
			TS:       time.Now(),
			Type:     core.EvtFileEdit,
			Priority: core.PriP3,
			Data:     map[string]any{"path": hot},
		}
	}
	snap := &Snapshot{Events: events, ByteSize: 100, TierOrder: []string{"p3:" + itoa(len(events))}}
	out := NewMarkdownRenderer().Render(snap)

	if strings.Count(out, hot) != 1 {
		t.Errorf("hot path should appear exactly once (in Refs table); got %d occurrences",
			strings.Count(out, hot))
	}
	if !strings.Contains(out, "**Refs:**") {
		t.Error("Refs table missing")
	}
	if !strings.Contains(out, "[r0]") {
		t.Error("event should reference path via [r0] token")
	}
}

// TestRenderMD_PathInterning_BelowThresholdStaysVerbatim: paths that don't
// repeat enough must continue rendering verbatim. Otherwise small snapshots
// would carry a Refs table for nothing — pure overhead.
func TestRenderMD_PathInterning_BelowThresholdStaysVerbatim(t *testing.T) {
	path := "/path/once.go"
	events := []core.Event{
		{
			ID:       "e1",
			TS:       time.Now(),
			Type:     core.EvtFileEdit,
			Priority: core.PriP3,
			Data:     map[string]any{"path": path},
		},
	}
	snap := &Snapshot{Events: events, ByteSize: 100, TierOrder: []string{"p3:1"}}
	out := NewMarkdownRenderer().Render(snap)

	if strings.Contains(out, "**Refs:**") {
		t.Error("Refs table should not appear when no path crosses the threshold")
	}
	if !strings.Contains(out, "`"+path+"`") {
		t.Errorf("path should render verbatim with backticks; output: %s", out)
	}
}

// TestRenderMD_PathInterning_ByteWin verifies the optimization actually saves
// bytes on a realistic repeated-path workload. Compares byte length against
// a hypothetical "always verbatim" lower bound by counting raw path bytes.
func TestRenderMD_PathInterning_ByteWin(t *testing.T) {
	hot := "/very/long/repository/internal/sandbox/intent.go"
	const reps = 30
	events := make([]core.Event, reps)
	for i := range events {
		events[i] = core.Event{
			ID:       "e" + itoa(i),
			TS:       time.Unix(0, 0).UTC(),
			Type:     core.EvtFileEdit,
			Priority: core.PriP3,
			Data:     map[string]any{"path": hot},
		}
	}
	snap := &Snapshot{Events: events, ByteSize: 100, TierOrder: []string{"p3:30"}}
	out := NewMarkdownRenderer().Render(snap)

	// Lower bound on a "no interning" render: we can't run the un-interned
	// version directly without a flag, so we estimate by counting how many
	// path-bytes a verbatim render *would* have emitted. With interning,
	// path-bytes appear only in the Refs entry once.
	if strings.Count(out, hot) != 1 {
		t.Errorf("with interning the long path should appear once; got %d",
			strings.Count(out, hot))
	}
	wouldHaveBeen := reps * len(hot)
	tokenCost := strings.Count(out, "[r0]") * len("[r0]")
	if tokenCost == 0 {
		t.Fatal("expected events to use [r0] tokens")
	}
	if tokenCost >= wouldHaveBeen {
		t.Errorf("token rendering should be cheaper than verbatim; tokens=%d verbatim=%d",
			tokenCost, wouldHaveBeen)
	}
}

// TestRenderMD_PathInterning_Deterministic: two renders of the same input
// must produce byte-identical output, so snapshots stay diffable across
// invocations. Token assignment is alphabetical, so paths sort then index.
func TestRenderMD_PathInterning_Deterministic(t *testing.T) {
	events := []core.Event{
		mkEdit("/a.go"),
		mkEdit("/a.go"),
		mkEdit("/a.go"),
		mkEdit("/b.go"),
		mkEdit("/b.go"),
		mkEdit("/b.go"),
	}
	snap := &Snapshot{Events: events, ByteSize: 100, TierOrder: []string{"p3:6"}}
	r := NewMarkdownRenderer()
	out1 := r.Render(snap)
	out2 := r.Render(snap)
	if out1 != out2 {
		t.Error("Render must be deterministic; got different outputs")
	}
	// Alphabetical: a.go -> r0, b.go -> r1.
	if !strings.Contains(out1, "r0=`/a.go`") {
		t.Errorf("expected r0=`/a.go`; got %s", out1)
	}
	if !strings.Contains(out1, "r1=`/b.go`") {
		t.Errorf("expected r1=`/b.go`; got %s", out1)
	}
}

func mkEdit(path string) core.Event {
	return core.Event{
		ID:       "e",
		TS:       time.Unix(0, 0).UTC(),
		Type:     core.EvtFileEdit,
		Priority: core.PriP3,
		Data:     map[string]any{"path": path},
	}
}

// itoa is a tiny stdlib-free int-to-string for test labels.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
