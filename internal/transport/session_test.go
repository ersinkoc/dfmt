package transport

import (
	"context"
	"testing"
)

// TestProjectIDContext verifies the WithProjectID / ProjectIDFrom round-trip
// and the empty-ctx default.
func TestProjectIDContext(t *testing.T) {
	if got := ProjectIDFrom(context.Background()); got != "" {
		t.Errorf("empty ctx: got %q, want %q", got, "")
	}
	ctx := WithProjectID(context.Background(), "/abs/proj")
	if got := ProjectIDFrom(ctx); got != "/abs/proj" {
		t.Errorf("after WithProjectID: got %q, want %q", got, "/abs/proj")
	}
	// Empty value is stored as-is — callers that pass "" must handle
	// the absence explicitly.
	ctx2 := WithProjectID(context.Background(), "")
	if got := ProjectIDFrom(ctx2); got != "" {
		t.Errorf("WithProjectID(\"\") round-trip: got %q, want %q", got, "")
	}
}

// TestHandlersResolveBundleFallsBackToDirectFields verifies that without
// a fetcher installed, resolveBundle returns a Bundle synthesized from
// the Handlers' direct fields (legacy single-project mode). This is the
// invariant that keeps pre-Phase-2 behavior intact.
func TestHandlersResolveBundleFallsBackToDirectFields(t *testing.T) {
	h := &Handlers{}
	h.SetProject("/proj/A")
	// journal/index/redactor/store/sandbox stay nil — the bundle should
	// reflect that exact state, not panic.

	got, err := h.resolveBundle(context.Background())
	if err != nil {
		t.Fatalf("resolveBundle: %v", err)
	}
	if got.ProjectPath != "/proj/A" {
		t.Errorf("ProjectPath: got %q, want %q", got.ProjectPath, "/proj/A")
	}
	if got.Journal != nil || got.Index != nil {
		t.Errorf("expected nil Journal/Index in degraded direct-field mode; got Journal=%v Index=%v",
			got.Journal, got.Index)
	}
}

// TestHandlersResolveBundleUsesFetcherWhenSet verifies that once a
// ResourceFetcher is installed, resolveBundle delegates to it and
// passes through the ctx project_id. The fetcher's return value
// outranks direct fields entirely — that's the global-daemon hook.
func TestHandlersResolveBundleUsesFetcherWhenSet(t *testing.T) {
	h := &Handlers{}
	h.SetProject("/legacy/default") // should be ignored once fetcher is set

	var seenPID string
	wantBundle := Bundle{ProjectPath: "/proj/from-fetcher"}
	h.SetResourceFetcher(func(pid string) (Bundle, error) {
		seenPID = pid
		return wantBundle, nil
	})

	ctx := WithProjectID(context.Background(), "/proj/per-call")
	got, err := h.resolveBundle(ctx)
	if err != nil {
		t.Fatalf("resolveBundle: %v", err)
	}
	if seenPID != "/proj/per-call" {
		t.Errorf("fetcher saw pid %q, want %q", seenPID, "/proj/per-call")
	}
	if got.ProjectPath != "/proj/from-fetcher" {
		t.Errorf("ProjectPath: got %q, want %q (fetcher result should win)",
			got.ProjectPath, "/proj/from-fetcher")
	}
}

// TestHandlersRememberRoutesThroughFetcher proves the user-visible wire-
// up: when a ResourceFetcher is installed, Handlers.Remember actually
// reads its journal/index from the fetcher's Bundle, not from the
// direct fields. This is the smoke test that the global-daemon dispatch
// path (commit 4d) will rely on for cross-project routing — without it
// the fetcher would be a dead seam.
func TestHandlersRememberRoutesThroughFetcher(t *testing.T) {
	// Default fields — these MUST NOT be touched once a fetcher is set.
	defaultJournal := &mockJournal{}

	// Per-call fields — Remember must write here.
	perCallJournal := &mockJournal{}

	h := &Handlers{}
	h.journal = defaultJournal
	h.SetProject("/legacy/default")

	h.SetResourceFetcher(func(pid string) (Bundle, error) {
		if pid != "/proj/per-call" {
			t.Fatalf("fetcher called with unexpected pid %q", pid)
		}
		return Bundle{
			Journal:     perCallJournal,
			ProjectPath: pid,
		}, nil
	})

	ctx := WithProjectID(context.Background(), "/proj/per-call")
	if _, err := h.Remember(ctx, RememberParams{
		Type:   "note",
		Source: "test",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	if len(defaultJournal.events) != 0 {
		t.Errorf("default journal got %d events; fetcher should have routed them away", len(defaultJournal.events))
	}
	if len(perCallJournal.events) != 1 {
		t.Errorf("per-call journal got %d events, want 1", len(perCallJournal.events))
	}
	if got := perCallJournal.events[0].Project; got != "/proj/per-call" {
		t.Errorf("event.Project: got %q, want %q", got, "/proj/per-call")
	}
}

// TestHandlersResolveBundleFetcherErrorPropagates verifies that a
// fetcher error short-circuits resolveBundle without silently falling
// back to direct fields. Global-daemon callers want to see "unknown
// project" rather than getting served stale default-project state.
func TestHandlersResolveBundleFetcherErrorPropagates(t *testing.T) {
	h := &Handlers{}
	h.SetProject("/legacy/default")
	h.SetResourceFetcher(func(string) (Bundle, error) {
		return Bundle{}, context.Canceled // any non-nil error
	})

	if _, err := h.resolveBundle(context.Background()); err == nil {
		t.Error("expected fetcher error to propagate; got nil")
	}
}

// TestHandlersGetProjectFor checks the resolution order: ctx wins when set,
// h.project is the fallback.
func TestHandlersGetProjectFor(t *testing.T) {
	h := &Handlers{}
	h.SetProject("/legacy/default")

	// 1. No ctx pid → fallback to h.project.
	if got := h.getProjectFor(context.Background()); got != "/legacy/default" {
		t.Errorf("empty ctx: got %q, want %q", got, "/legacy/default")
	}
	// 2. Ctx pid set → wins.
	ctx := WithProjectID(context.Background(), "/per-call/proj")
	if got := h.getProjectFor(ctx); got != "/per-call/proj" {
		t.Errorf("ctx pid set: got %q, want %q", got, "/per-call/proj")
	}
	// 3. Ctx empty pid → fallback (empty stored value treated as "no override").
	ctx2 := WithProjectID(context.Background(), "")
	if got := h.getProjectFor(ctx2); got != "/legacy/default" {
		t.Errorf("ctx pid \"\": got %q, want %q", got, "/legacy/default")
	}
	// 4. h.project unset and no ctx pid → empty.
	h2 := &Handlers{}
	if got := h2.getProjectFor(context.Background()); got != "" {
		t.Errorf("both empty: got %q, want %q", got, "")
	}
}
