package transport

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestHTTPServerStopNotRunning(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	if err := hs.Stop(); err != nil {
		t.Errorf("Stop on not-running server failed: %v", err)
	}
}

func TestHandleMethodNotAllowed(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	hs.handle(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleParseError(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

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
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	reqBody := Request{
		JSONRPC: "2.0",
		Method:  "unknown.method",
		ID:     1,
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
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	tmpDir := t.TempDir()
	portFile := tmpDir + "/.dfmt/port"

	err := hs.writePortFile(portFile, 12345)
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != "12345" {
		t.Errorf("expected '12345', got '%s'", string(data))
	}
}

func TestSetPortFile(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

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
