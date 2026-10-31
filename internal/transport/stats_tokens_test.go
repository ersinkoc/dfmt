package transport

import (
	"context"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

func TestBytesToTokens(t *testing.T) {
	for _, c := range []struct{ in, want int }{{-5, 0}, {0, 0}, {1, 1}, {4, 1}, {5, 2}, {4000, 1000}} {
		if got := bytesToTokens(c.in); got != c.want {
			t.Errorf("bytesToTokens(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// Grep and Glob record raw == returned: a native grep prints the same lines,
// so the ledger must show the call without inventing a saving.
func TestStats_GrepGlobRecordedWithoutSaving(t *testing.T) {
	journal := &mockJournal{}
	sb := &stubSandbox{
		globResp: sandbox.GlobResp{Files: []string{"a.go", "b.go"}, Summary: "2 files"},
		grepResp: sandbox.GrepResp{
			Matches: []sandbox.GrepMatch{{File: "a.go", Line: 3, Content: "needle", Before: []string{"x"}}},
			Summary: "1 match",
		},
	}
	h := NewHandlers(core.NewIndex(), journal, sb)
	ctx := context.Background()
	if _, err := h.Glob(ctx, GlobParams{Pattern: "*.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Grep(ctx, GrepParams{Pattern: "needle"}); err != nil {
		t.Fatal(err)
	}
	wantSize := map[string]int{
		"tool.glob": len("2 files") + len("a.go") + 1 + len("b.go") + 1,
		"tool.grep": len("1 match") + len("a.go") + len("needle") + len("x"),
	}
	if len(journal.events) != 2 {
		t.Fatalf("events = %d, want 2", len(journal.events))
	}
	for _, ev := range journal.events {
		raw, okR := getInt(ev.Data, core.KeyRawBytes)
		ret, okT := getInt(ev.Data, core.KeyReturnedBytes)
		if !okR || !okT || raw != ret {
			t.Fatalf("%s: raw=%d(%v) returned=%d(%v), want equal and present", ev.Type, raw, okR, ret, okT)
		}
		if want := wantSize[string(ev.Type)]; raw != want {
			t.Errorf("%s size = %d, want %d", ev.Type, raw, want)
		}
	}
	st, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if st.BytesSaved != 0 || st.EstTokensSaved != 0 {
		t.Errorf("grep/glob claimed a saving: bytes=%d tokens=%d", st.BytesSaved, st.EstTokensSaved)
	}
	if st.ToolBytes["tool.grep"].Calls != 1 || st.ToolBytes["tool.glob"].Calls != 1 {
		t.Errorf("ToolBytes missing grep/glob: %+v", st.ToolBytes)
	}
}

// A big output that comes back whole is where context actually fills; an
// excerpted one and a small one must not be counted.
func TestStats_LargeWholeCalls(t *testing.T) {
	big := strings.Repeat("x", 8000)
	for name, tc := range map[string]struct {
		resp sandbox.ExecResp
		want int
	}{
		"whole":     {sandbox.ExecResp{Stdout: big, RawStdout: big}, 1},
		"excerpted": {sandbox.ExecResp{Stdout: "tiny", RawStdout: big, Summary: "s"}, 0},
		"small":     {sandbox.ExecResp{Stdout: "ok", RawStdout: "ok"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewHandlers(core.NewIndex(), &mockJournal{}, &stubSandbox{execResp: tc.resp})
			ctx := context.Background()
			if _, err := h.Exec(ctx, ExecParams{Code: "x", Intent: "i"}); err != nil {
				t.Fatal(err)
			}
			st, err := h.Stats(ctx, StatsParams{})
			if err != nil {
				t.Fatal(err)
			}
			if st.LargeWholeCalls != tc.want {
				t.Errorf("LargeWholeCalls = %d, want %d", st.LargeWholeCalls, tc.want)
			}
			if tc.want == 1 && st.LargeWholeTokens != 2000 {
				t.Errorf("LargeWholeTokens = %d, want 2000", st.LargeWholeTokens)
			}
		})
	}
}

// Tokens are recorded per event, so a CJK body is not mis-sized by bytes/4
// (3 bytes per rune would read as 1500 tokens, ApproxTokens says 2000), and
// the return mode is what separates an avoidable leak from a plain big output.
func TestStats_TokensRecordedAtEventTime(t *testing.T) {
	cjk := strings.Repeat("日", 2000)
	journal := &mockJournal{}
	sb := &stubSandbox{execResp: sandbox.ExecResp{Stdout: cjk, RawStdout: cjk}}
	h := NewHandlers(core.NewIndex(), journal, sb)
	ctx := context.Background()
	if _, err := h.Exec(ctx, ExecParams{Code: "x", Return: "raw"}); err != nil {
		t.Fatal(err)
	}
	ev := journal.events[0]
	if got, _ := getInt(ev.Data, core.KeyRawTokens); got != 2000 {
		t.Errorf("raw_tokens = %d, want 2000", got)
	}
	if mode, _ := ev.Data[core.KeyReturnMode].(string); mode != "raw" {
		t.Errorf("return_mode = %q, want raw", mode)
	}
	st, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if st.EstRawTokens != 2000 || st.LargeWholeCalls != 1 || st.RawModeCalls != 1 || st.RawModeTokens != 2000 {
		t.Errorf("stats = raw %d, whole %d, rawmode %d/%d; want 2000, 1, 1/2000",
			st.EstRawTokens, st.LargeWholeCalls, st.RawModeCalls, st.RawModeTokens)
	}
}

func TestInsertTopSaver(t *testing.T) {
	var top []TopSaver
	for _, saved := range []int{5, 50, 1, 20, 30, 10, 40} {
		top = insertTopSaver(top, TopSaver{Saved: saved})
	}
	var got []int
	for _, s := range top {
		got = append(got, s.Saved)
	}
	want := []int{50, 40, 30, 20, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSaverLabel(t *testing.T) {
	if got := saverLabel(map[string]any{"code": "go  test\n./..."}); got != "go test ./..." {
		t.Errorf("label = %q", got)
	}
	long := strings.Repeat("a", 200)
	if got := []rune(saverLabel(map[string]any{"path": long})); len(got) != topSaverLabel {
		t.Errorf("label length = %d, want %d", len(got), topSaverLabel)
	}
	if got := saverLabel(map[string]any{}); got != "" {
		t.Errorf("label = %q, want empty", got)
	}
}

func TestStats_DailyAndTopSavers(t *testing.T) {
	big := strings.Repeat("x", 8000)
	sb := &stubSandbox{execResp: sandbox.ExecResp{Stdout: "tiny", RawStdout: big, Summary: "s"}}
	h := NewHandlers(core.NewIndex(), &mockJournal{}, sb)
	ctx := context.Background()
	if _, err := h.Exec(ctx, ExecParams{Code: "go test ./...", Intent: "i"}); err != nil {
		t.Fatal(err)
	}
	st, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Daily) != 1 || st.Daily[0].Calls != 1 || st.Daily[0].Saved <= 0 {
		t.Fatalf("Daily = %+v", st.Daily)
	}
	if len(st.TopSavers) != 1 || st.TopSavers[0].Label != "go test ./..." || st.TopSavers[0].Tool != "tool.exec" {
		t.Fatalf("TopSavers = %+v", st.TopSavers)
	}
	// The cached copy must not alias the slices handed to callers.
	st.TopSavers[0].Label = "mutated"
	again, _ := h.Stats(ctx, StatsParams{})
	if again.TopSavers[0].Label != "go test ./..." {
		t.Errorf("cache aliased: %q", again.TopSavers[0].Label)
	}
}

func TestStats_EstimatedTokensAndFixedCost(t *testing.T) {
	journal := &mockJournal{}
	sb := &stubSandbox{execResp: sandbox.ExecResp{
		Stdout: "tiny", RawStdout: strings.Repeat("x", 8000), Summary: "s",
	}}
	h := NewHandlers(core.NewIndex(), journal, sb)
	ctx := context.Background()
	if _, err := h.Exec(ctx, ExecParams{Code: "x", Intent: "i"}); err != nil {
		t.Fatal(err)
	}
	st, err := h.Stats(ctx, StatsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if want := bytesToTokens(st.TotalRawBytes); st.EstRawTokens != want {
		t.Errorf("EstRawTokens = %d, want %d", st.EstRawTokens, want)
	}
	if st.EstRawTokens-st.EstReturnedTokens != st.EstTokensSaved || st.EstTokensSaved <= 0 {
		t.Errorf("token invariant broken: raw=%d returned=%d saved=%d",
			st.EstRawTokens, st.EstReturnedTokens, st.EstTokensSaved)
	}
	if st.ToolsListBytes <= 0 || st.ToolsListTokens != bytesToTokens(st.ToolsListBytes) {
		t.Errorf("fixed cost = %d bytes / %d tokens", st.ToolsListBytes, st.ToolsListTokens)
	}
}
