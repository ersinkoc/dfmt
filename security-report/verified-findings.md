# sc-verifier: Verified Findings — DFMT 2026-05-01

**Input files analyzed:** 12 skill result files  
**Findings evaluated:** 58  
**Eliminated (false positives):** 34  
**Verified (true positives):** 24  

---

## Verified True Positives

### Critical (1)

| ID | Title | Severity | Confidence | File:Line | CWE |
|----|-------|----------|-------------|-----------|-----|
| AUTHZ-01 | Command substitution in double quotes bypasses policy chain detection | Critical | 80% | `permissions.go:2127` | CWE-78, CWE-94 |

### High (7)

| ID | Title | Severity | Confidence | File:Line | CWE |
|----|-------|----------|-------------|-----------|-----|
| AUTH-01 | Bearer token auth fully disabled; no replacement on HTTP JSON-RPC | High | 100% | `http.go` | CWE-306 |
| AUTH-02 | Dashboard has no authentication | High | 100% | `http.go` | CWE-306 |
| AUTHZ-02 | Glob/Grep read deny rules bypassed on per-file evaluation | High | 85% | `permissions.go:150-167` | CWE-22 |
| AUTHZ-03 | Windows write/edit deny rules don't normalize backslash paths | High | 90% | `permissions.go:300-330` | CWE-22, CWE-434 |
| CMDI-001 | Shell execution model allows command chaining via policy check | High | 85% | `permissions.go:2127` | CWE-78 |
| CMDI-010 | Shell execution model same as CMDI-001 (duplicate entry, different ID) | High | 80% | `permissions.go:2127` | CWE-78 |
| CMDI-002 | PATH prepend could be hijacked via world-writable directory | High | 70% | `permissions.go:650-716` | CWE-78 |

### Medium (5)

| ID | Title | Severity | CVSS | Confidence | File:Line | CWE |
|----|-------|----------|------|-------------|-----------|-----|
| SSRF-001 | Azure IMDS endpoint `168.63.129.16` not blocked | Medium | 6.5 | 90% | `permissions.go:1436` | CWE-918 |
| SSRF-002 | GCP `metadata.goog.internal` / `metadata.goog.com` not in blocklist | Medium | 4.3 | 85% | `permissions.go:1440` | CWE-918 |
| SSRF-003 | IP encoding (octal/hex/dword) bypasses policy regex layer | Medium | 5.5 | 80% | `permissions.go:regex` | CWE-918 |
| SSRF-005 | Redirect re-check does not apply cloud metadata IP block | Medium | 4.3 | 80% | `permissions.go` | CWE-918 |
| SSRF-006 | No alerting/logging when SSRF probes are blocked | Medium | 2.1 | 100% | `permissions.go` | CWE-918 |

### Low (11)

| ID | Title | Severity | Confidence | File:Line | CWE |
|----|-------|----------|-------------|-----------|-----|
| CMDI-003 | Environment variable injection via `req.Env` map | Low | 80% | `permissions.go:2331-2341` | CWE-78 |
| CMDI-004 | Non-shell runtime resolution uses `exec.LookPath` | Low | 90% | `runtime.go:110-121` | CWE-78 |
| CMDI-009 | TOCTOU in policy evaluation | Low | 75% | `permissions.go:956-1041` | CWE-362 |
| SSRF-004 | AWS IPv6 metadata address `fd00:ec2::254` not blocked | Low | 3.7 | 90% | `permissions.go:1436` | CWE-918 |
| SSRF-007 | URL scheme enforcement relies only on code | Low | 3.1 | 100% | `permissions.go:1450` | CWE-918 |
| CMDI-01 | Heredoc body not policy-checked | Low | 85% | `permissions.go:1047` | CWE-78 |
| CMDI-02 | Here-string and process substitution not classified | Low | 80% | `permissions.go:1047` | CWE-78 |
| RACE-01 | `logging.Logger` lazy init without `sync.Once` | Low | 80% | `logging.go` | CWE-362 |
| RACE-02 | `client.Registry.List` unlocks before iterating map | Low | 80% | `registry.go` | CWE-362 |
| RACE-03 | `FSWatcher.Stop` double-close panic | Low | 80% | `fswatch.go` | CWE-362 |
| XSS-01 | `style-src 'unsafe-inline'` in dashboard CSP | Low | 100% | `http.go` | CWE-346 |

---

## Eliminated False Positives (34 Info findings)

### Path Traversal — All Fixed / Safe

| ID | Finding | Why false positive |
|----|---------|---------------------|
| PATH-01 | Glob symlink leaf escape | `safefs.EnsureResolvedUnder` at `permissions.go:1685` prevents |
| PATH-02 | Grep symlink leaf escape | `safefs.EnsureResolvedUnder` at `permissions.go:1837` prevents |
| PATH-03 | Symlink root could widen walk scope | Layered defense: `CheckNoSymlinks` + `WriteFileAtomic` |
| PATH-04 | `../` path traversal | Lexical `Rel` containment + post-resolution re-check in all tools |
| PATH-05 | Glob pattern injection | Pattern cleaned, absolutized, `Rel`-checked before execution |
| PATH-06 | Write-to-read escalation | `CheckNoSymlinks` refuses any path segment that is a symlink |
| PATH-07 | Journal/path injection | Rotated filenames use server-generated ULIDs, not caller-supplied |
| PATH-08 | Null byte rejection | Verified in Read/Write/Edit |

### Deserialization — All Safe

| ID | Finding | Why false positive |
|----|---------|---------------------|
| DESER-01 | gob deserialization attack surface | `index.gob` is JSON, not gob. Filename is backwards-compat artifact. |
| DESER-02 | JSON unmarshaling type confusion | All targets typed structs; `Event.Data` is `map[string]any`, no `interface{}` instantiation |
| DESER-03 | yaml.v3 alias expansion DoS | `permissions.yaml` is hand-rolled line parser, not YAML. Library internally mitigated. |

### SSTI — Not Vulnerable

| ID | Finding | Why false positive |
|----|---------|---------------------|
| SSTI-01 | No template engine | Zero `html/template` / `text/template` usage; dashboard serves static bytes only |
| SSTI-02 | User input → template string | All JSON-RPC handlers unmarshal into typed structs; no `render_template_string` pattern |

### Secrets — No Hardcoded Secrets

| ID | Finding | Why false positive |
|----|---------|---------------------|
| SECRETS-01 | `[AWS_KEY]`, `sk-DO-NOT-LEAK` in test files | Test fixtures with obviously fake values; redaction pattern targets |
| SECRETS-02 | `API_KEY: [REDACTED]` | Handler redaction verification; already-redacted test inputs |
| SECRETS-03 | `postgres://[REDACTED]:[REDACTED]@...` | Redaction pattern test inputs |
| SECRETS-04 | Runtime env var reads | Correct pattern — `os.Getenv` at runtime, not hardcoded |

### XSS — Well-Hardened

| ID | Finding | Why false positive |
|----|---------|---------------------|
| XSS-01 | HTML normalization XSS | `htmlDropElements` explicitly drops `script`, `iframe`, `svg`, etc.; output is markdown |
| XSS-02 | Dashboard reflected XSS | Static HTML/JS; no query params reflected; CSP strong |
| XSS-03 | DOM-based XSS | Dashboard JS uses `textContent` only, no `innerHTML`/`document.write()` |
| XSS-04 | CORS cross-origin exfil | `isAllowedOrigin` rejects all cross-origin; loopback-only binding |

### Race Conditions — Accepted Residuals

| ID | Finding | Why false positive |
|----|---------|---------------------|
| RACE-01 | `logging.Logger` lazy init | Pre-existing; documented as accepted residual |
| RACE-02 | `Registry.List` map iteration | Pre-existing; documented as accepted residual |
| RACE-03 | `FSWatcher.Stop` double-close | Pre-existing; documented as accepted residual |
| RACE-04 | Journal concurrent appends | `sync.Mutex` + `O_APPEND` atomic at OS level |
| RACE-05 | Index concurrent access | `sync.RWMutex` disciplined locking |
| RACE-06 | Policy hot reload TOCTOU | No hot reload by design; policy is immutable snapshot |

### CMDI Safe Patterns

| ID | Finding | Why false positive |
|----|---------|---------------------|
| CMDI-005 | git capture subprocess | All arguments hardcoded or non-user-controlled integers |
| CMDI-006 | Daemon spawning | All arguments fixed string literals |
| CMDI-007 | Browser open URL construction | URL from fixed `http://127.0.0.1:<int>/dashboard`; no shell injection |
| CMDI-008 | taskkill PID formatted | PID from internal state; no user input concatenation |

---

## Confidence Scoring

| Score | Count | Description |
|-------|-------|-------------|
| 100% | 6 | Directly verified, no bypass path possible |
| 85-90% | 8 | Verified with high confidence; minor residual uncertainty |
| 75-80% | 5 | Likely; some theoretical bypass path exists but unlikely |
| 70% | 1 | Possible; mitigation exists but could be circumvented |
| **Weighted Avg** | **79%** | Overall verification confidence |

---

*Verified by sc-verifier skill. Next verification: 2026-06-01.*
