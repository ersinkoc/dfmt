package transport

import (
	"fmt"
	"os"
	"strings"

	"github.com/ersinkoc/dfmt/internal/config"
)

// supportedMCPProtocolVersions lists the MCP revisions this server can
// speak, newest first. dfmt uses no revision-specific feature beyond
// tools/list + tools/call, so every listed revision is served by the same
// code; the list exists so initialize can echo the client's revision
// instead of pinning one (spec: a server that supports the requested
// version MUST answer with it).
var supportedMCPProtocolVersions = []string{
	"2025-11-25",
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

// negotiateMCPProtocolVersion returns the revision to answer initialize
// with: the client's own when supported, otherwise the newest supported
// one (the client then decides whether to continue). An empty request —
// a thin client that sends no params — gets the historical default.
func negotiateMCPProtocolVersion(requested string) string {
	if requested == "" {
		return config.DefaultMCPProtocolVersion
	}
	for _, v := range supportedMCPProtocolVersions {
		if v == requested {
			return v
		}
	}
	return supportedMCPProtocolVersions[0]
}

// structuredContentClients are clientInfo.name fragments (lower-cased) of
// MCP hosts verified to hand structuredContent to the model. Only these get
// the token-saving sentinel in content[0].text; every other client gets the
// payload serialized into content as the spec recommends for backwards
// compatibility. GitHub issue #1: Windsurf forwarded only content blocks, so
// under the old sentinel-for-everyone default it saw nothing but the
// sentinel for every call. Add a host here only after confirming it reads
// structuredContent — a wrong entry makes dfmt silently unusable there.
var structuredContentClients = []string{
	"claude-code",
	"cursor",
	"codex",
}

// mcpContentOverride reads DFMT_MCP_LEGACY_CONTENT: "1" forces the full
// payload into content, "0" forces the sentinel, anything else defers to
// client detection. ok is false when no override is set.
func mcpContentOverride() (fullText, ok bool) {
	switch os.Getenv("DFMT_MCP_LEGACY_CONTENT") {
	case "1":
		return true, true
	case "0":
		return false, true
	}
	return false, false
}

// errNoMCPProject explains a tool call on a `dfmt mcp` session that found no
// project at startup. Hosts with one global MCP config (Windsurf) spawn the
// server outside any project, so the message names the directory searched
// and both ways to point dfmt at a project (issue #1).
func errNoMCPProject() error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "(unknown)"
	}
	return fmt.Errorf("dfmt: no project found from %s (no .dfmt/ or .git/ there or above) — "+
		"add \"--project\", \"<absolute project path>\" to this MCP server's args, or set DFMT_PROJECT", cwd)
}

// clientNeedsTextContent reports whether a client identified by name must
// receive the payload in content[0].text. Unknown and anonymous clients do:
// a duplicated payload costs tokens, a sentinel costs the whole result.
func clientNeedsTextContent(name string) bool {
	if full, ok := mcpContentOverride(); ok {
		return full
	}
	n := strings.ToLower(name)
	for _, frag := range structuredContentClients {
		if strings.Contains(n, frag) {
			return false
		}
	}
	return true
}
