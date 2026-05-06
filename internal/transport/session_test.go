package transport

import (
	"context"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/redact"
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

// TestHandlersRedactorForRoutesThroughBundle proves the per-call
// redactor selection: when a ResourceFetcher is installed, every
// redactor-touching path (redactString / redactData / redactEventForRender)
// reads bundle.Redactor — the project's own redact.yaml — instead of
// the default project's redactor on the Handlers struct.
//
// Without this, project A's secrets-pattern would scrub project B's
// tool output. A pattern like "\bpetya\b" added to A/.dfmt/redact.yaml
// would silently match the same word in B's responses; conversely B's
// own pattern would NOT match because B's redactor was never loaded.
func TestHandlersRedactorForRoutesThroughBundle(t *testing.T) {
	defaultR := redact.NewRedactor()
	if err := defaultR.AddPattern("default", `DEFAULT_SECRET`, "[D]"); err != nil {
		t.Fatalf("default AddPattern: %v", err)
	}
	perCallR := redact.NewRedactor()
	if err := perCallR.AddPattern("percall", `PERCALL_SECRET`, "[P]"); err != nil {
		t.Fatalf("percall AddPattern: %v", err)
	}

	h := &Handlers{}
	h.SetRedactor(defaultR) // default project's redactor — must NOT be used when fetcher is set
	h.SetResourceFetcher(func(string) (Bundle, error) {
		return Bundle{Redactor: perCallR, ProjectPath: "/proj/per-call"}, nil
	})

	ctx := WithProjectID(context.Background(), "/proj/per-call")

	// Per-call pattern fires; default pattern does NOT.
	got := h.redactString(ctx, "hello PERCALL_SECRET and DEFAULT_SECRET world")
	if !strings.Contains(got, "[P]") {
		t.Errorf("per-call pattern did not fire on bundle redactor: %q", got)
	}
	if strings.Contains(got, "[D]") {
		t.Errorf("default pattern fired on per-call ctx — redactor leaked across projects: %q", got)
	}
	if !strings.Contains(got, "DEFAULT_SECRET") {
		t.Errorf("default secret should be untouched in per-call ctx (per-call redactor doesn't know it): %q", got)
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
