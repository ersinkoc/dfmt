# sc-csrf Results

## Target: DFMT HTTP / JSON-RPC dashboard surface

## Methodology

- Enumerated every state-changing endpoint and its accepted methods
  (`http.go:155-162`).
- Verified the same-origin gate (`wrapSecurity` → `isAllowedOrigin`,
  `http.go:222-261`) blocks cross-origin POSTs.
- Checked for cookie-based authentication (none).
- Checked for SameSite / CSRF-token mechanisms.
- Reviewed the dashboard's only POST issuer (`/api/stats`) for replayability.

## Findings

No issues found by sc-csrf. — N/A: dashboard has no cookie/credentialed
auth, all state-changing JSON-RPC endpoints require POST + same-origin
`Origin` (`http.go:236-241`), and `/api/daemons` / `/healthz` /
`/readyz` are pure read endpoints.

### Supporting analysis

- The `/` JSON-RPC endpoint and `/api/stats` reject any non-POST method
  (`http.go:363-365`, `:625-628`). A cross-origin form-encoded POST cannot
  reach them because:
  1. A foreign-origin `<form>` POST sets `Origin: <attacker>` →
     `isAllowedOrigin` returns false → 403 (`http.go:236-241`).
  2. `Content-Type: application/json` triggers a CORS preflight; the
     server emits no `Access-Control-Allow-*` headers, so the preflight
     fails closed and the actual request never fires.
- No `Set-Cookie` is ever issued; the daemon does not authenticate the
  caller — its trust boundary is loopback + port-file 0o600 + (Unix)
  socket 0o700 (`http.go:529-571`, `socket.go:97`). A CSRF flow needs
  ambient authority on the victim's browser; there is none here.
- The same-origin gate also covers the `X-DFMT-Session` header path
  (`http.go:384-388`): the header only re-enables wire-dedup, never
  authenticates.
- `Stats()` (the only POST endpoint reachable from the dashboard JS) is
  read-only: it streams the journal and returns aggregates. Even if a
  CSRF bypass were found, a forged call would not mutate state.

### Confidence: High
