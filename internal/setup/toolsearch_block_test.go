package setup

import (
	"strings"
	"testing"
)

// The generated instruction blocks must tell an agent how to load deferred
// dfmt tools, with a query that is a single line (a newline inside a
// ToolSearch select list would split it).
func TestBlocksTeachDeferredToolLoading(t *testing.T) {
	for name, body := range map[string]string{
		"markdown":  markdownProjectBlockBody,
		"agents.md": agentsMdProjectBlockBody,
	} {
		if !strings.Contains(body, toolSearchLoadQuery) {
			t.Errorf("%s block lacks the ToolSearch load query", name)
		}
		if strings.Contains(toolSearchLoadQuery, "\n") {
			t.Errorf("load query spans lines")
		}
	}
	for _, tool := range []string{"exec", "read", "grep", "glob", "edit", "write", "fetch", "remember", "recall", "search"} {
		if !strings.Contains(toolSearchLoadQuery, "mcp__dfmt__dfmt_"+tool+",") &&
			!strings.HasSuffix(strings.TrimSuffix(toolSearchLoadQuery, "`"), "mcp__dfmt__dfmt_"+tool) {
			t.Errorf("load query misses dfmt_%s", tool)
		}
	}
}
