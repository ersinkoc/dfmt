# sc-open-redirect Results

## Target: DFMT HTTP server

## Methodology

- `grep` for `http.Redirect`, `Location` header, `301`/`302`/`303`/`307`/
  `308` status codes, and any user-controlled URL passed to `w.Header().Set`.
- Reviewed every handler in `http.go`; none returns a 3xx.

## Findings

No issues found by sc-open-redirect. — N/A: DFMT HTTP server exposes no
redirect endpoints. No call to `http.Redirect`, no `Location` header
emission, no 3xx status code anywhere in `internal/transport/`.

### Supporting analysis

- All endpoints respond with terminal 2xx/4xx/5xx and no `Location`.
- The HTTP fetch client used by the **sandbox** (`sandbox/permissions.go`,
  `Fetch`) does follow redirects, but each redirect target is re-validated
  against the SSRF gate via `CheckRedirect` (`permissions.go:1265-1271`).
  The redirect cap is 10. This is not an HTTP server-side open redirect —
  it is an outbound HTTP client, and the protection lives at the SSRF
  layer (covered by `sc-business-logic-results.md`).

### Confidence: High
