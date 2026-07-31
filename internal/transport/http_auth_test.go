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

// The dashboard pages and health probes stay open — they are served from the
// same origin and carry no token. But the /api/* data endpoints now require
// the bearer token (TRN-4): they stream the full journal of any project
// named in the query string, including tool.exec code fields and remembered
// notes, and on Windows loopback TCP has no ACL.
func TestDashboardAndHealthStayOpen(t *testing.T) {
	base, _ := startAuthTestServer(t)

	for _, path := range []string{"/dashboard", "/healthz"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("GET %s = 401, want it reachable without a token", path)
		}
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
// file unlocks the /api/* endpoints (the dashboard receives it injected
// into its served JS).
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

// TestDashboardJSServesInjectedToken verifies the dashboard JS carries the
// bearer token when the server is in port-file mode, so the dashboard's
// /api/* fetches can authenticate (TRN-4).
func TestDashboardJSServesInjectedToken(t *testing.T) {
	base, _ := startAuthTestServer(t)

	resp, err := http.Get(base + "/dashboard.js")
	if err != nil {
		t.Fatalf("GET dashboard.js: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	jsBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read dashboard.js: %v", err)
	}
	js := string(jsBytes)
	want := "DFMT_AUTH_TOKEN"
	if !strings.Contains(js, want) {
		t.Fatalf("dashboard.js does not contain injected %q — the dashboard cannot authenticate /api/* calls", want)
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
