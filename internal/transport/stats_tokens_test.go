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
