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
