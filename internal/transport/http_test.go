package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestHTTPHandle(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	hs := NewHTTPServer(":0", handlers)

	// Test GET request - should return method not allowed
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestHTTPHandlePOSTInvalidJSON(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	// Test with invalid JSON body
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("{invalid}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	// Note: handle() doesn't set HTTP status code for JSON parse errors,
	// it just writes the JSON-RPC error response directly
	var resp Response
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Error should not be nil")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("Error.Code = %d, want -32700", resp.Error.Code)
	}
}

func TestHTTPHandleUnknownMethod(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	body, _ := json.Marshal(Request{
		JSONRPC: "2.0",
		Method:  "unknown.method",
		ID:      1,
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	var resp Response
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Error should not be nil for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("Error.Code = %d, want -32601", resp.Error.Code)
	}
}

func TestHTTPHandleRememberSuccess(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	hs := NewHTTPServer(":0", handlers)

	body, _ := json.Marshal(Request{
		JSONRPC: "2.0",
		Method:  "dfmt.remember",
		Params:  json.RawMessage(`{"type":"note","source":"test"}`),
		ID:      1,
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	var resp Response
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
	// JSON unmarshals numbers as float64, so compare as interface{}
	if fmt.Sprintf("%v", resp.ID) != "1" {
		t.Errorf("ID = %v, want 1", resp.ID)
	}
}

func TestHTTPHandleSearchSuccess(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	body, _ := json.Marshal(Request{
		JSONRPC: "2.0",
		Method:  "dfmt.search",
		Params:  json.RawMessage(`{"query":"test"}`),
		ID:      1,
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	var resp Response
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
}

func TestHTTPHandleRecallSuccess(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	hs := NewHTTPServer(":0", handlers)

	body, _ := json.Marshal(Request{
		JSONRPC: "2.0",
		Method:  "dfmt.recall",
		Params:  json.RawMessage(`{"budget":1024}`),
		ID:      1,
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	var resp Response
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
}

func TestHTTPHandleWithSessionID(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	hs := NewHTTPServer(":0", handlers)

	body, _ := json.Marshal(Request{
		JSONRPC: "2.0",
		Method:  "dfmt.remember",
		Params:  json.RawMessage(`{"type":"note","source":"test"}`),
		ID:      1,
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-DFMT-Session", "my-session-123")
	rr := httptest.NewRecorder()
	hs.handle(rr, req)

	// Should still succeed with session header
	var resp Response
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
}

func TestHTTPServerStartAndStop(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping network tests on Windows")
	}

	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := hs.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	if err := hs.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestSocketServerStartAndStop(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir := t.TempDir()
	socketPath := tmpDir + "/test.sock"

	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	ss := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := ss.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	if err := ss.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestSocketServerStartAddressInUse(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix socket tests on Windows")
	}

	tmpDir := t.TempDir()
	socketPath := tmpDir + "/test.sock"

	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	ss1 := NewSocketServer(socketPath, handlers)
	ss2 := NewSocketServer(socketPath, handlers)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := ss1.Start(ctx); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}

	// Second server on same path should fail
	err := ss2.Start(ctx)
	if err == nil {
		t.Error("Second Start should fail for same socket path")
	}

	ss1.Stop(ctx)
}

func TestSocketServerDispatchWithNilParams(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  nil,
		ID:      1,
	}

	result, err := ss.dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}
