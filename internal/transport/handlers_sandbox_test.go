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
	globResp  sandbox.GlobResp
	globErr   error
	grepResp  sandbox.GrepResp
	grepErr   error
	editResp  sandbox.EditResp
	editErr   error
	writeResp sandbox.WriteResp
	writeErr  error

	lastExecReq  sandbox.ExecReq
	lastReadReq  sandbox.ReadReq
	lastFetchReq sandbox.FetchReq
	lastGlobReq  sandbox.GlobReq
	lastGrepReq  sandbox.GrepReq
	lastEditReq  sandbox.EditReq
	lastWriteReq sandbox.WriteReq
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

func (s *stubSandbox) Glob(ctx context.Context, req sandbox.GlobReq) (sandbox.GlobResp, error) {
	s.lastGlobReq = req
	return s.globResp, s.globErr
}

func (s *stubSandbox) Grep(ctx context.Context, req sandbox.GrepReq) (sandbox.GrepResp, error) {
	s.lastGrepReq = req
	return s.grepResp, s.grepErr
}

func (s *stubSandbox) Edit(ctx context.Context, req sandbox.EditReq) (sandbox.EditResp, error) {
	s.lastEditReq = req
	return s.editResp, s.editErr
}

func (s *stubSandbox) Write(ctx context.Context, req sandbox.WriteReq) (sandbox.WriteResp, error) {
	s.lastWriteReq = req
	return s.writeResp, s.writeErr
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

func TestHandlers_Stats_NativeToolCalls(t *testing.T) {
	// note events with tool tags (PreToolUse hook captures of native tools)
	// and tool.exec events (dfmt MCP sandbox calls)
	now := time.Now()
	events := []core.Event{
		{ID: "1", TS: now, Type: core.EventType("note"), Tags: []string{"Bash"}},
		{ID: "2", TS: now, Type: core.EventType("note"), Tags: []string{"Read"}},
		{ID: "3", TS: now, Type: core.EventType("note"), Tags: []string{"Bash"}},
		{ID: "4", TS: now, Type: core.EventType("note"), Tags: []string{"Grep"}},
		{ID: "5", TS: now, Type: core.EventType("note"), Tags: []string{"note"}},           // not a native tool
		{ID: "6", TS: now, Type: core.EventType("tool.exec"), Tags: []string{}},           // dfmt MCP
		{ID: "7", TS: now, Type: core.EventType("tool.exec"), Tags: []string{}},
		{ID: "8", TS: now, Type: core.EventType("tool.read"), Tags: []string{}},
	}
	journal := &mockJournal{events: events}
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	resp, err := h.Stats(context.Background(), StatsParams{})
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	// Native tool calls should be tracked by tool name
	if resp.NativeToolCalls == nil {
		t.Fatal("NativeToolCalls is nil")
	}
	if got := resp.NativeToolCalls["Bash"]; got != 2 {
		t.Errorf("NativeToolCalls[Bash] = %d, want 2", got)
	}
	if got := resp.NativeToolCalls["Read"]; got != 1 {
		t.Errorf("NativeToolCalls[Read] = %d, want 1", got)
	}
	if got := resp.NativeToolCalls["Grep"]; got != 1 {
		t.Errorf("NativeToolCalls[Grep] = %d, want 1", got)
	}
	if _, ok := resp.NativeToolCalls["note"]; ok {
		t.Error("NativeToolCalls should not contain non-tool tag 'note'")
	}

	// MCP tool calls should be tracked by type
	if resp.MCPToolCalls == nil {
		t.Fatal("MCPToolCalls is nil")
	}
	if got := resp.MCPToolCalls["tool.exec"]; got != 2 {
		t.Errorf("MCPToolCalls[tool.exec] = %d, want 2", got)
	}
	if got := resp.MCPToolCalls["tool.read"]; got != 1 {
		t.Errorf("MCPToolCalls[tool.read] = %d, want 1", got)
	}

	// Bypass rate: 4 native out of 7 total = 57.14%
	if resp.NativeToolBypassRate <= 0 {
		t.Errorf("NativeToolBypassRate = %v, want > 0", resp.NativeToolBypassRate)
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

func TestHandlers_Glob_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		globResp: sandbox.GlobResp{
			Files:   []string{"a.go", "b.go"},
			Matches: []sandbox.ContentMatch{{Text: "func test", Score: 1}},
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Glob(context.Background(), GlobParams{
		Pattern: "*.go",
		Intent:  "test functions",
	})
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if len(resp.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(resp.Files))
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Glob_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{globErr: errors.New("pattern error")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Glob(context.Background(), GlobParams{Pattern: "*.go"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandlers_Grep_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		grepResp: sandbox.GrepResp{
			Matches: []sandbox.GrepMatch{
				{File: "a.go", Line: 10, Content: "func test() {}"},
				{File: "b.go", Line: 20, Content: "func Test() {}"},
			},
			Summary: "Found 2 matches in 2 files",
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Grep(context.Background(), GrepParams{
		Pattern: "func test",
		Files:   "*.go",
		Intent:  "test functions",
	})
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if len(resp.Matches) != 2 {
		t.Errorf("expected 2 matches, got %d", len(resp.Matches))
	}
	if !strings.Contains(resp.Summary, "2 matches") {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Grep_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{grepErr: errors.New("invalid pattern")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Grep(context.Background(), GrepParams{Pattern: "[invalid"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandlers_Edit_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		editResp: sandbox.EditResp{
			Success: true,
			Summary: "Replaced string in test.go",
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Edit(context.Background(), EditParams{
		Path:      "test.go",
		OldString: "old",
		NewString: "new",
	})
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}
	if !resp.Success {
		t.Error("Edit should succeed")
	}
	if sb.lastEditReq.OldString != "old" || sb.lastEditReq.NewString != "new" {
		t.Errorf("unexpected edit params: %+v", sb.lastEditReq)
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Edit_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{editErr: errors.New("not found")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Edit(context.Background(), EditParams{
		Path:      "test.go",
		OldString: "notfound",
		NewString: "new",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandlers_Write_Success(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	sb := &stubSandbox{
		writeResp: sandbox.WriteResp{
			Success: true,
			Summary: "Wrote 100 bytes to new.go",
		},
	}
	h := NewHandlers(idx, journal, sb)

	resp, err := h.Write(context.Background(), WriteParams{
		Path:    "new.go",
		Content: "package main\n",
	})
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if !resp.Success {
		t.Error("Write should succeed")
	}
	if sb.lastWriteReq.Path != "new.go" || sb.lastWriteReq.Content != "package main\n" {
		t.Errorf("unexpected write params: %+v", sb.lastWriteReq)
	}
	if len(journal.events) != 1 {
		t.Errorf("expected 1 event logged, got %d", len(journal.events))
	}
}

func TestHandlers_Write_Error(t *testing.T) {
	idx := core.NewIndex()
	sb := &stubSandbox{writeErr: errors.New("permission denied")}
	h := NewHandlers(idx, &mockJournal{}, sb)

	_, err := h.Write(context.Background(), WriteParams{
		Path:    "/etc/passwd",
		Content: "malicious",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
