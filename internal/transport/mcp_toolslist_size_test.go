package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolsListBytesUnderBudget pins an upper bound on the tools/list
// JSON-RPC response size. The MCP `initialize` + `tools/list` exchange
// runs once at the start of every session — the bytes the agent reads
// here are paid on every fresh connection. The previous descriptions
// pushed each session's startup over 8 KB; the trimmed forms target
// well under 6 KB so the budget is meaningful headroom for new tools.
//
// If a future change needs to push past this cap, raise the constant
// deliberately rather than letting the size drift.
func TestToolsListBytesUnderBudget(t *testing.T) {
	// Raised from 6 KiB to 6.75 KiB — the first deliberate raise, for the
	// async exec surface (`async`, `job_id`, `cancel` plus the timeout
	// note): ~420 bytes for a capability that has no other way to be
	// discovered. The alternative, a separate dfmt_job tool, would have
	// cost more: a second tool object carries its own name, description,
	// and schema envelope.
	//
	// The rule this constant encodes has not changed: raise it for a new
	// capability, never to absorb prose drift. Every earlier addition in
	// this pass (replace_all, search's type filter, the tag vocabulary,
	// line-oriented read) was paid for by trimming elsewhere.
	const toolsListMaxBytes = 6912

	p := NewMCPProtocol(&Handlers{})
	resp, err := p.handleToolsList(&MCPRequest{ID: 1, Method: "tools/list"})
	if err != nil {
		t.Fatalf("handleToolsList: %v", err)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := len(body); got > toolsListMaxBytes {
		t.Errorf("tools/list response = %d bytes, want <= %d. Either compress descriptions or raise the budget intentionally.",
			got, toolsListMaxBytes)
	}
}

// TestToolsListBytesReport is a -v-only telemetry probe that prints the
// actual wire-byte count. Useful for tracking the trend across releases
// without needing to instrument outside the test binary. Always passes.
func TestToolsListBytesReport(t *testing.T) {
	p := NewMCPProtocol(&Handlers{})
	resp, _ := p.handleToolsList(&MCPRequest{ID: 1, Method: "tools/list"})
	body, _ := json.Marshal(resp)
	t.Logf("tools/list JSON-RPC response: %d bytes", len(body))
}

// TestToolsListReusesSharedDescriptions guards against a regression that
// would re-introduce duplicated long-form prose. If the verbose
// "STRONGLY RECOMMENDED. A short phrase describing what you actually need"
// string is found in the wire bytes, the trimming was undone — likely
// by a copy-paste during a future tool addition.
func TestToolsListReusesSharedDescriptions(t *testing.T) {
	p := NewMCPProtocol(&Handlers{})
	resp, _ := p.handleToolsList(&MCPRequest{ID: 1, Method: "tools/list"})
	body, _ := json.Marshal(resp)
	str := string(body)
	bannedFragments := []string{
		"A short phrase describing what you actually need",
		"the response is filtered down to matching excerpts plus a summary",
		"the full bytes are stashed for later retrieval",
		"the most token-efficient mode",
	}
	for _, banned := range bannedFragments {
		if strings.Contains(str, banned) {
			t.Errorf("verbose pre-trim fragment still in wire bytes: %q", banned)
		}
	}
}
