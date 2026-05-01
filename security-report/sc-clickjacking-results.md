# sc-clickjacking Results

## Target: DFMT dashboard (`internal/transport/http.go`,
`internal/transport/dashboard.go`)

## Methodology

- Inspected the dashboard handler for `X-Frame-Options` and
  `Content-Security-Policy: frame-ancestors`.
- Checked whether the JS endpoint and JSON endpoints set frame-protection
  headers (defense in depth).
- Reviewed the CSP for `frame-ancestors` *and* a `default-src`/`base-uri`
  pair tight enough to keep an attacker from bouncing iframes through a
  child-window hop.

## Findings

No issues found by sc-clickjacking.

### Why this is N/A

- `handleDashboard` (`http.go:592-601`) sets:
  - `X-Frame-Options: DENY`
  - `Content-Security-Policy: ... frame-ancestors 'none'; base-uri 'none'`
  Both directives independently block any `<iframe>` / `<frame>` /
  `<object>` / `<embed>` from rendering the dashboard, including in
  modern browsers that ignore `X-Frame-Options` in favor of CSP.
- The CSP also pins `default-src 'self'` and `script-src 'self'`, so an
  attacker cannot use a CSP-permitted path (e.g. inline-script or remote
  origin) to mount a UI-redress overlay from a frameable child resource.
- `/dashboard.js` is served as `application/javascript` with `nosniff`
  (`http.go:606-607`); browsers will not load it as a document, so it
  cannot be framed independently.
- The JSON endpoints (`/`, `/api/stats`, `/api/daemons`) return JSON
  payloads with `application/json`. Browsers do not navigate to JSON
  responses as top-level documents, and the same-origin Origin gate
  rejects any cross-origin XHR. There is nothing clickable to redress.

### Confidence: High
