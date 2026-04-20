package retrieve

// Package retrieve provides session snapshot building and rendering.
// It implements tier-ordered greedy fill within byte budgets and
// supports multiple output formats (markdown, JSON, XML).
//
// Snapshot building:
//   - Events are classified into priority tiers (P1-P4)
//   - P1 events (decisions, task outcomes) are included first
//   - Remaining budget fills with P2-P4 events
//   - Within each tier, events are sorted by timestamp (newest first)
//
// Rendering:
//   - Markdown: Human-readable with sections grouped by event type
//   - JSON: Full structure with all metadata
//   - XML: Hierarchical format with tier information
