# sc-rate-limiting / sc-open-redirect / sc-business-logic / sc-mass-assignment / sc-header-injection / sc-crypto

Sweep across the remaining universal skills. None apply meaningfully
to a local-trust loopback daemon.

## sc-rate-limiting

**Status:** N/A.

The HTTP server is loopback-only (non-loopback bind refused). The Unix
socket is filesystem-gated. There is no "client" for which rate-limiting
makes sense; the only callers are local processes the user trusts.

The closest analog is the **subprocess timeout** in `Exec`:
- `DefaultExecTimeout = 60s`
- `MaxExecTimeout = 300s` (caller cannot exceed)

And resource bounds:
- `MaxRawBytes = 256 KiB` (subprocess stdout cap)
- `MaxSandboxReadBytes = 4 MiB`
- `MaxFetchBodyBytes = 8 MiB`
- `MaxJSONRPCLineBytes = 1 MiB`
- HTTP `MaxBytesReader` = 1 MiB
- `MaxHeaderBytes = 16 KiB`
- `IdleTimeout = 60s`, `ReadHeaderTimeout = 5s`, `WriteTimeout = 30s`

These are not "rate limits" but they bound the per-request resource use,
which is the same goal at the local-trust level.

## sc-open-redirect

**Status:** N/A. The HTTP server has no redirect endpoints. The dashboard
is a static page. No `Location:` header is ever written.

## sc-business-logic

**Status:** N/A in the typical sense. The product is a local sandbox; the
relevant "business logic" is the Policy, which has been audited under
sc-cmdi/sc-path-traversal/sc-ssrf/sc-authz.

## sc-mass-assignment

**Status:** N/A. There is no ORM, no auto-binding of request fields to
storage rows, no JSON-merge-patch endpoint that updates user records.

## sc-header-injection (CWE-93)

**Status:** safe.

- Response headers are set with literal string values
  (`X-Content-Type-Options`, CSP, `Content-Type`).
- Request headers in `Fetch` are forwarded with `httpReq.Header.Set(k, v)`
  where Go's `http.Header.Set` validates the value via the standard library
  (rejects CR/LF in newer Go versions) — no manual concatenation.
- No code constructs HTTP headers via `Sprintf` and writes them with
  `w.Header().Add()` from user input.

## sc-crypto

**Status:** mostly N/A (no in-repo cryptography).

The codebase does not generate keys, sign tokens, encrypt data, or hash
passwords. It uses TLS via `net/http` (default Go transport) and trusts
the system's certificate store.

The only crypto-adjacent code is `internal/core/ulid.go`, an ULID
implementation. ULIDs are not secrets — they are sortable IDs — so the
`crypto/rand` vs. `math/rand` question is informational only. (Verified:
ulid.go does not generate auth tokens.)

## Findings

None across these six skills.
