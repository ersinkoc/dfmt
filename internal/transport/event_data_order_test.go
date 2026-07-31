package transport

import (
	"strings"
	"testing"
)

// Regression: formatEventData ranged over the data map directly, so which
// fields a recall line carried — and their order — varied between calls on
// identical input. Recall snapshots are fed back to an agent as context, so a
// reshuffled prefix invalidates its prompt cache every time; they are also
// impossible to diff across sessions.
func TestFormatEventDataIsDeterministic(t *testing.T) {
	data := map[string]any{
		"path":       "internal/core/index.go",
		"size":       1488,
		"raw_bytes":  1488,
		"read_bytes": 1488,
		"duration":   12,
		"exit":       0,
	}

	first := formatEventData(data)
	for range 200 {
		if got := formatEventData(data); got != first {
			t.Fatalf("formatEventData is not stable across calls:\n  %q\n  %q", first, got)
		}
	}
}

// The ordering is by informativeness, not just alphabetical: plain sorting
// would be deterministic but would push `message` past the three-field budget
// for every tool.exec event (code/duration/exit all sort earlier).
func TestFormatEventDataPrefersIdentifyingFields(t *testing.T) {
	data := map[string]any{
		"code":       "go test ./...",
		"duration":   4200,
		"exit":       1,
		"raw_bytes":  90210,
		"message":    "test suite failed",
		"actor_kind": "agent",
	}

	got := formatEventData(data)

	if !strings.Contains(got, "message:test suite failed") {
		t.Errorf("formatEventData = %q, want the message field to survive the budget", got)
	}
	if idx, codeIdx := strings.Index(got, "message:"), strings.Index(got, "code:"); idx > codeIdx {
		t.Errorf("formatEventData = %q, want message before code", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("formatEventData = %q, want the elision marker when fields were dropped", got)
	}
}

func TestOrderedEventDataKeysSortsRemainder(t *testing.T) {
	keys := orderedEventDataKeys(map[string]any{
		"zeta": 1, "alpha": 2, "path": 3, "beta": 4,
	})
	want := []string{"path", "alpha", "beta", "zeta"}
	if len(keys) != len(want) {
		t.Fatalf("orderedEventDataKeys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("orderedEventDataKeys = %v, want %v", keys, want)
		}
	}
}
