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
