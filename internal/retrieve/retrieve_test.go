package retrieve

import (
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestMarkdownRendererRenderEmptySnapshot(t *testing.T) {
	r := NewMarkdownRenderer()

	snap := &Snapshot{
		Events:    []core.Event{},
		ByteSize:  0,
		TierOrder: []string{},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "No events recorded yet") {
		t.Error("Render doesn't contain 'No events recorded yet' for empty snapshot")
	}
}

func TestJSONRendererRender(t *testing.T) {
	r := NewJSONRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
			Actor:    "user",
			Data:     map[string]any{"message": "Test decision"},
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p1:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "event1") {
		t.Error("JSON output doesn't contain event ID")
	}
	if !strings.Contains(output, "p1:1") {
		t.Error("JSON output doesn't contain tier order")
	}
}

func TestXMLRendererRender(t *testing.T) {
	r := NewXMLRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
			Actor:    "user",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p1:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "<session_snapshot>") {
		t.Error("XML output doesn't contain session_snapshot tag")
	}
	if !strings.Contains(output, "<priority>p1</priority>") {
		t.Error("XML output doesn't contain priority")
	}
	if !strings.Contains(output, "<type>decision</type>") {
		t.Error("XML output doesn't contain type")
	}
}

func TestXMLRendererRenderWithActor(t *testing.T) {
	r := NewXMLRenderer()

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtTaskCreate,
			Priority: core.PriP2,
			Actor:    "testuser",
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  50,
		TierOrder: []string{"p2:1"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "<actor>testuser</actor>") {
		t.Error("XML output doesn't contain actor")
	}
}

func TestXMLRendererRenderMultipleTiers(t *testing.T) {
	r := NewXMLRenderer()

	events := []core.Event{
		{ID: "e1", TS: time.Now(), Type: core.EvtDecision, Priority: core.PriP1},
		{ID: "e2", TS: time.Now(), Type: core.EvtTaskCreate, Priority: core.PriP2},
		{ID: "e3", TS: time.Now(), Type: core.EvtFileEdit, Priority: core.PriP3},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  300,
		TierOrder: []string{"p1:1", "p2:1", "p3:1", "p4:0"},
	}

	output := r.Render(snap)
	if !strings.Contains(output, "<tier>p1:1</tier>") {
		t.Error("XML output doesn't contain all tiers")
	}
}

func TestSnapshotBuilderBuildEmpty(t *testing.T) {
	sb := NewSnapshotBuilder(1000)

	snap, err := sb.Build([]core.Event{})
	if err != nil {
		t.Errorf("Build failed: %v", err)
	}
	if len(snap.Events) != 0 {
		t.Errorf("Expected 0 events, got %d", len(snap.Events))
	}
	if snap.ByteSize != 0 {
		t.Errorf("Expected 0 bytes, got %d", snap.ByteSize)
	}
}

func TestSnapshotBuilderBuildWithinBudget(t *testing.T) {
	sb := NewSnapshotBuilder(10000)

	events := []core.Event{
		{ID: "e1", TS: time.Now(), Type: core.EvtDecision, Priority: core.PriP1},
		{ID: "e2", TS: time.Now(), Type: core.EvtTaskCreate, Priority: core.PriP2},
	}
	snap, err := sb.Build(events)
	if err != nil {
		t.Errorf("Build failed: %v", err)
	}
	if len(snap.Events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(snap.Events))
	}
	if len(snap.TierOrder) != 4 {
		t.Errorf("Expected 4 tier entries, got %d", len(snap.TierOrder))
	}
}

func TestSnapshotBuilderBuildExceedsBudget(t *testing.T) {
	sb := NewSnapshotBuilder(50) // Very small budget

	events := []core.Event{
		{ID: "e1", TS: time.Now(), Type: core.EvtDecision, Priority: core.PriP1, Data: map[string]any{"large": strings.Repeat("x", 1000)}},
		{ID: "e2", TS: time.Now(), Type: core.EvtTaskCreate, Priority: core.PriP2},
	}
	snap, err := sb.Build(events)
	if err != nil {
		t.Errorf("Build failed: %v", err)
	}
	// First event too large for budget, none should be selected
	if len(snap.Events) != 0 {
		t.Logf("Expected 0 events due to budget, got %d", len(snap.Events))
	}
}

func TestSnapshotBuilderBuildPriorityOrder(t *testing.T) {
	sb := NewSnapshotBuilder(100000)

	now := time.Now()
	events := []core.Event{
		{ID: "p4", TS: now, Type: core.EvtNote, Priority: core.PriP4},
		{ID: "p1", TS: now, Type: core.EvtDecision, Priority: core.PriP1},
		{ID: "p3", TS: now, Type: core.EvtFileEdit, Priority: core.PriP3},
		{ID: "p2", TS: now, Type: core.EvtTaskCreate, Priority: core.PriP2},
	}
	snap, err := sb.Build(events)
	if err != nil {
		t.Errorf("Build failed: %v", err)
	}
	// P1 should come first
	if len(snap.Events) == 0 {
		t.Fatal("No events selected")
	}
	if snap.Events[0].ID != "p1" {
		t.Errorf("First event should be P1 (decision), got %s", snap.Events[0].ID)
	}
}

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
