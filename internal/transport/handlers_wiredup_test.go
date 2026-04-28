package transport

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/content"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// newWireDedupHandlers returns a Handlers with an on-disk content store and
// a programmable stub sandbox. Both are needed: stashContent only generates
// a content_id when the store is wired, and the wire-dedup short-circuit
// only fires when stashContent's content_id has been emitted before.
func newWireDedupHandlers(t *testing.T, sb *stubSandbox) *Handlers {
	t.Helper()
	store, err := content.NewStore(content.StoreOptions{
		Path:    filepath.Join(t.TempDir(), "content"),
		MaxSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := core.NewIndex()
	h := NewHandlers(idx, &mockJournal{}, sb)
	h.SetContentStore(store)
	return h
}

// TestWireDedup_Read_RepeatStripsPayload pins the ADR-0009 contract on the
// happy path: identical bytes read twice make the second response carry
// only metadata + the sentinel summary, not the body.
func TestWireDedup_Read_RepeatStripsPayload(t *testing.T) {
	body := "package main\nfunc main() {}\n"
	sb := &stubSandbox{
		readResp: sandbox.ReadResp{
			Content:    body,
			RawContent: body,
			Summary:    "2 lines",
			Size:       int64(len(body)),
			ReadBytes:  int64(len(body)),
		},
	}
	h := newWireDedupHandlers(t, sb)

	first, err := h.Read(context.Background(), ReadParams{Path: "main.go", Return: "auto"})
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if first.Content == "" {
		t.Fatal("first Read should carry Content")
	}
	if first.ContentID == "" {
		t.Fatal("first Read should carry ContentID")
	}

	second, err := h.Read(context.Background(), ReadParams{Path: "main.go", Return: "auto"})
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if second.Content != "" {
		t.Errorf("second Read should drop Content; got %q", second.Content)
	}
	if len(second.Matches) != 0 {
		t.Errorf("second Read should drop Matches; got %d", len(second.Matches))
	}
	if second.Summary != sentUnchangedSummary {
		t.Errorf("second Read Summary = %q, want %q", second.Summary, sentUnchangedSummary)
	}
	if second.ContentID != first.ContentID {
		t.Errorf("second Read ContentID = %q, want %q", second.ContentID, first.ContentID)
	}
	if second.Size != first.Size || second.ReadBytes != first.ReadBytes {
		t.Errorf("metadata fields must survive dedup; got Size=%d ReadBytes=%d",
			second.Size, second.ReadBytes)
	}
}

// TestWireDedup_Read_RawBypasses verifies the agent's escape hatch: passing
// Return:"raw" forces the daemon to re-emit even if it has seen the
// content_id before.
func TestWireDedup_Read_RawBypasses(t *testing.T) {
	body := "shared content\n"
	sb := &stubSandbox{
		readResp: sandbox.ReadResp{
			Content:    body,
			RawContent: body,
			Size:       int64(len(body)),
			ReadBytes:  int64(len(body)),
		},
	}
	h := newWireDedupHandlers(t, sb)

	if _, err := h.Read(context.Background(), ReadParams{Path: "f.txt", Return: "auto"}); err != nil {
		t.Fatalf("first Read: %v", err)
	}
	second, err := h.Read(context.Background(), ReadParams{Path: "f.txt", Return: "raw"})
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if second.Content == "" {
		t.Errorf("Return=raw must bypass dedup; Content was empty")
	}
	if second.Summary == sentUnchangedSummary {
		t.Errorf("Return=raw must NOT carry the unchanged sentinel summary")
	}
}

// TestWireDedup_Exec_DifferentBodySendsFull guards the negative path: when
// stdout differs between calls, the second response must be full payload,
// not stripped. Otherwise a single Exec would forever block its content_id
// space.
func TestWireDedup_Exec_DifferentBodySendsFull(t *testing.T) {
	sb := &stubSandbox{
		execResp: sandbox.ExecResp{
			Exit:      0,
			Stdout:    "first run output\n",
			RawStdout: "first run output\n",
		},
	}
	h := newWireDedupHandlers(t, sb)

	first, err := h.Exec(context.Background(), ExecParams{Code: "echo a", Lang: "bash", Return: "auto"})
	if err != nil {
		t.Fatalf("first Exec: %v", err)
	}

	// Switch the stub's stdout — this simulates a non-deterministic command.
	sb.execResp.Stdout = "second run output\n"
	sb.execResp.RawStdout = "second run output\n"

	second, err := h.Exec(context.Background(), ExecParams{Code: "echo a", Lang: "bash", Return: "auto"})
	if err != nil {
		t.Fatalf("second Exec: %v", err)
	}
	if second.Stdout == "" {
		t.Error("different body must produce a full response, not a stripped one")
	}
	if second.ContentID == first.ContentID {
		t.Errorf("different body must produce different ContentIDs; both were %q", first.ContentID)
	}
}

// TestSeenBefore_EmptyIDIsNeverSeen verifies the (no content store / empty
// body) path doesn't accidentally dedupe — every empty-ID response must be
// emitted in full.
func TestSeenBefore_EmptyIDIsNeverSeen(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	ctx := context.Background()
	if h.seenBefore(ctx, "") {
		t.Error("seenBefore(\"\") must be false")
	}
	h.markSent(ctx, "") // must not panic, must not pollute cache
	h.sentMu.Lock()
	defer h.sentMu.Unlock()
	if len(h.sentCache) != 0 {
		t.Errorf("markSent(\"\") leaked into cache: %d entries", len(h.sentCache))
	}
}

// TestMarkSent_FIFOEvictsOldest verifies the LRU bound. Inserting > sentCap
// distinct IDs must keep the cache at or below sentCap, with the oldest
// IDs forgotten. Uses an empty session ID so all keys land in the same
// bucket — exercises the cap on the default-session path.
func TestMarkSent_FIFOEvictsOldest(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	ctx := context.Background()
	first := "first-content-id"
	h.markSent(ctx, first)
	for i := 0; i < sentCap; i++ {
		h.markSent(ctx, "filler-"+itoa(i))
	}
	if h.seenBefore(ctx, first) {
		t.Error("first ID should have been evicted by FIFO pressure")
	}
	h.sentMu.Lock()
	defer h.sentMu.Unlock()
	if len(h.sentCache) > sentCap {
		t.Errorf("sentCache size %d exceeds cap %d", len(h.sentCache), sentCap)
	}
}

// TestWireDedup_SessionIsolation pins the ADR-0011 contract: two distinct
// sessions reading the same body must each see a full payload on their
// FIRST read, and only see the dedup short-circuit on their OWN second
// read. Without the per-session keying, session B would receive the
// "(unchanged)" sentinel for content it has never seen.
func TestWireDedup_SessionIsolation(t *testing.T) {
	body := "shared content across sessions\n"
	sb := &stubSandbox{
		readResp: sandbox.ReadResp{
			Content:    body,
			RawContent: body,
			Size:       int64(len(body)),
			ReadBytes:  int64(len(body)),
		},
	}
	h := newWireDedupHandlers(t, sb)

	ctxA := WithSessionID(context.Background(), "session-A")
	ctxB := WithSessionID(context.Background(), "session-B")

	a1, err := h.Read(ctxA, ReadParams{Path: "shared.txt", Return: "auto"})
	if err != nil {
		t.Fatalf("session A first read: %v", err)
	}
	if a1.Content == "" {
		t.Fatal("session A first read should carry Content")
	}

	a2, err := h.Read(ctxA, ReadParams{Path: "shared.txt", Return: "auto"})
	if err != nil {
		t.Fatalf("session A second read: %v", err)
	}
	if a2.Content != "" {
		t.Errorf("session A second read should be deduped (Content empty); got %q", a2.Content)
	}
	if a2.Summary != sentUnchangedSummary {
		t.Errorf("session A second read Summary = %q, want %q", a2.Summary, sentUnchangedSummary)
	}

	b1, err := h.Read(ctxB, ReadParams{Path: "shared.txt", Return: "auto"})
	if err != nil {
		t.Fatalf("session B first read: %v", err)
	}
	if b1.Content == "" {
		t.Errorf("session B first read should carry Content (session B has never seen this body); got empty")
	}
	if b1.Summary == sentUnchangedSummary {
		t.Errorf("session B should NOT receive the (unchanged) sentinel on its first read")
	}
	if b1.ContentID != a1.ContentID {
		t.Errorf("ContentID should be stable across sessions; A=%q B=%q", a1.ContentID, b1.ContentID)
	}
}

// TestWireDedup_NoSessionFallsBackToDefault: paths that haven't been threaded
// with WithSessionID still dedupe (under a single shared "default" bucket).
// Preserves behaviour for every test and any pre-ADR-0011 caller that
// hasn't been migrated yet.
func TestWireDedup_NoSessionFallsBackToDefault(t *testing.T) {
	body := "default-bucket content\n"
	sb := &stubSandbox{
		readResp: sandbox.ReadResp{
			Content:    body,
			RawContent: body,
			Size:       int64(len(body)),
			ReadBytes:  int64(len(body)),
		},
	}
	h := newWireDedupHandlers(t, sb)
	ctx := context.Background() // no session ID attached

	first, err := h.Read(ctx, ReadParams{Path: "x.txt", Return: "auto"})
	if err != nil || first.Content == "" {
		t.Fatalf("first read: err=%v content=%q", err, first.Content)
	}
	second, err := h.Read(ctx, ReadParams{Path: "x.txt", Return: "auto"})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.Content != "" {
		t.Errorf("default bucket should still dedupe; got %q", second.Content)
	}
}
