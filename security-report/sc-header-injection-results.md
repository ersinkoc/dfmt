# sc-header-injection Results

## Target: DFMT HTTP / JSON-RPC / MCP transport

## Methodology

Walked every `w.Header().Set` / `w.Header().Add` / `http.Error` / `WriteHeader`
call in `internal/transport/http.go` and `internal/transport/dashboard.go`.
Looked for header values that splice user-controlled input (request path,
query, body, headers) into response headers without `\r\n` filtering. Also
checked that fixed headers cannot be overridden by handler order.

## Findings

No issues found by sc-header-injection.

### Why this is N/A in the current code

- All response-header values are **string literals** (`application/json`,
  `nosniff`, `DENY`, the CSP, `text/plain`). No request data ever reaches
  `Header().Set`. (`http.go:223,361,594-599,606-607,623,681-693`,
  `dashboard.go` is HTML/JS only.)
- Go's `net/http` rejects header values containing `\r` or `\n` at the
  `WriteHeader` boundary (`http.checkValidHeaderValue`), so even if a bug
  spliced user input into a header it would not produce a CRLF split — the
  request would error out instead.
- `http.Error` is called with constant strings only
  (`"host header rejected"`, `"cross-origin request rejected"`,
  `"Method not allowed"`, `"Request too large"`, `"Bad request"`,
  `"handlers not configured"`, `"internal error"`). None of these include
  user input. (`http.go:232,238,357,364,371,615,619,626,632,690`.)
- The `Host` and `Origin` request headers are validated against the
  listener and **rejected**, not echoed back, so neither is a header-
  injection vector.

### Confidence: High

No exploitable header-injection sink in the HTTP surface. The MCP stdio
transport does not produce HTTP headers at all.
