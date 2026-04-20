package retrieve

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// MarkdownRenderer renders a snapshot as markdown.
type MarkdownRenderer struct{}

// NewMarkdownRenderer creates a markdown renderer.
func NewMarkdownRenderer() *MarkdownRenderer {
	return &MarkdownRenderer{}
}

// Render renders a snapshot as markdown.
func (r *MarkdownRenderer) Render(snap *Snapshot) string {
	var b strings.Builder

	b.WriteString("# Session Snapshot\n\n")
	fmt.Fprintf(&b, "**Events:** %d | **Size:** %d bytes | **Tiers:** %s\n\n",
		len(snap.Events), snap.ByteSize, strings.Join(snap.TierOrder, ", "))

	if len(snap.Events) == 0 {
		b.WriteString("_No events recorded yet._\n")
		return b.String()
	}

	// Group by type
	byType := make(map[core.EventType][]core.Event)
	for _, e := range snap.Events {
		byType[e.Type] = append(byType[e.Type], e)
	}

	// Render each type
	b.WriteString("## Events\n\n")

	// Decisions first
	if evts, ok := byType[core.EvtDecision]; ok && len(evts) > 0 {
		b.WriteString("### Decisions\n\n")
		for _, e := range evts {
			r.renderEvent(&b, e)
		}
		b.WriteString("\n")
	}

	// Tasks
	if evts, ok := byType[core.EvtTaskCreate]; ok && len(evts) > 0 {
		b.WriteString("### Tasks\n\n")
		for _, e := range evts {
			r.renderEvent(&b, e)
		}
		b.WriteString("\n")
	}

	// File edits
	if evts, ok := byType[core.EvtFileEdit]; ok && len(evts) > 0 {
		b.WriteString("### File Edits\n\n")
		for _, e := range evts {
			r.renderEvent(&b, e)
		}
		b.WriteString("\n")
	}

	// Git operations
	for _, evtType := range []core.EventType{core.EvtGitCommit, core.EvtGitPush, core.EvtGitCheckout} {
		if evts, ok := byType[evtType]; ok && len(evts) > 0 {
			title := strings.ReplaceAll(string(evtType), "git.", "Git ")
			title = strings.ToUpper(string(title[0])) + title[1:]
			fmt.Fprintf(&b, "### %s\n\n", title)
			for _, e := range evts {
				r.renderEvent(&b, e)
			}
			b.WriteString("\n")
		}
	}

	// Other events
	b.WriteString("### Other Events\n\n")
	for typ, evts := range byType {
		if typ != core.EvtDecision && typ != core.EvtTaskCreate &&
			typ != core.EvtFileEdit && !strings.HasPrefix(string(typ), "git.") {
			for _, e := range evts {
				r.renderEvent(&b, e)
			}
		}
	}

	return b.String()
}

func (r *MarkdownRenderer) renderEvent(b *strings.Builder, e core.Event) {
	fmt.Fprintf(b, "- **[%s]** %s", e.Priority, e.Type)
	if e.Actor != "" {
		fmt.Fprintf(b, " by %s", e.Actor)
	}
	fmt.Fprintf(b, " — %s\n", e.TS.Format(time.RFC3339))
	if e.Data != nil {
		if msg, ok := e.Data["message"].(string); ok {
			fmt.Fprintf(b, "  - %s\n", msg)
		}
		if path, ok := e.Data["path"].(string); ok {
			fmt.Fprintf(b, "  - `%s`\n", path)
		}
	}
	if len(e.Tags) > 0 {
		fmt.Fprintf(b, "  - Tags: %s\n", strings.Join(e.Tags, ", "))
	}
}

// JSONRenderer renders a snapshot as JSON.
type JSONRenderer struct{}

// NewJSONRenderer creates a JSON renderer.
func NewJSONRenderer() *JSONRenderer {
	return &JSONRenderer{}
}

// Render renders a snapshot as JSON.
func (r *JSONRenderer) Render(snap *Snapshot) string {
	data, _ := json.MarshalIndent(snap, "", "  ")
	return string(data)
}

// XMLRenderer renders a snapshot as XML.
type XMLRenderer struct{}

// NewXMLRenderer creates an XML renderer.
func NewXMLRenderer() *XMLRenderer {
	return &XMLRenderer{}
}

// Render renders a snapshot as XML.
func (r *XMLRenderer) Render(snap *Snapshot) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<session_snapshot>\n")
	fmt.Fprintf(&b, "  <byte_size>%d</byte_size>\n", snap.ByteSize)
	b.WriteString("  <tier_order>\n")
	for _, t := range snap.TierOrder {
		fmt.Fprintf(&b, "    <tier>%s</tier>\n", t)
	}
	b.WriteString("  </tier_order>\n")
	b.WriteString("  <events>\n")
	for _, e := range snap.Events {
		b.WriteString("    <event>\n")
		fmt.Fprintf(&b, "      <id>%s</id>\n", e.ID)
		fmt.Fprintf(&b, "      <type>%s</type>\n", e.Type)
		fmt.Fprintf(&b, "      <priority>%s</priority>\n", e.Priority)
		fmt.Fprintf(&b, "      <ts>%s</ts>\n", e.TS.Format(time.RFC3339Nano))
		if e.Actor != "" {
			fmt.Fprintf(&b, "      <actor>%s</actor>\n", e.Actor)
		}
		b.WriteString("    </event>\n")
	}
	b.WriteString("  </events>\n")
	b.WriteString("</session_snapshot>\n")
	return b.String()
}
