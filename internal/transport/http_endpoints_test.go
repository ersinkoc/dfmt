package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

func newTestHTTPServerWithSandbox(sb sandbox.Sandbox) *HTTPServer {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, &mockJournal{}, sb)
	return NewHTTPServer("127.0.0.1:0", handlers)
}

func doRPC(t *testing.T, hs *HTTPServer, method string, params any) Response {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reqBody, err := json.Marshal(Request{
		JSONRPC: jsonRPCVersion,
		ID:      1,
		Method:  method,
		Params:  raw,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.handle(rec, req)

	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	return resp
}

func TestHTTPHandle_Exec(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{Exit: 0, Stdout: "hi"}}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "dfmt.exec", ExecParams{Code: "echo hi", Intent: "greet"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Exec_Alias(t *testing.T) {
	sb := &stubSandbox{execResp: sandbox.ExecResp{Exit: 1}}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "exec", ExecParams{Code: "x"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Exec_Error(t *testing.T) {
	sb := &stubSandbox{execErr: errors.New("boom")}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "dfmt.exec", ExecParams{Code: "x"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603, got %d", resp.Error.Code)
	}
}

func TestHTTPHandle_Read(t *testing.T) {
	sb := &stubSandbox{readResp: sandbox.ReadResp{Content: "abc", Size: 3, ReadBytes: 3}}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "dfmt.read", ReadParams{Path: "/tmp/x", Intent: "body"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Read_Error(t *testing.T) {
	sb := &stubSandbox{readErr: errors.New("missing")}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "dfmt.read", ReadParams{Path: "/nope"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPHandle_Fetch(t *testing.T) {
	sb := &stubSandbox{fetchResp: sandbox.FetchResp{Status: 200, Body: "ok"}}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "dfmt.fetch", FetchParams{URL: "https://x", Intent: "status"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Fetch_Error(t *testing.T) {
	sb := &stubSandbox{fetchErr: errors.New("dns")}
	hs := newTestHTTPServerWithSandbox(sb)

	resp := doRPC(t, hs, "dfmt.fetch", FetchParams{URL: "x"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestHTTPHandle_Stats(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	resp := doRPC(t, hs, "dfmt.stats", StatsParams{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Stats_Error(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failSearch: true}
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	resp := doRPC(t, hs, "dfmt.stats", StatsParams{})
	if resp.Error == nil {
		t.Fatal("expected error from failing stream")
	}
}

func TestHTTPHandleDashboard(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()

	hs.handleDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected HTML content type, got %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff header")
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty dashboard body")
	}
}

func TestHTTPHandleHealth(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	hs.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Errorf("expected 'ok' body, got %q", rec.Body.String())
	}
}

func TestHTTPHandleAPIStats_OK(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	body, _ := json.Marshal(Request{JSONRPC: jsonRPCVersion, ID: 1, Method: "stats"})
	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandleAPIStats_MethodNotAllowed(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIStats(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHTTPHandleAPIStats_ParseError(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	hs.handleAPIStats(rec, req)

	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("expected parse error, got %+v", resp.Error)
	}
}

func TestHTTPHandleAPIStats_HandlerError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failSearch: true}
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	body, _ := json.Marshal(Request{JSONRPC: jsonRPCVersion, ID: 1, Method: "stats"})
	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error")
	}
}

func TestHTTPHandleAPIDaemons_NoRegistry(t *testing.T) {
	// Redirect HOME so reading daemons.json fails (empty array returned).
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Errorf("expected empty array, got %q", body)
	}
}

// TestHTTPHandleAPIDaemons_FailsClosedWhenProjectPathUnset covers F-16.
// When the server has no projectPath set (test harness, future caller that
// forgot SetProjectPath, integrator subclass), the previous filter logic
// dropped to the unfiltered branch and returned every daemon on the host —
// disclosing the existence of unrelated projects to whoever can reach this
// loopback port. The fix is fail-closed: empty list rather than full list.
func TestHTTPHandleAPIDaemons_FailsClosedWhenProjectPathUnset(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	dir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries := []map[string]any{
		{"project_path": "/proj/a", "port": 9001},
		{"project_path": "/proj/b", "port": 9002},
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "daemons.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	hs := newTestHTTPServerWithSandbox(nil) // projectPath left empty
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Errorf("projectPath unset should fail closed; want '[]' got %q", body)
	}
}

// TestHTTPHandleAPIDaemons_FiltersToOwnProject confirms that when
// projectPath is set, only the matching entry is returned — the sibling to
// the F-16 fail-closed case.
func TestHTTPHandleAPIDaemons_FiltersToOwnProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	dir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries := []map[string]any{
		{"project_path": "/proj/a", "port": 9001},
		{"project_path": "/proj/b", "port": 9002},
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "daemons.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	hs := newTestHTTPServerWithSandbox(nil)
	hs.SetProjectPath("/proj/b")
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)

	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 entry filtered to /proj/b, got %d", len(got))
	}
	if path, _ := got[0]["project_path"].(string); path != "/proj/b" {
		t.Errorf("filter returned wrong entry: %+v", got[0])
	}
}

func TestHTTPHandleAPIDaemons_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	dir := filepath.Join(tmp, ".dfmt")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "daemons.json"), []byte("not json"), 0644)

	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("expected '[]' on bad json, got %q", rec.Body.String())
	}
}

func TestNewHTTPServerWithListener(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, &mockJournal{}, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	hs := NewHTTPServerWithListener(ln, handlers, "/tmp/does-not-matter.sock")
	if hs == nil {
		t.Fatal("expected server")
	}
	if hs.handlers != handlers {
		t.Error("handlers not wired")
	}
	if hs.socketPath == "" {
		t.Error("socketPath not set")
	}
	if hs.listener == nil {
		t.Error("listener not set")
	}
}

func TestHTTPHandleAPIStream_OK(t *testing.T) {
	mj := &mockJournal{
		events: []core.Event{
			{ID: "1", TS: core.Now(), Type: "tool.exec", Priority: "p1", Data: map[string]any{"code": "echo hi"}},
			{ID: "2", TS: core.Now(), Type: "tool.read", Priority: "p2", Data: map[string]any{"path": "/tmp/foo"}},
		},
	}
	idx := core.NewIndex()
	handlers := NewHandlers(idx, mj, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Error("expected SSE data: lines")
	}
}

func TestHTTPHandleAPIStream_MethodNotAllowed(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/stream", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIStream(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHTTPHandleAPIStream_HandlersNil(t *testing.T) {
	hs := NewHTTPServer("127.0.0.1:0", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIStream(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHTTPServerIsAllowedOrigin_TCPListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: could not create TCP listener: %v", err)
	}
	defer ln.Close()

	hs := newTestHTTPServerWithSandbox(nil)
	hs.listener = ln
	defer hs.Stop(context.Background())

	addr := ln.Addr().(*net.TCPAddr)
	want := fmt.Sprintf("http://%s", addr.String())

	if !hs.isAllowedOrigin(want) {
		t.Errorf("isAllowedOrigin(%q) = false, want true", want)
	}
	// Wrong origin should be rejected
	if hs.isAllowedOrigin("http://evil.com") {
		t.Error("isAllowedOrigin(evil.com) = true, want false")
	}
	// Different port should be rejected
	wrongPort := fmt.Sprintf("http://127.0.0.1:%d", addr.Port+1)
	if hs.isAllowedOrigin(wrongPort) {
		t.Errorf("isAllowedOrigin(%q) = true, want false (wrong port)", wrongPort)
	}
}

func TestHTTPServerIsAllowedOrigin_NonTCPListener(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	hs.listener = nil

	if hs.isAllowedOrigin("http://127.0.0.1:12345") {
		t.Error("isAllowedOrigin with nil listener = true, want false")
	}
}

func TestHTTPServerIsAllowedHost_TCPListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: could not create TCP listener: %v", err)
	}
	defer ln.Close()

	hs := newTestHTTPServerWithSandbox(nil)
	hs.listener = ln
	defer hs.Stop(context.Background())

	addr := ln.Addr().(*net.TCPAddr)
	if !hs.isAllowedHost(addr.String()) {
		t.Errorf("isAllowedHost(%q) = false, want true", addr.String())
	}
	if hs.isAllowedHost("evil.com") {
		t.Error("isAllowedHost(evil.com) = true, want false")
	}
	// localhost form
	if !hs.isAllowedHost(fmt.Sprintf("localhost:%d", addr.Port)) {
		t.Errorf("isAllowedHost(localhost:%d) = false, want true", addr.Port)
	}
	// IPv6 localhost form
	if !hs.isAllowedHost(fmt.Sprintf("[::1]:%d", addr.Port)) {
		t.Errorf("isAllowedHost([::1]:%d) = false, want true", addr.Port)
	}
	// unknown host
	if hs.isAllowedHost("unknown:9999") {
		t.Error("isAllowedHost(unknown:9999) = true, want false")
	}
}

func TestHTTPServerIsAllowedHost_NonTCPListener(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	hs.listener = nil
	// nil listener = fail-closed (no listener to validate against).
	if hs.isAllowedHost("anything") {
		t.Error("isAllowedHost with nil listener = true, want false")
	}
}

func TestHTTPHandle_Glob(t *testing.T) {
	sb := &stubSandbox{
		globResp: sandbox.GlobResp{
			Files:   []string{"/a/b.go", "/a/c.go"},
			Matches: []sandbox.ContentMatch{{Text: "match", Score: 1}},
		},
	}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.glob", GlobParams{Pattern: "*.go", Intent: "files"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Glob_Error(t *testing.T) {
	sb := &stubSandbox{globErr: errors.New("glob failed")}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.glob", GlobParams{Pattern: "*.go"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603, got %d", resp.Error.Code)
	}
}

func TestHTTPHandle_Grep(t *testing.T) {
	sb := &stubSandbox{
		grepResp: sandbox.GrepResp{
			Matches: []sandbox.GrepMatch{{File: "/a/b.go", Line: 10, Content: "func foo()"}},
			Summary: "1 match",
		},
	}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.grep", GrepParams{Pattern: "func foo", Path: "/a"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Grep_Error(t *testing.T) {
	sb := &stubSandbox{grepErr: errors.New("grep failed")}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.grep", GrepParams{Pattern: "foo"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603, got %d", resp.Error.Code)
	}
}

func TestHTTPHandle_Edit(t *testing.T) {
	sb := &stubSandbox{
		editResp: sandbox.EditResp{Success: true, Summary: "1 replacement"},
	}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.edit", EditParams{Path: "/a/b.go", OldString: "foo", NewString: "bar"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Edit_Error(t *testing.T) {
	sb := &stubSandbox{editErr: errors.New("edit failed")}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.edit", EditParams{Path: "/a/b.go", OldString: "foo", NewString: "bar"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603, got %d", resp.Error.Code)
	}
}

func TestHTTPHandle_Write(t *testing.T) {
	sb := &stubSandbox{
		writeResp: sandbox.WriteResp{Success: true, Summary: "wrote 10 bytes"},
	}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.write", WriteParams{Path: "/a/b.go", Content: "hello"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestHTTPHandle_Write_Error(t *testing.T) {
	sb := &stubSandbox{writeErr: errors.New("write failed")}
	hs := newTestHTTPServerWithSandbox(sb)
	resp := doRPC(t, hs, "dfmt.write", WriteParams{Path: "/a/b.go", Content: "hello"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("expected -32603, got %d", resp.Error.Code)
	}
}

func TestHTTPHandleDashboardJS(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/dashboard.js", nil)
	rec := httptest.NewRecorder()
	hs.handleDashboardJS(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/javascript") {
		t.Errorf("expected application/javascript, got %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff header")
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body")
	}
}

func TestHTTPHandleAPIAllDaemons_Empty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/all-daemons", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIAllDaemons(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("expected [], got %q", rec.Body.String())
	}
}

func TestHTTPHandleAPIAllDaemons_WithEntries(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	dir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries := []map[string]any{
		{"project_path": "/proj/a", "port": 9001},
		{"project_path": "/proj/b", "port": 9002},
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "daemons.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/all-daemons", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIAllDaemons(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 daemons, got %d", len(got))
	}
}

func TestHTTPHandleAPIAllDaemons_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	dir := filepath.Join(tmp, ".dfmt")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "daemons.json"), []byte("not json"), 0644)
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/all-daemons", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIAllDaemons(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("expected [], got %q", rec.Body.String())
	}
}

func TestHTTPHandleAPIProxy_MethodNotAllowed(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/proxy", nil)
	rec := httptest.NewRecorder()
	hs.handleAPIProxy(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHTTPHandleAPIProxy_InvalidBody(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/proxy", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.handleAPIProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (error in body), got %d", rec.Code)
	}
	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error in response")
	}
}

func TestHTTPHandleAPIProxy_MissingTargetPath(t *testing.T) {
	hs := newTestHTTPServerWithSandbox(nil)
	body := `{"method":"stats","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/api/proxy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hs.handleAPIProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (error in body), got %d", rec.Code)
	}
	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error == nil {
		t.Error("expected error in response for missing target_project_path")
	}
}
