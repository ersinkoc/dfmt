package transport

import (
	"context"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestHandlersSetProjectStampsRememberEvents(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)
	h.SetProject("D:\\Codebox\\PROJECTS\\DFMT")

	if _, err := h.Remember(context.Background(), RememberParams{
		Type:   "note",
		Source: "test",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	if len(journal.events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(journal.events))
	}
	if got := journal.events[0].Project; got != "D:\\Codebox\\PROJECTS\\DFMT" {
		t.Errorf("Event.Project = %q, want the project set via SetProject", got)
	}
}

func TestHandlersProjectEmptyWhenNotSet(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)

	if _, err := h.Remember(context.Background(), RememberParams{
		Type:   "note",
		Source: "test",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	if got := journal.events[0].Project; got != "" {
		t.Errorf("Event.Project = %q, want empty when SetProject not called", got)
	}
}

func TestHandlersSetProjectIsOverridable(t *testing.T) {
	idx := core.NewIndex()
	journal := &mockJournal{}
	h := NewHandlers(idx, journal, nil)

	h.SetProject("proj-a")
	if _, err := h.Remember(context.Background(), RememberParams{Type: "note", Source: "t"}); err != nil {
		t.Fatalf("Remember #1: %v", err)
	}
	h.SetProject("proj-b")
	if _, err := h.Remember(context.Background(), RememberParams{Type: "note", Source: "t"}); err != nil {
		t.Fatalf("Remember #2: %v", err)
	}

	if len(journal.events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(journal.events))
	}
	if journal.events[0].Project != "proj-a" {
		t.Errorf("first event Project = %q, want proj-a", journal.events[0].Project)
	}
	if journal.events[1].Project != "proj-b" {
		t.Errorf("second event Project = %q, want proj-b", journal.events[1].Project)
	}
}
