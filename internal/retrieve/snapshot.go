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

// CollectTiers streams events and keeps a bounded per-tier bucket, so
// recall's memory cost is independent of journal length. Extracted from
// the transport handler (TRN-6) so the bounded-memory collector is the
// single implementation shared by every output format.
//
// Each bucket FIFO-evicts its oldest event on overflow — recall serves
// "most relevant", which for tiers means most recent within-tier. P1
// events from any journal position survive as long as the total P1 count
// fits caps[0].
//
// Returns events tier-ordered (P1 first) with each tier newest-first,
// matching the old handler's priority sort + TS tiebreak.
func CollectTiers(stream <-chan core.Event, caps [4]int) []core.Event {
	classifier := core.NewClassifier()
	var buckets [4][]core.Event

	for e := range stream {
		// Classification decides the tier, so it must also decide the label
		// (see the note in the former handler about Priority vs Classify
		// disagreeing for dfmt_remember's p3 coercion).
		effective := classifier.Classify(e)
		e.Priority = effective
		var idx int
		switch effective {
		case core.PriP1:
			idx = 0
		case core.PriP2:
			idx = 1
		case core.PriP3:
			idx = 2
		case core.PriP4:
			idx = 3
		default:
			idx = 3 // unknown priority → P4 bucket so events are still surfaced
		}
		if len(buckets[idx]) >= caps[idx] {
			// In-place FIFO shift. `s = s[1:]` would also drop the front
			// element but slowly grows the backing array on repeated append,
			// and would retain a reference to the dropped Event in the
			// unreachable head slot. copy + overwrite keeps the cap bounded
			// and lets GC collect dropped event payloads.
			copy(buckets[idx], buckets[idx][1:])
			buckets[idx][len(buckets[idx])-1] = e
		} else {
			buckets[idx] = append(buckets[idx], e)
		}
	}

	sorted := make([]core.Event, 0, len(buckets[0])+len(buckets[1])+len(buckets[2])+len(buckets[3]))
	for tier := range 4 {
		bucket := buckets[tier]
		for i := len(bucket) - 1; i >= 0; i-- {
			sorted = append(sorted, bucket[i])
		}
	}
	return sorted
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
// The fill is a single pass over tier-ordered events: once the budget
// cannot hold the next event, selection stops entirely rather than
// skipping to a lower tier (TRN-6) — a smaller P3 event must never appear
// before a skipped P2 event, because that breaks the tier ordering recall
// promises.
func (sb *SnapshotBuilder) Build(events []core.Event) (*Snapshot, error) {
	// Sort events by priority tier
	tiered := sb.groupByTier(events)

	var selected []core.Event
	var size int

	// Add events P1 → P4 in one pass.
	for _, tier := range []string{"p1", "p2", "p3", "p4"} {
		for _, e := range tiered[tier] {
			es := sb.eventSize(e)
			if size+es > sb.budget {
				// Stop entirely: tier order is the invariant.
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
			selected = append(selected, e)
			size += es
		}
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
