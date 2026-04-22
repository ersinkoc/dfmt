package hook

import (
	"sync"
	"testing"
	"time"
)

func TestNudgeLevel(t *testing.T) {
	if NudgeNone != 0 {
		t.Errorf("NudgeNone = %d, want 0", NudgeNone)
	}
	if NudgeFirst != 1 {
		t.Errorf("NudgeFirst = %d, want 1", NudgeFirst)
	}
	if NudgeSecond != 2 {
		t.Errorf("NudgeSecond = %d, want 2", NudgeSecond)
	}
	if NudgeThird != 3 {
		t.Errorf("NudgeThird = %d, want 3", NudgeThird)
	}
}

func TestNewDriftDetector(t *testing.T) {
	d := NewDriftDetector()
	if d == nil {
		t.Fatal("NewDriftDetector returned nil")
	}
	if d.sessions == nil {
		t.Error("sessions map is nil")
	}
	if !d.nudgeEnabled {
		t.Error("nudgeEnabled should be true by default")
	}
	if d.nudgeSilence != 50 {
		t.Errorf("nudgeSilence = %d, want 50", d.nudgeSilence)
	}
}

func TestDriftDetectorRecordCall(t *testing.T) {
	d := NewDriftDetector()

	call := ToolCall{
		Time:      time.Now(),
		Type:      "native",
		Tool:      "Bash",
		Size:      1024,
		HasIntent: false,
	}

	d.RecordCall("session1", call)

	state, ok := d.sessions["session1"]
	if !ok {
		t.Fatal("Session not created")
	}
	if len(state.Calls) != 1 {
		t.Errorf("len(Calls) = %d, want 1", len(state.Calls))
	}
}

func TestDriftDetectorRecordCallMultiple(t *testing.T) {
	d := NewDriftDetector()

	for i := 0; i < 25; i++ {
		call := ToolCall{
			Time: time.Now(),
			Type: "native",
			Tool: "Bash",
			Size: 1024,
		}
		d.RecordCall("session1", call)
	}

	state := d.sessions["session1"]
	// Should only keep last 20
	if len(state.Calls) > 20 {
		t.Errorf("len(Calls) = %d, want at most 20", len(state.Calls))
	}
}

func TestDriftDetectorGetNudgeLevelNoSession(t *testing.T) {
	d := NewDriftDetector()

	level := d.GetNudgeLevel("nonexistent")
	if level != NudgeNone {
		t.Errorf("GetNudgeLevel = %d, want NudgeNone", level)
	}
}

func TestDriftDetectorGetNudgeLevelDisabled(t *testing.T) {
	d := NewDriftDetector()
	d.nudgeEnabled = false

	call := ToolCall{
		Time: time.Now(),
		Type: "native",
		Tool: "Bash",
		Size: 20000,
	}
	d.RecordCall("session1", call)

	level := d.GetNudgeLevel("session1")
	if level != NudgeNone {
		t.Errorf("GetNudgeLevel = %d, want NudgeNone when disabled", level)
	}
}

func TestDriftDetectorGetNudgeLevelFirst(t *testing.T) {
	d := NewDriftDetector()

	// One drift signal
	calls := []ToolCall{
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 500},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	level := d.GetNudgeLevel("session1")
	if level != NudgeFirst {
		t.Errorf("GetNudgeLevel = %d, want NudgeFirst", level)
	}
}

func TestDriftDetectorGetNudgeLevelSecond(t *testing.T) {
	d := NewDriftDetector()

	// Two drift signals: native ratio > 0.3 and large output
	calls := []ToolCall{
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 1000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 1000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	level := d.GetNudgeLevel("session1")
	// With ratio 0.5 native (2/4) > 0.3, should get at least NudgeFirst
	if level < NudgeFirst {
		t.Errorf("GetNudgeLevel = %d, want at least NudgeFirst", level)
	}
}

func TestDriftDetectorGetNudgeLevelThird(t *testing.T) {
	d := NewDriftDetector()

	// Three+ drift signals
	calls := []ToolCall{
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	level := d.GetNudgeLevel("session1")
	if level != NudgeThird {
		t.Errorf("GetNudgeLevel = %d, want NudgeThird", level)
	}
}

func TestDriftDetectorGetNudgeLevelSilencePeriod(t *testing.T) {
	d := NewDriftDetector()
	// Small silence threshold for testing
	d.nudgeSilence = 5

	// Record calls to get to NudgeThird level
	calls := []ToolCall{
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	// Get to NudgeThird
	level := d.GetNudgeLevel("session1")
	if level != NudgeThird {
		t.Fatalf("Expected NudgeThird, got %d", level)
	}

	// Manually set LastNudgeLevel and NudgeCount to simulate having already
	// sent 3 nudges and being in the silence period
	d.sessions["session1"].LastNudgeLevel = NudgeThird
	d.sessions["session1"].NudgeCount = 3

	// Within silence period (not enough new calls since last nudge), should return NudgeNone
	level = d.GetNudgeLevel("session1")
	if level != NudgeNone {
		t.Errorf("GetNudgeLevel in silence period = %d, want NudgeNone", level)
	}
}

func TestDriftDetectorGetNudgeLevelSilenceEnds(t *testing.T) {
	d := NewDriftDetector()
	// Small silence threshold for testing
	d.nudgeSilence = 3

	// Record calls to get to NudgeThird level
	calls := []ToolCall{
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 20000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 1000},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	// Acknowledge the nudge to set LastNudgeTime to now
	d.AcknowledgeNudge("session1", NudgeThird)

	// Add enough new calls to exceed silence threshold
	for i := 0; i < 5; i++ {
		d.RecordCall("session1", ToolCall{
			Time: time.Now(),
			Type: "native",
			Tool: "Bash",
			Size: 20000,
		})
	}

	// Silence period should be over now
	level := d.GetNudgeLevel("session1")
	// Should calculate new drift based on accumulated calls
	if level != NudgeThird {
		t.Errorf("GetNudgeLevel after silence = %d, want NudgeThird", level)
	}
}

func TestDriftDetectorCountDriftSignals(t *testing.T) {
	d := NewDriftDetector()

	tests := []struct {
		name           string
		calls          []ToolCall
		wantMinSignals int
	}{
		{
			name:  "no calls",
			calls: []ToolCall{},
		},
		{
			name: "all dfmt small",
			calls: []ToolCall{
				{Type: "dfmt", Size: 1000},
				{Type: "dfmt", Size: 1000},
			},
		},
		{
			name: "native ratio over 0.3",
			calls: []ToolCall{
				{Type: "native", Size: 1000},
				{Type: "native", Size: 1000},
				{Type: "native", Size: 1000},
				{Type: "dfmt", Size: 1000},
			},
			wantMinSignals: 1,
		},
		{
			name: "large native output",
			calls: []ToolCall{
				{Type: "native", Size: 20000},
			},
			wantMinSignals: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signals := d.countDriftSignals(tt.calls)
			if signals < tt.wantMinSignals {
				t.Errorf("countDriftSignals = %d, want >= %d", signals, tt.wantMinSignals)
			}
		})
	}
}

func TestDriftDetectorAcknowledgeNudge(t *testing.T) {
	d := NewDriftDetector()

	call := ToolCall{
		Time: time.Now(),
		Type: "native",
		Tool: "Bash",
		Size: 20000,
	}
	d.RecordCall("session1", call)

	d.AcknowledgeNudge("session1", NudgeSecond)

	state := d.sessions["session1"]
	if state.LastNudgeLevel != NudgeSecond {
		t.Errorf("LastNudgeLevel = %d, want NudgeSecond", state.LastNudgeLevel)
	}
	if state.NudgeCount != 1 {
		t.Errorf("NudgeCount = %d, want 1", state.NudgeCount)
	}
}

func TestDriftDetectorAcknowledgeNudgeNone(t *testing.T) {
	d := NewDriftDetector()

	call := ToolCall{
		Time: time.Now(),
		Type: "native",
		Tool: "Bash",
		Size: 20000,
	}
	d.RecordCall("session1", call)

	d.AcknowledgeNudge("session1", NudgeNone)

	state := d.sessions["session1"]
	if state.NudgeCount != 0 {
		t.Errorf("NudgeCount = %d, want 0 for NudgeNone", state.NudgeCount)
	}
}

func TestDriftDetectorGetNudgeMessage(t *testing.T) {
	d := NewDriftDetector()

	msg := d.GetNudgeMessage("session1", NudgeNone)
	if msg != "" {
		t.Errorf("Message for NudgeNone = %q, want empty", msg)
	}

	msg2 := d.GetNudgeMessage("session1", NudgeSecond)
	if msg2 == "" {
		t.Error("Message for NudgeSecond should not be empty")
	}
	if msg2 == "" {
		t.Error("Message is empty but should contain warning")
	}

	msg3 := d.GetNudgeMessage("session1", NudgeFirst)
	if msg3 != "" {
		t.Errorf("Message for NudgeFirst = %q, want empty", msg3)
	}

	// Test default case (NudgeLevel 99)
	msg4 := d.GetNudgeMessage("session1", 99)
	if msg4 != "" {
		t.Errorf("Message for unknown level = %q, want empty", msg4)
	}
}

func TestDriftDetectorGetNudgeMessageThird(t *testing.T) {
	d := NewDriftDetector()

	msg := d.GetNudgeMessage("session1", NudgeThird)
	if msg == "" {
		t.Error("Message for NudgeThird should not be empty")
	}
}

func TestDriftDetectorEvictOldSessions(t *testing.T) {
	d := NewDriftDetector()

	// Old session
	oldCall := ToolCall{
		Time: time.Now().Add(-3 * time.Hour),
		Type: "native",
		Tool: "Bash",
		Size: 1000,
	}
	d.RecordCall("oldsession", oldCall)

	// Recent session
	newCall := ToolCall{
		Time: time.Now(),
		Type: "native",
		Tool: "Bash",
		Size: 1000,
	}
	d.RecordCall("newsession", newCall)

	d.evictOldSessions()

	if _, ok := d.sessions["oldsession"]; ok {
		t.Error("Old session should have been evicted")
	}
	if _, ok := d.sessions["newsession"]; !ok {
		t.Error("Recent session should still exist")
	}
}

func TestDriftDetectorSessionStats(t *testing.T) {
	d := NewDriftDetector()

	calls := []ToolCall{
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 1000},
		{Time: time.Now(), Type: "native", Tool: "Bash", Size: 2000},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 500},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	native, dfmt, total := d.SessionStats("session1")
	if native != 2 {
		t.Errorf("nativeCalls = %d, want 2", native)
	}
	if dfmt != 1 {
		t.Errorf("dfmtCalls = %d, want 1", dfmt)
	}
	if total != 3500 {
		t.Errorf("totalSize = %d, want 3500", total)
	}
}

func TestDriftDetectorSessionStatsNoSession(t *testing.T) {
	d := NewDriftDetector()

	native, dfmt, total := d.SessionStats("nonexistent")
	if native != 0 || dfmt != 0 || total != 0 {
		t.Errorf("SessionStats for nonexistent returned non-zero: %d, %d, %d", native, dfmt, total)
	}
}

func TestDriftDetectorSetNudgeEnabled(t *testing.T) {
	d := NewDriftDetector()

	// Check initial state
	if !d.nudgeEnabled {
		t.Error("Initially should be enabled")
	}

	// Disable
	d.SetNudgeEnabled(false)
	if d.nudgeEnabled {
		t.Error("After SetNudgeEnabled(false), nudgeEnabled should be false")
	}

	// Re-enable
	d.SetNudgeEnabled(true)
	if !d.nudgeEnabled {
		t.Error("After SetNudgeEnabled(true), nudgeEnabled should be true")
	}
}

func TestDriftDetectorConcurrentAccess(t *testing.T) {
	d := NewDriftDetector()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				d.RecordCall("session1", ToolCall{
					Time: time.Now(),
					Type: "native",
					Tool: "Bash",
					Size: 1000,
				})
				d.GetNudgeLevel("session1")
			}
		}()
	}
	wg.Wait()
}

func TestToolCall(t *testing.T) {
	call := ToolCall{
		Time:      time.Now(),
		Type:      "native",
		Tool:      "Bash",
		Size:      4096,
		HasIntent: true,
	}

	if call.Type != "native" {
		t.Errorf("Type = %s, want 'native'", call.Type)
	}
	if call.Tool != "Bash" {
		t.Errorf("Tool = %s, want 'Bash'", call.Tool)
	}
	if call.Size != 4096 {
		t.Errorf("Size = %d, want 4096", call.Size)
	}
	if !call.HasIntent {
		t.Error("HasIntent should be true")
	}
}

func TestDriftDetectorAcknowledgeNudgeNonExistentSession(t *testing.T) {
	d := NewDriftDetector()

	// Acknowledge for nonexistent session should not panic
	d.AcknowledgeNudge("nonexistent", NudgeSecond)
}

func TestDriftDetectorNoIntentSuboptimal(t *testing.T) {
	d := NewDriftDetector()

	// Large dfmt call without intent should count as suboptimal
	calls := []ToolCall{
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 70000, HasIntent: false},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 70000, HasIntent: false},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 70000, HasIntent: false},
		{Time: time.Now(), Type: "dfmt", Tool: "read", Size: 70000, HasIntent: false},
	}
	for _, c := range calls {
		d.RecordCall("session1", c)
	}

	// Should get some drift signals due to suboptimal intent usage
	level := d.GetNudgeLevel("session1")
	if level < NudgeFirst {
		t.Errorf("GetNudgeLevel = %d, want at least NudgeFirst for noIntentSuboptimal", level)
	}
}
