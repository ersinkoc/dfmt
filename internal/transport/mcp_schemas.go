package transport

// MCP tool schema definitions. handleToolsList builds its response from
// mcpToolsBuiltin() and overlays the per-tool telemetry blurb (compression
// ratio observed in the journal) onto exec/read/fetch's descriptions before
// sending. The schemas themselves are static — the dynamic surface is only
// the suffix on those three tools.
//
// Each schemaXxx() returns a fresh MCPTool so callers can safely mutate the
// Description field without affecting other invocations.

// mcpToolsBuiltin returns the 11-tool MCP surface in canonical order.
// Order matches the agent-facing presentation, so listing-mutation tests
// can use positional indices (0=exec, 1=read, 2=fetch, ...) without
// pinning to brittle name lookups.
func mcpToolsBuiltin() []MCPTool {
	return []MCPTool{
		schemaExec(),
		schemaRead(),
		schemaFetch(),
		schemaRemember(),
		schemaStats(),
		schemaSearch(),
		schemaRecall(),
		schemaGlob(),
		schemaGrep(),
		schemaEdit(),
		schemaWrite(),
	}
}

func schemaExec() MCPTool {
	return MCPTool{
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
					"description": "Timeout in seconds (max 900; 7200 when async).",
					"default":     60,
				},
				"async": map[string]any{
					"type":        "boolean",
					"description": "Submit and return a job_id immediately instead of waiting. For work longer than the sync ceiling.",
					"default":     false,
				},
				"job_id": map[string]any{
					"type":        "string",
					"description": "Poll a job submitted with async. Returns status 'running' or the finished result.",
				},
				"cancel": map[string]any{
					"type":        "boolean",
					"description": "With job_id: stop that job (kills its whole process tree).",
					"default":     false,
				},
			},
			"required": []string{"code"},
		},
	}
}

func schemaRead() MCPTool {
	return MCPTool{
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
					"description": intentDescRead,
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "First line to return (1-based)",
					"default":     0,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines",
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
	}
}

func schemaFetch() MCPTool {
	return MCPTool{
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
	}
}

func schemaRemember() MCPTool {
	return MCPTool{
		Name:        mcpToolRemember,
		Description: "Save a note/decision to session memory (survives compaction).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{
					"type":        "string",
					"description": "'note' for memories, 'llm.response' for token stats.",
					"default":     "note",
				},
				"input_tokens": map[string]any{
					"type":        "integer",
					"description": "Input tokens sent.",
				},
				"output_tokens": map[string]any{
					"type":        "integer",
					"description": "Output tokens received.",
				},
				"cached_tokens": map[string]any{
					"type":        "integer",
					"description": "Prompt-cache tokens.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Model name.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "The note: what a future session needs to know.",
				},
				"tags": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Retention: summary/decision/strengths/ledger→P2, audit/finding/followup/preserve→P3, else P4 (dropped first).",
				},
			},
			"required": []string{"type"},
		},
	}
}

func schemaStats() MCPTool {
	return MCPTool{
		Name:        mcpToolStats,
		Description: "Get token savings statistics for the session",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func schemaSearch() MCPTool {
	return MCPTool{
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
				"type": map[string]any{
					"type":        "string",
					"description": "Filter by event type, e.g. 'note' to search only your own memories.",
				},
			},
			"required": []string{"query"},
		},
	}
}

func schemaRecall() MCPTool {
	return MCPTool{
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
	}
}

func schemaGlob() MCPTool {
	return MCPTool{
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
	}
}

func schemaGrep() MCPTool {
	return MCPTool{
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
					"description": "Directory or file to scope the search to. Defaults to the project root.",
				},
				"files": map[string]any{
					"type":        "string",
					"description": "Basename glob (e.g. *.go) applied per visited file.",
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
	}
}

func schemaEdit() MCPTool {
	return MCPTool{
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
					"description": "The exact string to replace. Must be unique in the file unless replace_all is set.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement string",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace all occurrences.",
					"default":     false,
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func schemaWrite() MCPTool {
	return MCPTool{
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
	}
}
