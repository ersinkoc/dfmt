package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestMCPProtocolStampsProjectIDOnRemember verifies that an MCP Remember
// call from a subprocess pinned to /proj/A produces a journal event with
// Project = "/proj/A", even when the params struct itself omits the field.
// This is the load-bearing test for Phase 2 commit 2: the per-call ctx
// pid (set by handleToolsCall) must outrank h.project (the legacy default
// pin set via SetProject).
func TestMCPProtocolStampsProjectIDOnRemember(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	// Legacy default pin — different from the MCP-stamped value, so we
	// can prove the ctx pid wins.
	handlers.SetProject("/legacy/default")

	mcp := NewMCPProtocol(handlers)
	mcp.SetProjectID("/proj/A")

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note","source":"test"}}`),
		ID:      1,
	}
	resp, err := mcp.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error: %v", resp.Error)
	}
	if len(journal.events) != 1 {
		t.Fatalf("journal events: got %d, want 1", len(journal.events))
	}
	if got := journal.events[0].Project; got != "/proj/A" {
		t.Errorf("event.Project: got %q, want %q (ctx pid should outrank h.project)", got, "/proj/A")
	}
}

// TestMCPProtocolPerCallProjectIDOverridesPin checks that an explicit
// project_id in the params payload outranks the session-bound pin. This
// matters for the global daemon CLI clients that pass per-project values
// through to a daemon serving multiple projects.
func TestMCPProtocolPerCallProjectIDOverridesPin(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)
	mcp.SetProjectID("/proj/session-pinned")

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params: json.RawMessage(
			`{"name":"dfmt.remember","arguments":{"project_id":"/proj/per-call","type":"note","source":"test"}}`),
		ID: 1,
	}
	resp, err := mcp.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error: %v", resp.Error)
	}
	if len(journal.events) != 1 {
		t.Fatalf("journal events: got %d, want 1", len(journal.events))
	}
	if got := journal.events[0].Project; got != "/proj/per-call" {
		t.Errorf("event.Project: got %q, want %q (per-call params should outrank session pin)", got, "/proj/per-call")
	}
}

// TestMCPProtocolEmptyProjectIDFallsBack confirms the legacy fallback path:
// no SetProjectID, no per-call value → handlers' default project wins.
// This is the pre-Phase-2 single-project daemon contract; commit 2 must
// not break it.
func TestMCPProtocolEmptyProjectIDFallsBack(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	handlers.SetProject("/legacy/default")

	mcp := NewMCPProtocol(handlers)
	// No SetProjectID — projectID stays empty.

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note","source":"test"}}`),
		ID:      1,
	}
	resp, err := mcp.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error: %v", resp.Error)
	}
	if got := journal.events[0].Project; got != "/legacy/default" {
		t.Errorf("event.Project: got %q, want %q (handlers.project should be the fallback)", got, "/legacy/default")
	}
}
