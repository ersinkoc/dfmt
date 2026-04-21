package retrieve

import (
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestMarkdownRendererRenderTaskCreate(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtTaskCreate,
			Priority: core.PriP2,
			Actor:    "user",
			Data:     map[string]any{"message": "Created task"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p2:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Tasks") {
		t.Error("Render doesn't contain 'Tasks' section for EvtTaskCreate")
	}
}

func TestMarkdownRendererRenderGitCommit(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtGitCommit,
			Priority: core.PriP2,
			Actor:    "user",
			Data:     map[string]any{"message": "Commit message"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p2:1"},
	}

	output := r.Render(snap)
	// Git commits are formatted as "Git Commit" (capitalized, dot replaced)
	if !strings.Contains(output, "Git") {
		t.Error("Render doesn't contain 'Git' section for git events")
	}
}

func TestMarkdownRendererRenderGitPush(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtGitPush,
			Priority: core.PriP2,
			Actor:    "user",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p2:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Git") {
		t.Error("Render doesn't contain 'Git' section for EvtGitPush")
	}
}

func TestMarkdownRendererRenderGitCheckout(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtGitCheckout,
			Priority: core.PriP2,
			Actor:    "user",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p2:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Git") {
		t.Error("Render doesn't contain 'Git' section for EvtGitCheckout")
	}
}

func TestMarkdownRendererRenderMCPCall(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtMCPCall,
			Priority: core.PriP4,
			Actor:    "user",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p4:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Other Events") {
		t.Error("Render doesn't contain 'Other Events' section for EvtMCPCall")
	}
}

func TestMarkdownRendererRenderNote(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtNote,
			Priority: core.PriP4,
			Actor:    "user",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p4:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Other Events") {
		t.Error("Render doesn't contain 'Other Events' section for EvtNote")
	}
}

func TestMarkdownRendererRenderPrompt(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtPrompt,
			Priority: core.PriP4,
			Actor:    "user",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p4:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Other Events") {
		t.Error("Render doesn't contain 'Other Events' section for EvtPrompt")
	}
}

func TestMarkdownRendererRenderMultipleEventsPerType(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
			Actor:    "user1",
			Data:     map[string]any{"message": "First decision"},
		},
		{
			ID:       "event2",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
			Actor:    "user2",
			Data:     map[string]any{"message": "Second decision"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  200,
		TierOrder: []string{"p1:2"},
	}

	output := r.Render(snap)
	// Should contain both decisions
	if !strings.Contains(output, "First decision") {
		t.Error("Render doesn't contain first decision message")
	}
	if !strings.Contains(output, "Second decision") {
		t.Error("Render doesn't contain second decision message")
	}
}

func TestMarkdownRendererRenderEventWithTags(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
			Actor:    "user",
			Tags:     []string{"important", "bugfix"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p1:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Tags:") {
		t.Error("Render doesn't contain 'Tags:' for events with tags")
	}
	if !strings.Contains(output, "important") {
		t.Error("Render doesn't contain tag 'important'")
	}
}

func TestMarkdownRendererRenderEventWithPath(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtFileEdit,
			Priority: core.PriP3,
			Source:   core.SrcFSWatch,
			Data:     map[string]any{"path": "/test/file.go"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p3:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "/test/file.go") {
		t.Error("Render doesn't contain path from event data")
	}
}

func TestMarkdownRendererRenderEventWithMessage(t *testing.T) {
	r := NewMarkdownRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtError,
			Priority: core.PriP2,
			Actor:    "user",
			Data:     map[string]any{"message": "Something went wrong"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p2:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "Something went wrong") {
		t.Error("Render doesn't contain message from event data")
	}
}
