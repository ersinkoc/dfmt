package transport

import (
	"strings"
	"testing"
)

// TestMCPMissingArgumentIsToolError: the missing-argument message exists so
// a model that guessed `command` for `code` can correct itself. As a
// -32602 JSON-RPC error many hosts showed only "tool call failed"; as an
// isError result the model reads the text.
func TestMCPMissingArgumentIsToolError(t *testing.T) {
	m := newTestMCPProtocol(&stubSandbox{}, nil)
	resp := callTool(t, m, methodExec, map[string]any{"command": "ls"})
	if resp.Error != nil {
		t.Fatalf("got JSON-RPC error %+v, want an isError tool result", resp.Error)
	}
	res, ok := resp.Result.(MCPCallToolResult)
	if !ok || !res.IsError {
		t.Fatalf("Result = %#v, want isError", resp.Result)
	}
	if !strings.Contains(res.Content[0].Text, `"code"`) {
		t.Errorf("error text %q does not name the missing argument", res.Content[0].Text)
	}
}

// TestExecSchemaAllowsJobPoll: `required: ["code"]` made hosts that validate
// arguments client-side refuse every {job_id} poll and cancel.
func TestExecSchemaAllowsJobPoll(t *testing.T) {
	if req, ok := schemaExec().InputSchema["required"]; ok {
		t.Errorf("dfmt_exec schema declares required=%v; a job_id poll has no code", req)
	}
	// Server-side the rule still holds: code unless job_id.
	if err := (ExecParams{}).Validate(); err == nil {
		t.Error("ExecParams{} validated; want missing code")
	}
	if err := (ExecParams{JobID: "j1"}).Validate(); err != nil {
		t.Errorf("job_id poll rejected: %v", err)
	}
}
