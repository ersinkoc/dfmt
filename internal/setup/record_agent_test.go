package setup

import (
	"testing"
	"time"
)

func TestManifestRecordAgentInsertsEntry(t *testing.T) {
	m := &Manifest{}
	before := time.Now().UTC()

	m.RecordAgent("claude-code", "/home/u/.claude")

	if m.Version != 1 {
		t.Errorf("Version = %d, want 1 (normalized from 0)", m.Version)
	}
	if len(m.Agents) != 1 {
		t.Fatalf("len(Agents) = %d, want 1", len(m.Agents))
	}
	a := m.Agents[0]
	if a.AgentID != "claude-code" {
		t.Errorf("AgentID = %q, want claude-code", a.AgentID)
	}
	if !a.Configured {
		t.Error("Configured = false, want true")
	}
	if a.ConfigDir != "/home/u/.claude" {
		t.Errorf("ConfigDir = %q", a.ConfigDir)
	}
	ts, err := time.Parse(time.RFC3339, m.Timestamp)
	if err != nil {
		t.Fatalf("Timestamp %q not RFC3339: %v", m.Timestamp, err)
	}
	if ts.Before(before.Add(-time.Second)) {
		t.Errorf("Timestamp %v older than call start %v", ts, before)
	}
}

func TestManifestRecordAgentUpsertsExisting(t *testing.T) {
	m := &Manifest{
		Version: 1,
		Agents: []AgentEntry{
			{AgentID: "claude-code", Configured: false, ConfigDir: "/old"},
			{AgentID: "codex", Configured: true, ConfigDir: "/home/u/.codex"},
		},
	}

	m.RecordAgent("claude-code", "/new")

	if len(m.Agents) != 2 {
		t.Fatalf("len(Agents) = %d, want 2 (no duplicate)", len(m.Agents))
	}
	if m.Agents[0].ConfigDir != "/new" {
		t.Errorf("ConfigDir = %q, want /new (should have been updated)", m.Agents[0].ConfigDir)
	}
	if !m.Agents[0].Configured {
		t.Error("Configured = false, want true after RecordAgent")
	}
	if m.Agents[1].ConfigDir != "/home/u/.codex" {
		t.Errorf("unrelated entry mutated: ConfigDir = %q", m.Agents[1].ConfigDir)
	}
}

func TestManifestRecordAgentAppendsNewAlongsideExisting(t *testing.T) {
	m := &Manifest{
		Version: 1,
		Agents: []AgentEntry{
			{AgentID: "claude-code", Configured: true, ConfigDir: "/home/u/.claude"},
		},
	}

	m.RecordAgent("codex", "/home/u/.codex")

	if len(m.Agents) != 2 {
		t.Fatalf("len(Agents) = %d, want 2", len(m.Agents))
	}
	if m.Agents[1].AgentID != "codex" {
		t.Errorf("second entry AgentID = %q, want codex", m.Agents[1].AgentID)
	}
}

func TestManifestRecordAgentBumpsTimestampOnReEntry(t *testing.T) {
	m := &Manifest{}
	m.RecordAgent("claude-code", "/a")
	first := m.Timestamp

	time.Sleep(1100 * time.Millisecond)
	m.RecordAgent("claude-code", "/a")

	if m.Timestamp == first {
		t.Errorf("Timestamp not refreshed: still %q", first)
	}
}
