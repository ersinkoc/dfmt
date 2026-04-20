package hook

import (
	"sync"
	"time"
)

// NudgeLevel represents the level of compliance nudge.
type NudgeLevel int

const (
	NudgeNone NudgeLevel = iota
	NudgeFirst
	NudgeSecond
	NudgeThird
)

// DriftDetector tracks tool usage patterns for compliance nudging.
type DriftDetector struct {
	mu sync.Mutex

	// Per-session tracking
	sessions map[string]*SessionState

	// Configuration
	nudgeEnabled bool
	nudgeSilence int // Call count to silence after level 3
}

type SessionState struct {
	Calls         []ToolCall
	LastNudgeLevel NudgeLevel
	LastNudgeTime  time.Time
	NudgeCount     int // How many times we've nudged
}

// ToolCall represents a single tool call from an agent.
type ToolCall struct {
	Time     time.Time
	Type    string // "native" or "dfmt"
	Tool    string // "Bash", "Read", "WebFetch", "exec", "read", "fetch"
	Size    int64  // Approximate output size
	HasIntent bool // Whether intent was provided
}

// NewDriftDetector creates a new drift detector.
func NewDriftDetector() *DriftDetector {
	return &DriftDetector{
		sessions:     make(map[string]*SessionState),
		nudgeEnabled: true,
		nudgeSilence: 50,
	}
}

// RecordCall records a tool call for a session.
func (d *DriftDetector) RecordCall(sessionID string, call ToolCall) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.sessions[sessionID]
	if !ok {
		state = &SessionState{}
		d.sessions[sessionID] = state
	}

	// Add call to rolling window (keep last 20)
	state.Calls = append(state.Calls, call)
	if len(state.Calls) > 20 {
		state.Calls = state.Calls[len(state.Calls)-20:]
	}

	// Evict old sessions (> 2 hours)
	d.evictOldSessions()
}

// GetNudgeLevel returns the current nudge level for a session.
func (d *DriftDetector) GetNudgeLevel(sessionID string) NudgeLevel {
	if !d.nudgeEnabled {
		return NudgeNone
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.sessions[sessionID]
	if !ok {
		return NudgeNone
	}

	// Check if we're in silence period after level 3
	if state.LastNudgeLevel == NudgeThird {
		if state.NudgeCount >= 3 {
			// Count calls since last nudge
			silenceCalls := 0
			for _, c := range state.Calls {
				if c.Time.After(state.LastNudgeTime) {
					silenceCalls++
				}
			}
			if silenceCalls < d.nudgeSilence {
				return NudgeNone
			}
		}
	}

	// Calculate drift signals
	driftSignals := d.countDriftSignals(state.Calls)

	if driftSignals >= 3 {
		return NudgeThird
	} else if driftSignals >= 2 {
		return NudgeSecond
	} else if driftSignals >= 1 {
		return NudgeFirst
	}

	return NudgeNone
}

// countDriftSignals counts the number of drift signals in recent calls.
func (d *DriftDetector) countDriftSignals(calls []ToolCall) int {
	if len(calls) == 0 {
		return 0
	}

	// Count native vs dfmt calls
	var nativeCount, dfmtCount int
	var largeNativeCount int
	var noIntentSuboptimal int

	for _, c := range calls {
		if c.Type == "native" {
			nativeCount++
			if c.Size > 16*1024 {
				largeNativeCount++
			}
		} else {
			dfmtCount++
		}

		// Check for suboptimal intent usage
		if c.Type == "dfmt" && !c.HasIntent && c.Size > 64*1024 {
			noIntentSuboptimal++
		}
	}

	total := nativeCount + dfmtCount
	if total == 0 {
		return 0
	}

	driftSignals := 0

	// Native tool ratio > 0.3
	if float64(nativeCount)/float64(total) > 0.3 {
		driftSignals++
	}

	// Large output on native tool
	if largeNativeCount > 0 {
		driftSignals += largeNativeCount
	}

	// Suboptimal intent usage
	if noIntentSuboptimal > 0 {
		driftSignals += noIntentSuboptimal / 2
	}

	return driftSignals
}

// AcknowledgeNudge records that a nudge was sent.
func (d *DriftDetector) AcknowledgeNudge(sessionID string, level NudgeLevel) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.sessions[sessionID]
	if !ok {
		return
	}

	state.LastNudgeLevel = level
	state.LastNudgeTime = time.Now()
	if level != NudgeNone {
		state.NudgeCount++
	}
}

// GetNudgeMessage returns the nudge message for a given level.
func (d *DriftDetector) GetNudgeMessage(sessionID string, level NudgeLevel) string {
	switch level {
	case NudgeNone:
		return ""
	case NudgeFirst:
		return ""
	case NudgeSecond:
		return "Heads up: recent tool calls are bypassing DFMT's sandbox. Consider `dfmt.exec` with an `intent` argument for outputs that may exceed a few KB. Native tools are fine for small, known-small outputs."
	case NudgeThird:
		return "Heads up: recent tool calls are bypassing DFMT's sandbox. Consider `dfmt.exec` with an `intent` argument for outputs that may exceed a few KB. Native tools are fine for small, known-small outputs."
	default:
		return ""
	}
}

// evictOldSessions removes sessions older than 2 hours.
func (d *DriftDetector) evictOldSessions() {
	cutoff := time.Now().Add(-2 * time.Hour)
	for id, state := range d.sessions {
		if len(state.Calls) > 0 && state.Calls[len(state.Calls)-1].Time.Before(cutoff) {
			delete(d.sessions, id)
		}
	}
}

// SessionStats returns statistics for a session.
func (d *DriftDetector) SessionStats(sessionID string) (nativeCalls, dfmtCalls, totalSize int64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.sessions[sessionID]
	if !ok {
		return 0, 0, 0
	}

	for _, c := range state.Calls {
		if c.Type == "native" {
			nativeCalls++
		} else {
			dfmtCalls++
		}
		totalSize += c.Size
	}

	return nativeCalls, dfmtCalls, totalSize
}

// SetNudgeEnabled enables or disables nudging.
func (d *DriftDetector) SetNudgeEnabled(enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nudgeEnabled = enabled
}
