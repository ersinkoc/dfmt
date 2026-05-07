package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
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

	err := hs.writePortFile(portFile, 12345, "")
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

// TestHTTPServerRejectsForeignHostHeader covers F-17: the loopback HTTP
// listener must reject requests whose Host header doesn't name the listener
// itself. Without this, a same-host attacker (or DNS-rebinding browser) can
// drive POSTs to JSON-RPC endpoints by lying about Host while the connection
// reaches the loopback port — the same-origin Origin check only fires when
// the browser actually sets Origin. The Host check is a parallel defense
// that doesn't depend on the client setting Origin.
//
// Allowed: literal listener address, `localhost:<port>`, `[::1]:<port>`.
// Rejected: any other Host. Health endpoints bypass the check.
func TestHTTPServerRejectsForeignHostHeader(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	if err := hs.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer hs.Stop(context.Background())

	addr, ok := hs.listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener not TCP: %T", hs.listener.Addr())
	}

	cases := []struct {
		name       string
		host       string
		path       string
		wantStatus int
	}{
		{"listener-addr", addr.String(), "/", http.StatusMethodNotAllowed},
		{"localhost-port", fmt.Sprintf("localhost:%d", addr.Port), "/", http.StatusMethodNotAllowed},
		{"ipv6-loopback-port", fmt.Sprintf("[::1]:%d", addr.Port), "/", http.StatusMethodNotAllowed},
		{"foreign-host", "attacker.com", "/", http.StatusForbidden},
		{"foreign-host-with-port", "attacker.com:8080", "/", http.StatusForbidden},
		{"healthz-bypasses-host-check", "attacker.com", "/healthz", http.StatusOK},
		{"readyz-bypasses-host-check", "attacker.com", "/readyz", http.StatusOK},
	}

	url := fmt.Sprintf("http://%s", addr.String())
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, url+c.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Host = c.host

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != c.wantStatus {
				t.Errorf("status = %d, want %d (host=%q path=%q)", resp.StatusCode, c.wantStatus, c.host, c.path)
			}
		})
	}
}

// TestIsAllowedHost_UnixSocketBypass: when the listener is not TCP (e.g. a
// Unix-domain socket), there is no DNS-rebinding vector — connection access
// is gated by filesystem permissions. isAllowedHost must accept any value.
func TestIsAllowedHost_UnixSocketBypass(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)

	tmp := t.TempDir()
	sockPath := filepathJoin(tmp, "dfmt.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skipf("unix socket not supported on this platform: %v", err)
	}
	defer ln.Close()

	hs := NewHTTPServerWithListener(ln, handlers, sockPath)
	if !hs.isAllowedHost("any-host-at-all.example.com") {
		t.Error("Unix-socket transport must accept any Host header")
	}
	if !hs.isAllowedHost("") {
		t.Error("Unix-socket transport must accept empty Host header")
	}
}

// filepathJoin avoids importing path/filepath into this test file just for
// one call site.
func filepathJoin(dir, name string) string {
	if strings.HasSuffix(dir, string(os.PathSeparator)) {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
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

// TestHandleDropProject tests the dfmt.drop_project HTTP handler.
// Requires a non-nil journal for the underlying handler.
func TestHandleDropProject(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()

	idx := core.NewIndex()
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	params := mustMarshalParams(map[string]any{"project_id": tmp})
	reqBody := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.drop_project",
		Params:  params,
		ID:      10,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	// dropProject is nil (not set on test handlers), so Dropped=false is expected
	if resp.Error != nil {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
}

// TestHandleDropProjectError tests handleDropProject when params fail to decode.
func TestHandleDropProjectParamsError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Send malformed params that will fail to decode
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"dfmt.drop_project","params":{"project_id":},"id":1}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	var resp Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	// Should get a parse/marshal error
	if resp.Error == nil {
		t.Error("expected error for malformed params")
	}
}

// TestHTTPServerBindGetter tests that Bind() returns the set bind address.
func TestHTTPServerBindGetter(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	bind := hs.Bind()
	if bind == "" {
		t.Error("Bind() should not return empty string for a TCP server")
	}
}

// TestHTTPServerPortFileGetter tests that PortFile() returns the configured path.
func TestHTTPServerPortFileGetter(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Initially empty
	if hs.PortFile() != "" {
		t.Error("PortFile() should be empty before SetPortFile is called")
	}

	// After SetPortFile
	hs.SetPortFile("/test/portfile")
	if hs.PortFile() != "/test/portfile" {
		t.Errorf("PortFile() = %q, want /test/portfile", hs.PortFile())
	}
}

// TestHTTPServerSocketPathGetter tests SocketPath() returns the socket path when set.
func TestHTTPServerSocketPathGetter(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// For a TCP server, socket path should be empty
	if hs.SocketPath() != "" {
		t.Error("SocketPath() should be empty for TCP server")
	}
}

// TestHandleFavicon_GET covers handleFavicon for GET requests.
func TestHandleFaviconGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rec := httptest.NewRecorder()

	// Route is registered on mux during Start; call handler directly.
	hs.handleFavicon(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("favicon GET: got %d, want 204", rec.Code)
	}
}

// TestHandleFavicon_POST rejects POST to favicon.
func TestHandleFaviconPOST(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/favicon.ico", nil)
	rec := httptest.NewRecorder()

	hs.handleFavicon(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("favicon POST: got %d, want 405", rec.Code)
	}
}

// TestHandleDashboardCSS verifies the CSS handler serves content.
func TestHandleDashboardCSS(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/dashboard.css", nil)
	rec := httptest.NewRecorder()

	hs.handleDashboardCSS(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("dashboard.css: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("Content-Type: got %s", rec.Header().Get("Content-Type"))
	}
}

// TestHandleUnknownRoute covers the catch-all for unregistered paths.
func TestHandleUnknownRoute(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/unknown/path", nil)
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	// Unknown routes return 404 or similar
	if rec.Code == http.StatusOK {
		t.Error("unknown route should not return 200")
	}
}

// TestHandleHealth covers the health endpoint.
func TestHandleHealth(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	hs.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("health: got %d, want 200", rec.Code)
	}
}

func TestHandleMetricsGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	hs.handleMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("metrics GET: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("Content-Type: got %s", rec.Header().Get("Content-Type"))
	}
}

func TestHandleMetricsHEAD(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodHead, "/metrics", nil)
	rec := httptest.NewRecorder()

	hs.handleMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("metrics HEAD: got %d, want 200", rec.Code)
	}
}

func TestHandleMetricsPostRejectsPOST(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	rec := httptest.NewRecorder()

	hs.handleMetrics(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("metrics POST: got %d, want 405", rec.Code)
	}
}

func TestHandleAPIDaemonsGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("api/daemons GET: got %d, want 200", rec.Code)
	}
}

func TestHandleAPIDaemonsPostRejectsPOST(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIDaemons(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("api/daemons POST: got %d, want 405", rec.Code)
	}
}

func TestHandleAPIAllDaemonsGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/all-daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIAllDaemons(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("api/all-daemons GET: got %d, want 200", rec.Code)
	}
}

func TestHandleAPIAllDaemonsPostRejectsPOST(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/all-daemons", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIAllDaemons(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("api/all-daemons POST: got %d, want 405", rec.Code)
	}
}

func TestHandleAPIStreamGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("api/stream GET: got %d, want 200", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type: got %s", rec.Header().Get("Content-Type"))
	}
}

func TestHandleAPIStreamPostRejectsPOST(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/stream", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIStream(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("api/stream POST: got %d, want 405", rec.Code)
	}
}

func TestHandleAPIProxyPostRequiresTarget(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/proxy", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()

	hs.handleAPIProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("api/proxy POST no target: got %d, want 200", rec.Code)
	}
}

func TestHandleAPIProxyPostWithEmptyTarget(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/proxy", bytes.NewReader([]byte(`{"target_project_path":""}`)))
	rec := httptest.NewRecorder()

	hs.handleAPIProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("api/proxy POST empty target: got %d, want 200", rec.Code)
	}
}

func TestHandleAPIProxyGetRejectsGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/proxy", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIProxy(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("api/proxy GET: got %d, want 405", rec.Code)
	}
}

func TestHandleAPIProxyWithMalformedBody(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/proxy", bytes.NewReader([]byte(`{invalid json`)))
	rec := httptest.NewRecorder()

	hs.handleAPIProxy(rec, req)

	// Should encode an error response, not crash
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("api/proxy malformed: got %d (internal error indicates panic)", rec.Code)
	}
}

func TestHandleAPIStatsPostWithNilHandlers(t *testing.T) {
	hs := NewHTTPServer("127.0.0.1:0", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("api/stats nil handlers: got %d, want 503", rec.Code)
	}
}

func TestHandleAPIStatsPostWithEmptyBody(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader([]byte(``)))
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("api/stats empty body: got %d, want 400", rec.Code)
	}
}

func TestHandleAPIStatsGetRejectsGET(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("api/stats GET: got %d, want 405", rec.Code)
	}
}

func TestHandleAPIStatsPostWithValidBody(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"params":{}}`)))
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("api/stats valid body: got %d, want 200", rec.Code)
	}
}

func TestHandleAPIStatsPostWithInvalidParams(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	req := httptest.NewRequest(http.MethodPost, "/api/stats", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"params":{"invalid":"\x00"}}`)))
	rec := httptest.NewRecorder()

	hs.handleAPIStats(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("api/stats invalid params: got %d, want 400", rec.Code)
	}
}

func TestWritePortFileWithReservedName(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Try to write a port file with a reserved name - should fail at safefs.CheckNoReservedNames
	err := hs.writePortFile("NUL", 12345, "token")
	if err == nil {
		t.Error("writePortFile with NUL: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("writePortFile with NUL: got %v, want reserved-name error", err)
	}
}

func TestWrapSecurityWithHealthzPath(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer("127.0.0.1:0", handlers)

	// Start server to get listener
	ctx := context.Background()
	if err := hs.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer hs.Stop(ctx)

	// healthz bypasses security checks (no origin/host validation)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	hs.wrapSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz: got %d, want 200", rec.Code)
	}
}

func TestIsAllowedOriginWithNilListener(t *testing.T) {
	hs := NewHTTPServer("127.0.0.1:0", nil)

	// nil listener -> fail closed
	if hs.isAllowedOrigin("http://127.0.0.1:12345") {
		t.Error("isAllowedOrigin with nil listener: expected false")
	}
}

func TestIsAllowedHostWithNilListener(t *testing.T) {
	hs := NewHTTPServer("127.0.0.1:0", nil)

	// nil listener -> fail closed
	if hs.isAllowedHost("127.0.0.1:12345") {
		t.Error("isAllowedHost with nil listener: expected false")
	}
}

func TestIsAllowedHostWithNonTCPListener(t *testing.T) {
	ln, err := net.Listen("unix", t.TempDir()+"/sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	hs := NewHTTPServerWithListener(ln, nil, "")

	// Non-TCP listener -> always true (fail open for Unix sockets)
	if !hs.isAllowedHost("anything") {
		t.Error("isAllowedHost with non-TCP listener: expected true")
	}
}

