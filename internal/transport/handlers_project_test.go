package transport

import (
	"context"
	"errors"
	"strings"
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

// Regression: when dfmt mcp runs outside any project (no .dfmt/ or .git/ in
// the cwd ancestry), runMCP constructs handlers with a nil journal/index so
// that no project state leaks into the user home dir. The four memory tools
// (Remember/Recall/Stats/Stream) must surface a clear "no project" error
// instead of nil-deref panicking. Sandbox tools (exec/read/fetch/glob/grep/
// edit/write) are intentionally NOT covered here — they don't touch the
// journal and continue working in degraded mode.
func TestHandlersDegradedModeNoProject(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	t.Run("Remember", func(t *testing.T) {
		_, err := h.Remember(context.Background(), RememberParams{Type: "note", Source: "t"})
		if !errors.Is(err, errNoProject) {
			t.Fatalf("Remember err = %v, want errNoProject", err)
		}
		if !strings.Contains(err.Error(), "no dfmt project") {
			t.Errorf("error message = %q, want it to mention 'no dfmt project'", err.Error())
		}
	})

	t.Run("Recall", func(t *testing.T) {
		_, err := h.Recall(context.Background(), RecallParams{Budget: 1024})
		if !errors.Is(err, errNoProject) {
			t.Fatalf("Recall err = %v, want errNoProject", err)
		}
	})

	t.Run("Stats", func(t *testing.T) {
		_, err := h.Stats(context.Background(), StatsParams{})
		if !errors.Is(err, errNoProject) {
			t.Fatalf("Stats err = %v, want errNoProject", err)
		}
	})

	t.Run("Stream", func(t *testing.T) {
		_, err := h.Stream(context.Background(), StreamParams{})
		if !errors.Is(err, errNoProject) {
			t.Fatalf("Stream err = %v, want errNoProject", err)
		}
	})
}
