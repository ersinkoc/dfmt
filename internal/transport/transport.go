package transport

// Package transport provides multiple transport layers for the DFMT daemon:
//   - Unix socket (primary control plane, line-delimited JSON-RPC 2.0)
//   - HTTP server (opt-in, localhost-only, JSON-RPC 2.0)
//   - MCP over stdio (for agent integration)
//
// All transports serve the same operations with identical semantics;
// only wire formats differ.
