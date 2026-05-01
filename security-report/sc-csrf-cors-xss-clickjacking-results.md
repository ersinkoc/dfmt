# sc-csrf / sc-cors / sc-xss / sc-clickjacking — Browser-Surface Hunt

The HTTP server's only browser-facing surface is the dashboard at
`/dashboard` (and `/dashboard.js`). The JSON-RPC POST at `/` and the
public `/api/*` endpoints are reachable from a browser only if Origin
and Host pass `wrapSecurity`.

## CSRF (CWE-352)

**Status:** mitigated by same-origin Origin/Host gate.

`internal/transport/http.go::wrapSecurity` enforces:
- `Origin` header (when present) must equal `http://<listener-host>:<port>`.
- `Host` header must equal the literal listener address, `localhost:<port>`,
  or `[::1]:<port>` (DNS-rebinding protection — F-17 closure).
- Health endpoints `/healthz`, `/readyz` bypass these checks (intentional
  for k8s-style readiness probes; they don't expose state).

This is a same-origin enforcement, not a CSRF token, but for a
same-origin-only API it has the same effect: a foreign page cannot
make the browser send a request that the server accepts.

**Residual:** legitimate non-browser clients (curl, dfmt CLI itself)
omit Origin entirely; the gate only fires when Origin is present. This
is correct behavior — non-browser clients aren't subject to the same
threat model.

## CORS (CWE-942 if mis-configured)

**Status:** strict — no permissive CORS.

The server returns **no** `Access-Control-Allow-Origin` header. Any
cross-origin browser request is refused with 403 by the same-origin
check. There is no "allow list" of foreign origins; the dashboard is
designed to be loaded only from its own origin.

## XSS (CWE-79)

**Status:** safe.

`DashboardHTML` is a static string with no template substitution. All
runtime data (events_total, token counts, project names, etc.) is
inserted via `textContent` in `DashboardJS`. No `innerHTML` of dynamic
data, no `insertAdjacentHTML`, no `eval`/`Function`/`setTimeout(string)`.
The single `container.innerHTML = ''` call is a clear (no content), not
an injection.

CSP header on the dashboard:

```
default-src 'self';
style-src 'self' 'unsafe-inline';
script-src 'self';
img-src 'self' data:;
connect-src 'self';
frame-ancestors 'none';
base-uri 'none'
```

The script is served from `/dashboard.js` (same origin) so `script-src
'self'` is sufficient — no fragile inline-hash list to maintain. Inline
styles are allowed (`unsafe-inline` for `style-src`); this is a small
trade-off but keeps the CSS embedded in the HTML.

## Clickjacking (CWE-1021)

**Status:** mitigated.

- `X-Frame-Options: DENY` on the dashboard.
- `frame-ancestors 'none'` in the CSP (modern equivalent).

Both belt and suspenders.

## Header injection (CWE-93)

`X-Content-Type-Options: nosniff` set on every response (including the
JSON-RPC POST at `/`). Other security headers (`Strict-Transport-Security`,
`Referrer-Policy`) are not set — this is a loopback-only HTTP server, so
HSTS would be pointless and a Referrer leak isn't reachable.

## Findings

No CSRF / CORS / XSS / Clickjacking issues identified.

### Hardening notes (Info)

- Consider setting `Referrer-Policy: no-referrer` on the dashboard for
  defense in depth — the dashboard navigates to nothing, but a future
  external link would leak the local-port URL otherwise.
- The `unsafe-inline` style-src directive could be tightened by moving
  styles to a separate file (`/dashboard.css`). Low value; no script
  execution risk.
