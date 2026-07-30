package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// slowHandlerDuration is comfortably past the 30s write deadline this file
// exists to keep out of the server. It has to be a real wall-clock wait:
// http.Server's write deadline is absolute from the moment request headers
// are read, so nothing shorter can distinguish "no deadline" from "a deadline
// we have not reached yet".
const slowHandlerDuration = 35 * time.Second

// TestHTTPServerHasNoWriteDeadline is the cheap always-on guard. The
// behavioral proof below takes 35 seconds and is skipped under -short, so
// this assertion is what runs on every quick pass.
//
// Regression: WriteTimeout was 30s, which capped every tool call at 30
// seconds regardless of the timeout the agent asked for (dfmt_exec's schema
// advertises 60s by default and the sandbox allows up to MaxExecTimeout).
// Past the cap the server closed the connection and the agent saw a bare
// EOF while the command kept running server-side. The same deadline killed
// /api/stream (SSE) every 30 seconds.
func TestHTTPServerHasNoWriteDeadline(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:0", NewHandlers(nil, nil, nil))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Stop(context.Background()) }()

	if got := srv.server.WriteTimeout; got != 0 {
		t.Errorf("WriteTimeout = %s, want 0 — any non-zero value is a hard ceiling on "+
			"tool-call duration, below the %s the sandbox itself allows", got, maxExecTimeoutForTest)
	}
	// The protections that must NOT have been removed along with it.
	if srv.server.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout = 0: the Slowloris guard is gone")
	}
	if srv.server.IdleTimeout == 0 {
		t.Error("IdleTimeout = 0: idle keep-alive connections are no longer reaped")
	}
}

// maxExecTimeoutForTest mirrors sandbox.MaxExecTimeout. Duplicated rather
// than imported because internal/transport does not otherwise need the
// sandbox package's constant here, and the value only appears in a message.
const maxExecTimeoutForTest = 300 * time.Second

// TestHTTPServerServesRequestLongerThanOldDeadline is the behavioral proof:
// a request whose handler runs past the old 30s ceiling must return its
// result, not an EOF.
func TestHTTPServerServesRequestLongerThanOldDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 35s by construction: the deadline being tested is 30s")
	}

	handlers := NewHandlers(nil, nil, nil)
	srv := NewHTTPServer("127.0.0.1:0", handlers)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Stop(context.Background()) }()

	// Swap in a handler that just sleeps. Going through the real exec path
	// would need a shell and would test the sandbox, not the server deadline.
	srv.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(slowHandlerDuration)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "slow-but-done"})
	})

	addr := srv.listener.Addr().String()
	client := &http.Client{Timeout: slowHandlerDuration + 30*time.Second}
	start := time.Now()
	resp, err := client.Post(fmt.Sprintf("http://%s/", addr), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST after %s: %v (a write deadline shorter than the handler shows up here as EOF)",
			time.Since(start).Round(time.Second), err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "slow-but-done" {
		t.Errorf("body = %v, want the handler's own response", body)
	}
}
