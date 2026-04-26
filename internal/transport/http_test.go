package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestHTTPServerStopNotRunning(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	if err := hs.Stop(context.Background()); err != nil {
		t.Errorf("Stop on not-running server failed: %v", err)
	}
}

func TestHTTPServerStartAndStop(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil) // nil journal for testing
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	ctx := context.Background()
	err := hs.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !hs.running {
		t.Error("server should be running after Start")
	}

	err = hs.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestHTTPServerStartTwice(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	ctx := context.Background()
	if err := hs.Start(ctx); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	defer hs.Stop(context.Background())

	// Second start should fail
	if err := hs.Start(ctx); err == nil {
		t.Error("Second Start should fail")
	}
}

// TestHTTPServerStartRefusesNonLoopbackBind covers F-09: a non-loopback bind
// would expose unauthenticated JSON-RPC (dfmt.exec, dfmt.write, dfmt.fetch)
// to the LAN because bearer-token auth is not currently wired. Start() must
// refuse rather than silently shipping an unauthenticated public endpoint.
func TestHTTPServerStartRefusesNonLoopbackBind(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	// "0.0.0.0:0" binds to all interfaces; net.Listen succeeds and Addr() is
	// the unspecified IP, which IsLoopback() rejects.
	hs := NewHTTPServer("0.0.0.0:0", handlers)
	ctx := context.Background()
	err := hs.Start(ctx)
	if err == nil {
		_ = hs.Stop(context.Background())
		t.Fatal("Start() must refuse non-loopback bind")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("Start() error = %q; want to contain 'non-loopback'", err.Error())
	}
}

func TestHTTPServerStartPortFileWriteError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Set a port file path that cannot be written
	portFile := "/proc/invalid_port_file_dfmt_test/port"
	if os.PathSeparator == '\\' {
		// On Windows, use a path with invalid characters
		portFile = "NUL:/dfmt_invalid_portfile_test"
	}
	hs.SetPortFile(portFile)

	ctx := context.Background()
	err := hs.Start(ctx)
	// If Start fails due to port file write error, we've covered that path
	// If it succeeds, the path was writable on this system
	if err != nil {
		t.Logf("HTTPServer.Start failed as expected for port file write: %v", err)
	} else {
		hs.Stop(context.Background())
	}
}

func TestHandleMethodNotAllowed(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleParseError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Invalid JSON should return parse error
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp.Error == nil {
		t.Error("expected error response for invalid JSON")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("expected -32700 (parse error), got %d", resp.Error.Code)
	}
}

func TestHandleUnknownMethod(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	reqBody := Request{
		JSONRPC: "2.0",
		Method:  "unknown.method",
		ID:      1,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp.Error == nil {
		t.Error("expected error response for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected -32601, got %d", resp.Error.Code)
	}
}

func TestWritePortFile(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	tmpDir := t.TempDir()
	portFile := tmpDir + "/.dfmt/port"

	err := hs.writePortFile(portFile, 12345, "tok123")
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	var pf PortFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal port file: %v", err)
	}
	if pf.Port != 12345 {
		t.Errorf("expected port 12345, got %d", pf.Port)
	}
	if pf.Token != "tok123" {
		t.Errorf("expected token tok123, got %q", pf.Token)
	}
}

func TestSetPortFile(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	hs.SetPortFile("/test/portfile")

	if hs.portFile != "/test/portfile" {
		t.Errorf("expected '/test/portfile', got '%s'", hs.portFile)
	}
}

func TestHTTPServerHandleWithSessionID(t *testing.T) {
	// This test is skipped because calling handlers with nil journal causes panic
	// The actual handleRemember calls handlers.Remember which requires a non-nil journal
	t.Skip("Skipping - handlers method requires non-nil journal")
}

func TestHandleReadBodyError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Create a request that will fail to read body
	req := httptest.NewRequest(http.MethodPost, "/", &errorReader{})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	// Should return Bad Request or Request Entity Too Large
	// (MaxBytesReader returns 413 when body exceeds limit, errorReader causes error)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected status %d or %d, got %d", http.StatusBadRequest, http.StatusRequestEntityTooLarge, rec.Code)
	}
}

func TestHandleJSONUnmarshalError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Valid JSON but missing required fields - will unmarshal but have empty method
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"not":"a request"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp.Error == nil {
		t.Error("expected error response for invalid request")
	}
	// Method is empty string, so it falls to default case with -32601
	if resp.Error.Code != -32601 {
		t.Errorf("expected -32601 (method not found), got %d", resp.Error.Code)
	}
}

func TestHandleRememberError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failRemember: true}
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	params := mustMarshalParams(map[string]any{"type": "note", "source": "test"})
	reqBody := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.remember",
		Params:  params,
		ID:      1,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp.Error == nil {
		t.Error("expected error response for handler failure")
	}
}

func TestHandleSearchError(t *testing.T) {
	// Search via HTTP handler doesn't have a direct error path
	// because handlers.Search uses index, not journal
	// This test verifies the success path
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	params := mustMarshalParams(map[string]any{"query": "test"})
	reqBody := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.search",
		Params:  params,
		ID:      2,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)

	// Search should succeed (no error)
	if resp.Error != nil {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
}

func TestHandleRecallError(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{failRecall: true}
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	params := mustMarshalParams(map[string]any{"budget": 100})
	reqBody := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.recall",
		Params:  params,
		ID:      3,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp.Error == nil {
		t.Error("expected error response for handler failure")
	}
}

func TestHTTPServerWritePortFileCreatesNestedDir(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	tmpDir := t.TempDir()
	portFile := tmpDir + "/.dfmt/nested/deep/port"

	err := hs.writePortFile(portFile, 9999, "")
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	var pf PortFile
	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal port file: %v", err)
	}
	if pf.Port != 9999 {
		t.Errorf("expected port 9999, got %d", pf.Port)
	}
}

// errorReader is a reader that always returns an error
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("simulated read error")
}

// =============================================================================
// writePortFile error path tests
// =============================================================================

func TestHTTPServerWritePortFileInvalidDir(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Try to write to an invalid path that can't be created
	// On Unix, /proc is not writable; on Windows, a path with invalid chars
	portFile := "/proc/invalid_port_file_12345/port"
	if os.PathSeparator == '\\' {
		// On Windows, use a path with invalid characters
		portFile = "NUL:/invalid"
	}

	err := hs.writePortFile(portFile, 12345, "")
	if err == nil {
		t.Log("writePortFile succeeded (may be allowed on some systems)")
	}
}

func TestHTTPServerWritePortFileEmptyDir(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Empty path should fail
	err := hs.writePortFile("", 12345, "")
	if err == nil {
		t.Error("writePortFile should fail with empty path")
	}
}

// TestHandleInvalidParams covers V-16: every JSON-RPC method must respond
// with code -32602 ("Invalid params") when the params field is malformed
// JSON or the wrong shape. Previously the marshal/unmarshal round-trip
// silently produced a zero-value params struct and the request slid through
// to the handler, masking client bugs and producing generic errors.
func TestHandleInvalidParams(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Each method advertised through the JSON-RPC dispatcher. We send a
	// params field that is JSON-valid at the outer layer (so it survives
	// the request decoder) but invalid for the per-method params struct
	// (a non-object where an object is expected, or wrong field type).
	cases := []struct {
		method string
		params json.RawMessage
	}{
		{"dfmt.remember", json.RawMessage(`"not-an-object"`)},
		{"dfmt.search", json.RawMessage(`{"limit":"not-an-int"}`)},
		{"dfmt.recall", json.RawMessage(`12345`)},
		{"dfmt.stats", json.RawMessage(`"oops"`)},
		{"dfmt.exec", json.RawMessage(`["array","instead","of","object"]`)},
		{"dfmt.read", json.RawMessage(`{"offset":"not-a-number"}`)},
		{"dfmt.fetch", json.RawMessage(`{"timeout":{}}`)},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			body, err := json.Marshal(Request{
				JSONRPC: "2.0",
				Method:  c.method,
				Params:  c.params,
				ID:      1,
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			hs.handle(rec, req)

			var resp Response
			if uerr := json.Unmarshal(rec.Body.Bytes(), &resp); uerr != nil {
				t.Fatalf("unmarshal response: %v (body=%s)", uerr, rec.Body.String())
			}
			if resp.Error == nil {
				t.Fatalf("expected JSON-RPC error for malformed params, got result=%v", resp.Result)
			}
			if resp.Error.Code != -32602 {
				t.Errorf("expected code -32602 (Invalid params), got %d (msg=%q)", resp.Error.Code, resp.Error.Message)
			}
		})
	}
}

// TestHandleEmptyParamsAccepted: empty/absent params must NOT produce -32602.
// Each method has nullable/optional fields, so a zero-value struct is a valid
// request shape. This pins the boundary so a future change to decodeRPCParams
// doesn't accidentally start rejecting empty params.
func TestHandleEmptyParamsAccepted(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	for _, method := range []string{"dfmt.search", "dfmt.recall", "dfmt.stats"} {
		t.Run(method, func(t *testing.T) {
			body, _ := json.Marshal(Request{
				JSONRPC: "2.0",
				Method:  method,
				ID:      1,
				// Params field omitted (zero-value json.RawMessage)
			})
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			hs.handle(rec, req)

			var resp Response
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if resp.Error != nil && resp.Error.Code == -32602 {
				t.Errorf("empty params should not produce -32602; got err=%v", resp.Error)
			}
		})
	}
}

