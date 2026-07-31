package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/timeouts"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// The client's entire RPC surface — 13 methods on *Client plus the 12
// ClientBackend delegations — was completely uncovered, which is why
// internal/client sat at 41.7% while every other package cleared its
// threshold. internal/client is not in the coverage thresholds documented in
// CLAUDE.md, so nothing flagged it.
//
// These tests stand up a fake daemon over httptest and drive each method
// through the real HTTP path, so they exercise request construction, the
// JSON-RPC envelope, result unwrapping, and error propagation — not just
// line presence.

// fakeDaemon serves JSON-RPC over HTTP the way the real daemon does.
// result is echoed back inside transport.Response.Result for whatever
// method the client asked for; rpcErr, when set, is returned instead.
type fakeDaemon struct {
	srv *httptest.Server

	mu       sync.Mutex
	lastPath string
	lastReq  transport.Request
	lastAuth string

	result any
	rpcErr *transport.RPCError
	status int
	raw    string // when set, returned verbatim (for malformed-body tests)
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	f := &fakeDaemon{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastPath = r.URL.Path
		f.lastAuth = r.Header.Get("Authorization")
		var req transport.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastReq = req
		result, rpcErr, status, raw := f.result, f.rpcErr, f.status, f.raw
		f.mu.Unlock()

		// SSE stream endpoint gets its own shape.
		if strings.HasPrefix(r.URL.Path, "/api/stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"01\",\"type\":\"tool.exec\"}\n\n"))
			return
		}

		w.WriteHeader(status)
		if raw != "" {
			_, _ = w.Write([]byte(raw))
			return
		}
		resp := transport.Response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// client returns a *Client wired to the fake daemon.
func (f *fakeDaemon) client(projectID string) *Client {
	return &Client{
		network:   "tcp",
		address:   strings.TrimPrefix(f.srv.URL, "http://"),
		timeout:   timeouts.RPC,
		sessionID: "test-session",
		projectID: projectID,
	}
}

func (f *fakeDaemon) setResult(v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result, f.rpcErr, f.raw = v, nil, ""
}

func (f *fakeDaemon) setRPCError(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result, f.raw = nil, ""
	f.rpcErr = &transport.RPCError{Code: -32603, Message: msg}
}

func (f *fakeDaemon) request() transport.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq
}

func (f *fakeDaemon) path() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPath
}

// TestClientRPCSurfaceHappyPath drives every JSON-RPC method and asserts the
// wire method name and that the result is unwrapped into the typed response.
func TestClientRPCSurfaceHappyPath(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		wireMethod string
		result     any
		call       func(*Client) (any, error)
		verify     func(*testing.T, any)
	}{
		{
			name: "Exec", wireMethod: "exec",
			result: transport.ExecResponse{Exit: 3, Stdout: "out", Stderr: "err"},
			call: func(c *Client) (any, error) {
				return c.Exec(ctx, transport.ExecParams{Code: "echo hi"})
			},
			verify: func(t *testing.T, got any) {
				r := got.(*transport.ExecResponse)
				if r.Exit != 3 || r.Stdout != "out" || r.Stderr != "err" {
					t.Errorf("ExecResponse = %+v, want exit 3 / out / err", r)
				}
			},
		},
		{
			name: "Read", wireMethod: "read",
			result: transport.ReadResponse{Content: "file body"},
			call: func(c *Client) (any, error) {
				return c.Read(ctx, transport.ReadParams{Path: "main.go"})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.ReadResponse); r.Content != "file body" {
					t.Errorf("Content = %q", r.Content)
				}
			},
		},
		{
			name: "Fetch", wireMethod: "fetch",
			result: transport.FetchResponse{Status: 200},
			call: func(c *Client) (any, error) {
				return c.Fetch(ctx, transport.FetchParams{URL: "https://example.com"})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.FetchResponse); r.Status != 200 {
					t.Errorf("Status = %d", r.Status)
				}
			},
		},
		{
			name: "Glob", wireMethod: "glob",
			result: transport.GlobResponse{Files: []string{"a.go", "b.go"}},
			call: func(c *Client) (any, error) {
				return c.Glob(ctx, transport.GlobParams{Pattern: "**/*.go"})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.GlobResponse); len(r.Files) != 2 {
					t.Errorf("Files = %v", r.Files)
				}
			},
		},
		{
			name: "Grep", wireMethod: "grep",
			result: transport.GrepResponse{},
			call: func(c *Client) (any, error) {
				return c.Grep(ctx, transport.GrepParams{Pattern: "func"})
			},
			verify: func(t *testing.T, got any) {
				if got.(*transport.GrepResponse) == nil {
					t.Error("nil GrepResponse")
				}
			},
		},
		{
			name: "Edit", wireMethod: "edit",
			result: transport.EditResponse{Success: true, Summary: "1 replacement"},
			call: func(c *Client) (any, error) {
				return c.Edit(ctx, transport.EditParams{Path: "f.go", OldString: "a", NewString: "b"})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.EditResponse); !r.Success {
					t.Error("Success = false")
				}
			},
		},
		{
			name: "Write", wireMethod: "write",
			result: transport.WriteResponse{Success: true},
			call: func(c *Client) (any, error) {
				return c.Write(ctx, transport.WriteParams{Path: "f.go", Content: "x"})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.WriteResponse); !r.Success {
					t.Error("Success = false")
				}
			},
		},
		{
			name: "Remember", wireMethod: "remember",
			result: transport.RememberResponse{ID: "01ABC", TS: "2026-07-29T00:00:00Z"},
			call: func(c *Client) (any, error) {
				return c.Remember(ctx, transport.RememberParams{Type: "decision"})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.RememberResponse); r.ID != "01ABC" {
					t.Errorf("ID = %q", r.ID)
				}
			},
		},
		{
			name: "Search", wireMethod: "search",
			result: transport.SearchResponse{},
			call: func(c *Client) (any, error) {
				return c.Search(ctx, transport.SearchParams{Query: "daemon"})
			},
			verify: func(t *testing.T, got any) {
				if got.(*transport.SearchResponse) == nil {
					t.Error("nil SearchResponse")
				}
			},
		},
		{
			name: "Recall", wireMethod: "recall",
			result: transport.RecallResponse{Snapshot: "# Session", Format: "md"},
			call: func(c *Client) (any, error) {
				return c.Recall(ctx, transport.RecallParams{})
			},
			verify: func(t *testing.T, got any) {
				if r := got.(*transport.RecallResponse); r.Snapshot != "# Session" {
					t.Errorf("Snapshot = %q", r.Snapshot)
				}
			},
		},
		{
			name: "DropProject", wireMethod: "drop_project",
			result: transport.DropProjectResponse{},
			call: func(c *Client) (any, error) {
				return c.DropProject(ctx, "D:\\proj")
			},
			verify: func(t *testing.T, got any) {
				if got.(*transport.DropProjectResponse) == nil {
					t.Error("nil DropProjectResponse")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.setResult(tc.result)
			c := f.client("D:\\proj")

			got, err := tc.call(c)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if m := f.request().Method; m != tc.wireMethod {
				t.Errorf("wire method = %q, want %q", m, tc.wireMethod)
			}
			tc.verify(t, got)
		})
	}
}

// Stats uses a different endpoint from the other RPCs; pin it so a refactor
// that unifies the paths cannot silently break the dashboard's data source.
func TestClientStatsUsesStatsEndpoint(t *testing.T) {
	f := newFakeDaemon(t)
	f.setResult(transport.StatsResponse{EventsTotal: 42})
	c := f.client("D:\\proj")

	resp, err := c.Stats(context.Background(), transport.StatsParams{})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if resp.EventsTotal != 42 {
		t.Errorf("EventsTotal = %d, want 42", resp.EventsTotal)
	}
	if got := f.path(); got != "/api/stats" {
		t.Errorf("path = %q, want /api/stats", got)
	}
}

// A daemon-side JSON-RPC error must surface as a Go error, not as a
// zero-valued success. Checked on one representative method per response
// shape rather than all thirteen — the unwrap code is identical.
func TestClientPropagatesRPCErrors(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*Client) error{
		"Exec":   func(c *Client) error { _, err := c.Exec(ctx, transport.ExecParams{Code: "x"}); return err },
		"Read":   func(c *Client) error { _, err := c.Read(ctx, transport.ReadParams{Path: "p"}); return err },
		"Stats":  func(c *Client) error { _, err := c.Stats(ctx, transport.StatsParams{}); return err },
		"Search": func(c *Client) error { _, err := c.Search(ctx, transport.SearchParams{Query: "q"}); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.setRPCError("policy denied")
			if err := call(f.client("D:\\proj")); err == nil {
				t.Fatal("nil error for an RPC error response")
			} else if !strings.Contains(err.Error(), "policy denied") {
				t.Errorf("err = %v, want it to carry the daemon's message", err)
			}
		})
	}
}

// The client stamps its own projectID when the caller leaves it empty —
// this is what lets one daemon route calls from many projects.
func TestClientStampsProjectID(t *testing.T) {
	f := newFakeDaemon(t)
	f.setResult(transport.ExecResponse{})
	c := f.client("D:\\my-project")

	if _, err := c.Exec(context.Background(), transport.ExecParams{Code: "x"}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var sent transport.ExecParams
	if err := json.Unmarshal(f.request().Params, &sent); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if sent.ProjectID != "D:\\my-project" {
		t.Errorf("ProjectID = %q, want the client's own project", sent.ProjectID)
	}
}

// An explicit ProjectID from the caller must win over the client default,
// so a single client can address another project when asked.
func TestClientKeepsExplicitProjectID(t *testing.T) {
	f := newFakeDaemon(t)
	f.setResult(transport.ExecResponse{})
	c := f.client("D:\\default")

	_, err := c.Exec(context.Background(), transport.ExecParams{Code: "x", ProjectID: "D:\\target"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var sent transport.ExecParams
	_ = json.Unmarshal(f.request().Params, &sent)
	if sent.ProjectID != "D:\\target" {
		t.Errorf("ProjectID = %q, want the caller's explicit value", sent.ProjectID)
	}
}

func TestClientRejectsMalformedResponseBody(t *testing.T) {
	f := newFakeDaemon(t)
	f.mu.Lock()
	f.raw = "{not json"
	f.mu.Unlock()

	if _, err := f.client("D:\\p").Exec(context.Background(), transport.ExecParams{Code: "x"}); err == nil {
		t.Fatal("nil error for a malformed response body")
	}
}

func TestClientDropProjectRequiresPath(t *testing.T) {
	f := newFakeDaemon(t)
	if _, err := f.client("D:\\p").DropProject(context.Background(), ""); err == nil {
		t.Fatal("nil error for an empty project path")
	}
}

func TestClientStreamEventsDeliversEvents(t *testing.T) {
	f := newFakeDaemon(t)
	c := f.client("D:\\proj")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := c.StreamEvents(ctx, "")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before delivering an event")
		}
		if ev.Type != "tool.exec" {
			t.Errorf("event type = %q, want tool.exec", ev.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for a streamed event")
	}
}

// ── ClientBackend ───────────────────────────────────────────────────
//
// The adapter is twelve one-line delegations. They were entirely
// uncovered, so a method wired to the wrong underlying call would not have
// been caught. Driving each through the fake daemon proves the delegation
// by asserting the wire method name that arrives.

func TestClientBackendDelegatesEveryMethod(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		wireMethod string
		result     any
		call       func(*ClientBackend) error
	}{
		{"Exec", "exec", transport.ExecResponse{}, func(b *ClientBackend) error {
			_, err := b.Exec(ctx, transport.ExecParams{Code: "x"})
			return err
		}},
		{"Read", "read", transport.ReadResponse{}, func(b *ClientBackend) error {
			_, err := b.Read(ctx, transport.ReadParams{Path: "p"})
			return err
		}},
		{"Fetch", "fetch", transport.FetchResponse{}, func(b *ClientBackend) error {
			_, err := b.Fetch(ctx, transport.FetchParams{URL: "https://e.com"})
			return err
		}},
		{"Glob", "glob", transport.GlobResponse{}, func(b *ClientBackend) error {
			_, err := b.Glob(ctx, transport.GlobParams{Pattern: "*"})
			return err
		}},
		{"Grep", "grep", transport.GrepResponse{}, func(b *ClientBackend) error {
			_, err := b.Grep(ctx, transport.GrepParams{Pattern: "x"})
			return err
		}},
		{"Edit", "edit", transport.EditResponse{}, func(b *ClientBackend) error {
			_, err := b.Edit(ctx, transport.EditParams{Path: "f", OldString: "a"})
			return err
		}},
		{"Write", "write", transport.WriteResponse{}, func(b *ClientBackend) error {
			_, err := b.Write(ctx, transport.WriteParams{Path: "f"})
			return err
		}},
		{"Remember", "remember", transport.RememberResponse{}, func(b *ClientBackend) error {
			_, err := b.Remember(ctx, transport.RememberParams{Type: "note"})
			return err
		}},
		{"Search", "search", transport.SearchResponse{}, func(b *ClientBackend) error {
			_, err := b.Search(ctx, transport.SearchParams{Query: "q"})
			return err
		}},
		{"Recall", "recall", transport.RecallResponse{}, func(b *ClientBackend) error {
			_, err := b.Recall(ctx, transport.RecallParams{})
			return err
		}},
		{"Stats", "stats", transport.StatsResponse{}, func(b *ClientBackend) error {
			_, err := b.Stats(ctx, transport.StatsParams{})
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.setResult(tc.result)
			b := NewBackend(f.client("D:\\proj"))

			if err := tc.call(b); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if m := f.request().Method; m != tc.wireMethod {
				t.Errorf("wire method = %q, want %q — the adapter delegates to the wrong call",
					m, tc.wireMethod)
			}
		})
	}
}

func TestClientBackendStreamEventsDelegates(t *testing.T) {
	f := newFakeDaemon(t)
	b := NewBackend(f.client("D:\\proj"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := b.StreamEvents(ctx, "")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before delivering an event")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for a streamed event")
	}
}
