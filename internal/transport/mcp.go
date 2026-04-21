package transport

import (
	"context"
	"encoding/json"
	"fmt"
)

// MCPProtocol implements the Model Context Protocol over JSON-RPC.
type MCPProtocol struct {
	handlers *Handlers
}

// MCPTool represents an MCP tool definition.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// MCPInitializeResult is the result of initialization.
type MCPInitializeResult struct {
	ProtocolVersion string              `json:"protocolVersion"`
	Capabilities    MCPClientCapabilities `json:"capabilities"`
	ServerInfo      MCPServerInfo        `json:"serverInfo"`
}

// MCPClientCapabilities represents client capabilities.
type MCPClientCapabilities struct {
	Roots struct {
		ListChanged bool `json:"listChanged"`
	} `json:"roots"`
	Sampling struct{} `json:"sampling"`
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

// MCPResponse represents an MCP response.
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  any         `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      any         `json:"id,omitempty"`
}

// NewMCPProtocol creates a new MCP protocol handler.
func NewMCPProtocol(handlers *Handlers) *MCPProtocol {
	return &MCPProtocol{handlers: handlers}
}

// Handle handles an MCP request.
func (m *MCPProtocol) Handle(req *MCPRequest) (*MCPResponse, error) {
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
			JSONRPC: "2.0",
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
		ProtocolVersion: "2024-11-05",
		Capabilities: MCPClientCapabilities{},
		ServerInfo: MCPServerInfo{
			Name:    "dfmt",
			Version: "0.1.0",
		},
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		Result:  result,
		ID:      req.ID,
	}, nil
}

func (m *MCPProtocol) handleToolsList(req *MCPRequest) (*MCPResponse, error) {
	tools := []MCPTool{
		{
			Name:        "dfmt.remember",
			Description: "Record an LLM interaction with token usage for session tracking",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type": map[string]interface{}{
						"type":        "string",
						"description": "Event type (use 'llm.response' for LLM calls, 'note' for notes)",
						"default":     "llm.response",
					},
					"input_tokens": map[string]interface{}{
						"type":        "integer",
						"description": "Number of input tokens sent to LLM",
					},
					"output_tokens": map[string]interface{}{
						"type":        "integer",
						"description": "Number of output tokens received from LLM",
					},
					"cached_tokens": map[string]interface{}{
						"type":        "integer",
						"description": "Number of cached tokens (prompt cache savings)",
					},
					"model": map[string]interface{}{
						"type":        "string",
						"description": "LLM model name (e.g., claude-opus-4-7, gpt-4o)",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Description or summary of the interaction",
					},
					"tags": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string",
						},
						"description": "Tags for categorizing the event",
					},
				},
				"required": []string{"type"},
			},
		},
		{
			Name:        "dfmt.stats",
			Description: "Get token savings statistics for the session",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "dfmt.search",
			Description: "Search session events",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum results",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "dfmt.recall",
			Description: "Build a session snapshot with token budget",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"budget": map[string]interface{}{
						"type":        "integer",
						"description": "Byte budget for snapshot",
					},
					"format": map[string]interface{}{
						"type":        "string",
						"description": "Output format (md, json, xml)",
					},
				},
			},
		},
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		Result: map[string]interface{}{
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
		Name   string          `json:"name"`
		Args   json.RawMessage `json:"arguments,omitempty"`
	}

	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &RPCError{
				Code:    -32602,
				Message: fmt.Sprintf("invalid params: %v", err),
			},
			ID: req.ID,
		}, nil
	}

	ctx := context.Background()
	switch params.Name {
	case "dfmt.remember":
		var args RememberParams
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Remember(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: "2.0",
			Result:  result,
			ID:      req.ID,
		}, nil

	case "dfmt.stats":
		var args StatsParams
		if params.Args != nil {
			json.Unmarshal(params.Args, &args)
		}
		result, err := m.handlers.Stats(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: "2.0",
			Result:  result,
			ID:      req.ID,
		}, nil

	case "dfmt.search":
		var args SearchParams
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Search(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: "2.0",
			Result:  result,
			ID:      req.ID,
		}, nil

	case "dfmt.recall":
		var args RecallParams
		if err := json.Unmarshal(params.Args, &args); err != nil {
			return m.errorResult(req.ID, -32602, err.Error())
		}
		result, err := m.handlers.Recall(ctx, args)
		if err != nil {
			return m.errorResult(req.ID, -32603, err.Error())
		}
		return &MCPResponse{
			JSONRPC: "2.0",
			Result:  result,
			ID:      req.ID,
		}, nil

	default:
		return m.errorResult(req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

func (m *MCPProtocol) handlePing(req *MCPRequest) (*MCPResponse, error) {
	return &MCPResponse{
		JSONRPC: "2.0",
		Result:  map[string]interface{}{},
		ID:      req.ID,
	}, nil
}

func (m *MCPProtocol) errorResult(id any, code int, message string) (*MCPResponse, error) {
	return &MCPResponse{
		JSONRPC: "2.0",
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
		ID: id,
	}, nil
}
