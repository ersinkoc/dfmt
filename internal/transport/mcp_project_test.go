package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	// An explicit project_id must name a real project root (issue #2
	// guard), so the per-call target is a temp dir with .dfmt/.
	perCall := newProjectRoot(t)

	args, _ := json.Marshal(map[string]any{"project_id": perCall, "type": "note", "source": "test"})
	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":` + string(args) + `}`),
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
	if got := journal.events[0].Project; got != perCall {
		t.Errorf("event.Project: got %q, want %q (per-call params should outrank session pin)", got, perCall)
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

func newProjectRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if err := os.Mkdir(filepath.Join(dir, ".dfmt"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestMCPRejectsUnsafeProjectID: the daemon creates .dfmt/ in whatever
// directory a project_id names and makes it the sandbox root, so a
// model-supplied value must be a real project root — never the filesystem
// root, the home directory, a relative path, or an arbitrary directory.
func TestMCPRejectsUnsafeProjectID(t *testing.T) {
	journal := &mockJournal{}
	mcp := NewMCPProtocol(NewHandlers(core.NewIndex(), journal, nil))
	mcp.SetProjectID("/proj/pinned")
	home, _ := os.UserHomeDir()
	for _, bad := range []string{"/", home, "relative/dir", t.TempDir(), "/definitely/missing/dfmt"} {
		resp := callTool(t, mcp, methodRemember, map[string]any{"project_id": bad, "type": "note"})
		res, ok := resp.Result.(MCPCallToolResult)
		if !ok || !res.IsError {
			t.Errorf("project_id %q accepted: %+v", bad, resp)
		}
	}
	if len(journal.events) != 0 {
		t.Errorf("a rejected call still wrote %d events", len(journal.events))
	}
}

// TestUnboundSessionAdvertisesAndRequiresProjectID: a session started with
// no project (Windsurf's single global config) could serve nothing, and the
// schema gave the model no way to name a project. It now advertises a
// required project_id; a bound session's tools/list is unchanged.
func TestUnboundSessionAdvertisesAndRequiresProjectID(t *testing.T) {
	journal := &mockJournal{}
	unbound := NewMCPProtocol(NewHandlers(core.NewIndex(), journal, nil))
	unbound.SetUnbound()

	listed := func(m *MCPProtocol) string {
		resp, _ := m.handleToolsList(&MCPRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "tools/list"})
		raw, _ := json.Marshal(resp.Result)
		return string(raw)
	}
	if got := listed(unbound); strings.Count(got, `"project_id"`) < 2*11 { // property + required, 11 tools
		t.Errorf("unbound tools/list does not advertise project_id on every tool")
	}
	if got := listed(NewMCPProtocol(nil)); strings.Contains(got, "project_id") {
		t.Error("bound session pays for project_id in tools/list")
	}

	resp := callTool(t, unbound, methodRemember, map[string]any{"type": "note"})
	if res, ok := resp.Result.(MCPCallToolResult); !ok || !res.IsError ||
		!strings.Contains(res.Content[0].Text, "project_id") {
		t.Errorf("unbound call without project_id: %+v", resp)
	}

	root := newProjectRoot(t)
	resp = callTool(t, unbound, methodRemember, map[string]any{"type": "note", "project_id": root})
	if resp.Error != nil || len(journal.events) != 1 || journal.events[0].Project != root {
		t.Errorf("unbound call with project_id not routed: resp=%+v events=%+v", resp, journal.events)
	}
}
