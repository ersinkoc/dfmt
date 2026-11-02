package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestBypassReport(t *testing.T) {
	big := strings.Repeat("x", 8000) // 2000 tokens, above the inline threshold
	transcript := strings.Join([]string{
		`{"message":{"content":[{"type":"tool_use","id":"a","name":"Bash"}]}}`,
		`{"message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"` + big + `"}]}}`,
		`{"message":{"content":[{"type":"tool_use","id":"b","name":"Bash"}]}}`,
		`{"message":{"content":[{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"ok"}]}]}}`,
		`{"message":{"content":[{"type":"tool_use","id":"c","name":"mcp__dfmt__dfmt_exec"}]}}`,
		`{"message":{"content":[{"type":"tool_use","id":"d","name":"mcp__dfmt__dfmt_read"}]}}`,
		`{"message":{"content":[{"type":"tool_use","id":"e","name":"ToolSearch"}]}}`,
		`{"message":{"content":"plain user text"}}`,
		`not json`,
	}, "\n")

	var out bytes.Buffer
	if err := bypassReport([]io.Reader{strings.NewReader(transcript)}, &out); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"Bash 2 1 33% 2000 1",  // 2 native, 1 dfmt_exec; "ok" is under a token
		"Read 0 1 100% 0 0",    // dfmt-only row still listed
		"TOTAL 2 2 50% 2000 1", // ToolSearch is neither
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ToolSearch") {
		t.Errorf("unrelated tool leaked into report:\n%s", got)
	}
}
