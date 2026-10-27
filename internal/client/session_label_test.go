package client

import (
	"context"
	"testing"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestSessionForPrefersMCPSession: the MCP layer labels its session with
// the agent's clientInfo, but the proxy sent its own bare ULID, so the
// daemon never saw which agent made a call.
func TestSessionForPrefersMCPSession(t *testing.T) {
	c := &Client{sessionID: "own-ulid"}
	if got := c.sessionFor(context.Background()); got != "own-ulid" {
		t.Errorf("no MCP session: got %q, want the client's own", got)
	}
	ctx := transport.WithSessionID(context.Background(), "claude-code/2.1:01ABC")
	if got := c.sessionFor(ctx); got != "claude-code/2.1:01ABC" {
		t.Errorf("MCP session not forwarded: got %q", got)
	}
}
