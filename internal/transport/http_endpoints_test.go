package transport

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestHTTPHandleAPIDaemons_WithRegistry(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	dir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries := []map[string]any{{"project": "/p", "port": 9999}}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(filepath.Join(dir, "daemons.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	hs := newTestHTTPServerWithSandbox(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)

	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 entry, got %d", len(got))
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
