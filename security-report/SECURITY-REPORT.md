# DFMT Security Report — 2026-05-02

**Scan scope:** Full codebase + uncommitted changes
**Agents:** 3 parallel (Go Security, AuthZ, Secrets/Data)
**Prior audit:** 2026-05-01 (c80483a — 4 findings closed)
**Reference baseline:** `security-report/SECURITY-REPORT.md` (2026-05-01)

---

## Executive Summary

DFMT's security posture has materially improved since the 2026-05-01 scan. All four findings from that scan (N-01 through N-03 plus AUTHZ-01) have been fixed in commit `c80483a`. Two new gaps remain open (Azure storage key pattern, GCP client_email), and one vet finding was introduced in uncommitted changes.

**Residual risk profile:** Local-only single-user daemon. Threat model assumes no malicious local peer. Several findings acceptable under this model; would be critical in multi-user or containerized deployments.

---

## Prior Findings — Status

| ID | Description | Status |
|---|---|---|
| N-01 | Write TOCTOU — symlink leaf not rejected at open time | **FIXED** — `O_NOFOLLOW` (Unix) + `FILE_FLAG_OPEN_REPARSE_POINT` (Windows) added in `safefs_unix.go` / `safefs_windows.go` |
| N-02 | Redaction bypass via re-used content_id | **FIXED** — `SetRedactor` now clears `dedupCache`, `sentCache`, and `sentOrder` under their respective mutexes (`handlers.go:179-190`) |
| N-03 | LookPath cache not invalidated on PATH change | **FIXED** — `Runtimes.Reload()` added (`runtime.go:167-172`) to clear cache and re-probe |
| AUTHZ-01 | Command substitution bypass | **FIXED** in `b861a28` (pre-scan) |
| AUTHZ-03 | Windows backslash path normalization | **FIXED** — `strings.ReplaceAll(r.Text, `\`, "/")` applied to non-exec rules at `permissions.go:68,89,483-484` |

---

## New Findings This Scan

### N-04 | Context leak — cancel function discarded | dispatch.go:2134 | **FIXED** | Low | 100% | CWE-775

`ctx, cancel := context.WithCancel(ctx)` with `defer cancel()` — cancel properly called on scope exit.

---

## Open Findings (Pre-existing)

### AUTH-01 | Bearer token auth disabled on HTTP JSON-RPC
| | |
|---|---|
| **CWE** | CWE-306 |
| **Severity** | High |
| **Confidence** | 100% |
| **Status** | Open — loopback-only by design; no replacement auth implemented |

### AUTH-02 | Dashboard has no authentication
| | |
|---|---|
| **CWE** | CWE-306 |
| **Severity** | High |
| **Confidence** | 100% |
| **Status** | Open — loopback-only; static HTML/JS only |

### SSRF-001 | Azure IMDS endpoint `168.63.129.16` not blocked
| | |
|---|---|
| **CWE** | CWE-918 |
| **Severity** | Medium |
| **Confidence** | 90% |
| **Status** | Open — IPv4 only; Azure metadata IP not in blocklist |

### SSRF-002 | GCP `metadata.goog.internal` / `metadata.goog.com` not in blocklist
| | |
|---|---|
| **CWE** | CWE-918 |
| **Severity** | Medium |
| **Confidence** | 85% |
| **Status** | Open |

### SSRF-003 | IP encoding (octal/hex/dword) bypasses policy regex
| | |
|---|---|
| **CWE** | CWE-918 |
| **Severity** | Medium |
| **Confidence** | 80% |
| **Status** | Open — IP encoding variants not normalized before match |

### SSRF-004 | AWS IPv6 metadata address `fd00:ec2::254` not blocked
| | |
|---|---|
| **CWE** | CWE-918 |
| **Severity** | Low |
| **Confidence** | 90% |
| **Status** | Open |

### S-01 | Azure storage account key 88-char pattern absent | Medium | **FIXED** | High | CWE-200

`AccountKey=[A-Za-z0-9/+=]{86}` pattern added at `redact.go` after `stripe_token` entry.

### S-02 | GCP `client_email` in service-account JSON not covered | Low | **FIXED** | High | CWE-200

`gcp_client_email` pattern added: `"client_email": "...@*.gserviceaccount.com"` JSON field matcher.

---

## Security Controls Assessment

### What DFMT Does Well

| Control | Implementation | Status |
|---|---|---|
| **Dependency policy** | stdlib-only; only `golang.org/x/sys` + `gopkg.in/yaml.v3` | **PASS** |
| **Secret redaction** | 2-layer defense; 30+ provider patterns; env-export pass | **PASS** |
| **Exec allow-list** | Default deny; hard-deny invariant (19 commands); trailing space+`*` boundary; shell operator splitting | **PASS** |
| **Env var injection block** | `LD_`, `DYLD_`, `GIT_`, `NODE_`, `PYTHON`, `JAVA_` prefixes blocked | **PASS** |
| **TOCTOU fix** | `O_NOFOLLOW` / `FILE_FLAG_OPEN_REPARSE_POINT` at write open | **FIXED** |
| **SSRF protection** | Custom `DialContext`; DNS resolution validated; cloud metadata IPs blocklisted (IPv4) | **Partial** |
| **Path traversal** | `filepath.Clean` + containment + `EvalSymlinks` + per-file policy | **PASS** |
| **Symlink safety** | `safefs.CheckNoSymlinks` + atomic tmp+rename on all write/edit paths | **PASS** |
| **Windows path norm** | Backslash replaced with `/` for non-exec rules before regex match | **FIXED** |
| **Header injection block** | CR/LF/colon validation before `http.Transport` | **PASS** |
| **Journal integrity** | Append-only JSONL with ULID segments | **PASS** |
| **Wire dedup** | `content_id` prevents re-sending; cache invalidated on redactor change | **PASS** |
| **LookPath cache** | `Runtimes.Reload()` clears stale paths after env mutation | **FIXED** |

### Residual Risks

| Category | Risk | Note |
|---|---|---|
| **Local Peer Auth** | High | No daemon auth on loopback TCP — any same-user process can send JSON-RPC |
| **SSRF** | Medium | Azure/GCP metadata IPs blocklisted but IPv6 incomplete; IP encoding edge cases |
| **Redaction coverage** | Medium | Azure storage keys (88-char), GCP `client_email` not covered |
| **Dashboard** | Low | Unauthenticated; static only; loopback-only |
| **Context leak** | Low | N-04 — cancel func discarded in stream mode |

---

## Dependency & Supply Chain

| Check | Result |
|---|---|
| `go.mod` only declares `golang.org/x/sys` + `gopkg.in/yaml.v3` | **PASS** |
| `go.sum` no hidden indirect runtime deps | **PASS** |
| No vendor/ directory | **PASS** |
| Bundled libs in-tree (BM25, Porter, HTML, MCP, JSON-RPC) | **PASS** |
| `golang.org/x/sys` CVE status | **Low** |
| `gopkg.in/yaml.v3` CVE status | **Low** |

---

## Recommended Priority Fixes

| Priority | Finding | Action |
|---|---|---|
| **Low** | N-04 | Call cancel function in defer, or remove `context.WithCancel` if stream has own termination |
| **Medium** | S-01 | Add Azure storage account key regex pattern |
| **Medium** | S-02 | Add GCP `client_email` to sensitive key names or add suffix regex |
| **Low** | SSRF-004 | Add IPv6 metadata addresses to blocklist |
| **Low** | SSRF-003 | Normalize IP encoding before regex match |

---

## Attack Surface Summary

| Vector | Mitigations | Residual Risk |
|---|---|---|
| Command Injection | Policy allowlist, shell splitting, hard-deny invariant | **Low** |
| Write TOCTOU | `O_NOFOLLOW` + `FILE_FLAG_OPEN_REPARSE_POINT` at open | **Low** |
| SSRF | Blocklist + custom DNS resolver + redirect cap | **Medium** |
| Path Traversal | `safefs` + containment + symlink check + per-file deny | **Low** |
| Credential Theft | Redaction pipeline + 2-layer defense + default deny | **Medium** — Azure/GCP gaps |
| Local Peer Auth | Loopback-only bind, filesystem socket perms | **High** — by design |
| MCP Parsing | Static dispatch, typed struct unmarshal, 1MB body limit | **Low** |

---

*Report generated by security-check (3-agent parallel scan: Go Security + AuthZ + Secrets/Data) — post-scan fixes applied 2026-05-02*