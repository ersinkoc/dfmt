package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: the daemon generated a 32-byte bearer token, kept it in memory,
// wrote an empty string into the port file, and never checked the header —
// while clients dutifully sent `Authorization: Bearer`. On Windows the RPC
// transport is loopback TCP, which has no ACL, so "/" (exec, write, edit,
// fetch) was reachable by any process on the host.

// startAuthTestServer starts a server with a port file, which is what makes
// it mint and require a token, and returns the base URL plus the token from
// the file (the same place a real client reads it).
func startAuthTestServer(t *testing.T) (baseURL, token string) {
	t.Helper()
	portFile := filepath.Join(t.TempDir(), "port")

	srv := NewHTTPServer("127.0.0.1:0", NewHandlers(nil, nil, nil))
	srv.SetPortFile(portFile)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("read port file: %v", err)
	}
	var pf PortFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("parse port file: %v", err)
	}
	if pf.Token == "" {
		t.Fatal("port file carries no token: clients have no way to authenticate, " +
			"which is what made the whole mechanism dead code")
	}
	return fmt.Sprintf("http://127.0.0.1:%d", pf.Port), pf.Token
}

// postRPC POSTs a stats call to "/" and returns the status code. It returns
// the code rather than the response so the body is closed on every path.
func postRPC(t *testing.T, url, token string) int {
	t.Helper()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"stats","params":{}}`)
	req, err := http.NewRequest(http.MethodPost, url+"/", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestRPCEndpointRejectsMissingToken(t *testing.T) {
	base, _ := startAuthTestServer(t)

	status := postRPC(t, base, "")

	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: an unauthenticated caller reaches exec/write/edit otherwise", status)
	}
}

func TestRPCEndpointRejectsWrongToken(t *testing.T) {
	base, token := startAuthTestServer(t)

	status := postRPC(t, base, token+"x")

	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a token that is a prefix of the real one", status)
	}
}

func TestRPCEndpointAcceptsPortFileToken(t *testing.T) {
	base, token := startAuthTestServer(t)

	status := postRPC(t, base, token)

	if status == http.StatusUnauthorized {
		t.Fatal("status = 401 for the token published in the port file: real clients would be locked out")
	}
}

// Health probes stay open. The dashboard page answers without a session —
// with a page saying how to get one, as 401 — and its script is withheld.
func TestHealthStaysOpenDashboardLocked(t *testing.T) {
	base, _ := startAuthTestServer(t)

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get(base + "/dashboard")
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "dfmt dashboard") {
		t.Errorf("GET /dashboard without session = %d, want 401 with the login hint", resp.StatusCode)
	}
}

// TestAPIRoutesRejectMissingToken covers TRN-4: /api/* must require the
// bearer token just like "/", because it streams any project's journal.
func TestAPIRoutesRejectMissingToken(t *testing.T) {
	base, _ := startAuthTestServer(t)

	for _, path := range []string{"/api/stats", "/api/daemons"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without token = %d, want 401 (TRN-4)", path, resp.StatusCode)
		}
	}
}

// TestAPIRoutesAcceptPortFileToken verifies the token published in the port
// file unlocks the /api/* endpoints for non-browser clients.
func TestAPIRoutesAcceptPortFileToken(t *testing.T) {
	base, token := startAuthTestServer(t)

	req, err := http.NewRequest(http.MethodGet, base+"/api/stats", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("GET /api/stats with port-file token = 401, want reachable")
	}
}

// Regression: dashboard.js opened with the bearer token as a `const` and
// sat behind no check, so any local process — or any web page, through a
// cross-site <script src> that sends no Origin — could read the credential
// that guards exec and write on "/". And the live event log never worked:
// EventSource cannot set an Authorization header.
func TestDashboardJSDoesNotLeakToken(t *testing.T) {
	base, token := startAuthTestServer(t)

	resp, err := http.Get(base + "/dashboard.js")
	if err != nil {
		t.Fatalf("GET dashboard.js: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /dashboard.js without session = %d, want 401", resp.StatusCode)
	}
	if strings.Contains(string(body), token) {
		t.Fatal("dashboard.js served the bearer token to an unauthenticated request")
	}
}

// TestDashboardLoginSessionUnlocksAPIOnly walks the browser flow: the login
// link sets a session cookie and redirects the token out of the URL; the
// cookie then unlocks the dashboard, its script, /api/* and the SSE stream,
// but never "/" — a browser session can read the journal, not exec.
func TestDashboardLoginSessionUnlocksAPIOnly(t *testing.T) {
	base, token := startAuthTestServer(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	bad, err := noRedirect.Get(base + "/dashboard?token=wrong")
	if err != nil {
		t.Fatalf("GET bad login: %v", err)
	}
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized || len(bad.Cookies()) != 0 {
		t.Errorf("wrong login token = %d with %d cookies, want 401 and none", bad.StatusCode, len(bad.Cookies()))
	}

	login, err := noRedirect.Get(base + "/dashboard?token=" + token)
	if err != nil {
		t.Fatalf("GET login: %v", err)
	}
	_ = login.Body.Close()
	if login.StatusCode != http.StatusSeeOther || login.Header.Get("Location") != "/dashboard" {
		t.Fatalf("login = %d to %q, want 303 to /dashboard", login.StatusCode, login.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, c := range login.Cookies() {
		if c.Name == dashboardCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login set no session cookie")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie HttpOnly=%v SameSite=%v, want HttpOnly and Strict", session.HttpOnly, session.SameSite)
	}
	if session.Value == token {
		t.Error("session cookie is the bearer token itself; it must be derived")
	}

	get := func(method, path string) int {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(`{"jsonrpc":"2.0","method":"dfmt.stats","id":1}`))
		if err != nil {
			t.Fatalf("build %s: %v", path, err)
		}
		req.AddCookie(session)
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for _, path := range []string{"/dashboard", "/dashboard.js", "/api/daemons"} {
		if code := get(http.MethodGet, path); code != http.StatusOK {
			t.Errorf("GET %s with session = %d, want 200", path, code)
		}
	}
	if code := get(http.MethodPost, "/"); code != http.StatusUnauthorized {
		t.Errorf("POST / with only the dashboard session = %d, want 401", code)
	}
}

// A server with no port file (tests, embedded use, and the Unix socket
// transport whose access control is the socket's file mode) mints no token
// and must not start refusing calls.
func TestRPCEndpointOpenWithoutPortFile(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:0", NewHandlers(nil, nil, nil))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	status := postRPC(t, "http://"+srv.listener.Addr().String(), "")

	if status == http.StatusUnauthorized {
		t.Error("status = 401 on a server that never minted a token")
	}
}
