package transport

import (
	"context"
	"encoding/json"
	"fmt"
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

// The dashboard and the read-only endpoints it calls have no token to
// present — they run in a browser. Gating them would break the dashboard
// without protecting anything that isn't already read-only.
func TestDashboardAndHealthStayOpen(t *testing.T) {
	base, _ := startAuthTestServer(t)

	for _, path := range []string{"/dashboard", "/healthz", "/api/stats"} {
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
