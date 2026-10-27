package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{network: "tcp", address: u.Host, timeout: 30 * time.Second}
}

// LIF-5: canceling the caller's context aborts the RPC. The context used
// to be dropped, so the call ran until the HTTP client's own timeout.
func TestRPCHonorsContextCancel(t *testing.T) {
	release := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) }) // before srv.Close (LIFO)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Exec(ctx, transport.ExecParams{Code: "sleep 100", Timeout: 120})
	if err == nil {
		t.Fatal("Exec returned nil error after its context expired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Exec took %v after a 200ms context; cancellation ignored", d)
	}
}

// LIF-9: a non-JSON HTTP failure names its cause instead of surfacing as
// a JSON decode error.
func TestRPCStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want string
	}{
		{http.StatusUnauthorized, "unauthorized: present the token\n", "rejected the auth token"},
		{http.StatusNotFound, "404 page not found\n", "older dfmt"},
		{http.StatusInternalServerError, "<html>boom</html>", "HTTP 500: <html>boom</html>"},
	} {
		c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = io.WriteString(w, tc.body)
		})
		_, err := c.Search(context.Background(), transport.SearchParams{Query: "x"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("HTTP %d: err = %v, want it to mention %q", tc.code, err, tc.want)
		}
		if tc.code == http.StatusUnauthorized && !errors.Is(err, ErrUnauthorized) {
			t.Errorf("HTTP 401: err = %v, want ErrUnauthorized", err)
		}
	}
}

// A JSON-RPC error the daemon sends with a 4xx status keeps its message.
func TestRPCStatusPassesJSONRPCErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params: bad"}}`)
	})
	_, err := c.Search(context.Background(), transport.SearchParams{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "Invalid params: bad") {
		t.Errorf("err = %v, want the JSON-RPC error message", err)
	}
}
