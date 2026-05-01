# sc-auth Results — DFMT Authentication Security

**Target:** DFMT daemon transports (HTTP, Unix socket / loopback TCP, MCP stdio)
**Scan date:** 2026-05-01
**Previous scan:** 2026-04-26 (sc-auth-results.md, sc-transport-results.md)

---

## Executive Summary

DFMT v1 has **no bearer-token authentication** on any transport. The bearer-token plumbing was fully removed in commit `97c25fa` (F-22) with no replacement. All access control relies entirely on:
- **Unix socket / loopback TCP bind** — the only network-level barrier
- **Filesystem permissions** — socket file mode 0o700, port file mode 0o600
- **Single-user threat model** — "same UID = trusted" is the documented security boundary

No credential stuffing, hardcoded password, or weak hashing findings. The authentication surface is intentionally minimal by design for a single-user local daemon, but there are gaps in how that design is enforced.

---

## Findings

### AUTH-01 — Bearer token auth is fully disabled; no replacement access control on HTTP JSON-RPC

- **Severity:** High
- **CWE:** CWE-287 (Improper Authentication)
- **Confidence:** High
- **File:** `internal/transport/http.go:139-156` (non-loopback refusal), `internal/transport/http.go:222-256` (`wrapSecurity`)
- **Description:** Bearer-token authentication was disabled unconditionally in R15-2 and the plumbing was fully removed in F-22. The HTTP server (`HTTPServer`) now has **no authentication on any endpoint** — `/` (all JSON-RPC methods including `dfmt.exec`, `dfmt.write`, `dfmt.fetch`), `/api/stats`, `/api/daemons`, `/api/token`, and all other routes are fully accessible to any process that can reach the listener address.

  The non-loopback bind refusal at `http.go:149-156` is the only access control:
  ```go
  if addr, ok := ln.Addr().(*net.TCPAddr); ok {
      if !addr.IP.IsLoopback() {
          return fmt.Errorf("non-loopback HTTP bind refused: listener bound to %s — bearer-token auth not implemented (F-09)", addr.IP.String())
      }
  }
  ```

  This check lives in `Start()`, not in the constructor. A caller that bypasses `Start()` — or a future feature that creates an `HTTPServer` and calls `Start(ctx)` with a non-loopback bind — would expose all JSON-RPC methods without any auth.

  Additionally, the comment at `http.go:144-145` explicitly states: `"bearer-token auth is not implemented (F-09)"`. There is no token comparison anywhere in the codebase.

- **Impact:** Any local user or process that can reach the daemon's loopback port has full access to:
  - `dfmt.exec` — arbitrary code execution via sandbox
  - `dfmt.write` / `dfmt.edit` — arbitrary file modification
  - `dfmt.fetch` — HTTP requests from the daemon host (SSRF, credential harvesting)
  - `dfmt.remember` — write arbitrary events to the journal
  - `dfmt.stats` / `dfmt.recall` — read all historical session data

  The threat model documents this as acceptable because "port file is mode 0600" and "single-user host." This is a reasonable design choice, but it means the security boundary is **OS-level UID separation**, not token-based auth.

- **Remediation:** If DFMT ever needs auth (e.g., multi-user host, HTTP exposed beyond loopback), restore bearer-token comparison in `wrapSecurity` gated on `addr.IP.IsLoopback()`. Until then, document clearly that the daemon must only be started by the primary user on a single-user system.

- **Reference:** CWE-287 — https://cwe.mitre.org/data/definitions/287.html

---

### AUTH-02 — Dashboard (`/dashboard`, `/dashboard.js`) has no authentication

- **Severity:** High
- **CWE:** CWE-287 (Improper Authentication)
- **Confidence:** High
- **File:** `internal/transport/http.go:158-162` (route registration), `internal/transport/http.go:507-522` (`handleDashboard`)
- **Description:** The dashboard HTML page and its JavaScript bundle are served without any session or token check. `handleDashboard` sets CSP and security headers but performs **no authentication**:

  ```go
  mux.HandleFunc("/dashboard", s.handleDashboard)
  mux.HandleFunc("/dashboard.js", s.handleDashboardJS)
  ```

  Any local user who can reach the loopback port can open the dashboard in a browser and view:
  - Full session history (via `dfmt.recall` rendered in-browser)
  - Token usage statistics and compression ratios
  - Event timeline with file edit, exec, and fetch traces
  - Stash dedup hit rates and wire-dedup cache state
  - MCP tool compression telemetry

  The dashboard JS (`internal/transport/dashboard.go`) fetches `/api/stats` and `/api/daemons` but carries no auth token — it sends the `X-DFMT-Session` header only for wire-dedup, not access control.

- **Impact:** Any local user with browser access to the loopback port can read the full session journal through the dashboard. The journal (`journal.jsonl`, mode 0o600) is also directly readable by the same UID, so this is consistent with the "same UID = trusted" model — but it means the dashboard provides a convenient visual interface to data that would otherwise require reading the raw JSONL file.

- **Remediation:** If dashboard access needs to be restricted (e.g., shared multi-user system), add a session cookie or token check to `handleDashboard` and `handleDashboardJS`. For single-user systems this is acceptable by design.

- **Reference:** CWE-287 — https://cwe.mitre.org/data/definitions/287.html

---

### AUTH-03 — MCP stdio transport has no authentication

- **Severity:** Info (documented as by-design)
- **CWE:** CWE-287 (Improper Authentication)
- **Confidence:** High
- **File:** `internal/transport/mcp.go:273-308` (`Handle` — no auth check)
- **Description:** The MCP stdio transport (`MCPProtocol.Handle`) accepts `initialize`, `tools/list`, `tools/call`, and `ping` from any STDIO client without any token validation. The `Handle` method:

  ```go
  func (m *MCPProtocol) Handle(ctx context.Context, req *MCPRequest) (*MCPResponse, error) {
      if req.ID == nil { return nil, nil } // notifications have no response
      switch req.Method {
      case "initialize":  return m.handleInitialize(req)
      case "tools/list":  return m.handleToolsList(req)
      case "tools/call":  return m.handleToolsCall(ctx, req)
      case "ping":        return m.handlePing(req)
      default:            return errorResult(...)
      }
  }
  ```

  No token, no signature, no authentication of any kind.

- **Assessment:** This is **documented as intentional** in prior sc-auth results and consistent with DFMT's design — MCP stdio is the agent's private channel. A compromised parent process (Claude Code, Codex, Cursor) that can spawn the `dfmt mcp` subprocess can issue any tool call regardless of what the agent intends. This is within the threat model: "the MCP stdio transport is the agent's private channel."

- **Reference:** Prior sc-auth scan confirmed this as PASS (documented).

---

### AUTH-04 — CLI-to-daemon connection has no token auth; trust is filesystem-based

- **Severity:** Info (documented as by-design)
- **CWE:** CWE-287 (Improper Authentication)
- **Confidence:** High
- **File:** `internal/client/client.go` (no auth token in client), `internal/transport/socket.go` (no token check in `handleConn`)
- **Description:** The CLI client (`Client`) connects via Unix socket or loopback TCP. It carries no `Authorization` header and the socket server performs no token check. The comment at `http.go:144-145` explains: `"bearer-token auth not implemented (F-09)"`.

  Access control for the socket transport is entirely at the OS level: the socket file is mode 0o700 (`socket.go:97-104`) and on Unix the daemon removes any stale socket before re-binding. Any process running as the same UID can connect.

- **Assessment:** Consistent with the "same UID = trusted" model. No token to steal or bypass because there is none.

---

## Verified Clean Controls

| Control | Location | Status |
|---|---|---|
| Non-loopback TCP bind refused | `http.go:149-156` | PASS — `IsLoopback()` rejects `0.0.0.0`, `::`, LAN IPs; only `127.0.0.0/8`, `::1`, `::ffff:127.0.0.0/8` accepted |
| Port file atomic write + mode 0o600 | `http.go:529-592` | PASS — `os.CreateTemp` + rename + `Chmod 0600`; no partial-read window |
| Host header validation closes DNS rebinding | `http.go:270-311` (`isAllowedHost`) | PASS — rejects `attacker.com`, accepts only literal listener addr, `localhost:<port>`, `[::1]:<port>` |
| Same-origin Origin gate for browser requests | `http.go:247-261` (`isAllowedOrigin`) | PASS — cross-origin POSTs rejected with 403; `healthz`/`readyz` bypass intentional |
| Bearer-token plumbing fully removed (F-22) | `internal/transport/http.go:139-156` (comment) | PASS — no `authToken` field, no `Authorization` header check, no `/api/token` handler |
| JSON-RPC parse error uses `ID: nil` | `http.go:410-417` | PASS — JSON-RPC 2.0 §5.1 compliant |
| `decodeRPCParams` returns `-32602` on bad params | `http.go:383-395` | PASS — correctly replaces prior V-16 zero-value silent failure |
| MCP stdio auth absence documented as intentional | `mcp.go` (no auth in `Handle`) | PASS (documented) |

---

## Auth Model Assessment

DFMT's authentication design is intentional: there is **no token-based auth** because the trust model is "same UID = trusted." All access control is enforced at the OS level:
- Unix socket mode 0o700
- Port file mode 0o600
- Loopback-only TCP bind

This is **appropriate for a single-user local daemon** on a trusted multi-user system. However:
1. There is no enforcement that the `HTTPServer` can only be constructed with a loopback bind — the `Start()` check is the only gate
2. The dashboard has no auth and exposes full session data to any browser that can reach the loopback port
3. No token means no audit trail tying RPC calls to a authenticated principal

For the current threat model (single-user workstation), the design is consistent. If the product evolves to serve multiple users or expose HTTP beyond loopback, bearer-token auth must be restored.

---

## Prior Scan Status

| Finding | Status |
|---|---|
| F-22 (dead authToken plumbing removed) | CLOSED — confirmed clean in current codebase |
| F-09 (non-loopback bind refused) | CLOSED — `IsLoopback()` check in place |
| V-16 (decodeRPCParams silent failure) | CLOSED — now returns `-32602` properly |
| sc-auth-01 (isAllowedOrigin diverge) | INFO — acknowledged; dashboard is TCP-only in practice |