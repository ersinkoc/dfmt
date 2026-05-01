# DFMT Security Report — 2026-05-01

**Scan scope:** Full codebase  
**Agents:** 5 parallel (Recon, Go Security, Secrets/Data, Injection/AuthZ, Dependency)  
**Prior audit:** 2026-04-28 (8 commits, 6 defects closed)

---

## Executive Summary

DFMT is a well-architected local daemon with strong security foundations:
- **stdlib-only dependency policy** — only `golang.org/x/sys` + `gopkg.in/yaml.v3`, everything else bundled in-tree
- **Defense-in-depth redaction** — two independent layers before journal write
- **Strict exec allow-list policy** — hard-deny invariant, shell operator splitting, env var injection blocked
- **SSRF protection** — DNS rebinding resistant, cloud metadata IPs blocklisted
- **Symlink-safe atomic writes** — `safefs` helper at all write seams

**Residual risk profile:** Local-only daemon for a single-user workstation. The threat model does NOT assume a malicious local peer. Several findings rated High/Medium are acceptable under this model but would be critical in multi-user or containerized deployments.

---

## New Findings This Scan

### N-01 | Write TOCTOU — Symlink Leaf Not Rejected at Open Time
| | |
|---|---|
| **CWE** | CWE-367 (Time-of-check Time-of-use) |
| **File** | `internal/safefs/safefs.go:178-196` |
| **Severity** | High |
| **Confidence** | 85% |
| **Description** | `os.WriteFile` is called without `O_NOFOLLOW`. A symlink planted at the final path component (e.g., `malicious → /etc/passwd`) is followed during write open, even though `EvalSymlinks` was previously checked on the resolved path. The containment check catches the target, but only via `EvalSymlinks` — a race between `EvalSymlinks` check and `WriteFile` open could bypass it. |
| **Recommendation** | Open with `O_NOFOLLOW` (Unix) / `FILE_FLAG_OPEN_REPARSE_POINT` (Windows) at the write call site |

### N-02 | Redaction Bypass via Re-used content_id
| | |
|---|---|
| **CWE** | CWE-200 (Exposure of Sensitive Information) |
| **File** | `internal/transport/handlers.go:67`, `internal/content/store.go` |
| **Severity** | Medium |
| **Confidence** | 75% |
| **Description** | Cross-call wire dedup returns `(unchanged; same content_id)` without re-applying redaction. If the first emission was pre-redaction and the second is post-redaction (journal reload scenario), cached redacted output is returned without re-verification. |
| **Recommendation** | Re-apply redaction on content_id cache hit, or invalidate cache on redact.yaml changes |

### N-03 | Exec LookPath Cache Not Invalidated on PATH Change
| | |
|---|---|
| **CWE** | CWE-78 (Command Injection) |
| **File** | `internal/sandbox/runtime.go:116` |
| **Severity** | Low |
| **Confidence** | 80% |
| **Description** | `exec.LookPath` result cached in `sync.RWMutex` map, re-probed only on SIGUSR1 or `dfmt doctor`. If a prior allowed exec modifies `PATH` (e.g., `.bashrc`), subsequent lookups for `git`, `npm`, `python` could resolve to a different binary. The exec policy allowlist uses base command strings (not resolved paths), so policy itself remains effective. |
| **Recommendation** | Add `LookPath` cache invalidation on significant env var changes, or log a warning when `PATH` env var is modified via an allowed exec |

---

## Verified Prior Findings — Status

| ID | Description | Status |
|---|---|---|
| AUTHZ-01 | Command substitution bypass | **FIXED** in `b861a28` |
| AUTH-01 | Bearer token auth disabled | **Open** — No auth on HTTP JSON-RPC loopback |
| AUTHZ-03 | Windows backslash path normalization | **Open** — High severity; backslashes not normalized before regex matching |
| CMDI-001/010 | Shell chaining allowed | **Open** — By design for `bash`/`sh`; policy check runs first |
| F-09 | Non-loopback HTTP bind refused | **Closed** — Loopback-only enforced at `Start()` |
| F-22 | Dead bearer-token plumbing removed | **Closed** |
| F-04/F-07/F-08/F-25 | Symlink-safe write helper | **Closed** — `safefs` helper centralizes atomic writes |

---

## Security Controls Assessment

### What DFMT Does Well

| Control | Implementation |
|---|---|
| **Dependency policy** | stdlib-only; only `golang.org/x/sys` + `gopkg.in/yaml.v3` permitted (ADR-0004) |
| **Secret redaction** | 2-layer defense; regex patterns for 30+ providers; env-export pass; idempotent |
| **Exec allow-list** | Default deny; hard-deny invariant (ADR-0014); trailing space+`*` boundary; shell operator splitting |
| **Env var injection block** | `LD_`, `DYLD_`, `GIT_`, `NODE_`, `PYTHON`, `JAVA_` prefixes blocked; `PATH`, `IFS` blocked by name |
| **SSRF protection** | Custom `DialContext`; DNS resolution validated before dial; redirect chain checked; 10-redirect cap; cloud metadata IPs blocklisted |
| **Path traversal** | `filepath.Clean` + containment + `EvalSymlinks` check + per-file policy enforcement |
| **Symlink safety** | `safefs.CheckNoSymlinks` on every write/edit path; atomic tmp+rename |
| **Header injection block** | CR/LF/colon validation before passing to `http.Transport` |
| **Concurrency** | `sync.Mutex`/`sync.RWMutex` in journal, index, dedup map, handler state |
| **Journal integrity** | Append-only JSONL with ULID segments; gzip compression on rotation |
| **Wire dedup** | `content_id` prevents re-sending already-seen payloads (ADR-0009/ADR-0011) |

### Residual Risks by Category

| Category | Risk | Note |
|---|---|---|
| **Local Privilege** | High | No daemon auth on loopback TCP — any same-user process can send JSON-RPC calls |
| **Windows Path Norm** | High | `AUTHZ-03` open — backslash paths bypass deny regex on Windows |
| **Write TOCTOU** | High | `N-01` — symlink leaf at final path component followed on write open |
| **SSRF** | Medium | IP encoding bypass possible; Azure/GCP metadata endpoints blocklisted but IPv6 not fully tested |
| **Redaction coverage** | Medium | Bare AWS secrets, Azure storage keys, GCP `client_email` not covered |
| **LookPath cache** | Low | `N-03` — cached binary paths survive env changes |
| **Redaction dedup bypass** | Medium | `N-02` — content_id reuse skips redaction re-apply |
| **Dashboard** | Low | Unauthenticated; serves static HTML only; loopback-only |
| **Path/URL not redacted** | Low | By design — operators with secrets in path names must use `redact.yaml` override |

---

## Dependency & Supply Chain

| Check | Result |
|---|---|
| `go.mod` only declares `golang.org/x/sys` + `gopkg.in/yaml.v3` | **PASS** |
| `go.sum` no hidden indirect runtime deps | **PASS** (test-only `check.v1` not compiled into binaries) |
| No vendor/ directory | **PASS** |
| All bundled libs in-tree (BM25, Porter, HTML, MCP, JSON-RPC) | **PASS** |
| `golang.org/x/sys` CVE status | **Low** |
| `gopkg.in/yaml.v3` CVE status | **Low-current** (v3.0.0 DoS fixed in v3.0.1; used only for operator config) |
| Dockerfile / docker-compose | **Not present** |

**Policy compliance: PASS.** No third-party code beyond the two allowed modules.

---

## Recommended Priority Fixes

| Priority | Finding | Action |
|---|---|---|
| **High** | AUTHZ-03 | Normalize Windows backslash paths before regex match in `permissions.go:300-330` |
| **High** | N-01 | Add `O_NOFOLLOW` / `FILE_FLAG_OPEN_REPARSE_POINT` to `os.WriteFile` call sites in `safefs` |
| **High** | AUTH-01 | Add bearer-token auth to HTTP JSON-RPC endpoint, or document loopback-only threat model clearly in `SECURITY.md` |
| **Medium** | N-02 | Re-apply redaction on content_id cache hit, or invalidate cache on redact.yaml reload |
| **Medium** | Redaction gaps | Add Azure storage account key pattern (`[A-Za-z0-9/+=]{88}`) and bare AWS secret value catchall |
| **Low** | N-03 | Log warning when `PATH` env var modified via allowed exec; add SIGUSR1 trigger for cache refresh |

---

## Attack Surface Summary

| Vector | Mitigations | Residual Risk |
|---|---|---|
| Command Injection | Policy allowlist, shell splitting, hard-deny invariant | **Medium** — shell languages intentionally allow `bash -c cmd` |
| Write TOCTOU | `safefs` + atomic write | **High** — final-symlink not rejected at open time |
| SSRF | Blocklist + custom DNS resolver + redirect cap | **Medium** — IPv6 encoding edge cases |
| Path Traversal | `safefs` + containment + symlink check + per-file deny | **Low** |
| Credential Theft | Redaction pipeline + 2-layer defense + default deny | **Medium** — path/URL not redacted by design |
| Local Peer Auth | Loopback-only bind, filesystem socket perms | **High** — no daemon auth on loopback |
| Windows Path | Backslash normalization | **High** — AUTHZ-03 unfixed |
| MCP Parsing | Static dispatch, concrete struct unmarshal, 1MB body limit | **Low** |

---

*Report generated by security-check (5-agent parallel scan: Recon + Go Security + Secrets/Data + Injection/AuthZ + Dependency Audit)*
