package transport

import (
	"context"
	"strings"
	"testing"
)

// TestHandleInitialize_NoClientInfo: when params are absent the
// sessionID stays a bare ULID — no client-name prefix to scoop into
// telemetry. Result still carries the negotiated protocol version
// and server info.
func TestHandleInitialize_NoClientInfo(t *testing.T) {
	p := &MCPProtocol{backend: nil, sessionID: "ulid-base"}
	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  nil, // no params at all
		ID:      1,
	}
	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if p.sessionID != "ulid-base" {
		t.Errorf("sessionID mutated without clientInfo: %q", p.sessionID)
	}
}

// TestHandleInitialize_WithClientInfo: a clientInfo block prepends
// "name/version:" to the sessionID so the telemetry path can
// attribute stats per agent (claude-code, cursor, …).
func TestHandleInitialize_WithClientInfo(t *testing.T) {
	p := &MCPProtocol{backend: nil, sessionID: "01ABCDEF"}
	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  []byte(`{"clientInfo":{"name":"claude-code","version":"1.2.3"}}`),
		ID:      2,
	}
	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !strings.HasPrefix(p.sessionID, "claude-code/1.2.3:") {
		t.Errorf("sessionID prefix: want claude-code/1.2.3: prefix, got %q", p.sessionID)
	}
}

// TestHandleInitialize_ClientInfoNoVersion: only the name field is
// set; the prefix omits the slash + version.
func TestHandleInitialize_ClientInfoNoVersion(t *testing.T) {
	p := &MCPProtocol{backend: nil, sessionID: "01ABCDEF"}
	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  []byte(`{"clientInfo":{"name":"cursor"}}`),
		ID:      3,
	}
	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !strings.HasPrefix(p.sessionID, "cursor:") {
		t.Errorf("sessionID prefix: want cursor: prefix, got %q", p.sessionID)
	}
	if strings.Contains(p.sessionID, "/") {
		t.Errorf("sessionID should not contain / when version absent: %q", p.sessionID)
	}
}

// TestHandleInitialize_BadParams: malformed JSON in params doesn't
// trip the function — clientInfo is tolerated as best-effort and
// the response still goes through.
func TestHandleInitialize_BadParams(t *testing.T) {
	origID := "01ABCDEF"
	p := &MCPProtocol{backend: nil, sessionID: origID}
	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  []byte(`{not-json`),
		ID:      4,
	}
	resp, err := p.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("malformed clientInfo should not error: %+v", resp.Error)
	}
	if p.sessionID != origID {
		t.Errorf("sessionID mutated on parse failure: %q", p.sessionID)
	}
}
