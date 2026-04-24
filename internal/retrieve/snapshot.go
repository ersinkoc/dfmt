package retrieve

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ersinkoc/dfmt/internal/core"
)

// SnapshotBuilder builds a session snapshot within a byte budget.
type SnapshotBuilder struct {
	budget     int
	classifier *core.Classifier
}

// NewSnapshotBuilder creates a new snapshot builder.
func NewSnapshotBuilder(budget int) *SnapshotBuilder {
	return &SnapshotBuilder{
		budget:     budget,
		classifier: core.NewClassifier(),
	}
}

// Snapshot represents a built session snapshot.
type Snapshot struct {
	Events    []core.Event `json:"events"`
	ByteSize  int          `json:"byte_size"`
	TierOrder []string     `json:"tier_order"` // p1, p2, p3, p4 counts
}

// Build builds a snapshot from events within the budget.
// Events are added in priority order (P1 first) until budget is exhausted.
func (sb *SnapshotBuilder) Build(events []core.Event) (*Snapshot, error) {
	// Sort events by priority tier
	tiered := sb.groupByTier(events)

	var selected []core.Event
	var size int

	// Add P1 events first
	for _, e := range tiered["p1"] {
		es := sb.eventSize(e)
		if size+es > sb.budget {
			break
		}
		selected = append(selected, e)
		size += es
	}

	// Then P2
	for _, e := range tiered["p2"] {
		es := sb.eventSize(e)
		if size+es > sb.budget {
			break
		}
		selected = append(selected, e)
		size += es
	}

	// Then P3
	for _, e := range tiered["p3"] {
		es := sb.eventSize(e)
		if size+es > sb.budget {
			break
		}
		selected = append(selected, e)
		size += es
	}

	// Then P4 (up to remaining space)
	for _, e := range tiered["p4"] {
		es := sb.eventSize(e)
		if size+es > sb.budget {
			break
		}
		selected = append(selected, e)
		size += es
	}

	return &Snapshot{
		Events:   selected,
		ByteSize: size,
		TierOrder: []string{
			fmt.Sprintf("p1:%d", len(tiered["p1"])),
			fmt.Sprintf("p2:%d", len(tiered["p2"])),
			fmt.Sprintf("p3:%d", len(tiered["p3"])),
			fmt.Sprintf("p4:%d", len(tiered["p4"])),
		},
	}, nil
}

func (sb *SnapshotBuilder) groupByTier(events []core.Event) map[string][]core.Event {
	tiered := map[string][]core.Event{
		"p1": {},
		"p2": {},
		"p3": {},
		"p4": {},
	}

	for _, e := range events {
		pri := sb.classifier.Classify(e)
		tiered[string(pri)] = append(tiered[string(pri)], e)
	}

	// Sort each tier by timestamp (newest first)
	for tier := range tiered {
		sort.Slice(tiered[tier], func(i, j int) bool {
			return tiered[tier][i].TS.After(tiered[tier][j].TS)
		})
	}

	return tiered
}

func (sb *SnapshotBuilder) eventSize(e core.Event) int {
	data, err := json.Marshal(e)
	if err != nil {
		// Treat an unmarshalable event as oversized so the budget check
		// rejects it. Returning 0 would let broken events bypass the budget
		// and potentially blow up downstream renderers.
		return 1 << 30
	}
	return len(data)
}
