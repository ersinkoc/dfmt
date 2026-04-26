package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ersinkoc/dfmt/internal/config"
)

// MCPProtocol implements the Model Context Protocol over JSON-RPC.
type MCPProtocol struct {
	handlers *Handlers
}

// MCPTool represents an MCP tool definition.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
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
// The JSON-stringified payload lives in content[0].text so old text-only
// clients still see the data; structuredContent surfaces the typed object
// for clients that prefer it.
func mcpToolResult(payload any) MCPCallToolResult {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(fmt.Sprintf("%v", payload))
	}
	return MCPCallToolResult{
		Content:           []MCPContent{{Type: "text", Text: string(body)}},
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

// NewMCPProtocol creates a new MCP protocol handler.
func NewMCPProtocol(handlers *Handlers) *MCPProtocol {
	return &MCPProtocol{handlers: handlers}
}

// Handle handles an MCP request.
//
// JSON-RPC notifications — messages without an "id" field — MUST NOT receive
// a response. The MCP handshake uses notifications/initialized; Claude Code
// considers a server that replies to notifications broken. Callers should
// treat a (nil, nil) return as "no response to send".
func (m *MCPProtocol) Handle(req *MCPRequest) (*MCPResponse, error) {
	if req.ID == nil {
		return nil, nil
	}
	switch req.Method {
	case "initialize":
		return m.handleInitialize(req)
	case "tools/list":
		return m.handleToolsList(req)
	case "tools/call":
		return m.handleToolsCall(req)
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
	result := MCPInitializeResult{
		ProtocolVersion: config.DefaultMCPProtocolVersion,
		Capabilities: MCPServerCapabilities{
			Tools: MCPToolsCapability{ListChanged: false},
		},
		ServerInfo: MCPServerInfo{
			Name:    "dfmt",
			Version: "0.1.0",
		},
	}

	return &MCPResponse{
		JSONRPC: jsonRPCVersion,
		Result:  result,
		ID:      req.ID,
	}, nil
}

func (m *MCPProtocol) handleToolsList(req *MCPRequest) (*MCPResponse, error) {
	tools := []MCPTool{
		{
			Name:        mcpToolExec,
			Description: "Execute code in sandbox. Returns intent-matched excerpts to save tokens.",
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
						"description": "STRONGLY RECOMMENDED. A short phrase describing what you actually need from the output (e.g. 'failing tests', 'imports', 'error message'). When provided, the response is filtered down to matching excerpts plus a summary, saving 70-90% of tokens vs the raw output. Without intent, large outputs (>4KB) return only a summary and the full bytes are stashed for later retrieval — set return=\"raw\" if you genuinely need the full output.",
					},
					"return": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "raw", "summary", "search"},
						"description": "Output mode. 'auto' (default): inline if small, summary+matches if large. 'raw': always inline (full token cost — use only when you need the bytes). 'summary': summary + intent-matches, never inline. 'search': matches + vocabulary only, the most token-efficient mode.",
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
			Description: "Read file via sandbox. Use this instead of native Read.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to read",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "STRONGLY RECOMMENDED. A short phrase describing what you need from the file (e.g. 'database config', 'TODO comments', 'exported types'). When provided, the response is filtered to matching excerpts. Without intent, files larger than 4KB return a summary only and the full content is stashed for retrieval; small files always inline.",
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
						"description": "Output mode. 'auto' (default): inline if small, summary+matches if large. 'raw': always inline (full token cost — use only when you need the bytes). 'summary': summary + intent-matches, never inline. 'search': matches + vocabulary only, the most token-efficient mode.",
						"default":     "auto",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        mcpToolFetch,
			Description: "Fetch URL via sandbox. Use this instead of native WebFetch.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "URL to fetch",
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "STRONGLY RECOMMENDED. A short phrase describing what you need from the response (e.g. 'API rate limits', 'auth endpoints', 'error codes'). When provided, the response is filtered to matching excerpts. Without intent, responses larger than 4KB return a summary only and the full body is stashed.",
					},
					"method": map[string]any{
						"type":        "string",
						"description": "HTTP method. Default: GET",
						"default":     "GET",
					},
					"return": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "raw", "summary", "search"},
						"description": "Output mode. 'auto' (default): inline if small, summary+matches if large. 'raw': always inline (full token cost — use only when you need the bytes). 'summary': summary + intent-matches, never inline. 'search': matches + vocabulary only, the most token-efficient mode.",
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
					"files": map[string]any{
						"type":        "string",
						"description": "File pattern (e.g., *.go, *.txt)",
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

func (m *MCPProtocol) handleToolsCall(req *MCPRequest) (*MCPResponse, error) {
	if m.handlers == nil {
		return m.errorResult(req.ID, -32603, "daemon not connected")
	}

	var params struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments,omitempty"`
	}

	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Error: &RPCError{
				Code:    -32602,
				Message: fmt.Sprintf("invalid params: %v", err),
			},
			ID: req.ID,
		}, nil
	}

	ctx := context.Background()
	// Accept both the MCP-spec-compliant underscored names (mcpToolXxx) and
	// the legacy dotted methodXxx names. Old `mcp__dfmt__dfmt.exec` allow
	// rules in user settings.json and any client still calling the dotted
	// name keep working.
	switch params.Name {
	case mcpToolExec, methodExec:
		var args ExecParams
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Exec(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Read(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Fetch(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Remember(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: jsonRPCVersion,
			Result:  mcpToolResult(result),
			ID:      req.ID,
		}, nil

	case mcpToolStats, methodStats:
		var args StatsParams
		if params.Args != nil {
			json.Unmarshal(params.Args, &args)
		}
		result, err := m.handlers.Stats(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Search(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Recall(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Glob(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Grep(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Edit(ctx, args)
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
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Write(ctx, args)
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
