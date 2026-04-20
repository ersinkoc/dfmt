package retrieve

import (
	"strings"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestNewSnapshotBuilder(t *testing.T) {
	sb := NewSnapshotBuilder(4096)
	if sb == nil {
		t.Fatal("NewSnapshotBuilder returned nil")
	}
	if sb.budget != 4096 {
		t.Errorf("budget = %d, want 4096", sb.budget)
	}
}

func TestSnapshotBuilderBuild(t *testing.T) {
	sb := NewSnapshotBuilder(8192)

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
		},
		{
			ID:       "event2",
			TS:       time.Now(),
			Type:     core.EvtTaskCreate,
			Priority: core.PriP2,
		},
	}

	snap, err := sb.Build(events)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if snap == nil {
		t.Fatal("Build returned nil")
	}
	if len(snap.Events) != 2 {
		t.Errorf("len(Events) = %d, want 2", len(snap.Events))
	}
	if snap.ByteSize == 0 {
		t.Error("ByteSize is 0")
	}
	if len(snap.TierOrder) != 4 {
		t.Errorf("len(TierOrder) = %d, want 4", len(snap.TierOrder))
	}
}

func TestSnapshotBuilderBuildEmpty(t *testing.T) {
	sb := NewSnapshotBuilder(4096)

	snap, err := sb.Build(nil)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(snap.Events) != 0 {
		t.Errorf("len(Events) = %d, want 0", len(snap.Events))
	}
}

func TestSnapshotBuilderBuildBudgetExceeded(t *testing.T) {
	sb := NewSnapshotBuilder(50) // Very small budget

	events := []core.Event{
		{
			ID:       "event1",
			TS:       time.Now(),
			Type:     core.EvtDecision,
			Priority: core.PriP1,
		},
		{
			ID:       "event2",
			TS:       time.Now(),
			Type:     core.EvtTaskCreate,
			Priority: core.PriP2,
		},
	}

	snap, err := sb.Build(events)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	// With tiny budget, at most one event should fit
	if len(snap.Events) > 1 {
		t.Errorf("len(Events) = %d, want <= 1 with budget 50", len(snap.Events))
	}
}

func TestSnapshotBuilderGroupByTier(t *testing.T) {
	sb := NewSnapshotBuilder(4096)

	events := []core.Event{
		{ID: "1", Type: core.EvtDecision, Priority: core.PriP1},
		{ID: "2", Type: core.EvtTaskDone, Priority: core.PriP1},
		{ID: "3", Type: core.EvtGitCommit, Priority: core.PriP2},
		{ID: "4", Type: core.EvtFileEdit, Priority: core.PriP3},
	}

	tiered := sb.groupByTier(events)

	p1Count := len(tiered["p1"])
	p2Count := len(tiered["p2"])
	p3Count := len(tiered["p3"])
	p4Count := len(tiered["p4"])

	if p1Count != 2 {
		t.Errorf("len(p1) = %d, want 2", p1Count)
	}
	if p2Count != 1 {
		t.Errorf("len(p2) = %d, want 1", p2Count)
	}
	if p3Count != 1 {
		t.Errorf("len(p3) = %d, want 1", p3Count)
	}
	if p4Count != 0 {
		t.Errorf("len(p4) = %d, want 0", p4Count)
	}
}

func TestSnapshotBuilderEventSize(t *testing.T) {
	sb := NewSnapshotBuilder(4096)

	e := core.Event{
		ID:       "test-id",
		TS:       time.Now(),
		Type:     core.EvtDecision,
		Priority: core.PriP1,
	}

	size := sb.eventSize(e)
	if size == 0 {
		t.Error("eventSize returned 0")
	}
}

func TestSnapshot(t *testing.T) {
	snap := &Snapshot{
		Events:   []core.Event{},
		ByteSize: 1024,
		TierOrder: []string{"p1:5", "p2:3"},
	}

	if snap.ByteSize != 1024 {
		t.Errorf("ByteSize = %d, want 1024", snap.ByteSize)
	}
	if len(snap.TierOrder) != 2 {
		t.Errorf("len(TierOrder) = %d, want 2", len(snap.TierOrder))
	}
}

func TestNewMarkdownRenderer(t *testing.T) {
	r := NewMarkdownRenderer()
	if r == nil {
		t.Fatal("NewMarkdownRenderer returned nil")
	}
}

func TestMarkdownRendererRenderEmpty(t *testing.T) {
	r := NewMarkdownRenderer()

	snap := &Snapshot{
		Events:   []core.Event{},
		ByteSize: 0,
		TierOrder: []string{},
	}

	output := r.Render(snap)
	if output == "" {
		t.Error("Render returned empty string")
	}
	if !strings.Contains(output, "No events") {
		t.Error("Render doesn't contain 'No events' for empty snapshot")
	}
}

func TestMarkdownRendererRenderWithEvents(t *testing.T) {
	r := NewMarkdownRenderer()

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
	if !strings.Contains(output, "Session Snapshot") {
		t.Error("Render doesn't contain header")
	}
	if !strings.Contains(output, "Decisions") {
		t.Error("Render doesn't contain 'Decisions' section")
	}
}

func TestMarkdownRendererRenderFileEdit(t *testing.T) {
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
	if !strings.Contains(output, "File Edits") {
		t.Error("Render doesn't contain 'File Edits' section")
	}
}

func TestNewJSONRenderer(t *testing.T) {
	r := NewJSONRenderer()
	if r == nil {
		t.Fatal("NewJSONRenderer returned nil")
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
		},
	}
	snap := &Snapshot{
		Events:    events,
		ByteSize:  100,
		TierOrder: []string{"p1:1"},
	}

	output := r.Render(snap)
	if output == "" {
		t.Error("Render returned empty string")
	}
	if !strings.Contains(output, "event1") {
		t.Error("Render doesn't contain event ID")
	}
}

func TestNewXMLRenderer(t *testing.T) {
	r := NewXMLRenderer()
	if r == nil {
		t.Fatal("NewXMLRenderer returned nil")
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
	if !strings.Contains(output, "<?xml") {
		t.Error("Render doesn't contain XML declaration")
	}
	if !strings.Contains(output, "<session_snapshot>") {
		t.Error("Render doesn't contain session_snapshot root")
	}
	if !strings.Contains(output, "<event>") {
		t.Error("Render doesn't contain event tags")
	}
}
