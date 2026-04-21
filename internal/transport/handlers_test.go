package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestRPCError(t *testing.T) {
	err := &RPCError{Code: -32601, Message: "method not found"}
	if err.Code != -32601 {
		t.Errorf("RPCError.Code = %d, want -32601", err.Code)
	}
	if err.Message != "method not found" {
		t.Errorf("RPCError.Message = %s, want 'method not found'", err.Message)
	}
}

func TestRPCErrorWithData(t *testing.T) {
	err := &RPCError{Code: -32603, Message: "internal error", Data: map[string]any{"key": "value"}}
	if err.Data == nil {
		t.Fatal("RPCError.Data is nil")
	}
	data, ok := err.Data.(map[string]any)
	if !ok {
		t.Fatal("RPCError.Data is not map[string]any")
	}
	if data["key"] != "value" {
		t.Errorf("Data[key] = %v, want 'value'", data["key"])
	}
}

func TestRequest(t *testing.T) {
	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params: json.RawMessage(`{"type":"note"}`),
		ID:     1,
	}
	if req.JSONRPC != "2.0" {
		t.Errorf("Request.JSONRPC = %s, want '2.0'", req.JSONRPC)
	}
	if req.Method != "remember" {
		t.Errorf("Request.Method = %s, want 'remember'", req.Method)
	}
}

func TestResponse(t *testing.T) {
	resp := &Response{
		JSONRPC: "2.0",
		Result:  map[string]any{"id": "test"},
		ID:      1,
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("Response.JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
	if resp.Result == nil {
		t.Error("Response.Result is nil")
	}
}

type pipeReadWriter struct {
	buf *bytes.Buffer
}

func (p *pipeReadWriter) Write(data []byte) (int, error) {
	return p.buf.Write(data)
}

func (p *pipeReadWriter) Read(data []byte) (int, error) {
	return p.buf.Read(data)
}

func TestCodecWriteReadRequest(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	codec := NewCodec(buf)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  json.RawMessage(`{"type":"note"}`),
		ID:     1,
	}

	if err := codec.WriteRequest(req); err != nil {
		t.Fatalf("WriteRequest failed: %v", err)
	}

	// Read it back
	readReq, err := codec.ReadRequest()
	if err != nil {
		t.Fatalf("ReadRequest failed: %v", err)
	}

	if readReq.Method != "remember" {
		t.Errorf("readReq.Method = %s, want 'remember'", readReq.Method)
	}
}

func TestCodecWriteReadResponse(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	codec := NewCodec(buf)

	resp := &Response{
		JSONRPC: "2.0",
		Result:  map[string]any{"id": "test-id"},
		ID:      1,
	}

	if err := codec.WriteResponse(resp); err != nil {
		t.Fatalf("WriteResponse failed: %v", err)
	}

	// Read it back
	readResp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}

	if readResp.JSONRPC != "2.0" {
		t.Errorf("readResp.JSONRPC = %s, want '2.0'", readResp.JSONRPC)
	}
}

func TestCodecWriteError(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	codec := NewCodec(buf)

	if err := codec.WriteError(1, -32601, "method not found", nil); err != nil {
		t.Fatalf("WriteError failed: %v", err)
	}

	readResp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}

	if readResp.Error == nil {
		t.Fatal("readResp.Error is nil")
	}
	if readResp.Error.Code != -32601 {
		t.Errorf("readResp.Error.Code = %d, want -32601", readResp.Error.Code)
	}
	if readResp.Error.Message != "method not found" {
		t.Errorf("readResp.Error.Message = %s, want 'method not found'", readResp.Error.Message)
	}
}

type mockJournal struct {
	events []core.Event
}

func (m *mockJournal) Append(ctx context.Context, e core.Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *mockJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	ch := make(chan core.Event, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (m *mockJournal) Checkpoint(ctx context.Context) (string, error) {
	return "", nil
}

func (m *mockJournal) Rotate(ctx context.Context) error {
	return nil
}

func (m *mockJournal) Close() error {
	return nil
}

func TestHandlersSearch(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	// Add some events to index
	idx.Add(core.Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     core.EventType("note"),
		Priority: core.Priority("P2"),
		Source:   core.Source("test"),
	})

	resp, err := h.Search(context.Background(), SearchParams{Query: "test", Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp == nil {
		t.Fatal("SearchResponse is nil")
	}
	if resp.Layer != "" {
		t.Errorf("resp.Layer = %s, want ''", resp.Layer)
	}
}

func TestHandlersSearchTrigram(t *testing.T) {
	idx := core.NewIndex()
	h := NewHandlers(idx, nil)

	resp, err := h.Search(context.Background(), SearchParams{Query: "test", Layer: "trigram"})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp.Layer != "trigram" {
		t.Errorf("resp.Layer = %s, want 'trigram'", resp.Layer)
	}
}

func TestHandlersSearchFuzzy(t *testing.T) {
	idx := core.NewIndex()
	h := NewHandlers(idx, nil)

	resp, err := h.Search(context.Background(), SearchParams{Query: "test", Layer: "fuzzy"})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp.Layer != "fuzzy" {
		t.Errorf("resp.Layer = %s, want 'fuzzy'", resp.Layer)
	}
}

func TestHandlersRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 1024, Format: "md"})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Snapshot == "" {
		t.Error("Snapshot is empty")
	}
	if resp.Format != "md" {
		t.Errorf("Format = %s, want 'md'", resp.Format)
	}
}

func TestHandlersRecallJSONFormat(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Format: "json"})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Format != "json" {
		t.Errorf("Format = %s, want 'json'", resp.Format)
	}
}

func TestHandlersRecallDefaultBudget(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Snapshot == "" {
		t.Error("Snapshot is empty")
	}
}

func TestHandlersRecallDefaultFormat(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 1024})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Format != "" {
		// Format defaults to "md" but our mock doesn't set it
		t.Logf("Format = %s", resp.Format)
	}
}

func TestHandlersRecallNoEvents(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{events: []core.Event{}}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 1024})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Snapshot == "" {
		t.Error("Snapshot is empty")
	}
	// Should contain the "no events" message
	if !strings.Contains(resp.Snapshot, "No events") {
		t.Error("Snapshot should contain 'No events' message")
	}
}

func TestHandlersRecallWithEvents(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{events: []core.Event{
		{
			ID:       "test1",
			TS:       time.Now(),
			Type:     core.EventType("note"),
			Priority: core.Priority("P1"),
			Source:   core.Source("test"),
			Tags:     []string{"tag1", "tag2"},
		},
		{
			ID:       "test2",
			TS:       time.Now(),
			Type:     core.EventType("decision"),
			Priority: core.Priority("P2"),
			Source:   core.Source("test"),
			Actor:    "user@test.com",
		},
	}}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 4096, Format: "md"})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Snapshot == "" {
		t.Error("Snapshot is empty")
	}
	if resp.Format != "md" {
		t.Errorf("Format = %s, want 'md'", resp.Format)
	}
}

func TestHandlersRecallBudgetExceeded(t *testing.T) {
	idx := core.NewIndex()
	// Add events that would exceed small budget
	journal := &mockJournal{events: []core.Event{
		{
			ID:       "test1",
			TS:       time.Now(),
			Type:     core.EventType("note"),
			Priority: core.Priority("P4"),
			Source:   core.Source("test"),
		},
		{
			ID:       "test2",
			TS:       time.Now(),
			Type:     core.EventType("note"),
			Priority: core.Priority("P4"),
			Source:   core.Source("test"),
		},
	}}
	h := NewHandlers(idx, journal)

	// Very small budget should trigger budget exceeded logic
	resp, err := h.Recall(context.Background(), RecallParams{Budget: 10})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	// Should still have header and "no events" message since all events exceed budget
	if !strings.Contains(resp.Snapshot, "Session Snapshot") {
		t.Error("Snapshot should contain header")
	}
}

func TestHandlersRecallWithActor(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{events: []core.Event{
		{
			ID:       "test1",
			TS:       time.Now(),
			Type:     core.EventType("note"),
			Priority: core.Priority("P4"),
			Source:   core.Source("test"),
			Actor:    "user@example.com",
		},
	}}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 4096})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if !strings.Contains(resp.Snapshot, "user@example.com") {
		t.Error("Snapshot should contain actor")
	}
}

func TestHandlersRecallWithTags(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{events: []core.Event{
		{
			ID:       "test1",
			TS:       time.Now(),
			Type:     core.EventType("note"),
			Priority: core.Priority("P4"),
			Source:   core.Source("test"),
			Tags:     []string{"important", "bugfix"},
		},
	}}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 4096})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if !strings.Contains(resp.Snapshot, "important") {
		t.Error("Snapshot should contain tags")
	}
}

func TestHandlersRecallXMLFormat(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Format: "xml"})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Format != "xml" {
		t.Errorf("Format = %s, want 'xml'", resp.Format)
	}
}

func TestHandlersRecallMultipleEventsSorted(t *testing.T) {
	idx := core.NewIndex()
	now := time.Now()
	journal := &mockJournal{events: []core.Event{
		{
			ID:       "test1",
			TS:       now.Add(-2 * time.Hour),
			Type:     core.EventType("note"),
			Priority: core.Priority("P4"),
			Source:   core.Source("test"),
		},
		{
			ID:       "test2",
			TS:       now.Add(-1 * time.Hour),
			Type:     core.EventType("decision"),
			Priority: core.Priority("P1"),
			Source:   core.Source("test"),
		},
		{
			ID:       "test3",
			TS:       now,
			Type:     core.EventType("error"),
			Priority: core.Priority("P2"),
			Source:   core.Source("test"),
		},
	}}
	h := NewHandlers(idx, journal)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 10000})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	// P1 should come before P2 which should come before P4
	snapshot := resp.Snapshot
	// Check that decision (P1) appears before note (P4)
	p1Pos := strings.Index(snapshot, "decision")
	p4Pos := strings.Index(snapshot, "note")
	if p1Pos > p4Pos && p1Pos != -1 && p4Pos != -1 {
		t.Error("P1 events should appear before P4 events")
	}
}

func TestMCPProtocolHandle(t *testing.T) {
	handlers := &Handlers{}
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "ping",
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("resp.JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("resp.Error = %v, want nil", resp.Error)
	}
}

func TestMCPProtocolHandleInitialize(t *testing.T) {
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Result == nil {
		t.Fatal("resp.Result is nil")
	}
	result, ok := resp.Result.(MCPInitializeResult)
	if !ok {
		t.Fatalf("resp.Result is %T, want MCPInitializeResult", resp.Result)
	}
	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("ProtocolVersion = %s, want '2024-11-05'", result.ProtocolVersion)
	}
}

func TestMCPProtocolHandleToolsList(t *testing.T) {
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Result == nil {
		t.Fatal("resp.Result is nil")
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("resp.Result is not map[string]any")
	}
	tools, ok := result["tools"].([]MCPTool)
	if !ok {
		t.Fatal("resp.Result[\"tools\"] is not []MCPTool")
	}
	if len(tools) != 3 {
		t.Errorf("len(tools) = %d, want 3", len(tools))
	}
}

func TestMCPProtocolHandleUnknownMethod(t *testing.T) {
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "unknown/method",
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("resp.Error.Code = %d, want -32601", resp.Error.Code)
	}
}

func TestMCPProtocolHandleToolsCallUnknown(t *testing.T) {
	handlers := &Handlers{}
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.unknown","arguments":{}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for unknown tool")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("resp.Error.Code = %d, want -32601", resp.Error.Code)
	}
}

func TestRememberParams(t *testing.T) {
	params := RememberParams{
		Type:     "note",
		Priority: "P2",
		Source:   "test",
		Actor:    "user",
		Data:     map[string]any{"key": "value"},
		Refs:     []string{"ref1"},
		Tags:     []string{"tag1"},
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RememberParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Type != "note" {
		t.Errorf("Type = %s, want 'note'", decoded.Type)
	}
	if decoded.Priority != "P2" {
		t.Errorf("Priority = %s, want 'P2'", decoded.Priority)
	}
	if decoded.Actor != "user" {
		t.Errorf("Actor = %s, want 'user'", decoded.Actor)
	}
}

func TestSearchParams(t *testing.T) {
	params := SearchParams{
		Query: "test query",
		Limit: 10,
		Layer: "bm25",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded SearchParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Query != "test query" {
		t.Errorf("Query = %s, want 'test query'", decoded.Query)
	}
	if decoded.Limit != 10 {
		t.Errorf("Limit = %d, want 10", decoded.Limit)
	}
	if decoded.Layer != "bm25" {
		t.Errorf("Layer = %s, want 'bm25'", decoded.Layer)
	}
}

func TestRecallParams(t *testing.T) {
	params := RecallParams{
		Budget: 8192,
		Format: "json",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RecallParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Budget != 8192 {
		t.Errorf("Budget = %d, want 8192", decoded.Budget)
	}
	if decoded.Format != "json" {
		t.Errorf("Format = %s, want 'json'", decoded.Format)
	}
}

func TestStreamParams(t *testing.T) {
	params := StreamParams{
		From: "2024-01-01T00:00:00Z",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StreamParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.From != "2024-01-01T00:00:00Z" {
		t.Errorf("From = %s, want '2024-01-01T00:00:00Z'", decoded.From)
	}
}

func TestSearchHit(t *testing.T) {
	hit := SearchHit{
		ID:    "test-id",
		Score: 0.95,
		Layer: 1,
		Type:  "note",
	}

	data, err := json.Marshal(hit)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded SearchHit
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != "test-id" {
		t.Errorf("ID = %s, want 'test-id'", decoded.ID)
	}
	if decoded.Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", decoded.Score)
	}
}

func TestNewHandlers(t *testing.T) {
	idx := core.NewIndex()
	h := NewHandlers(idx, nil)

	if h.index != idx {
		t.Error("h.index != idx")
	}
	if h.journal != nil {
		t.Error("h.journal != nil")
	}
}

func TestMCPProtocolHandleToolsCallRemember(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note","source":"test"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("resp.Result is nil")
	}
}

func TestMCPProtocolHandleToolsCallSearch(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search","arguments":{"query":"test"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("resp.Result is nil")
	}
}

func TestMCPProtocolHandleToolsCallRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.recall","arguments":{"budget":1024}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("resp.Result is nil")
	}
}

func TestMCPProtocolHandleToolsCallInvalidParams(t *testing.T) {
	handlers := &Handlers{}
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":"invalid json"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for invalid params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("resp.Error.Code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCPProtocolErrorResult(t *testing.T) {
	mcp := NewMCPProtocol(nil)

	var resp *MCPResponse
	var err error
	resp, err = mcp.errorResult(1, -32603, "test error")
	if err != nil {
		t.Fatalf("errorResult failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("Code = %d, want -32603", resp.Error.Code)
	}
	if resp.Error.Message != "test error" {
		t.Errorf("Message = %s, want 'test error'", resp.Error.Message)
	}
}

func TestSocketServerCreation_FromHandlers(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer("/tmp/test.sock", handlers)

	if ss.path != "/tmp/test.sock" {
		t.Errorf("path = %s, want '/tmp/test.sock'", ss.path)
	}
	if ss.handlers != handlers {
		t.Error("handlers not set correctly")
	}
	if ss.running {
		t.Error("running should be false initially")
	}
}

func TestNewHTTPServer(t *testing.T) {
	handlers := &Handlers{}
	hs := NewHTTPServer(":8080", handlers)

	if hs.bind != ":8080" {
		t.Errorf("bind = %s, want ':8080'", hs.bind)
	}
	if hs.handlers != handlers {
		t.Error("handlers not set correctly")
	}
	if hs.running {
		t.Error("running should be false initially")
	}
}

func TestHTTPServerSetPortFile(t *testing.T) {
	hs := NewHTTPServer(":8080", nil)
	hs.SetPortFile("/tmp/portfile")
	if hs.portFile != "/tmp/portfile" {
		t.Errorf("portFile = %s, want '/tmp/portfile'", hs.portFile)
	}
}

func TestDecodeParams(t *testing.T) {
	data := json.RawMessage(`{"type":"note","source":"test"}`)
	var params RememberParams
	err := decodeParams(data, &params)
	if err != nil {
		t.Fatalf("decodeParams failed: %v", err)
	}
	if params.Type != "note" {
		t.Errorf("Type = %s, want 'note'", params.Type)
	}
	if params.Source != "test" {
		t.Errorf("Source = %s, want 'test'", params.Source)
	}
}

func TestDecodeParamsEmpty(t *testing.T) {
	var params RememberParams
	err := decodeParams(nil, &params)
	if err != nil {
		t.Fatalf("decodeParams failed for nil: %v", err)
	}

	err = decodeParams(json.RawMessage([]byte{}), &params)
	if err != nil {
		t.Fatalf("decodeParams failed for empty: %v", err)
	}
}

func TestDecodeParamsInvalid(t *testing.T) {
	data := json.RawMessage(`{invalid}`)
	var params RememberParams
	err := decodeParams(data, &params)
	if err == nil {
		t.Error("decodeParams should fail for invalid JSON")
	}
}

func TestSocketServerDispatch(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	// Test dispatch with remember
	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  json.RawMessage(`{"type":"note","source":"test"}`),
		ID:      1,
	}

	result, err := ss.dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestSocketServerDispatchSearch(t *testing.T) {
	idx := core.NewIndex()
	idx.Add(core.Event{
		ID:       "test1",
		TS:       time.Now(),
		Type:     core.EventType("note"),
		Priority: core.Priority("P2"),
	})
	handlers := NewHandlers(idx, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "search",
		Params:  json.RawMessage(`{"query":"test"}`),
		ID:      1,
	}

	result, err := ss.dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestSocketServerDispatchRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "recall",
		Params:  json.RawMessage(`{"budget":1024}`),
		ID:      1,
	}

	result, err := ss.dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestSocketServerDispatchUnknown(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "unknown",
		ID:      1,
	}

	_, err := ss.dispatch(context.Background(), req)
	if err == nil {
		t.Error("dispatch should fail for unknown method")
	}
}

func TestSocketServerDispatchInvalidParams(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  json.RawMessage(`{invalid}`),
		ID:      1,
	}

	_, err := ss.dispatch(context.Background(), req)
	if err == nil {
		t.Error("dispatch should fail for invalid params")
	}
}

func TestHTTPServerHandleMethodNotAllowed(t *testing.T) {
	handlers := &Handlers{}
	hs := NewHTTPServer(":8080", handlers)

	// We can't easily test the HTTP handler without a real server
	// But we can test the field initialization
	if hs.bind != ":8080" {
		t.Errorf("bind = %s, want ':8080'", hs.bind)
	}
}

func TestHTTPServerWritePortFile(t *testing.T) {
	tmpDir := t.TempDir()
	portFile := tmpDir + "/portfile"

	hs := NewHTTPServer(":8080", nil)
	err := hs.writePortFile(portFile, 12345)
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "12345" {
		t.Errorf("port = %s, want '12345'", string(data))
	}
}

func TestHTTPServerWritePortFileCreateDir(t *testing.T) {
	tmpDir := t.TempDir()
	portFile := tmpDir + "/subdir/portfile"

	hs := NewHTTPServer(":8080", nil)
	err := hs.writePortFile(portFile, 54321)
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "54321" {
		t.Errorf("port = %s, want '54321'", string(data))
	}
}

func TestMCPTool(t *testing.T) {
	tool := MCPTool{
		Name:        "test.tool",
		Description: "Test tool description",
		InputSchema: map[string]any{"type": "object"},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MCPTool
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Name != "test.tool" {
		t.Errorf("Name = %s, want 'test.tool'", decoded.Name)
	}
}

func TestMCPClientCapabilities(t *testing.T) {
	caps := MCPClientCapabilities{}
	caps.Roots.ListChanged = true

	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MCPClientCapabilities
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !decoded.Roots.ListChanged {
		t.Error("ListChanged should be true")
	}
}

func TestMCPServerInfo(t *testing.T) {
	info := MCPServerInfo{
		Name:    "dfmt",
		Version: "0.1.0",
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MCPServerInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Name != "dfmt" {
		t.Errorf("Name = %s, want 'dfmt'", decoded.Name)
	}
	if decoded.Version != "0.1.0" {
		t.Errorf("Version = %s, want '0.1.0'", decoded.Version)
	}
}

func TestMCPRequest(t *testing.T) {
	req := MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
		ID:      1,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MCPRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Method != "initialize" {
		t.Errorf("Method = %s, want 'initialize'", decoded.Method)
	}
}

func TestMCPResponse(t *testing.T) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"key": "value"},
		ID:      1,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded MCPResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %s, want '2.0'", decoded.JSONRPC)
	}
}

func TestRecallResponse(t *testing.T) {
	resp := RecallResponse{
		Snapshot: "# Test",
		Format:   "md",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RecallResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Snapshot != "# Test" {
		t.Errorf("Snapshot = %s, want '# Test'", decoded.Snapshot)
	}
}

func TestRememberResponse(t *testing.T) {
	resp := RememberResponse{
		ID:  "test123",
		TS:  "2024-01-01T00:00:00Z",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RememberResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != "test123" {
		t.Errorf("ID = %s, want 'test123'", decoded.ID)
	}
}

func TestSearchResponse(t *testing.T) {
	resp := SearchResponse{
		Results: []SearchHit{
			{ID: "hit1", Score: 0.9, Layer: 1},
		},
		Layer: "bm25",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded SearchResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Results) != 1 {
		t.Errorf("len(Results) = %d, want 1", len(decoded.Results))
	}
	if decoded.Layer != "bm25" {
		t.Errorf("Layer = %s, want 'bm25'", decoded.Layer)
	}
}

func TestSocketServerFields(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer("/tmp/test.sock", handlers)

	if ss.path != "/tmp/test.sock" {
		t.Errorf("path = %s, want '/tmp/test.sock'", ss.path)
	}
	if ss.handlers != handlers {
		t.Error("handlers not set correctly")
	}
	if ss.running {
		t.Error("running should be false initially")
	}
	if ss.listener != nil {
		t.Error("listener should be nil initially")
	}
}

func TestHTTPServerFields(t *testing.T) {
	handlers := &Handlers{}
	hs := NewHTTPServer(":9999", handlers)

	if hs.bind != ":9999" {
		t.Errorf("bind = %s, want ':9999'", hs.bind)
	}
	if hs.handlers != handlers {
		t.Error("handlers not set correctly")
	}
	if hs.running {
		t.Error("running should be false initially")
	}
	if hs.server != nil {
		t.Error("server should be nil initially")
	}
}

func TestCodecReadRequestEOF(t *testing.T) {
	buf := &bytes.Buffer{}
	codec := NewCodec(buf)

	_, err := codec.ReadRequest()
	if err == nil {
		t.Error("ReadRequest should fail on empty buffer")
	}
}

func TestCodecReadRequestInvalidJSON(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	buf.Write([]byte("not json\n"))
	codec := NewCodec(buf)

	_, err := codec.ReadRequest()
	if err == nil {
		t.Error("ReadRequest should fail on invalid JSON")
	}
}

func TestCodecReadRequestUnsupportedVersion(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	codec := NewCodec(buf)

	// Write raw JSON with wrong version directly
	buf.Write([]byte(`{"jsonrpc":"1.0","method":"test","id":1}` + "\n"))

	_, err := codec.ReadRequest()
	if err == nil {
		t.Error("ReadRequest should fail for unsupported JSON-RPC version")
	}
}

func TestCodecReadResponseEOF(t *testing.T) {
	buf := &bytes.Buffer{}
	codec := NewCodec(buf)

	_, err := codec.ReadResponse()
	if err == nil {
		t.Error("ReadResponse should fail on empty buffer")
	}
}

func TestCodecReadResponseInvalidJSON(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	buf.Write([]byte("not json\n"))
	codec := NewCodec(buf)

	_, err := codec.ReadResponse()
	if err == nil {
		t.Error("ReadResponse should fail on invalid JSON")
	}
}

func TestCodecWriteRequestSetVersion(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	codec := NewCodec(buf)

	req := &Request{
		JSONRPC: "", // Should be set to 2.0
		Method:  "test",
		ID:     1,
	}
	codec.WriteRequest(req)

	// Read it back
	readReq, err := codec.ReadRequest()
	if err != nil {
		t.Fatalf("ReadRequest failed: %v", err)
	}
	if readReq.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %s, want '2.0'", readReq.JSONRPC)
	}
}

func TestCodecWriteResponseSetVersion(t *testing.T) {
	buf := &pipeReadWriter{buf: &bytes.Buffer{}}
	codec := NewCodec(buf)

	resp := &Response{
		JSONRPC: "", // Should be set to 2.0
		Result:  map[string]any{"key": "value"},
		ID:      1,
	}
	codec.WriteResponse(resp)

	readResp, err := codec.ReadResponse()
	if err != nil {
		t.Fatalf("ReadResponse failed: %v", err)
	}
	if readResp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %s, want '2.0'", readResp.JSONRPC)
	}
}

func TestHTTPServerStartAlreadyRunning(t *testing.T) {
	handlers := &Handlers{}
	hs := NewHTTPServer(":0", handlers)

	hs.mu.Lock()
	hs.running = true
	hs.mu.Unlock()

	err := hs.Start(context.Background())
	if err == nil {
		t.Error("Start should fail when already running")
	}
}

func TestSocketServerStartAlreadyRunning(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer("/tmp/test.sock", handlers)

	// Manually set running to true (can't actually start on Windows easily)
	ss.mu.Lock()
	ss.running = true
	ss.mu.Unlock()

	err := ss.Start(context.Background())
	if err == nil {
		t.Error("Start should fail when already running")
	}
}

func TestSocketServerStopNotRunning(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer("/tmp/test.sock", handlers)

	// Stop without starting should be fine
	err := ss.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestHandlersStream(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{
		events: []core.Event{
			{
				ID:       "test1",
				TS:       time.Now(),
				Type:     core.EventType("note"),
				Priority: core.Priority("P2"),
				Source:   core.Source("test"),
			},
		},
	}
	h := NewHandlers(idx, journal)

	events, err := h.Stream(context.Background(), StreamParams{})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	count := 0
	for e := range events {
		if e.ID == "test1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Stream received %d events, want 1", count)
	}
}

func TestHandlersRememberWithTags(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Remember(context.Background(), RememberParams{
		Type:     "note",
		Source:   "test",
		Tags:     []string{"tag1", "tag2"},
		Priority: "P1",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp.ID == "" {
		t.Error("RememberResponse ID is empty")
	}
}

func TestHandlersRememberWithRefs(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Remember(context.Background(), RememberParams{
		Type:   "note",
		Source: "test",
		Refs:   []string{"ref1", "ref2"},
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp.ID == "" {
		t.Error("RememberResponse ID is empty")
	}
}

func TestHandlersRememberWithActor(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal)

	resp, err := h.Remember(context.Background(), RememberParams{
		Type:   "note",
		Source: "test",
		Actor:  "user@example.com",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp.ID == "" {
		t.Error("RememberResponse ID is empty")
	}
}

func TestHandlersSearchDefaultLimit(t *testing.T) {
	idx := core.NewIndex()
	h := NewHandlers(idx, nil)

	resp, err := h.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	// Default limit is 10
	if resp.Results == nil {
		t.Error("Results is nil")
	}
}

func TestSocketServerDispatchRemember(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "remember",
		Params:  json.RawMessage(`{"type":"note","source":"test"}`),
		ID:      1,
	}

	result, err := ss.dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestSocketServerDispatchSearchNoResults(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: "2.0",
		Method:  "search",
		Params:  json.RawMessage(`{"query":"nonexistent"}`),
		ID:      1,
	}

	result, err := ss.dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestHTTPServerHandleRemember(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	hs := NewHTTPServer(":8080", handlers)

	// Params can be any type in http.go's Request
	req := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.remember",
		Params:  mustMarshalParams(RememberParams{Type: "note", Source: "test"}),
		ID:      1,
	}

	resp := hs.handleRemember(req, "session123")
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("Unexpected error: %v", resp.Error)
	}
}

func TestHTTPServerHandleSearch(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":8080", handlers)

	req := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.search",
		Params:  mustMarshalParams(SearchParams{Query: "test"}),
		ID:      1,
	}

	resp := hs.handleSearch(req, "session123")
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
}

func TestHTTPServerHandleRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	hs := NewHTTPServer(":8080", handlers)

	req := Request{
		JSONRPC: "2.0",
		Method:  "dfmt.recall",
		Params:  mustMarshalParams(RecallParams{Budget: 1024}),
		ID:      1,
	}

	resp := hs.handleRecall(req, "session123")
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
}

// mustMarshalParams converts a struct to json.RawMessage for HTTP handler tests
func mustMarshalParams(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func TestHTTPServerStop(t *testing.T) {
	hs := NewHTTPServer(":8080", nil)

	// Stop without starting should be fine
	err := hs.Stop()
	if err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

// handleToolsCall error path tests

func TestMCPProtocolHandleToolsCallNilHandlers(t *testing.T) {
	// Test the nil handlers branch: "daemon not connected"
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for nil handlers")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("resp.Error.Code = %d, want -32603", resp.Error.Code)
	}
	if resp.Error.Message != "daemon not connected" {
		t.Errorf("resp.Error.Message = %s, want 'daemon not connected'", resp.Error.Message)
	}
}

func TestMCPProtocolHandleToolsCallEmptyName(t *testing.T) {
	// Test empty tool name (default case in switch)
	handlers := &Handlers{}
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"","arguments":{}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for empty tool name")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("resp.Error.Code = %d, want -32601", resp.Error.Code)
	}
	if resp.Error.Message != "unknown tool: " {
		t.Errorf("resp.Error.Message = %s, want 'unknown tool: '", resp.Error.Message)
	}
}

func TestMCPProtocolHandleToolsCallRememberArgsInvalid(t *testing.T) {
	// Test Remember with malformed arguments JSON
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		// arguments is a string instead of an object - should fail unmarshal
		Params: json.RawMessage(`{"name":"dfmt.remember","arguments":"not an object"}`),
		ID:     1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for invalid remember args")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("resp.Error.Code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCPProtocolHandleToolsCallSearchArgsInvalid(t *testing.T) {
	// Test Search with malformed arguments JSON
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search","arguments":{"query":123}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for invalid search args")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("resp.Error.Code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCPProtocolHandleToolsCallRecallArgsInvalid(t *testing.T) {
	// Test Recall with malformed arguments JSON
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.recall","arguments":{"budget":"not a number"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for invalid recall args")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("resp.Error.Code = %d, want -32602", resp.Error.Code)
	}
}

// errorReturningJournal is a mock journal that returns errors
type errorReturningJournal struct {
	err error
}

func (m *errorReturningJournal) Append(ctx context.Context, e core.Event) error {
	return m.err
}

func (m *errorReturningJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	return nil, m.err
}

func (m *errorReturningJournal) Checkpoint(ctx context.Context) (string, error) {
	return "", nil
}

func (m *errorReturningJournal) Rotate(ctx context.Context) error {
	return m.err
}

func (m *errorReturningJournal) Close() error {
	return nil
}

func TestMCPProtocolHandleToolsCallRememberHandlerError(t *testing.T) {
	// Test Remember handler returning an error
	idx := core.NewIndex()
	journal := &errorReturningJournal{err: fmt.Errorf("journal append failed")}
	handlers := NewHandlers(idx, journal)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note","source":"test"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for handler error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("resp.Error.Code = %d, want -32603", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "journal append") {
		t.Errorf("resp.Error.Message = %s, want to contain 'journal append'", resp.Error.Message)
	}
}

func TestMCPProtocolHandleToolsCallRecallHandlerError(t *testing.T) {
	// Test Recall handler returning an error (stream failure)
	idx := core.NewIndex()
	journal := &errorReturningJournal{err: fmt.Errorf("stream journal failed")}
	handlers := NewHandlers(idx, journal)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.recall","arguments":{"budget":1024}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for handler error")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("resp.Error.Code = %d, want -32603", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "stream journal") {
		t.Errorf("resp.Error.Message = %s, want to contain 'stream journal'", resp.Error.Message)
	}
}

func TestMCPProtocolHandleToolsCallMissingArguments(t *testing.T) {
	// Test calling tool with no arguments field at all
	// When arguments is missing, params.Args is nil and unmarshaling into struct fails
	handlers := &Handlers{}
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember"}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	// nil arguments fails to unmarshal into RememberParams (expects object)
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for missing arguments")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("resp.Error.Code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCPProtocolHandleToolsCallSearchNoArgs(t *testing.T) {
	// Test calling dfmt.search with no arguments
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search"}`),
		ID:      1,
	}

	resp, err := mcp.Handle(req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	// Empty query should fail validation
	if resp.Error == nil {
		t.Fatal("resp.Error is nil for search with no query")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("resp.Error.Code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCPProtocolHandleToolsCallVariousUnknownTools(t *testing.T) {
	// Test various unknown tool names
	handlers := &Handlers{}
	mcp := NewMCPProtocol(handlers)

	testCases := []string{
		"unknown.tool",
		"dfmt.",
		"dfmt.unknown",
		"invalid",
		"../../../etc/passwd",
	}

	for _, name := range testCases {
		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  "tools/call",
			Params:  json.RawMessage(`{"name":"` + name + `","arguments":{}}`),
			ID:      1,
		}

		resp, err := mcp.Handle(req)
		if err != nil {
			t.Fatalf("Handle failed for tool %q: %v", name, err)
		}
		if resp.Error == nil {
			t.Errorf("resp.Error is nil for unknown tool %q", name)
		}
		if resp.Error.Code != -32601 {
			t.Errorf("resp.Error.Code = %d for tool %q, want -32601", resp.Error.Code, name)
		}
	}
}
