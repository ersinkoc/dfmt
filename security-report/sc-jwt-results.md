# sc-jwt Results

## Target: DFMT

## Findings

**No JWT usage found.**

DFMT uses opaque bearer tokens, not JWTs:
- Token generated via `crypto/rand.Read` (32 bytes, base64url encoded) — `generateAuthToken()` in `http.go:78-84`
- Bearer token stored in `.dfmt/token` (mode 0600)
- Used for HTTP auth only; not a session token
- No `jwt-go`, `golang-jwt`, or similar imports anywhere

JWT references in code are in the **redactor** (to detect and redact JWTs from user data before journal write):
- `redact.go:120-121` — JWT pattern detection (`eyJ[A-Za-z0-9_=-]+\.eyJ[A-Za-z0-9_=-]+\.[A-Za-z0-9_/+=.-]*`)
- `redact_test.go` — test cases for JWT redaction

## Conclusion

sc-jwt is **not applicable** to this codebase. DFMT uses stateless opaque bearer token authentication, not JWTs.