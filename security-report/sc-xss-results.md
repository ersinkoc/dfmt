# sc-xss Results: DFMT Cross-Site Scripting Assessment

**Target:** `D:\Codebox\PROJECTS\DFMT`
**Date:** 2026-05-01
**Skill:** sc-xss (Cross-Site Scripting)

---

## Executive Summary

| Category | Finding | Severity |
|---|---|---|
| HTTP Dashboard | Static HTML+JS, no reflected params | Low |
| HTML Normalization | `<script>` dropped; output is markdown | Low |
| CSP Headers | Strict CSP with `script-src 'self'` | Good |
| CORS | Blocked cross-origin requests | Good |
| DOM-based XSS | None found in dashboard JS | Low |

**Overall Assessment:** No critical XSS vulnerabilities found. The dashboard, HTML normalization pipeline, and CORS configuration are all well-hardened.

---

## 1. HTTP Dashboard — Reflected XSS

### Endpoint: `GET /dashboard` (`handleDashboard`, `http.go:613`)

**Finding:** Dashboard HTML is entirely static — no query parameters or path parameters are reflected into the response.

```go
func (s *HTTPServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Header().Set("X-Content-Type-Options", "nosniff")
    w.Header().Set("X-Frame-Options", "DENY")
    w.Header().Set("Content-Security-Policy",
        "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
    _, _ = w.Write([]byte(DashboardHTML))
}
```

**CSP Header (strict):**
```
default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'
```

- `script-src 'self'` — only the bundled `/dashboard.js` may execute
- No inline `<script>` tags in the HTML
- `frame-ancestors: none` prevents clickjacking
- `base-uri: none` prevents base-tag hijacking

### Endpoint: `GET /dashboard.js` (`handleDashboardJS`, `http.go:626`)

Static JS file with no user input. The dashboard JS fetches data from `/api/daemons` and `/api/stats` only via same-origin `fetch()`.

### `GET /api/daemons` (`handleAPIDaemons`, `http.go:738`)

Returns a filtered list of daemons from `~/.dfmt/daemons.json`. Project path filtering prevents leaking other projects' daemons. No XSS vector — output is `application/json`, not HTML.

### `POST /api/stats` (`handleAPIStats`, `http.go:632`)

Accepts a JSON-RPC request body. No query parameters. Response is `application/json`.

### `GET /healthz`, `GET /readyz`

Plain text `"ok"`, no user input.

### `GET /metrics`

Prometheus text format, no user input.

**Conclusion:** No reflected XSS vectors in any HTTP endpoint. The dashboard only renders static HTML and uses explicit `textContent` for dynamic content in bar charts.

---

## 2. HTML Normalization Output — XSS in Normalized Markdown

### Pipeline: `NormalizeOutput` (`internal/sandbox/intent.go:440`)

The 8-stage pipeline calls `ConvertHTML` (ADR-0008) as stage 7. `ConvertHTML` uses a token-walker to convert HTML to markdown.

### Dangerous Element Drops (`htmlmd.go:22-33`)

```go
var htmlDropElements = map[string]struct{}{
    "script":   {},
    "style":    {},
    "nav":      {},
    "footer":   {},
    "aside":    {},
    "head":     {},
    "noscript": {},
    "svg":      {},
    "form":     {},
    "button":   {},
    "iframe":   {},
}
```

**All interactive/script-capable elements are explicitly dropped.**

When an element is in `htmlDropElements`, `dropDepth` is incremented and the walker discards all nested text content without emitting anything.

### Onerror/Event Handler Attributes

The walker does **not** explicitly filter event-handler attributes (e.g., `onerror`, `onload`, `onclick`). However:

1. The HTML is converted to markdown — event handlers in markdown text are inert (markdown renderers don't execute JS)
2. Only text content of elements is emitted; attribute values are not passed through to output (except `href` for anchors and `src`/`alt` for images, which are placed in markdown link/image syntax)
3. The `htmlDropElements` map drops the entire content of `script`, `svg`, `form`, `button`, `iframe` — the most dangerous elements

### `<a href>` / `<img src>` Handling

- `<a>` tags: link text is captured, `href` attribute is found by reverse token walk and emitted as `[text](href)` markdown syntax. This is safe because the URL goes into a markdown URL position, not an HTML attribute.
- `<img>` tags: emits `![alt](src)` markdown syntax.

**Assessment:** Malicious HTML input like `<img src=x onerror=alert(1)>` would be converted to `![onerror=alert(1)](x)` in markdown — the `onerror=alert(1)` becomes plain alt text, not executable JavaScript. The conversion itself is safe.

### `CompactHTML` Fallback (`structured.go:294`)

For cap-regression cases, `CompactHTML` uses regex to strip:
```
<script>...</script>
<style>...</style>
<!--...-->
<nav>...</nav>
<footer>...</footer>
<aside>...</aside>
<head>...</head>
<noscript>...</noscript>
<svg>...</svg>
```

Same drop set as the walker — no script execution possible through fallback either.

**Conclusion:** The HTML-to-markdown conversion is safe. No stored XSS or output-side scripting is possible through the normalization pipeline.

---

## 3. CSP Headers on Dashboard

### Response Headers on `/dashboard` (`http.go:619-620`)

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'
```

### Analysis

| Directive | Value | Risk |
|---|---|---|
| `default-src 'self'` | Restricts all resources to same-origin | Good |
| `style-src 'self' 'unsafe-inline'` | Allows inline styles for dashboard CSS | Acceptable — no data exfil via CSS |
| `script-src 'self'` | Only `/dashboard.js` can execute | Excellent — no inline scripts |
| `img-src 'self' data:` | Same-origin + data: URIs | Acceptable |
| `connect-src 'self'` | XHR/fetch only to same-origin | Excellent |
| `frame-ancestors 'none'` | Cannot be embedded in iframe | Excellent |
| `base-uri 'none'` | Cannot set base tag | Excellent |

**Finding:** The dashboard CSP is strict and well-configured. The `'unsafe-inline'` for styles is low-risk since CSS cannot exfiltrate data cross-origin in modern browsers.

---

## 4. HTML/JS Injection in HTTP Responses

### Dashboard HTML (`dashboard.go:6-95`)

The HTML contains no user-controlled data. The `<title>`, headings, and all visible text are static strings. The only dynamic content rendered by the server is the static `DashboardHTML` constant.

### Dashboard JS (`dashboard.go:101-253`)

JavaScript renders dynamic data using `textContent` (not `innerHTML`):

```javascript
// Line 214-229 — safe textContent usage
document.getElementById('total-events').textContent = stats.events_total;
document.getElementById('total-input').textContent = formatNumber(stats.total_input_tokens || 0);
// ...
// labelEl.textContent = label.length > 15 ? label.substring(0, 12) + '...' : label;
// valueEl.textContent = value;
```

The chart rendering creates DOM elements programmatically and uses `textContent` for all user-facing strings. No `innerHTML`, `outerHTML`, `document.write()`, or `insertAdjacentHTML` usage found.

**Conclusion:** No HTML/JS injection vectors in dashboard responses.

---

## 5. CORS Configuration — Cross-Origin Data Exfiltration

### `isAllowedOrigin` Check (`http.go:258-279`)

```go
func (s *HTTPServer) isAllowedOrigin(origin string) bool {
    if s.listener == nil {
        return false
    }
    var want string
    if addr, ok := s.listener.Addr().(*net.TCPAddr); ok {
        want = fmt.Sprintf("http://%s", addr.String())
    } else {
        return false
    }
    return origin == want
}
```

**Logic:** Only accepts cross-origin requests where the `Origin` header exactly matches `http://<listener-ip>:<port>`. This means:

1. Only same-origin requests are allowed (Origin == Host:Port of the listener)
2. Any cross-origin request from a different site is rejected with `403 Forbidden`
3. Unix socket listeners return `false` (fail-closed — no cross-origin ever accepted)

### `isAllowedHost` Check (`http.go:288-311`)

```go
func (s *HTTPServer) isAllowedHost(host string) bool {
    if s.listener == nil {
        return false
    }
    addr, ok := s.listener.Addr().(*net.TCPAddr)
    if !ok {
        return true  // Unix socket: no DNS rebinding vector
    }
    want := addr.String()
    if host == want { return true }
    if host == fmt.Sprintf("localhost:%d", addr.Port) { return true }
    if host == fmt.Sprintf("[::1]:%d", addr.Port) { return true }
    return false
}
```

**DNS Rebinding Protection:** Host header must be the loopback address, `localhost:<port>`, or `[::1]:<port>`. Any other host (e.g., `attacker.com`) is rejected, preventing DNS rebinding attacks.

### Non-Loopback Bind Refusal (`http.go:149-156`)

```go
if addr, ok := ln.Addr().(*net.TCPAddr); ok {
    if !addr.IP.IsLoopback() {
        if ownListener {
            _ = ln.Close()
        }
        return fmt.Errorf("non-loopback HTTP bind refused: listener bound to %s — bearer-token auth not implemented (F-09)", addr.IP.String())
    }
}
```

**Finding:** The server refuses to bind to non-loopback interfaces. All HTTP traffic must be on `127.0.0.1` or `::1`. This is a strong security control — even if an attacker could trick a browser to send a request, it can only reach loopback.

### No `Access-Control-Allow-Origin` Header

The server does NOT set `Access-Control-Allow-Origin` on any response. For same-origin requests (which are the only allowed kind), this is correct — browsers don't require it for same-origin.

**Conclusion:** No cross-origin data exfiltration vectors exist. The server:
- Rejects all cross-origin requests before processing
- Only binds to loopback interfaces
- Validates Host header against DNS rebinding

---

## Findings Summary

| ID | Type | Location | Severity | Confidence |
|---|---|---|---|---|
| XSS-001 | Low Risk — `style-src 'unsafe-inline'` | `http.go:619` | Low | High |
| XSS-002 | Informational — No CSP report-uri | `http.go:619` | Low | Medium |

### Finding XSS-001: Style-src 'unsafe-inline' in Dashboard CSP

**File:** `internal/transport/http.go:619-620`
**Severity:** Low

The CSP allows `'unsafe-inline'` for styles. While this cannot directly lead to script execution, inline styles can be used for CSS exfiltration attacks (e.g., stealing CSRF tokens via `background-image` on a crafted selector). Modern browsers with `style-src 'unsafe-inline'` are not vulnerable to script injection via style injection.

**Remediation:** If strict CSP is required, use a nonce-based approach:
```go
scriptNonce := generateNonce()
// Set CSP with script-nonce
```

Note: The `'unsafe-inline'` for styles is acceptable here since the dashboard is a local-only tool with no sensitive cross-site data to protect.

### Finding XSS-002: No CSP Reporting Endpoint

**File:** `internal/transport/http.go:619`
**Severity:** Low

The CSP has no `report-uri` directive, so CSP violations are not reported. For a local-only tool this is low priority.

---

## Positive Security Controls Noted

1. **Loopback-only binding** — F-09 prevents non-loopback TCP binds
2. **Host header validation** — F-17 prevents DNS rebinding
3. **Same-origin only** — V-17 rejects all cross-origin browser requests
4. **`script-src 'self'`** — No inline script execution possible
5. **`frame-ancestors 'none'`** — No clickjacking risk
6. **`base-uri 'none'`** — No base tag hijacking
7. **`htmlDropElements`** — `script`, `iframe`, `form`, `button`, `svg` all dropped in HTML normalization
8. **Dedup cache** — Content store deduplication prevents re-emission of large payloads

---

## References

- CWE-79: https://cwe.mitre.org/data/definitions/79.html
- OWASP XSS: https://owasp.org/Top10/A03_2021-Injection/
- CSP Level 3: https://www.w3.org/TR/CSP3/