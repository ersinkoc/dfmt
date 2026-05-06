package retrieve

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// refTokenForgery matches the path-reference token shape this renderer
// emits ([r0], [r17], …). When the same shape appears in user-supplied
// event text (event.Data["message"], tags), an agent reading the
// snapshot can mistake the forged token for a real path reference and
// dereference it from the legend at the top — pointing at the wrong
// path or, with a number that doesn't exist, getting a confused "no
// such ref" cycle. V-06 closure: backslash-escape any matching shape
// in user-controlled text before rendering.
var refTokenForgery = regexp.MustCompile(`\[r\d+\]`)

// escapeRefTokenForgery backslash-escapes any [rN] sequence in s so
// agent parsers don't confuse hostile event text with real ref tokens.
// CommonMark renders `\[` as a literal `[`, so the escaped form remains
// human-readable.
func escapeRefTokenForgery(s string) string {
	return refTokenForgery.ReplaceAllStringFunc(s, func(m string) string {
		return `\` + m
	})
}

// internThreshold is the minimum number of times a path must appear before
// it earns a reference slot. Below this, interning costs more than it
// saves: a `[r17]` token is ~5 bytes plus the table entry (`r17=`/`path`/`,`
// is ~len(path)+5 bytes), so the breakeven is roughly when the verbatim
// emission exceeds (5 * count + len(path) + 5). For paths around 20 chars
// the math says count >= 2 already wins, but 3 is the conservative pick:
// it eliminates regressions on short paths where the per-event overhead
// catches up to the table overhead.
const internThreshold = 3

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

	// Build the path-reference table before rendering events. Paths that
	// appear >= internThreshold times get a short `[rNN]` token; the table
	// printed at the top maps tokens back to the full path. Snapshots with
	// long path lists (file watcher firing on a hot directory across an
	// hour-long session) shrink dramatically; small snapshots see no
	// change because nothing crosses the threshold.
	refs := buildPathRefs(snap.Events)
	if len(refs) > 0 {
		writeRefTable(&b, refs)
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
			r.renderEvent(&b, e, refs)
		}
		b.WriteString("\n")
	}

	// Tasks
	if evts, ok := byType[core.EvtTaskCreate]; ok && len(evts) > 0 {
		b.WriteString("### Tasks\n\n")
		for _, e := range evts {
			r.renderEvent(&b, e, refs)
		}
		b.WriteString("\n")
	}

	// File edits
	if evts, ok := byType[core.EvtFileEdit]; ok && len(evts) > 0 {
		b.WriteString("### File Edits\n\n")
		for _, e := range evts {
			r.renderEvent(&b, e, refs)
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
				r.renderEvent(&b, e, refs)
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
				r.renderEvent(&b, e, refs)
			}
		}
	}

	return b.String()
}

func (r *MarkdownRenderer) renderEvent(b *strings.Builder, e core.Event, refs map[string]string) {
	fmt.Fprintf(b, "- **[%s]** %s", e.Priority, e.Type)
	if e.Actor != "" {
		fmt.Fprintf(b, " by %s", e.Actor)
	}
	fmt.Fprintf(b, " — %s\n", e.TS.Format(time.RFC3339))
	if e.Data != nil {
		if msg, ok := e.Data["message"].(string); ok {
			// V-06: hostile message text can carry a literal [rN]-shape
			// that mimics a path-reference token; escape so the agent
			// parser doesn't dereference it from the legend.
			fmt.Fprintf(b, "  - %s\n", escapeRefTokenForgery(msg))
		}
		if path, ok := e.Data["path"].(string); ok {
			if tok, refed := refs[path]; refed {
				fmt.Fprintf(b, "  - [%s]\n", tok)
			} else {
				fmt.Fprintf(b, "  - `%s`\n", path)
			}
		}
	}
	if len(e.Tags) > 0 {
		// V-06: same forgery defense for tags.
		escTags := make([]string, len(e.Tags))
		for i, t := range e.Tags {
			escTags[i] = escapeRefTokenForgery(t)
		}
		fmt.Fprintf(b, "  - Tags: %s\n", strings.Join(escTags, ", "))
	}
}

// buildPathRefs scans the event list, counts path occurrences, and returns
// a map from path -> short reference token (`r0`, `r1`, ...) for those
// appearing at or above internThreshold. Token assignment is alphabetical
// by path so the same input always produces the same output — important
// for diffability of snapshots across runs.
func buildPathRefs(events []core.Event) map[string]string {
	freq := make(map[string]int)
	for _, e := range events {
		if e.Data == nil {
			continue
		}
		if path, ok := e.Data["path"].(string); ok && path != "" {
			freq[path]++
		}
	}
	var hot []string
	for p, c := range freq {
		if c >= internThreshold {
			hot = append(hot, p)
		}
	}
	if len(hot) == 0 {
		return nil
	}
	sort.Strings(hot)
	out := make(map[string]string, len(hot))
	for i, p := range hot {
		out[p] = fmt.Sprintf("r%d", i)
	}
	return out
}

// writeRefTable emits the path-reference legend at the top of the snapshot.
// Format: a single line so the table doesn't bloat short snapshots, with
// entries comma-separated. Agents that see a `[r17]` token in an event
// scan up to find its expansion. The table sits between the snapshot
// header and the Events section so it's the first thing parsed.
func writeRefTable(b *strings.Builder, refs map[string]string) {
	tokens := make([]string, 0, len(refs))
	for p, t := range refs {
		tokens = append(tokens, fmt.Sprintf("%s=`%s`", t, p))
	}
	sort.Strings(tokens)
	b.WriteString("**Refs:** ")
	b.WriteString(strings.Join(tokens, ", "))
	b.WriteString("\n\n")
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

// xmlEscape escapes XML metacharacters via encoding/xml. Event fields
// (Type, Actor, ID) come from agent input through Remember and can contain
// '<', '&', or ']]>' which would otherwise produce malformed XML or, at
// worst, be interpreted as new elements by a downstream consumer.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}

// Render renders a snapshot as XML.
func (r *XMLRenderer) Render(snap *Snapshot) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<session_snapshot>\n")
	fmt.Fprintf(&b, "  <byte_size>%d</byte_size>\n", snap.ByteSize)
	b.WriteString("  <tier_order>\n")
	for _, t := range snap.TierOrder {
		fmt.Fprintf(&b, "    <tier>%s</tier>\n", xmlEscape(t))
	}
	b.WriteString("  </tier_order>\n")
	b.WriteString("  <events>\n")
	for _, e := range snap.Events {
		b.WriteString("    <event>\n")
		fmt.Fprintf(&b, "      <id>%s</id>\n", xmlEscape(e.ID))
		fmt.Fprintf(&b, "      <type>%s</type>\n", xmlEscape(string(e.Type)))
		fmt.Fprintf(&b, "      <priority>%s</priority>\n", xmlEscape(string(e.Priority)))
		fmt.Fprintf(&b, "      <ts>%s</ts>\n", e.TS.Format(time.RFC3339Nano))
		if e.Actor != "" {
			fmt.Fprintf(&b, "      <actor>%s</actor>\n", xmlEscape(e.Actor))
		}
		b.WriteString("    </event>\n")
	}
	b.WriteString("  </events>\n")
	b.WriteString("</session_snapshot>\n")
	return b.String()
}
