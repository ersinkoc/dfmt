package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/version"
)

// mcpLegacyContentSentinel is what we put in content[0].text when the modern
// path is active. Strict MCP validators reject empty content arrays, so a tiny
// sentinel keeps them happy while the actual payload travels in
// structuredContent (where every modern client already reads it from).
const mcpLegacyContentSentinel = "dfmt: see structuredContent"

// mcpLegacyContent, when DFMT_MCP_LEGACY_CONTENT=1, restores the pre-token-
// optimization behavior of duplicating the full JSON payload into
// content[0].text alongside structuredContent. The duplicate roughly doubled
// the token cost of every tool response on clients that count both fields
// (Claude Code, Cursor, Codex). The sentinel-only default is safe for every
// MCP client that consumes structuredContent — which, as of mid-2025, is all
// of them. The escape hatch exists for the rare text-only client that parses
// JSON out of content[0].text.
func mcpLegacyContent() bool {
	return os.Getenv("DFMT_MCP_LEGACY_CONTENT") == "1"
}

// MCPProtocol implements the Model Context Protocol over JSON-RPC.
//
// As of v0.5.0, MCPProtocol depends on the Backend interface rather
// than *Handlers directly. Local-mode tests and legacy single-project
// daemons pass a *Handlers (which satisfies Backend); the proxy mode
// of `dfmt mcp` (the default in v0.5.0) passes a *client.ClientBackend
// that forwards every call to the host-wide daemon, eliminating the
// duplicate journal/index handles the subprocess used to open.
type MCPProtocol struct {
	backend Backend

	// sessionID identifies this MCPProtocol instance for the wire-dedup
	// cache (ADR-0011). Generated as a ULID at construction time so each
	// `dfmt mcp` subprocess gets a unique bucket; optionally augmented in
	// handleInitialize with the client's name/version when the agent
	// honors the MCP `clientInfo` payload — useful for the dashboard's
	// session list. The ULID alone guarantees uniqueness, so missing
	// clientInfo is harmless. Mutated only by handleInitialize, which
	// runs before any tool call, so no mutex is needed.
	sessionID string

	// projectID is the canonical project root path the MCP subprocess
	// pinned at startup (Phase 2). Each tool call attaches this to the
	// request context (via WithProjectID) so the daemon's per-call
	// resolver routes to the correct per-project resources. Empty when
	// the subprocess started in degraded mode (no project discoverable
	// from cwd) — in that case the daemon falls back to its default
	// project (legacy single-project daemon) or returns errNoProject.
	// Mutated only by SetProjectID before any tool call so no mutex
	// is needed; the field is treated as immutable post-startup.
	projectID string

	// statsMu guards the lazily-populated tool-call compression cache used
	// to enrich tool descriptions with self-tuning telemetry. Computing the
	// stats requires a journal stream; caching keeps tools/list cheap when
	// the client polls or reconnects within a single session.
	statsMu       sync.Mutex
	statsCache    map[string]toolCompression
	statsCachedAt time.Time
}

// toolCompression aggregates raw vs. returned bytes across recent tool.* events
// of a single type. Stored in MCPProtocol's stats cache, recomputed every
// toolStatsTTL.
type toolCompression struct {
	n             int
	rawBytes      int
	returnedBytes int
}

// toolStatsTTL is how long compression telemetry stays cached before the next
// tools/list call recomputes it. 60s is short enough that a session that just
// did a high-savings batch sees the new average within a refresh, long enough
// that the journal isn't streamed on every list call.
const toolStatsTTL = 60 * time.Second

// toolStatsMinSamples is the floor below which we suppress the description
// suffix entirely. Five calls is too few to be honest about an average, and
// the description should not advertise "savings: ~0% over 1 call" on a fresh
// project.
const toolStatsMinSamples = 5

// MCPTool represents an MCP tool definition.
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPInitializeResult is the result of initialization.
type MCPInitializeResult struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Capabilities    MCPServerCapabilities `json:"capabilities"`
	ServerInfo      MCPServerInfo         `json:"serverInfo"`
}

// MCPServerCapabilities is what dfmt advertises in the initialize reply.
// Without a non-empty `tools` field, MCP clients (Claude Code) won't call
// tools/list and the server appears to expose no tools at all.
type MCPServerCapabilities struct {
	Tools MCPToolsCapability `json:"tools"`
}

// MCPToolsCapability marks the tools capability as present. Empty object
// is sufficient per spec; listChanged advertises notification support.
type MCPToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// MCPServerInfo represents server information.
type MCPServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MCPRequest represents an incoming MCP request.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id,omitempty"`
}

// MCPContent is one block of an MCP CallToolResult content array. Only the
// text variant is emitted today; image/resource blocks would be added here.
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// MCPCallToolResult is the wire shape MCP clients expect from tools/call.
// Returning a plain handler struct as `result` makes Claude Code's strict
// schema validator reject the response — it walks `result.content` looking
// for a content-block array and trips on, e.g., ReadResponse.Content (string).
type MCPCallToolResult struct {
	Content           []MCPContent `json:"content"`
	StructuredContent any          `json:"structuredContent,omitempty"`
	IsError           bool         `json:"isError,omitempty"`
}

// mcpToolResult wraps a handler payload in the MCP CallToolResult envelope.
//
// Default (token-optimized): content[0].text carries a short sentinel and the
// full payload travels in structuredContent. Modern MCP clients (Claude Code,
// Cursor, Codex, Cline, Continue) read structuredContent; emitting the
// JSON-stringified payload in *both* fields was a flat ~50% token tax on
// every tool response.
//
// Legacy mode (DFMT_MCP_LEGACY_CONTENT=1): content[0].text gets the full
// JSON-stringified payload, matching the pre-optimization behavior. Use this
// only when paired with a text-only MCP client that parses JSON out of
// content[0].text and ignores structuredContent.
func mcpToolResult(payload any) MCPCallToolResult {
	if mcpLegacyContent() {
		body, err := json.Marshal(payload)
		if err != nil {
			body = []byte(fmt.Sprintf("%v", payload))
		}
		return MCPCallToolResult{
			Content:           []MCPContent{{Type: "text", Text: string(body)}},
			StructuredContent: payload,
		}
	}
	return MCPCallToolResult{
		Content:           []MCPContent{{Type: "text", Text: mcpLegacyContentSentinel}},
		StructuredContent: payload,
	}
}

// MCPResponse represents an MCP response. The ID field is emitted as
// "null" on parse errors per JSON-RPC 2.0 §5.1, so we intentionally do
// NOT use omitempty — notifications are filtered upstream by returning
// a nil *MCPResponse from Handle.
type MCPResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
	ID      any       `json:"id"`
}

// NewMCPProtocol creates a new MCP protocol handler. The session ID is
// minted up front so it's available even if a client skips `initialize`
// (some thin MCP clients go straight to `tools/list`). handleInitialize
// may later prepend a clientInfo prefix for telemetry; the ULID suffix
// keeps the ID unique either way.
//
// The projectID is left empty here — runMCP calls SetProjectID after
// resolving the cwd-bound project root, so test seams that don't care
// about per-call project routing can construct an MCPProtocol without
// touching this field.
// NewMCPProtocol accepts any Backend; pass a *Handlers for in-process
// dispatch (legacy daemon, unit tests) or a *client.ClientBackend for
// the v0.5.0 proxy path. Existing callers passing a *Handlers
// continue to compile because Handlers satisfies the interface.
func NewMCPProtocol(backend Backend) *MCPProtocol {
	return &MCPProtocol{
		backend:   backend,
		sessionID: string(core.NewULID(time.Now())),
	}
}

// SetProjectID pins the canonical project root path that this MCP
// subprocess targets. Must be called once at startup, before any tool
// call. An empty value puts the daemon-side resolver in legacy fallback
// mode (h.project default).
func (m *MCPProtocol) SetProjectID(projectID string) {
	m.projectID = projectID
}

// effectiveProjectID returns the project ID to attach to a tool call's
// context. Per-call params win (a future caller may target a different
// project explicitly); the session-bound projectID is the fallback.
func (m *MCPProtocol) effectiveProjectID(paramID string) string {
	if paramID != "" {
		return paramID
	}
	return m.projectID
}

// toolStatsBlurb returns the trailing description text we append to a tool's
// declaration based on observed compression history. The empty string is the
// safe default — no journal, no events, too few samples, or near-zero
// savings all suppress the blurb so the description stays honest. The blurb
// reads as evidence the agent can act on ("recent: ~87% savings over 23
// calls"), not as a marketing claim.
func (m *MCPProtocol) toolStatsBlurb(eventType string) string {
	stats := m.compressionStats()
	s, ok := stats[eventType]
	if !ok || s.n < toolStatsMinSamples || s.rawBytes <= 0 {
		return ""
	}
	saved := s.rawBytes - s.returnedBytes
	if saved <= 0 {
		return ""
	}
	pct := 100.0 * float64(saved) / float64(s.rawBytes)
	if pct < 5.0 {
		// Sub-5% savings probably mean the agent is using return=raw or all
		// outputs fit InlineThreshold — advertising it doesn't help.
		return ""
	}
	return fmt.Sprintf(" Recent: ~%.0f%% token savings over last %d calls.", pct, s.n)
}

// compressionStats returns a per-event-type aggregation of (raw, returned)
// bytes for tool.* events seen in the journal. The result is cached for
// toolStatsTTL because tools/list can be called repeatedly within a single
// session and recomputing on every call would stream the journal up to N
// times. A nil journal or stream error returns an empty map — never an
// error, because tools/list must succeed regardless of telemetry health.
//
// This function uses its own bounded ctx (context.Background + 2s timeout)
// rather than the request ctx by design: tools/list returns the cached
// result even when the request ctx is short, and the agent should not
// have to wait for telemetry that lives behind a slow journal read. The
// child producer goroutine inside Stream honors this ctx, so cancellation
// is observed within ~one event period of the deadline.
func (m *MCPProtocol) compressionStats() map[string]toolCompression {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()

	if m.statsCache != nil && time.Since(m.statsCachedAt) < toolStatsTTL {
		return m.statsCache
	}

	out := map[string]toolCompression{}
	if m.backend == nil {
		m.statsCache = out
		m.statsCachedAt = time.Now()
		return out
	}

	// Bound the time spent here: a misbehaving Stream shouldn't wedge the
	// MCP handshake. 2s is generous for an in-memory mock journal and the
	// production file-backed journal at the 10 MB cap. For the proxy
	// backend (ClientBackend) the timeout doubles as a network-failure
	// guard — if the daemon is slow or unreachable, tools/list still
	// returns within budget without per-tool savings hints.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := m.backend.StreamEvents(ctx, "")
	if err != nil {
		m.statsCache = out
		m.statsCachedAt = time.Now()
		return out
	}

	for e := range stream {
		t := string(e.Type)
		if t != "tool.exec" && t != "tool.read" && t != "tool.fetch" {
			continue
		}
		raw, _ := getInt(e.Data, core.KeyRawBytes)
		ret, _ := getInt(e.Data, core.KeyReturnedBytes)
		s := out[t]
		s.n++
		s.rawBytes += raw
		s.returnedBytes += ret
		out[t] = s
	}

	m.statsCache = out
	m.statsCachedAt = time.Now()
	return out
}

// Handle handles an MCP request.
//
// JSON-RPC notifications — messages without an "id" field — MUST NOT receive
// a response. The MCP handshake uses notifications/initialized; Claude Code
// considers a server that replies to notifications broken. Callers should
// treat a (nil, nil) return as "no response to send".
//
// ctx is the per-request lifetime. The MCP stdio loop derives it from the
// process-level cancellable context so SIGTERM (or future graceful-shutdown
// triggers) cancels any in-flight tool call. Pre-fix, handleToolsCall used
// context.Background() unconditionally, so a long-running dfmt_exec ignored
// shutdown signals and the agent had to wait for the handler's own timeout.
func (m *MCPProtocol) Handle(ctx context.Context, req *MCPRequest) (*MCPResponse, error) {
	if req.ID == nil {
		return nil, nil
	}
	switch req.Method {
	case "initialize":
		return m.handleInitialize(req)
	case "tools/list":
		return m.handleToolsList(req)
	case "tools/call":
		return m.handleToolsCall(ctx, req)
	case "ping":
		return m.handlePing(req)
	default:
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Error: &RPCError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
			ID: req.ID,
		}, nil
	}
}

func (m *MCPProtocol) handleInitialize(req *MCPRequest) (*MCPResponse, error) {
	// Parse clientInfo if present and prepend it to the session ID so
	// telemetry can attribute "(unchanged)" rates to specific agents
	// (claude-code vs cursor vs ad-hoc). Errors are tolerated — the ULID
	// suffix already guarantees uniqueness; clientInfo is just a label.
	if len(req.Params) > 0 {
		var params struct {
			ClientInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		if err := json.Unmarshal(req.Params, &params); err == nil && params.ClientInfo.Name != "" {
			label := params.ClientInfo.Name
			if params.ClientInfo.Version != "" {
				label += "/" + params.ClientInfo.Version
			}
			m.sessionID = label + ":" + m.sessionID
		}
	}

	result := MCPInitializeResult{
		ProtocolVersion: config.DefaultMCPProtocolVersion,
		Capabilities: MCPServerCapabilities{
			Tools: MCPToolsCapability{ListChanged: false},
		},
		ServerInfo: MCPServerInfo{
			Name:    "dfmt",
			Version: version.Current,
		},
	}

	return &MCPResponse{
		JSONRPC: jsonRPCVersion,
		Result:  result,
		ID:      req.ID,
	}, nil
}

// Per-parameter description text that's emitted on every tools/list call.
// Hoisted into named constants so the long prose ships once in source and
// the wire-bytes per session stay under control. Each tools/list response
// previously paid ~470 bytes for each `intent` description and ~360 for
// each `return` description, repeated across exec/read/fetch — about 2 KB
// of redundant prose per session start. The trimmed forms keep the
// "STRONGLY RECOMMENDED" hint and a couple of examples (the parts the
// agent actually uses to decide whether to set the field) and drop the
// long-form rationale (which lives in ADRs 0010/0011 for human readers).

const intentDescExec = "STRONGLY RECOMMENDED. Short phrase about what you need (e.g. 'failing tests', 'imports'). Filters response to matching excerpts; saves 70-90% tokens vs raw."

const intentDescRead = "STRONGLY RECOMMENDED. Short phrase about what you need (e.g. 'database config', 'TODO comments'). Filters response to matching excerpts; saves 70-90% tokens vs raw."

const intentDescFetch = "STRONGLY RECOMMENDED. Short phrase about what you need (e.g. 'API rate limits', 'auth endpoints'). Filters response to matching excerpts; saves 70-90% tokens vs raw."

const returnModeDesc = "Output mode: 'auto' inlines small / summarizes large (default), 'raw' full inline, 'summary' summary+matches only, 'search' matches+vocab only."

func (m *MCPProtocol) handleToolsList(req *MCPRequest) (*MCPResponse, error) {
	// Self-tuning telemetry: append observed compression ratios to the
	// exec/read/fetch descriptions so the agent sees up-to-date evidence
	// that intent-driven calls are paying off. Strings are empty (no
	// suffix) when the journal hasn't accumulated enough samples yet.
	execBlurb := m.toolStatsBlurb("tool.exec")
	readBlurb := m.toolStatsBlurb("tool.read")
	fetchBlurb := m.toolStatsBlurb("tool.fetch")

	tools := []MCPTool{
		{
			Name:        mcpToolExec,
			Description: "Execute code in sandbox. Returns intent-matched excerpts to save tokens." + execBlurb,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code": map[string]any{
						"type":        "string",
						"description": "Code or command to execute",
					},
					"lang": map[string]any{
						"type":        "string",
						"description": "Language: bash, sh, node, python, go, etc. Default: bash",
						"default":     "bash",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": intentDescExec,
					},
					"return": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "raw", "summary", "search"},
						"description": returnModeDesc,
						"default":     "auto",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Timeout in seconds. Default: 60",
						"default":     60,
					},
				},
				"required": []string{"code"},
			},
		},
		{
			Name:        mcpToolRead,
			Description: "Read file via sandbox. Use this instead of native Read." + readBlurb,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to read",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": intentDescRead,
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Byte offset to start reading",
						"default":     0,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum bytes to read",
						"default":     0,
					},
					"return": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "raw", "summary", "search"},
						"description": returnModeDesc,
						"default":     "auto",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        mcpToolFetch,
			Description: "Fetch URL via sandbox. Use this instead of native WebFetch." + fetchBlurb,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "URL to fetch",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": intentDescFetch,
					},
					"method": map[string]any{
						"type":        "string",
						"description": "HTTP method. Default: GET",
						"default":     "GET",
					},
					"return": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "raw", "summary", "search"},
						"description": returnModeDesc,
						"default":     "auto",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Timeout in seconds. Default: 30",
						"default":     30,
					},
				},
				"required": []string{"url"},
			},
		},
		{
			Name:        mcpToolRemember,
			Description: "Record an LLM interaction with token usage for session tracking",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Event type (use 'llm.response' for LLM calls, 'note' for notes)",
						"default":     "llm.response",
					},
					"input_tokens": map[string]any{
						"type":        "integer",
						"description": "Number of input tokens sent to LLM",
					},
					"output_tokens": map[string]any{
						"type":        "integer",
						"description": "Number of output tokens received from LLM",
					},
					"cached_tokens": map[string]any{
						"type":        "integer",
						"description": "Number of cached tokens (prompt cache savings)",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "LLM model name (e.g., claude-opus-4-7, gpt-4o)",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Description or summary of the interaction",
					},
					"tags": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "string",
						},
						"description": "Tags for categorizing the event",
					},
				},
				"required": []string{"type"},
			},
		},
		{
			Name:        mcpToolStats,
			Description: "Get token savings statistics for the session",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        mcpToolSearch,
			Description: "Search session events",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        mcpToolRecall,
			Description: "Build a session snapshot with token budget",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"budget": map[string]any{
						"type":        "integer",
						"description": "Byte budget for snapshot",
					},
					"format": map[string]any{
						"type":        "string",
						"description": "Output format (md, json, xml)",
					},
				},
			},
		},
		{
			Name:        mcpToolGlob,
			Description: "Glob pattern matching for files. Use this instead of native Glob.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Glob pattern (e.g., **/*.go, *.txt)",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "What you need from the results. Only matching files returned.",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        mcpToolGrep,
			Description: "Search for text pattern in files. Use this instead of native Grep.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Search pattern (regex)",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory or file to scope the search to (relative to project root or absolute under it). Defaults to the project root.",
					},
					"files": map[string]any{
						"type":        "string",
						"description": "Basename glob (e.g., *.go, *.txt). Applied per visited file under the search root.",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "What you need from the results. Only matching results returned.",
					},
					"case_insensitive": map[string]any{
						"type":        "boolean",
						"description": "Case insensitive search",
						"default":     false,
					},
					"context": map[string]any{
						"type":        "integer",
						"description": "Lines of context around matches",
						"default":     0,
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        mcpToolEdit,
			Description: "Edit a file by replacing text. Use this instead of native Edit.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to edit",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "The exact string to replace",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "The replacement string",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
		{
			Name:        mcpToolWrite,
			Description: "Write content to a file. Use this instead of native Write.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to write",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Content to write to the file",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	}

	return &MCPResponse{
		JSONRPC: jsonRPCVersion,
		Result: map[string]any{
			"tools": tools,
		},
		ID: req.ID,
	}, nil
}

func (m *MCPProtocol) handleToolsCall(ctx context.Context, req *MCPRequest) (*MCPResponse, error) {
	if m.backend == nil {
		return m.errorResult(req.ID, -32603, "daemon not connected")
	}

	// Attach the per-MCPProtocol session ID so the wire-dedup cache
	// (ADR-0011) keys per-agent. Each `dfmt mcp` subprocess has its own
	// MCPProtocol instance and therefore its own session ID — even when
	// two stdio MCP clients somehow shared a daemon's Handlers (current
	// architecture forks a subprocess per connect, but this guards
	// against future shared-handler refactors).
	ctx = WithSessionID(ctx, m.sessionID)

	// V-18: every per-tool args decode below uses decodeParams (strict —
	// DisallowUnknownFields + trailing-token reject). The socket
	// transport has used the strict decoder since inception; the MCP
	// path was lax (bare json.Unmarshal), which silently swallowed
	// agent-side typos in argument names by producing a zero-value field.
	// A misnamed `limt: 10` argument no longer just runs with limit=0;
	// it surfaces as -32602 so the agent learns about the typo.
	var params struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments,omitempty"`
	}

	if err := decodeParams(req.Params, &params); err != nil {
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Error: &RPCError{
				Code:    -32602,
				Message: fmt.Sprintf("invalid params: %v", err),
			},
			ID: req.ID,
		}, nil
	}
	// Accept both the MCP-spec-compliant underscored names (mcpToolXxx) and
	// the legacy dotted methodXxx names. Old `mcp__dfmt__dfmt.exec` allow
	// rules in user settings.json and any client still calling the dotted
	// name keep working.
	switch params.Name {
	case mcpToolExec, methodExec:
		var args ExecParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Exec(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolRead, methodRead:
		var args ReadParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Read(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolFetch, methodFetch:
		var args FetchParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Fetch(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolRemember, methodRemember:
		var args RememberParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Remember(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolStats, methodStats:
		// Stats accepts an empty params struct — it has no required fields.
		// Empty/absent Args is fine, but malformed JSON should still surface
		// as Invalid params (-32602) so the agent learns about the typo
		// instead of silently getting a zero-value StatsParams. Pre-fix the
		// json.Unmarshal error was discarded; every other case in this
		// switch already checks it and this one was the lone outlier.
		var args StatsParams
		if len(params.Args) != 0 {
			if err := json.Unmarshal(params.Args, &args); err != nil {
				return m.errorResult(req.ID, -32602, err.Error())
			}
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Stats(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolSearch, methodSearch:
		var args SearchParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Search(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolRecall, methodRecall:
		var args RecallParams
		if err := decodeParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Recall(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolGlob, methodGlob:
		var args GlobParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Glob(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolGrep, methodGrep:
		var args GrepParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Grep(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolEdit, methodEdit:
		var args EditParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Edit(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolWrite, methodWrite:
		var args WriteParams
		if err := decodeRequiredParams(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		ctx = WithProjectID(ctx, m.effectiveProjectID(args.ProjectID))
		result, err := m.backend.Write(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	default:
		return m.errorResult(req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

func (m *MCPProtocol) handlePing(req *MCPRequest) (*MCPResponse, error) {
	return &MCPResponse{
		JSONRPC: jsonRPCVersion,
		Result:  map[string]any{},
		ID:      req.ID,
	}, nil
}

func (m *MCPProtocol) errorResult(id any, code int, message string) (*MCPResponse, error) {
	return &MCPResponse{
		JSONRPC: jsonRPCVersion,
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
		ID: id,
	}, nil
}
