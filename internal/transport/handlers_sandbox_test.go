package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// stubSandbox implements sandbox.Sandbox for tests.
type stubSandbox struct {
	execResp  sandbox.ExecResp
	execErr   error
	readResp  sandbox.ReadResp
	readErr   error
	fetchResp sandbox.FetchResp
	fetchErr  error

	lastExecReq  sandbox.ExecReq
	lastReadReq  sandbox.ReadReq
	lastFetchReq sandbox.FetchReq
}

func (s *stubSandbox) Exec(ctx context.Context, req sandbox.ExecReq) (sandbox.ExecResp, error) {
	s.lastExecReq = req
	return s.execResp, s.execErr
}

func (s *stubSandbox) Read(ctx context.Context, req sandbox.ReadReq) (sandbox.ReadResp, error) {
	s.lastReadReq = req
	return s.readResp, s.readErr
}

func (s *stubSandbox) Fetch(ctx context.Context, req sandbox.FetchReq) (sandbox.FetchResp, error) {
	s.lastFetchReq = req
	return s.fetchResp, s.fetchErr
}

func (s *stubSandbox) BatchExec(ctx context.Context, items []any) ([]any, error) {
	return nil, nil
}

func TestHandlers_Exec_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		execResp: sandbox.ExecResp{
			Exit:       0,
			Stdout:     "hi",
			Stderr:     "",
			Summary:    "ran",
			Matches:    []sandbox.ContentMatch{{Text: "m", Score: 1}},
			Vocabulary: []string{"hi"},
			DurationMs: 5,
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Exec(context.Background(), ExecParams{
		Code:    "echo hi",
		Lang:    "bash",
		Intent:  "hello",
		Timeout: 3,
		Return:  "auto",
		Env:     map[string]string{"K": "V"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if resp.Exit != 0 || resp.Stdout != "hi" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if sb.lastExecReq.Timeout != 3*time.Second {
		t.Errorf("expected 3s timeout, got %v", sb.lastExecReq.Timeout)
	}
	if sb.lastExecReq.Env["K"] != "V" {
		t.Errorf("env not passed through")
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Exec_DefaultTimeout(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Exec(context.Background(), ExecParams{Code: "x"})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if sb.lastExecReq.Timeout != sandbox.DefaultExecTimeout {
		t.Errorf("expected default timeout, got %v", sb.lastExecReq.Timeout)
	}
}

func TestHandlers_Exec_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{execErr: errors.New("boom")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Exec(context.Background(), ExecParams{Code: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandlers_Read_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		readResp: sandbox.ReadResp{
			Content:   "file-body",
			Summary:   "small",
			Size:      10,
			ReadBytes: 9,
			Matches:   []sandbox.ContentMatch{{Text: "body", Score: 0.5, Line: 1}},
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Read(context.Background(), ReadParams{
		Path:   "/tmp/foo.txt",
		Intent: "body",
		Offset: 0,
		Limit:  1024,
		Return: "auto",
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if resp.Content != "file-body" || resp.Size != 10 || resp.ReadBytes != 9 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if sb.lastReadReq.Path != "/tmp/foo.txt" {
		t.Errorf("path not passed through")
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Read_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{readErr: errors.New("nope")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Read(context.Background(), ReadParams{Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandlers_Fetch_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		fetchResp: sandbox.FetchResp{
			Status:     200,
			Headers:    map[string]string{"Content-Type": "text/plain"},
			Body:       "ok",
			Summary:    "fetched",
			Matches:    []sandbox.ContentMatch{{Text: "ok", Score: 1}},
			Vocabulary: []string{"ok"},
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Fetch(context.Background(), FetchParams{
		URL:     "https://example.com/x",
		Intent:  "status",
		Method:  "GET",
		Headers: map[string]string{"X-Test": "1"},
		Body:    "",
		Return:  "auto",
		Timeout: 7,
	})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if resp.Status != 200 || resp.Body != "ok" {
		t.Errorf("unexpected: %+v", resp)
	}
	if sb.lastFetchReq.Timeout != 7*time.Second {
		t.Errorf("expected 7s timeout, got %v", sb.lastFetchReq.Timeout)
	}
	if sb.lastFetchReq.Headers["X-Test"] != "1" {
		t.Errorf("headers not passed through")
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Fetch_DefaultTimeout(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Fetch(context.Background(), FetchParams{URL: "https://x"})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if sb.lastFetchReq.Timeout != 30*time.Second {
		t.Errorf("expected 30s default timeout, got %v", sb.lastFetchReq.Timeout)
	}
}

func TestHandlers_Fetch_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{fetchErr: errors.New("bad url")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Fetch(context.Background(), FetchParams{URL: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandlers_Stats_Empty(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)

	resp, err := h.Stats(context.Background(), StatsParams{})
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if resp.EventsTotal != 0 {
		t.Errorf("expected 0 events, got %d", resp.EventsTotal)
	}
}

func TestHandlers_Stats_WithEvents(t *testing.T) {
	now := time.Now()
	ev1 := core.Event{
		ID:       "a",
		TS:       now.Add(-10 * time.Minute),
		Type:     core.EventType("note"),
		Priority: core.PriP1,
		Source:   core.SrcMCP,
		Data: map[string]any{
			core.KeyInputTokens:  100,
			core.KeyOutputTokens: 50,
			core.KeyCachedTokens: 25,
			core.KeyModel:        "claude",
		},
	}
	ev2 := core.Event{
		ID:       "b",
		TS:       now,
		Type:     core.EventType("note"),
		Priority: core.PriP1,
		Source:   core.SrcMCP,
		Data: map[string]any{
			core.KeyInputTokens:  int64(200),
			core.KeyOutputTokens: float64(80),
		},
	}
	journal := &mockJournal{events: []core.Event{ev1, ev2}}
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	resp, err := h.Stats(context.Background(), StatsParams{})
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if resp.EventsTotal != 2 {
		t.Errorf("expected 2 events, got %d", resp.EventsTotal)
	}
	if resp.TotalInputTokens != 300 {
		t.Errorf("expected 300 input tokens, got %d", resp.TotalInputTokens)
	}
	if resp.TotalOutputTokens != 130 {
		t.Errorf("expected 130 output tokens, got %d", resp.TotalOutputTokens)
	}
	if resp.TotalCachedTokens != 25 {
		t.Errorf("expected 25 cached tokens, got %d", resp.TotalCachedTokens)
	}
	if resp.SessionStart == "" || resp.SessionEnd == "" {
		t.Error("expected session start/end to be populated")
	}
	if resp.CacheHitRate <= 0 {
		t.Errorf("expected positive cache hit rate, got %v", resp.CacheHitRate)
	}
}

func TestHandlers_Stats_StreamError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failSearch: true}
	h := NewHandlers(idx, journal, nil)

	_, err := h.Stats(context.Background(), StatsParams{})
	if err == nil {
		t.Fatal("expected error from failing stream")
	}
}

func TestFormatEventData_Empty(t *testing.T) {
	if s := formatEventData(nil); s != "" {
		t.Errorf("expected empty, got %q", s)
	}
	if s := formatEventData(map[string]any{}); s != "" {
		t.Errorf("expected empty for empty map, got %q", s)
	}
}

func TestFormatEventData_Tokens(t *testing.T) {
	data := map[string]any{
		core.KeyInputTokens:  100,
		core.KeyOutputTokens: 50,
		core.KeyCachedTokens: 10,
		core.KeyModel:        "opus",
	}
	s := formatEventData(data)
	if s == "" {
		t.Fatal("expected non-empty formatted data")
	}
	// should contain in:100, out:50, cached:10, and model mention
	for _, want := range []string{"in:100", "out:50", "cached:10", "opus"} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q in %q", want, s)
		}
	}
}

func TestFormatEventData_GenericKeys(t *testing.T) {
	data := map[string]any{
		"a": "1",
		"b": "2",
		"c": "3",
		"d": "4",
		"e": "5",
	}
	s := formatEventData(data)
	if s == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(s, "...") {
		t.Errorf("expected ellipsis for >3 keys, got %q", s)
	}
}

func TestGetInt_Variants(t *testing.T) {
	if _, ok := getInt(nil, "k"); ok {
		t.Error("nil map should return false")
	}
	if _, ok := getInt(map[string]any{}, "missing"); ok {
		t.Error("missing key should return false")
	}
	if v, ok := getInt(map[string]any{"k": 42}, "k"); !ok || v != 42 {
		t.Errorf("int: got (%d, %v)", v, ok)
	}
	if v, ok := getInt(map[string]any{"k": int64(100)}, "k"); !ok || v != 100 {
		t.Errorf("int64: got (%d, %v)", v, ok)
	}
	if v, ok := getInt(map[string]any{"k": float64(7)}, "k"); !ok || v != 7 {
		t.Errorf("float64: got (%d, %v)", v, ok)
	}
	if _, ok := getInt(map[string]any{"k": "str"}, "k"); ok {
		t.Error("string should not be coerced")
	}
}

func TestHandlers_LogEvent_NilJournal(t *testing.T) {
	idx := core.NewIndex()
	h := NewHandlers(idx, nil, nil)
	// should not panic even with nil journal
	h.logEvent(context.Background(), "x", "y", map[string]any{"a": 1})
}
