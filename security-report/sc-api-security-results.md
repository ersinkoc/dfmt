# sc-api-security Results

## Target: DFMT JSON-RPC + MCP API surface

## Methodology

- Walked every JSON-RPC method handler on the HTTP transport (`http.go`)
  and the socket transport (`socket.go` → `dispatch`), and every MCP
  tools/call branch (`mcp.go`).
- Verified parameter decoding via `decodeRPCParams`/`decodeParams` returns
  a JSON-RPC -32602 error on malformed input rather than zero-value-
  filling silently (V-16 fix referenced in `http.go:444-459`).
- Checked that JSON-RPC error responses preserve the request ID per
  spec (`§5.1` null-on-parse-error in `http.go:392-399`,
  `:638-645`).
- Inspected the request-body and header limits for OOM/DoS protection
  (covered separately by sc-rate-limiting).
- Reviewed how the MCP `tools/call` path validates `name` against the
  registered set and how it rejects unknown methods.
- Checked for unauthenticated info leaks via stats / daemons endpoints.

## Findings

### sc-api-security-01 — `/api/stats` and `/` accept the same JSON-RPC body without method allow-listing on the API endpoint

- **Severity:** Info
- **CWE:** CWE-285 (Improper Authorization)
- **File:** `internal/transport/http.go:611-678`
- **Description:** `handleAPIStats` reads a JSON-RPC request body and
  always invokes `s.handlers.Stats(...)` regardless of `req.Method`. The
  `method` field is parsed and ignored. An attacker who can hit the
  endpoint and supply any well-formed JSON-RPC body gets the stats
  response. This is acceptable because the endpoint is intentionally a
  stats-only path and the rest of the API is reachable on `/`, but a
  reader of the code could mistakenly believe the method field is
  authoritative.
- **Attack scenario:** No exploitable scenario — every reachable caller is
  loopback, the response is the same as `dfmt.stats` on `/`, and no auth
  is bypassed. Documentation hazard only.
- **Evidence:**
  ```go
  // http.go:637-647
  var req Request
  if err := json.Unmarshal(body, &req); err != nil { ... }
  var params StatsParams
  if len(req.Params) != 0 { ... }
  resp, err := s.handlers.Stats(r.Context(), params)  // method never checked
  ```
- **Remediation:** Either reject when `req.Method` is non-empty and
  `!= "dfmt.stats"`, or document the endpoint as method-agnostic in a
  code comment so future maintainers do not introduce a method dispatch
  expectation. Pure documentation fix; no behavior change required.
- **Confidence:** High (code reading; not exploitable as-is).

### Verified-clean controls

| Control | Location | Status |
|---|---|---|
| JSON-RPC parse-error returns null ID per §5.1 | `http.go:392-399`, `:638-645` | PASS |
| Invalid params surface -32602 not -32603 | `http.go:447-459`, `socket.go:215-222` | PASS |
| Unknown method returns -32601 | `http.go:426-431`, `socket.go:316-318`, `mcp.go:298-305` | PASS |
| Method dispatch table exhaustive (no fall-through into wrong handler) | `http.go:402-432` | PASS |
| Body size capped (1 MiB) | `http.go:369`, `:630` | PASS |
| Header size capped (16 KiB) | `http.go:170` | PASS |
| Slowloris guard (5s `ReadHeaderTimeout`) | `http.go:167` | PASS |
| Recursive request payload cap (per-line 1 MiB) | `jsonrpc.go:38,77-79` | PASS |
| MCP notifications (no ID) silently ignored | `mcp.go:285-287` | PASS |
| MCP tool name validated against registered list | `mcp.go:handleToolsCall` (dispatch) | PASS |
| HTTP method enforcement (POST only on JSON-RPC) | `http.go:363-365`, `:625-628` | PASS |
| `Content-Type: application/json` set on every response | `http.go:361,623,693` | PASS |
| Panic recovery on every entry point | `http.go:343-354,612-617,687-692`, `socket.go:163-169` | PASS |
| `/api/daemons` filtered to caller's project (V-4 fix) | `http.go:725-735` | PASS |

### Confidence: High

The API surface is small (11 methods × 3 transports), all parameter
decoding is via `json.Unmarshal` into typed structs (no reflection-based
allocation, no map-key control), and there is no auth/session token to
mishandle. The single Info-level note is a maintainability concern, not
a vulnerability.
