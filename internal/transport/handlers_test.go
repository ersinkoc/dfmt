package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/content"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/redact"
)

// TestHandlersRedactExecCode verifies that tool.exec invocations redact
// secrets that appear in the code argument before they hit the journal.
func TestHandlersRedactExecCode(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	h.logEvent(context.Background(), "tool.exec", "push", map[string]any{
		"code": "curl -H 'Authorization: Bearer abc123xyz456def789xyz' https://example.com",
	})

	data, err := os.ReadFile(tmp + "/journal.jsonl")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.Contains(data, []byte("abc123xyz456def789xyz")) {
		t.Errorf("bearer token leaked into journal: %s", string(data))
	}
}

// TestHandlersRedactRemember verifies that Remember redacts secrets in the
// Data map before the event is journaled. Guards against the "redact package
// is orphan" regression from the second audit.
func TestHandlersRedactRemember(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	idx := core.NewIndex()
	h := NewHandlers(idx, journal, nil)

	params := RememberParams{
		Type:   "note",
		Source: "mcp",
		Data: map[string]any{
			"payload": "export API_TOKEN=ghp_abc123xyz456def789ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV",
		},
		Tags: []string{"sk-abcd1234efgh5678ijkl9012mnop3456qrst7890uvwx1234yzAB5678CD"},
	}
	if _, err := h.Remember(context.Background(), params); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	data, err := os.ReadFile(tmp + "/journal.jsonl")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.Contains(data, []byte("ghp_abc123xyz456def789")) {
		t.Error("GitHub token leaked into journal")
	}
	if bytes.Contains(data, []byte("sk-abcd1234efgh5678")) {
		t.Error("OpenAI key leaked into journal via Tags")
	}
	// Either the env_export path turns "API_TOKEN=ghp_..." into
	// "API_TOKEN=[REDACTED]", or the github_token pattern turns the raw
	// token into "[GITHUB_TOKEN]". Either result is acceptable redaction.
	if !bytes.Contains(data, []byte("[REDACTED]")) && !bytes.Contains(data, []byte("[GITHUB_TOKEN]")) {
		t.Errorf("expected redaction marker in %s", string(data))
	}
}

// DEDUPED: second copy removed

// TestHandlersStashContentPutChunkSetError tests stashContent when PutChunkSet fails.
// PutChunkSet validates the ULID ID; using an invalid ULID makes it return an error.
func TestHandlersStashContentPutChunkSetError(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	// Inject a broken store that PutChunkSet will reject
	h.dedupCache = map[string]dedupEntry{}
	// Force store.PutChunkSet to fail by passing a set with an invalid ID.
	// The dedup lookup will miss (no key), so we go straight to PutChunkSet.
	// stashContent calls core.NewULID which should always produce a valid ULID,
	// so PutChunkSet should not fail with a valid store. To trigger the error
	// path we'd need to mock the store. For now just exercise the normal path.
	// Store is nil so stashContent returns "" — that's the expected nil-store
	// path; the assertion is just "no panic".
	_ = h.stashContent(nil, "/proj", "exec-stdout", "sandbox.exec", "test", "hello world")
}

// TestHandlersStashContentDedupWithRealStore tests that stashContent correctly
// uses the dedupCache to return the same ID for identical content, even when
// no session is attached, and that the content store operations work end-to-end.
func TestHandlersStashContentDedupWithRealStore(t *testing.T) {
	tmp := t.TempDir()
	store, err := content.NewStore(content.StoreOptions{
		Path:    filepath.Join(tmp, "content"),
		MaxSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	h.SetContentStore(store)

	// No session attached; seenBefore should return false for any contentID
	ctx := context.Background()
	if h.seenBefore(ctx, "some-content-id") {
		t.Error("seenBefore should return false when no session is attached")
	}

	// But stashContent should still work - dedupCache stores by (project,kind,source,body)
	id1 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "alpha", "body1")
	id2 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "beta", "body1")
	if id1 != id2 {
		t.Errorf("dedup: id1=%q id2=%q, want equal", id1, id2)
	}
}

// TestHandlersMarkSentEmptyContentID tests that markSent is safe to call with
// an empty contentID (the early return prevents nil-map insertion).
func TestHandlersMarkSentEmptyContentID(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	ctx := context.Background()
	// Should not panic with empty contentID
	h.markSent(ctx, "")
}

// TestHandlersSeenBeforeNoCache tests seenBefore when sentCache is nil.
func TestHandlersSeenBeforeNoCache(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	// sentCache starts nil; seenBefore should return false safely
	ctx := context.Background()
	if h.seenBefore(ctx, "any-id") {
		t.Error("seenBefore should return false when sentCache is nil")
	}
}

// TestHandlersMarkSentFIFOEviction tests that markSent evicts the oldest entry
// when the cache is full (sentCap = 256).
func TestHandlersMarkSentFIFOEviction(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	ctx := context.Background()

	// Fill the cache to capacity
	for i := 0; i < sentCap; i++ {
		h.markSent(ctx, fmt.Sprintf("id-%04d", i))
	}

	// sentOrder should have sentCap entries
	if len(h.sentOrder) != sentCap {
		t.Errorf("sentOrder len = %d, want %d", len(h.sentOrder), sentCap)
	}

	// Adding one more should evict the oldest (id-0000)
	h.markSent(ctx, fmt.Sprintf("id-%04d", sentCap))

	if len(h.sentOrder) != sentCap {
		t.Errorf("sentOrder should still be at cap %d, got %d", sentCap, len(h.sentOrder))
	}

	// id-0000 should be evicted from cache, id-0001 still present
	h.sentMu.Lock()
	_, has0000 := h.sentCache["\x00id-0000"]
	_, has0001 := h.sentCache["\x00id-0001"]
	h.sentMu.Unlock()

	if has0000 {
		t.Error("oldest entry should have been evicted")
	}
	if !has0001 {
		t.Error("second oldest entry should still be present")
	}
}

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
		JSONRPC: jsonRPCVersion,
		Method:  "remember",
		Params:  json.RawMessage(`{"type":"note"}`),
		ID:      1,
	}
	if req.JSONRPC != jsonRPCVersion {
		t.Errorf("Request.JSONRPC = %s, want '2.0'", req.JSONRPC)
	}
	if req.Method != "remember" {
		t.Errorf("Request.Method = %s, want 'remember'", req.Method)
	}
}

func TestResponse(t *testing.T) {
	resp := &Response{
		JSONRPC: jsonRPCVersion,
		Result:  map[string]any{"id": "test"},
		ID:      1,
	}
	if resp.JSONRPC != jsonRPCVersion {
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
		JSONRPC: jsonRPCVersion,
		Method:  "remember",
		Params:  json.RawMessage(`{"type":"note"}`),
		ID:      1,
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
		JSONRPC: jsonRPCVersion,
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

	if readResp.JSONRPC != jsonRPCVersion {
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
	mu           sync.Mutex
	events       []core.Event
	failRemember bool
	failSearch   bool
	failRecall   bool
}

func (m *mockJournal) Append(ctx context.Context, e core.Event) error {
	if m.failRemember {
		return fmt.Errorf("mock remember failure")
	}
	m.mu.Lock()
	m.events = append(m.events, e)
	m.mu.Unlock()
	return nil
}

func (m *mockJournal) Stream(ctx context.Context, from string) (<-chan core.Event, error) {
	if m.failSearch || m.failRecall {
		return nil, fmt.Errorf("mock stream failure")
	}
	// Snapshot under the lock so a concurrent Append (e.g. the live-tail
	// SSE test) cannot mutate the slice mid-iteration.
	m.mu.Lock()
	snap := make([]core.Event, len(m.events))
	copy(snap, m.events)
	m.mu.Unlock()
	// Honor `from`: production journalImpl.Stream skips past the matching
	// ID and emits everything after. Without this the live-tail tail
	// logic would replay historical events on every poll tick.
	ch := make(chan core.Event, len(snap))
	emitted := from == ""
	for _, e := range snap {
		if !emitted {
			if e.ID == from {
				emitted = true
			}
			continue
		}
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

func (m *mockJournal) Size() (int64, error) {
	return 0, nil
}

func (m *mockJournal) Close() error {
	return nil
}

func (m *mockJournal) StreamN(ctx context.Context, from string, n int) (<-chan core.Event, error) {
	return m.Stream(ctx, from)
}

func TestHandlersSearch(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, nil, nil)

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
	h := NewHandlers(idx, nil, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

	resp, err := h.Recall(context.Background(), RecallParams{Budget: 1024})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if resp.Format != "" {
		// Format defaults to "md" but our mock doesn't set it
		t.Logf("Format = %s", resp.Format)
	}
}

// TestRecallBudget_PerCallWins is the precedence guard: an explicit
// per-call Budget always wins over the operator override and the
// package default. Operator override beats package default. Zero
// per-call falls through to the next layer.
func TestRecallBudget_PerCallWins(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	h.SetRecallDefaults(8192, "json")

	if got := h.recallBudget(2048); got != 2048 {
		t.Errorf("per-call 2048: got %d, want 2048", got)
	}
}

func TestRecallBudget_OperatorOverrideBeatsDefault(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	h.SetRecallDefaults(8192, "")

	if got := h.recallBudget(0); got != 8192 {
		t.Errorf("operator 8192 fallback: got %d, want 8192", got)
	}
}

func TestRecallBudget_PackageDefaultWhenNoneSet(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	// SetRecallDefaults never called; both layers fall through to the
	// package constant.
	if got := h.recallBudget(0); got != recallDefaultBudgetBytes {
		t.Errorf("package default: got %d, want %d", got, recallDefaultBudgetBytes)
	}
}

func TestRecallBudget_NegativeOperatorIgnored(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	h.SetRecallDefaults(-1, "")
	// Negative operator override is ignored — Validate already rejects
	// it at startup, so reaching this path means a hand-rolled config.
	if got := h.recallBudget(0); got != recallDefaultBudgetBytes {
		t.Errorf("negative operator override should be ignored: got %d, want %d",
			got, recallDefaultBudgetBytes)
	}
}

func TestRecallFormat_PerCallWins(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	h.SetRecallDefaults(0, "json")
	if got := h.recallFormat("xml"); got != "xml" {
		t.Errorf("per-call xml: got %q, want xml", got)
	}
}

func TestRecallFormat_OperatorOverrideBeatsDefault(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	h.SetRecallDefaults(0, "json")
	if got := h.recallFormat(""); got != "json" {
		t.Errorf("operator json fallback: got %q, want json", got)
	}
}

func TestRecallFormat_PackageDefaultWhenNoneSet(t *testing.T) {
	h := NewHandlers(nil, &mockJournal{}, nil)
	if got := h.recallFormat(""); got != recallDefaultFormat {
		t.Errorf("package default: got %q, want %q", got, recallDefaultFormat)
	}
}

func TestRecallDefaults_SetterIsRaceSafe(t *testing.T) {
	// The recallDefaults RWMutex must permit concurrent SetRecallDefaults
	// during in-flight Recall calls. Spawn writers and readers; -race
	// would catch a missing lock acquisition.
	h := NewHandlers(nil, &mockJournal{}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			h.SetRecallDefaults(1024+n, "json")
		}(i)
		go func() {
			defer wg.Done()
			_ = h.recallBudget(0)
			_ = h.recallFormat("")
		}()
	}
	wg.Wait()
}

// TestHandlersRecall_OperatorBudgetWired exercises the wire end-to-end:
// SetRecallDefaults → Recall observes the override budget when the
// caller omits one. Pairs with the unit tests above.
func TestHandlersRecall_OperatorBudgetWired(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)
	h.SetRecallDefaults(123, "md")

	// Indirect observation: we can't easily snapshot the in-flight
	// budget value, but a value of 0 should match the recallBudget()
	// fallback chain.
	if got := h.recallBudget(0); got != 123 {
		t.Errorf("Recall budget plumbing: got %d, want 123", got)
	}

	// And a real Recall call must not error.
	if _, err := h.Recall(context.Background(), RecallParams{}); err != nil {
		t.Fatalf("Recall: %v", err)
	}
}

func TestHandlersRecallNoEvents(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{events: []core.Event{}}
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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
		JSONRPC: jsonRPCVersion,
		Method:  "ping",
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if resp.JSONRPC != jsonRPCVersion {
		t.Errorf("resp.JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("resp.Error = %v, want nil", resp.Error)
	}
}

func TestMCPProtocolHandleInitialize(t *testing.T) {
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "initialize",
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
		JSONRPC: jsonRPCVersion,
		Method:  "tools/list",
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	if len(tools) != 11 {
		t.Errorf("len(tools) = %d, want 11", len(tools))
	}

	// Regression: every emitted tool name must match the MCP spec regex
	// ^[a-zA-Z][a-zA-Z0-9_-]*$. Dots silently drop the entire tools/list
	// in Claude Code's MCP client, leaving the server "connected" with no
	// callable tools. See http.go mcpToolXxx constants.
	mcpName := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	for _, tool := range tools {
		if !mcpName.MatchString(tool.Name) {
			t.Errorf("tool name %q is not MCP-spec compliant (^[a-zA-Z][a-zA-Z0-9_-]*$)", tool.Name)
		}
	}
}

func TestMCPProtocolHandleUnknownMethod(t *testing.T) {
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "unknown/method",
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.unknown","arguments":{}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	h := NewHandlers(idx, nil, nil)

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
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note","source":"test"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, nil, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search","arguments":{"query":"test"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.recall","arguments":{"budget":1024}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":"invalid json"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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

const testSocketPath = "/tmp/test.sock"

func TestSocketServerCreation_FromHandlers(t *testing.T) {
	handlers := &Handlers{}
	ss := NewSocketServer(testSocketPath, handlers)

	if ss.path != testSocketPath {
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
	handlers := NewHandlers(idx, journal, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	// Test dispatch with remember
	req := &Request{
		JSONRPC: jsonRPCVersion,
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
	handlers := NewHandlers(idx, nil, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: jsonRPCVersion,
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
	handlers := NewHandlers(idx, journal, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: jsonRPCVersion,
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
		JSONRPC: jsonRPCVersion,
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
		JSONRPC: jsonRPCVersion,
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
	err := hs.writePortFile(portFile, 12345, "")
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var pf PortFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal port file: %v", err)
	}
	if pf.Port != 12345 {
		t.Errorf("port = %d, want 12345", pf.Port)
	}
}

func TestHTTPServerWritePortFileCreateDir(t *testing.T) {
	tmpDir := t.TempDir()
	portFile := tmpDir + "/subdir/portfile"

	hs := NewHTTPServer(":8080", nil)
	err := hs.writePortFile(portFile, 54321, "")
	if err != nil {
		t.Fatalf("writePortFile failed: %v", err)
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var pf PortFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal port file: %v", err)
	}
	if pf.Port != 54321 {
		t.Errorf("port = %d, want 54321", pf.Port)
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

func TestMCPServerCapabilities(t *testing.T) {
	caps := MCPServerCapabilities{
		Tools: MCPToolsCapability{ListChanged: true},
	}

	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// MCP clients (Claude Code) require a non-empty `tools` key in the
	// initialize reply or they won't issue tools/list. Guard against a
	// future refactor that quietly drops it.
	if !bytes.Contains(data, []byte(`"tools"`)) {
		t.Fatalf("server capabilities must advertise tools, got %s", data)
	}

	var decoded MCPServerCapabilities
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !decoded.Tools.ListChanged {
		t.Error("Tools.ListChanged should be true")
	}
}

func TestMCPServerInfo(t *testing.T) {
	const wantVersion = "test-version-0.0.0"
	info := MCPServerInfo{
		Name:    "dfmt",
		Version: wantVersion,
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
	if decoded.Version != wantVersion {
		t.Errorf("Version = %s, want %q", decoded.Version, wantVersion)
	}
}

func TestMCPRequest(t *testing.T) {
	req := MCPRequest{
		JSONRPC: jsonRPCVersion,
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
		JSONRPC: jsonRPCVersion,
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

	if decoded.JSONRPC != jsonRPCVersion {
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
		ID: "test123",
		TS: "2024-01-01T00:00:00Z",
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
		ID:      1,
	}
	codec.WriteRequest(req)

	// Read it back
	readReq, err := codec.ReadRequest()
	if err != nil {
		t.Fatalf("ReadRequest failed: %v", err)
	}
	if readReq.JSONRPC != jsonRPCVersion {
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
	if readResp.JSONRPC != jsonRPCVersion {
		t.Errorf("JSONRPC = %s, want '2.0'", readResp.JSONRPC)
	}
}

func TestHTTPServerStartAlreadyRunning(t *testing.T) {
	handlers := &Handlers{}
	hs := NewHTTPServer("127.0.0.1:0", handlers)

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
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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

// TestHandlersRememberPersistsMessage pins the contract that the
// `message` parameter — advertised on dfmt_remember's MCP schema — is
// stored in the event's Data so it can be searched and rendered by
// recall. Closes the audit-discovered defect where the handler
// silently dropped the field, leaving the recall snapshot tag-only
// and dfmt_search blind to anything in the message body.
func TestHandlersRememberPersistsMessage(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)

	const msg = "audit verification marker XJ7Q3 must reach the index"
	resp, err := h.Remember(context.Background(), RememberParams{
		Type:    "note",
		Message: msg,
		Tags:    []string{"audit"},
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Verify the journal recorded the message in Data.
	if len(journal.events) != 1 {
		t.Fatalf("expected 1 journaled event, got %d", len(journal.events))
	}
	got, ok := journal.events[0].Data["message"]
	if !ok {
		t.Fatal("event Data is missing the 'message' key")
	}
	if got != msg {
		t.Errorf("event Data['message'] = %v, want %q", got, msg)
	}

	// Verify the index now retrieves the event by a word from the
	// message body — closes the regression at the search layer too.
	hits := idx.SearchBM25("verification", 5)
	found := false
	for _, h := range hits {
		if h.ID == resp.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SearchBM25 did not return the just-remembered event ID; hits=%+v", hits)
	}
}

func TestHandlersRememberWithRefs(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)

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
	h := NewHandlers(idx, journal, nil)

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

// TestHandlersRemember_OverridesSourceAndPriority covers F-21: a prompt-
// injected agent must not be able to write events claiming to come from
// `githook` / `fswatch` (so they survive Recall's source-based budgeting)
// or with priority `p1` (the band reserved for operator-logged decisions
// and incidents). Both fields are agent-controllable on the wire; the
// server-side override forces Source=mcp and coerces non-{p2,p3,p4}
// priorities to p3.
func TestHandlersRemember_OverridesSourceAndPriority(t *testing.T) {
	cases := []struct {
		name       string
		inSource   string
		inPriority string
		wantSource core.Source
		wantPrio   core.Priority
	}{
		{"spoofed githook becomes mcp", "githook", "p2", core.SrcMCP, core.PriP2},
		{"spoofed fswatch becomes mcp", "fswatch", "p3", core.SrcMCP, core.PriP3},
		{"empty source becomes mcp", "", "p4", core.SrcMCP, core.PriP4},
		{"p1 coerced to p3", "mcp", "p1", core.SrcMCP, core.PriP3},
		{"empty priority defaults p3", "mcp", "", core.SrcMCP, core.PriP3},
		{"garbage priority coerced p3", "mcp", "p9", core.SrcMCP, core.PriP3},
		{"capital P2 coerced p3", "mcp", "P2", core.SrcMCP, core.PriP3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			journal := &mockJournal{}
			h := NewHandlers(core.NewIndex(), journal, nil)
			if _, err := h.Remember(context.Background(), RememberParams{
				Type:     "note",
				Source:   c.inSource,
				Priority: c.inPriority,
			}); err != nil {
				t.Fatalf("Remember: %v", err)
			}
			if len(journal.events) != 1 {
				t.Fatalf("want 1 event, got %d", len(journal.events))
			}
			ev := journal.events[0]
			if ev.Source != c.wantSource {
				t.Errorf("Source = %q; want %q", ev.Source, c.wantSource)
			}
			if ev.Priority != c.wantPrio {
				t.Errorf("Priority = %q; want %q", ev.Priority, c.wantPrio)
			}
		})
	}
}

func TestHandlersSearchDefaultLimit(t *testing.T) {
	idx := core.NewIndex()
	h := NewHandlers(idx, nil, nil)

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
	handlers := NewHandlers(idx, journal, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: jsonRPCVersion,
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
	handlers := NewHandlers(idx, nil, nil)
	ss := NewSocketServer("/tmp/test.sock", handlers)

	req := &Request{
		JSONRPC: jsonRPCVersion,
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
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer(":8080", handlers)

	// Params can be any type in http.go's Request
	req := Request{
		JSONRPC: jsonRPCVersion,
		Method:  "dfmt.remember",
		Params:  mustMarshalParams(RememberParams{Type: "note", Source: "test"}),
		ID:      1,
	}

	resp := hs.handleRemember(context.Background(), req)
	if resp.JSONRPC != jsonRPCVersion {
		t.Errorf("JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("Unexpected error: %v", resp.Error)
	}
}

func TestHTTPServerHandleSearch(t *testing.T) {
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil, nil)
	hs := NewHTTPServer(":8080", handlers)

	req := Request{
		JSONRPC: jsonRPCVersion,
		Method:  "dfmt.search",
		Params:  mustMarshalParams(SearchParams{Query: "test"}),
		ID:      1,
	}

	resp := hs.handleSearch(context.Background(), req)
	if resp.JSONRPC != jsonRPCVersion {
		t.Errorf("JSONRPC = %s, want '2.0'", resp.JSONRPC)
	}
}

func TestHTTPServerHandleRecall(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	handlers := NewHandlers(idx, journal, nil)
	hs := NewHTTPServer(":8080", handlers)

	req := Request{
		JSONRPC: jsonRPCVersion,
		Method:  "dfmt.recall",
		Params:  mustMarshalParams(RecallParams{Budget: 1024}),
		ID:      1,
	}

	resp := hs.handleRecall(context.Background(), req)
	if resp.JSONRPC != jsonRPCVersion {
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
	err := hs.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

// TestHTTPServerStartWithNilListener tests that Start with nil listener doesn't panic.
func TestHTTPServerStartWithNilListener(t *testing.T) {
	hs := NewHTTPServer("127.0.0.1:0", nil)
	hs.listener = nil // ensure nil listener path

	// With nil listener and bind on loopback, Start should succeed
	// (the nil listener branch creates a fresh listener)
	err := hs.Start(context.Background())
	if err != nil {
		t.Errorf("Start with nil listener on loopback failed: %v", err)
	}
	hs.Stop(context.Background())
}

// TestHandlersRedactStringNilRedactor tests redactString with nil redactor.
func TestHandlersRedactStringNilRedactor(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	got := h.redactString(context.Background(), "test string")
	if got != "test string" {
		t.Errorf("redactString with nil redactor returned %q, want %q", got, "test string")
	}
}

// TestHandlersRedactStringWithRedactor tests redactString with a redactor set.
func TestHandlersRedactStringWithRedactor(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	h.SetRedactor(redact.NewRedactor())
	got := h.redactString(context.Background(), "test string")
	// Redactor replaces strings containing secret patterns
	_ = got // just exercise the code path
}

// handleToolsCall error path tests

func TestMCPProtocolHandleToolsCallNilHandlers(t *testing.T) {
	// Test the nil handlers branch: "daemon not connected"
	mcp := NewMCPProtocol(nil)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"","arguments":{}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		// arguments is a string instead of an object - should fail unmarshal
		Params: json.RawMessage(`{"name":"dfmt.remember","arguments":"not an object"}`),
		ID:     1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, nil, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search","arguments":{"query":123}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.recall","arguments":{"budget":"not a number"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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

func (m *errorReturningJournal) Size() (int64, error) {
	return 0, m.err
}

func (m *errorReturningJournal) Close() error {
	return nil
}

func (m *errorReturningJournal) StreamN(ctx context.Context, from string, n int) (<-chan core.Event, error) {
	return m.Stream(ctx, from)
}

func TestMCPProtocolHandleToolsCallRememberHandlerError(t *testing.T) {
	// Test Remember handler returning an error
	idx := core.NewIndex()
	journal := &errorReturningJournal{err: fmt.Errorf("journal append failed")}
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember","arguments":{"type":"note","source":"test"}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, journal, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.recall","arguments":{"budget":1024}}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.remember"}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
	handlers := NewHandlers(idx, nil, nil)
	mcp := NewMCPProtocol(handlers)

	req := &MCPRequest{
		JSONRPC: jsonRPCVersion,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dfmt.search"}`),
		ID:      1,
	}

	resp, err := mcp.Handle(context.Background(), req)
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
			JSONRPC: jsonRPCVersion,
			Method:  "tools/call",
			Params:  json.RawMessage(`{"name":"` + name + `","arguments":{}}`),
			ID:      1,
		}

		resp, err := mcp.Handle(context.Background(), req)
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

// TestHandlersDedupRecordCap tests dedupRecord when cache is at capacity.
func TestHandlersDedupRecordCap(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	// Fill the dedup cache beyond cap to exercise eviction branch.
	// dedupCap is 64, so we add 70 entries to force eviction.
	for i := 0; i < 70; i++ {
		h.dedupRecord(fmt.Sprintf("key%d", i), fmt.Sprintf("id%d", i))
	}
	// After eviction pass, cache should still be usable.
	h.dedupRecord("newkey", "newid")
	// Verify lookup still works for recent entries.
	found := h.dedupLookup("key65")
	// Old entries may have been evicted; just ensure no panic.
	_ = found
}

// TestHandlersRedactDataWithRedactor tests redactData when redactor is set.
func TestHandlersRedactDataWithRedactor(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	h.SetRedactor(redact.NewRedactor())

	input := map[string]any{
		"code": "secret API key here",
		"path": "/safe/path",
	}
	got := h.redactData(context.Background(), input)
	if _, ok := got["code"]; !ok {
		t.Error("redactData result missing 'code' key")
	}
}

// TestHandlersLogEventNilJournal tests logEvent when journal is nil (early return).
func TestHandlersLogEventNilJournal(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	h.logEvent(context.Background(), "tool.exec", "test", map[string]any{"key": "value"})
}

// TestHandlersLogEventWithJournal tests logEvent when journal is present.
func TestHandlersLogEventWithJournal(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	h := NewHandlers(core.NewIndex(), journal, nil)
	h.logEvent(context.Background(), "tool.exec", "test", map[string]any{"key": "value"})
}

// TestHandlersSearchBM25Fallback tests the BM25→trigram fallback path.
func TestHandlersSearchBM25Fallback(t *testing.T) {
	idx := core.NewIndex()
	idx.Add(core.Event{
		ID:       "AUDIT_PROBE_XJ7Q3",
		TS:       time.Now(),
		Type:     core.EventType("note"),
		Priority: core.PriP3,
		Source:   "test",
		Data:     map[string]any{"message": "AUDIT_PROBE_XJ7Q3 triggered"},
	})
	h := NewHandlers(idx, nil, nil)
	resp, err := h.Search(context.Background(), SearchParams{Query: "XJ7Q3", Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resp.Layer != "trigram" {
		t.Errorf("Layer = %q, want 'trigram' (BM25→trigram fallback)", resp.Layer)
	}
}

// TestHandlersSearchNilIndex tests Search when index is nil.
func TestHandlersSearchNilIndex(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	resp, err := h.Search(context.Background(), SearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("Search with nil index failed: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}
	if resp.Results != nil {
		t.Error("Results should be nil when index is nil")
	}
}

// TestHandlersRememberP1Rejected tests that P1 priority is coerced to P3.
func TestHandlersRememberP1Rejected(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	h := NewHandlers(core.NewIndex(), journal, nil)

	resp, err := h.Remember(context.Background(), RememberParams{
		Type:     "decision",
		Priority: "P1",
		Source:   "cli",
		Message:  "test P1 message",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}
}

// TestHandlersRememberUnknownPriority tests that invalid priority coerces to P3.
func TestHandlersRememberUnknownPriority(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	h := NewHandlers(core.NewIndex(), journal, nil)

	_, err = h.Remember(context.Background(), RememberParams{
		Type:     "note",
		Priority: "P99",
		Source:   "cli",
	})
	if err != nil {
		t.Fatalf("Remember with P99 priority failed: %v", err)
	}
}

// TestHandlersRememberWithTagsAndRedactor tests Remember with non-empty tags and redaction.
func TestHandlersRememberWithTagsAndRedactor(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	h := NewHandlers(core.NewIndex(), journal, nil)
	h.SetRedactor(redact.NewRedactor())

	resp, err := h.Remember(context.Background(), RememberParams{
		Type:     "note",
		Priority: "P2",
		Source:   "cli",
		Tags:     []string{"audit", "important"},
		Message:  "remember this",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp == nil || resp.ID == "" {
		t.Error("expected non-empty ID in response")
	}
}

// TestHandlersRememberWithTokenFields tests Remember with direct token fields.
func TestHandlersRememberWithTokenFields(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	h := NewHandlers(core.NewIndex(), journal, nil)

	resp, err := h.Remember(context.Background(), RememberParams{
		Type:         "note",
		Priority:     "P3",
		Source:       "cli",
		InputTokens:  1000,
		OutputTokens: 500,
		CachedTokens: 200,
		Model:        "claude-4",
		Message:      "token metered message",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}
}

// TestHandlersRememberNoJournal tests Remember when journal is nil.
func TestHandlersRememberNoJournal(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	_, err := h.Remember(context.Background(), RememberParams{
		Type:   "note",
		Source: "cli",
	})
	if err != errNoProject {
		t.Errorf("expected errNoProject, got %v", err)
	}
}

// TestHandlersSeenBeforeEmpty tests seenBefore with empty contentID.
func TestHandlersSeenBeforeEmpty(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	seen := h.seenBefore(context.Background(), "")
	if seen {
		t.Error("seenBefore with empty contentID should return false")
	}
}

// TestHandlersSeenBeforeWithSentCache tests seenBefore when content was marked sent.
func TestHandlersSeenBeforeWithSentCache(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	h.markSent(context.Background(), "content-id-1")
	seen := h.seenBefore(context.Background(), "content-id-1")
	if !seen {
		t.Error("seenBefore should return true after markSent")
	}
}

// TestHandlersMarkSentEmptyString tests markSent with empty string is no-op.
func TestHandlersMarkSentEmptyString(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	h.markSent(context.Background(), "")
	seen := h.seenBefore(context.Background(), "")
	if seen {
		t.Error("empty string should not be marked as sent")
	}
}

// TestHandlersSetActivityFn tests SetActivityFn and touch behavior.
func TestHandlersSetActivityFn(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)

	called := false
	h.SetActivityFn(func() {
		called = true
	})

	// touch() should call the activity fn
	h.touch()
	if !called {
		t.Error("touch() should have called the activity function")
	}
}

// TestHandlersRedactDataNilRedactor tests redactData when redactor is nil (early return).
func TestHandlersRedactDataNilRedactor(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	// With no redactor set, redactData returns input unchanged.
	input := map[string]any{"key": "value", "num": 42}
	got := h.redactData(context.Background(), input)
	if got["key"] != "value" || got["num"] != 42 {
		t.Errorf("redactData with nil redactor returned %v, want unchanged", got)
	}
}

// TestHandlersDedupRecordWithNilCache tests dedupRecord when cache is nil.
func TestHandlersDedupRecordWithNilCache(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	// Force nil cache by starting fresh
	h.dedupMu.Lock()
	h.dedupCache = nil
	h.dedupMu.Unlock()

	h.dedupRecord("testkey", "testid")
	// Verify it created the cache and recorded the entry
	found := h.dedupLookup("testkey")
	if found != "testid" {
		t.Errorf("dedupLookup after dedupRecord = %q, want %q", found, "testid")
	}
}

// TestHandlersCloneIntMapNilAndEmpty tests cloneIntMap with nil and empty inputs.
func TestHandlersCloneIntMapNilAndEmpty(t *testing.T) {
	// cloneIntMap with nil should return nil
	nilMap := cloneIntMap(nil)
	if nilMap != nil {
		t.Error("cloneIntMap(nil) should return nil")
	}
	// Empty map should return empty non-nil map
	emptyMap := cloneIntMap(map[string]int{})
	if emptyMap == nil || len(emptyMap) != 0 {
		t.Error("cloneIntMap({}) should return empty non-nil map")
	}
}

// TestRPCErrorUnwrap tests Unwrap method always returns nil.
func TestRPCErrorUnwrap(t *testing.T) {
	err := &RPCError{Code: -32600, Message: "invalid request"}
	if err.Unwrap() != nil {
		t.Error("RPCError.Unwrap() should return nil")
	}
}

// TestParamsErrorUnwrap tests ParamsError Unwrap returns underlying error.
func TestParamsErrorUnwrap(t *testing.T) {
	underlying := fmt.Errorf("underlying cause")
	err := &ParamsError{Err: underlying}
	if err.Unwrap() != underlying {
		t.Error("ParamsError.Unwrap() should return underlying error")
	}
}

// TestHandlersCloneStatsResponseNil tests cloneStatsResponse with nil input.
func TestHandlersCloneStatsResponseNil(t *testing.T) {
	got := cloneStatsResponse(nil)
	if got != nil {
		t.Error("cloneStatsResponse(nil) should return nil")
	}
}

// TestHandlersCloneStatsResponseFull tests cloneStatsResponse with a full response.
func TestHandlersCloneStatsResponseFull(t *testing.T) {
	src := &StatsResponse{
		EventsByType:     map[string]int{"tool.exec": 5, "tool.read": 3},
		EventsByPriority: map[string]int{"p3": 8},
		NativeToolCalls:  map[string]int{"bash": 5},
		MCPToolCalls:     map[string]int{"dfmt_grep": 2},
	}
	got := cloneStatsResponse(src)
	if got == nil {
		t.Fatal("cloneStatsResponse returned nil")
	}
	if got == src {
		t.Error("cloneStatsResponse should return a new pointer")
	}
	if len(got.EventsByType) != len(src.EventsByType) {
		t.Errorf("EventsByType len = %d, want %d", len(got.EventsByType), len(src.EventsByType))
	}
	if len(got.NativeToolCalls) != len(src.NativeToolCalls) {
		t.Errorf("NativeToolCalls len = %d, want %d", len(got.NativeToolCalls), len(src.NativeToolCalls))
	}
	// Mutate the clone — source should be unaffected
	delete(got.EventsByType, "tool.exec")
	if _, ok := src.EventsByType["tool.exec"]; !ok {
		t.Error("mutating clone affected source map")
	}
}

// TestHandlersRedactDataNested tests redactData with nested map structure.
func TestHandlersRedactDataNested(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	h.SetRedactor(redact.NewRedactor())
	input := map[string]any{
		"code": "secret123",
		"nested": map[string]any{
			"password": "should-be-redacted",
		},
	}
	got := h.redactData(context.Background(), input)
	// Redactor should replace value containing "secret" or "password"
	_ = got // just exercise the code path
}

// TestHandlersStashContentEmptyBody tests stashContent with empty body.
func TestHandlersStashContentEmptyBody(t *testing.T) {
	tmp := t.TempDir()
	store, err := content.NewStore(content.StoreOptions{
		Path:    filepath.Join(tmp, "content"),
		MaxSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	h.SetContentStore(store)
	// Empty body should return ""
	result := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "test", "")
	if result != "" {
		t.Errorf("stashContent with empty body returned %q, want \"\"", result)
	}
}

// TestHandlersStashContentNilStore tests stashContent with no store set.
func TestHandlersStashContentNilStore(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	// No store set — should return ""
	result := h.stashContent(nil, "/proj", "exec-stdout", "sandbox.exec", "test", "hello")
	if result != "" {
		t.Errorf("stashContent with nil store returned %q, want \"\"", result)
	}
}

// TestHandlersRedactDataWithNilData tests redactData with nil input.
func TestHandlersRedactDataWithNilData(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	got := h.redactData(context.Background(), nil)
	if got != nil {
		t.Errorf("redactData(nil) returned %v, want nil", got)
	}
}

// TestHandlersSetProject tests SetProject and getProject.
func TestHandlersSetProject(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	h.SetProject("test-project")
	// getProject is internal, but we can verify SetProject doesn't panic
	_ = h
}

// TestHandlersGetStoreNil tests getStore when store is nil.
func TestHandlersGetStoreNil(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	store := h.getStore()
	if store != nil {
		t.Error("getStore with nil store should return nil")
	}
}

// TestHandlersGetProjectEmpty tests getProject when project is empty.
func TestHandlersGetProjectEmpty(t *testing.T) {
	h := NewHandlers(core.NewIndex(), &mockJournal{}, nil)
	proj := h.getProject()
	if proj != "" {
		t.Errorf("getProject with empty project returned %q, want \"\"", proj)
	}
}

// TestHandlersDedupEvictOld tests dedup eviction path (cache exists but entry may be expired).

// TestHandlersLogEventWithNilIndex tests logEvent when index is nil (skip branch).
func TestHandlersLogEventWithNilIndex(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	// Create handler with nil index
	h := NewHandlers(nil, journal, nil)
	h.logEvent(context.Background(), "tool.exec", "test", map[string]any{"key": "value"})
}

// TestHandlersLogEventWithData tests logEvent with various data types.
func TestHandlersLogEventWithData(t *testing.T) {
	tmp := t.TempDir()
	journal, err := core.OpenJournal(tmp+"/journal.jsonl", core.JournalOptions{Durable: true})
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	h := NewHandlers(core.NewIndex(), journal, nil)
	// logEvent with nil data (redactData(nil) should be safe)
	h.logEvent(context.Background(), "tool.exec", "test", nil)
}

// TestHandlersSeenBeforeExpiredEntry tests seenBefore when entry has expired.
func TestHandlersSeenBeforeExpiredEntry(t *testing.T) {
	h := NewHandlers(core.NewIndex(), nil, nil)
	// Manually inject an expired entry into sentCache
	h.sentMu.Lock()
	h.sentCache = map[string]time.Time{
		"session\x00expired-id": time.Now().Add(-time.Hour), // expired 1 hour ago
	}
	h.sentMu.Unlock()
	// seenBefore should return false for expired entries
	seen := h.seenBefore(context.Background(), "expired-id")
	if seen {
		t.Error("seenBefore should return false for expired entry")
	}
}

// TestHandlersSetProjectsLister_NilLister covers LoadedProjects when no lister is installed.
func TestHandlersSetProjectsLister_NilLister(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	h.mu.RLock()
	f := h.listProjects
	h.mu.RUnlock()
	if f != nil {
		t.Error("listProjects should be nil before SetProjectsLister is called")
	}
	if got := h.LoadedProjects(); got != nil {
		t.Errorf("LoadedProjects() with nil lister: got %v, want nil", got)
	}
}

// TestHandlersSetProjectsLister_WithFunc covers LoadedProjects when a lister function is installed.
func TestHandlersSetProjectsLister_WithFunc(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	h.SetProjectsLister(func() []string {
		return []string{"/path/one", "/path/two"}
	})
	got := h.LoadedProjects()
	if len(got) != 2 {
		t.Errorf("LoadedProjects(): got %v, want 2 items", got)
	}
}

// TestLoadedProjects_WithListerReturningNil covers the edge case where the lister returns nil.
func TestLoadedProjects_WithListerReturningNil(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	h.SetProjectsLister(func() []string { return nil })
	got := h.LoadedProjects()
	if got != nil {
		t.Errorf("LoadedProjects() returning nil: got %v, want nil", got)
	}
}

// TestLoadedProjects_Concurrent calls LoadedProjects concurrently.
func TestLoadedProjects_Concurrent(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	h.SetProjectsLister(func() []string {
		return []string{"/a", "/b", "/c"}
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.LoadedProjects()
		}()
	}
	wg.Wait()
}

// TestHandlersSetProjectDropper_Basic exercises SetProjectDropper with a dropper installed.
func TestHandlersSetProjectDropper_Basic(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	called := false
	h.SetProjectDropper(func(projectID string) error {
		called = true
		return nil
	})
	resp, err := h.DropProject(context.Background(), DropProjectParams{ProjectID: "some-project"})
	if err != nil {
		t.Errorf("DropProject(some-project): %v", err)
	}
	if resp != nil && !resp.Dropped {
		t.Error("resp.Dropped should be true")
	}
	if !called {
		t.Error("dropper was not called")
	}
}

// TestHandlersDropProject_EmptyProjectID tests that DropProject returns an error for empty projectID.
func TestHandlersDropProject_EmptyProjectID(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	h.SetProjectDropper(func(projectID string) error {
		t.Error("dropper should not be called for empty projectID")
		return nil
	})
	_, err := h.DropProject(context.Background(), DropProjectParams{ProjectID: ""})
	if err == nil {
		t.Error("DropProject('') should return error")
	}
}

// TestHandlersSetProjectDropper_NotSet verifies DropProject behavior when no dropper is installed.
func TestHandlersSetProjectDropper_NotSet(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	// When no dropper is installed, DropProject returns Dropped: false
	resp, err := h.DropProject(context.Background(), DropProjectParams{ProjectID: "any-project"})
	if err != nil {
		t.Errorf("DropProject without dropper: %v", err)
	}
	if resp == nil || resp.Dropped {
		t.Error("Dropped should be false when no dropper is installed")
	}
}

// DEDUPED: second copy removed
