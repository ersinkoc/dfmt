# Transport / HTTP / Auth Security Audit — DFMT

Scope: HTTP (loopback TCP / Unix socket via HTTPServer), socket (raw JSON-RPC), MCP stdio, embedded HTML dashboard.
Scan date: 2026-04-26
Files in scope: `internal/transport/{http.go,handlers.go,dashboard.go,socket.go,socket_umask_*.go,mcp.go,jsonrpc.go,transport.go}` plus `internal/daemon/daemon.go`.

## Summary by severity

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High     | 1 |
| Medium   | 4 |
| Low      | 5 |
| Info     | 4 |

---

## CWE-287 — Authentication

### T-A1 [High] Bearer auth disabled unconditionally — bind address is not verified
- File: `internal/transport/http.go:128-141` (auth-token block) and `:222-241` (`wrapSecurity` / `isAllowedOrigin`)
- Commit: `ee500cd` ("fix: disable TCP auth token for loopback listeners (R15-2)")
- The commit message claims auth is disabled "for loopback listeners", but the code change is **unconditional**: the token-generation block is fully commented out and there is no `is-loopback` predicate. The only enforcement that the listener is actually loopback lives outside this file (`internal/daemon/daemon.go:154` hardcodes `"127.0.0.1:0"`).
- Consequences:
  1. The public constructor `transport.NewHTTPServer(bind string, ...)` accepts any bind. A second internal caller, a test, an integrator, or a future feature that passes `":0"` / `"0.0.0.0:0"` / a LAN address gets full unauthenticated JSON-RPC (`dfmt.exec`, `dfmt.write`, `dfmt.fetch`, …) on whatever interface they bound. The "loopback only" precondition is documented in a code comment, not enforced anywhere.
  2. The `port-file` token field stays in the wire format and the dashboard still calls `/api/token`, but the token is always `""`. Any party who can reach the loopback port has full RPC access. The threat model document this delegates to "port file is 0600" + "single-user host" — both reasonable, but the *code* no longer carries the second line of defence.
- Recommendation: gate the auth-skip on `addr.IP.IsLoopback()` (and reject non-loopback binds outright in NewHTTPServer unless a `bind=*` opt-in is set). At minimum, restore the token comparison and only skip it when `s.listener.Addr().(*net.TCPAddr).IP.IsLoopback()`.

### T-A2 [Info] Origin check is the *only* HTTP access control
- File: `internal/transport/http.go:207-241`
- Bearer auth is gone (T-A1). Same-origin Origin matching (`origin == fmt.Sprintf("http://%s", listener.Addr().String())`) is the sole barrier against browser-mounted CSRF. Modern browsers do send `Origin` on POST cross-origin and on form submissions, so the practical attack surface is small — but this is a **single-control** posture and should be called out as such.

### T-A3 [Info] Dead `authToken` field / `/api/token` / port-file `Token` remain wired
- File: `internal/transport/http.go:84,93,617-624`; `internal/transport/dashboard.go:189-199`
- Consequence-free today (always empty), but invites a future reviewer to assume auth exists. Recommend either ripping the field/endpoint out or restoring its use.

---

## CWE-285 / CWE-639 — Authorization / IDOR

### T-Z1 [Medium] `/api/daemons` discloses cross-project paths when `projectPath` is unset
- File: `internal/transport/http.go:626-672`, filter at `:658-666`
- The endpoint reads `~/.dfmt/daemons.json` (the global registry of every project's daemon on the host) and filters by `project_path == s.projectPath`. The daemon constructor wires `SetProjectPath` (`internal/daemon/daemon.go:157,168`), so production is fine. But the filter is `if s.projectPath != ""` — any caller (test seam, future binary, refactor) that constructs `HTTPServer` without `SetProjectPath` returns the full registry to anyone who can reach loopback.
- Recommend: change the filter to fail closed — return `[]` when `projectPath == ""`.

### T-Z2 [Low] Path equality is exact-match (Windows path normalisation)
- File: `internal/transport/http.go:661`
- `path == s.projectPath` won't match `D:\proj` vs `D:\proj\` vs `d:\proj` (Windows is case-insensitive and tolerates trailing separators). Functional bug; security impact is "filter accidentally returns 0 entries", which is fail-closed.

---

## CWE-384 / CWE-613 — Session management

### T-S1 [Info] No sessions
- DFMT does not maintain HTTP sessions. The MCP/socket/HTTP layers are all stateless request/response. Not applicable.

---

## CWE-352 — CSRF on dashboard mutating endpoints

### T-C1 [Medium] No CSRF token; sole defence is the same-origin `Origin` check
- File: `internal/transport/http.go:207-241`
- The mutating endpoints are the JSON-RPC root `POST /` (handles `dfmt.exec`, `dfmt.write`, `dfmt.edit`, `dfmt.remember`, `dfmt.fetch`) and `POST /api/stats`. Defence is solely `Origin` header same-origin equality.
- Holes:
  - **Missing-Origin bypass**: line 216 only enforces `if origin != ""`. Some legacy browsers omit `Origin` on same-site POSTs; the `<form enctype="text/plain">` simple-CORS form post historically didn't carry `Origin` in older browsers. Modern Chrome/Firefox ≥ 2018 always send `Origin` on cross-origin POSTs, so live exploitation is unlikely, but the gate trusts a header convention rather than an unforgeable token.
  - **No anti-CSRF token** on `/`, `/api/stats`, `/api/daemons`, `/api/token`. A future browser bug or a non-browser local process that somehow gets the loopback port can issue mutating RPCs without the user's intent.
- Recommend: add a per-daemon CSRF token cookie + `X-DFMT-CSRF` header check on mutating routes, and/or require `Content-Type: application/json` (which forces preflight) and reject `text/plain` / `application/x-www-form-urlencoded` bodies.

### T-C2 [Medium] No `Host` header validation → DNS-rebinding defence-in-depth gap
- Files: `internal/transport/http.go:207-225`
- The middleware never inspects `r.Host`. DNS rebinding attacks (attacker-controlled domain re-resolves to `127.0.0.1` after TTL) are currently blocked because cross-origin POSTs carry `Origin: http://attacker.tld` ≠ `http://127.0.0.1:NNN`. But this is a single line of defence — if the Origin check ever regresses or the browser ever relaxes (e.g., Origin: null on sandboxed iframes, which **does** appear with `origin != ""` and is rejected only because `null != "http://…"`), DNS rebinding becomes live.
- Recommend: also require `r.Host` to be `127.0.0.1:<port>` / `localhost:<port>` for non-CLI requests (or unconditionally — the daemon never legitimately receives a domain-name Host).

---

## CWE-942 — CORS misconfiguration

### T-CO1 [Info] No `Access-Control-Allow-Origin` / credentials reflected
- Files: `internal/transport/http.go` (no `Access-Control-*` headers anywhere)
- The server never emits CORS response headers. Cross-origin XHRs are blocked by the browser due to absence of `Access-Control-Allow-Origin` and additionally rejected by the `wrapSecurity` 403. Configuration is correct.

---

## CWE-1021 — Clickjacking

### T-FR1 [Low] Only `/dashboard` sets `X-Frame-Options` / `frame-ancestors`
- File: `internal/transport/http.go:519-528` (dashboard) vs `:532-536` (dashboard.js), `:538-605` (api/stats), `:626-672` (api/daemons), `:617-624` (api/token), `:607-611` (health).
- `/dashboard` sets `X-Frame-Options: DENY` and a strong CSP that includes `frame-ancestors 'none'`. Other endpoints set neither. JSON/JS responses framed by another page are not classically dangerous, but defence-in-depth is missing — recommend setting these headers globally in `wrapSecurity`.

---

## CWE-113 — HTTP header injection / response splitting

### T-HI1 [Info] No user-controlled header writes
- All `w.Header().Set(...)` calls in `internal/transport/http.go` use literal constant values (verified by grep). No header-injection surface.

---

## CWE-601 — Open redirect

### T-OR1 [Info] No redirects
- The HTTP server never writes `Location` and has no redirect handler. Not applicable.

---

## CWE-915 — Mass assignment

### T-MA1 [Low] No `DisallowUnknownFields` but bind targets are flat
- Files: `internal/transport/http.go:383-395` (`decodeRPCParams`), all `case` branches in `handle()` and `socket.go:dispatch`.
- `json.Unmarshal` happily accepts unknown fields. The handler structs (`ExecParams`, `ReadParams`, `RememberParams`, …) have no privileged hidden fields, so there is no escalation path. But adding a "trusted" field in the future without `DisallowUnknownFields` would be unsafe.
- Recommend: enable `dec.DisallowUnknownFields()` so future mass-assignment regressions surface immediately.

---

## API input validation

### T-V1 [Low] No bounds on `RememberParams.{Tags,Refs,Data}` size, only the 1 MiB body cap
- File: `internal/transport/http.go:317`, `internal/transport/jsonrpc.go:37` (cap 1 MiB)
- A caller can stuff up to 1 MiB of arbitrarily structured JSON into a single `Remember`. The journal absorbs it. With repeated calls, the journal grows quickly. The 1 MiB cap is the only limiter.
- Recommend: at the handler layer, cap `len(Tags)`, `len(Refs)`, depth/size of `Data`, and total event-byte cost.

### T-V2 [Info] `decodeRPCParams` returns -32602 on bad params (good)
- File: `internal/transport/http.go:383-395`
- The earlier round-trip-decode bug (V-16) was fixed; param errors now properly surface as JSON-RPC `Invalid params`.

---

## CWE-770 — Rate limiting / DoS

### T-DOS1 [Medium] `Read` / `Glob` / `Grep` / `Edit` / `Write` have no concurrency limit
- File: `internal/transport/handlers.go:54-56,730-1132`
- Only `Exec` (semaphore depth 4) and `Fetch` (depth 8) are throttled (`execSem` / `fetchSem`). `Read`, `Glob`, `Grep`, `Edit`, `Write` go straight to the sandbox. Any caller with socket access can open hundreds of concurrent `Read`s on large files or `Grep` over the whole project tree, exhausting FDs / memory.
- Recommend: a generic semaphore (depth ≈ 16) wrapping every handler entry, plus per-method overrides.

### T-DOS2 [Low] No request-rate cap
- The server has no rate limit on requests-per-second. With `socketReadIdleTimeout = 60s`, a single connection can pipeline thousands of requests. The 4/8 semaphores limit concurrent execution but not request churn (e.g., flooding `Search` recomputes BM25 on every call).
- Recommend: per-connection token-bucket on the socket and per-IP limit on HTTP.

### T-DOS3 [Info] Body / line caps look reasonable
- HTTP: `MaxBytesReader(... 1<<20)` (1 MiB), `MaxHeaderBytes 16 KiB`, `ReadHeaderTimeout 5s`, `ReadTimeout 30s`, `WriteTimeout 30s`, `IdleTimeout 60s`. (`internal/transport/http.go:154-159,317`).
- Socket: `MaxJSONRPCLineBytes = 1 MiB`, `socketReadIdleTimeout = 60s`. (`internal/transport/jsonrpc.go:37`, `internal/transport/socket.go:14`).
- All sane.

---

## CWE-345 — JWT integrity

### T-JWT1 [Info] No JWT
- DFMT does not issue or verify JWTs anywhere in the transport layer. Not applicable.

---

## CWE-1385 — WebSocket

### T-WS1 [Info] No WebSocket endpoint
- Confirmed via grep — no WS upgrade or `golang.org/x/net/websocket` import. Not applicable.

---

## Socket-specific findings (CWE-276 / file permissions)

### T-SK1 [Low] Windows Unix-socket umask is a no-op
- File: `internal/transport/socket_umask_windows.go:7-9`
- `listenUnixSocket` on Windows skips the umask and any chmod. On Win10+ Windows supports AF_UNIX, but ACL semantics are different from POSIX modes — the post-listen `os.Chmod(path, 0o700)` in `socket.go:63` is also effectively a no-op on Windows.
- Mitigated by `daemon.go:149-158` choosing the TCP path on `runtime.GOOS == "windows"`, so no production daemon ever uses the Unix-socket server on Windows. Kept here as a latent footgun if the platform branch ever changes.

### T-SK2 [Info] Unix umask race during listen is closed
- File: `internal/transport/socket_umask_unix.go:11-14`
- `syscall.Umask(0o077)` is set, listen creates the socket with mode `0o600`, deferred `Umask(old)` restores. Then `socket.go:63` chmods to `0o700`. The umask manipulation is process-global and racy with concurrent goroutines that also create files — but in practice the daemon only creates this socket here, so no race in this codebase.
- Note: the umask flip is observable to other goroutines in the same process for the duration of the listen. On a daemon that creates other files concurrently this could leave them at unexpectedly tight modes. Not exploitable.

### T-SK3 [Info] Other-uid socket access on Unix
- The chmod is `0o700` after listen → only the owning uid can connect. Good.

---

## Dashboard XSS

### T-X1 [Info] DashboardHTML is fully static, no template interpolation
- File: `internal/transport/dashboard.go:6-94`
- `_, _ = w.Write([]byte(DashboardHTML))` writes a constant. No agent-controlled or journal-derived data is rendered into the HTML at server time. JS-side rendering uses `textContent` (e.g., `dashboard.js:215-216`) for daemon paths — safe. CSP `script-src 'self'` blocks inline-script injection even if a future regression creates one.

---

## Top issues to fix

1. **T-A1 (High)** — Restore the loopback predicate before skipping bearer auth in `wrapSecurity` (`internal/transport/http.go:128-141,222-241`). The "loopback only" guarantee is a comment, not code.
2. **T-DOS1 (Medium)** — Add a concurrency semaphore covering `Read`/`Glob`/`Grep`/`Edit`/`Write` in `internal/transport/handlers.go`.
3. **T-C1 / T-C2 (Medium)** — Add CSRF token + `Host` header validation to `wrapSecurity`. Do not rely on browser `Origin` semantics as the sole gate against cross-origin/DNS-rebinding mutation.
4. **T-Z1 (Medium)** — Make `/api/daemons` fail closed when `projectPath` is unset (`internal/transport/http.go:658`).
5. **T-A3** — Either fully remove the dead `authToken` / `/api/token` / port-file `Token` plumbing, or restore its use; today it confuses the security posture without adding any.
