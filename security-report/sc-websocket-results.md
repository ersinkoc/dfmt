# sc-websocket Results

## Target: DFMT

## Findings

**No WebSocket usage found.**

- No `websocket` keyword in Go files
- No `ws://` or `wss://` URL patterns
- No `gorilla/websocket` or similar imports

## Conclusion

sc-websocket is **not applicable** to this codebase. DFMT uses Unix socket for CLI-daemon communication and HTTP for JSON-RPC, not WebSockets.