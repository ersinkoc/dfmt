package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestResolveSessionID_EnvOverride verifies DFMT_SESSION env var takes
// precedence over the auto-generated ULID. Lets a shell loop share a
// dedup bucket across multiple `dfmt` commands without code changes.
func TestResolveSessionID_EnvOverride(t *testing.T) {
	t.Setenv("DFMT_SESSION", "my-shell-session-42")
	got := resolveSessionID()
	if got != "my-shell-session-42" {
		t.Errorf("resolveSessionID() = %q, want my-shell-session-42", got)
	}
}

// TestResolveSessionID_FallbackULID verifies that without DFMT_SESSION the
// fallback is a ULID — non-empty, with two successive calls differing.
// DFMT's ULID is 32-char hex (see internal/core/ulid.go); we don't pin
// the length here to stay decoupled from that format choice.
func TestResolveSessionID_FallbackULID(t *testing.T) {
	t.Setenv("DFMT_SESSION", "")
	a := resolveSessionID()
	b := resolveSessionID()
	if a == "" || b == "" {
		t.Fatalf("expected non-empty IDs; got a=%q b=%q", a, b)
	}
	if a == b {
		t.Errorf("two ULIDs should differ; got identical %q", a)
	}
}

// TestDoHTTP_SendsSessionHeader is the core regression: the X-DFMT-Session
// header MUST appear on every outbound HTTP request. Without this assertion
// a future refactor that reshuffles doHTTP could silently drop the header
// and the daemon would treat every CLI call as its own session — defeating
// the optimization.
func TestDoHTTP_SendsSessionHeader(t *testing.T) {
	var capturedHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-DFMT-Session")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	c := &Client{
		network:   "tcp",
		address:   u.Host,
		timeout:   2 * time.Second,
		sessionID: "test-session-zzz",
	}

	if _, err := c.doHTTP("/dfmt.stats", transport.Request{JSONRPC: "2.0", Method: "dfmt.stats", ID: 1}); err != nil {
		t.Fatalf("doHTTP: %v", err)
	}
	if capturedHeader != "test-session-zzz" {
		t.Errorf("X-DFMT-Session header on the wire = %q, want test-session-zzz", capturedHeader)
	}
}

// TestDoHTTP_OmitsHeaderWhenSessionEmpty: the header is conditional. When
// sessionID is unset (e.g. an old client that hasn't been migrated, or a
// test stub), no header is sent. The daemon then mints a per-request ULID
// itself (its own fallback) — total session entropy preserved.
func TestDoHTTP_OmitsHeaderWhenSessionEmpty(t *testing.T) {
	var headerPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, headerPresent = r.Header["X-Dfmt-Session"]
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	c := &Client{
		network:   "tcp",
		address:   u.Host,
		timeout:   2 * time.Second,
		sessionID: "",
	}

	if _, err := c.doHTTP("/dfmt.stats", transport.Request{JSONRPC: "2.0", Method: "dfmt.stats", ID: 1}); err != nil {
		t.Fatalf("doHTTP: %v", err)
	}
	if headerPresent {
		t.Error("empty sessionID must not send X-DFMT-Session header")
	}
}

// guard against the canonical-header-name surprise: Go canonicalizes
// "X-DFMT-Session" to "X-Dfmt-Session" in net/http. The map check above
// uses the canonical form deliberately. If a future refactor switched to
// http.Header.Get (which is canonicalized lookup), this comment explains
// why the raw map access is the right shape for the absence check.
var _ = strings.ToLower
