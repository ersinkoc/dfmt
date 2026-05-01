# sc-cors Results

## Target: DFMT HTTP server (`internal/transport/http.go`, `dashboard.go`)

## Methodology

- Searched the codebase for any `Access-Control-*` header emission.
- Reviewed the `wrapSecurity` middleware (`http.go:222-245`) for origin
  reflection, wildcard with credentials, null-origin acceptance, and the
  pre-flight (`OPTIONS`) path.
- Verified the `isAllowedOrigin` comparison (`http.go:247-261`) is exact-
  match against the listener address, with no normalization that would
  collapse case, ports, or trailing-slash variants into a permissive set.
- Verified `/healthz` and `/readyz` are intentionally excluded from the
  origin gate (documented; harmless because they return only `"ok"`).
- Confirmed the dashboard JS only calls same-origin URLs (`/api/stats`,
  `/api/daemons`).

## Findings

No issues found by sc-cors.

### Why CORS is correctly handled

- **No `Access-Control-Allow-Origin` ever emitted.** Browsers therefore
  refuse cross-origin reads of every endpoint by default; there is no CORS
  trust to misconfigure.
- The `Origin` header is **rejected** when present and not equal to
  `http://<listener-addr>` (`http.go:236-241`). This is stricter than CORS
  itself — even simple GETs from a foreign origin fail before the handler
  runs.
- No credentialed flow exists: there are no cookies, no `Authorization`
  header schema, no localStorage tokens. CSRF-via-CORS is moot.
- `isAllowedOrigin` returns `false` for every non-TCP listener
  (`http.go:251-259`), so a Unix-socket-fronted dashboard cannot be tricked
  into accepting a foreign Origin via empty/synthetic addresses.

### Residual: low-impact behavior, not a finding

When `Origin` is absent (server-to-server JSON-RPC clients, `curl` without
`-H Origin:`) the request is allowed through. This is intentional —
browsers always send `Origin` on cross-origin XHR and `fetch`, so the
gate fires for every browser-attacker scenario. Non-browser callers are
out of the threat model (they have already cleared the loopback bind
+ port-file 0600 + same-host filesystem checks).

### Confidence: High
