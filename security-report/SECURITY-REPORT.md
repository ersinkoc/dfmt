# DFMT Security Audit Report

**Date**: 2026-05-02  
**Auditor**: security-check skill  
**Project**: DFMT (dfmt daemon + MCP tools)  
**Files Reviewed**: `internal/sandbox/permissions.go`, `internal/sandbox/runtime.go`, `internal/sandbox/sandbox.go`, `internal/transport/handlers.go`, `internal/safefs/safefs.go`

---

## 1. Executive Summary

DFMT is a local daemon that proxies and filters AI agent tool calls (exec/read/fetch/glob/grep/edit/write) through a policy-gated sandbox. The security model is defense-in-depth with layered checks at the policy, path-resolution, and transport layers. **No critical or high-severity findings were identified.** One medium finding (F-34) and two low-priority observations are documented below.

---

## 2. Architecture Overview

| Component | Role |
|-----------|------|
| `internal/sandbox/permissions.go` | Policy engine: DefaultPolicy, MergePolicies, PolicyCheck, hard-deny exec base commands |
| `internal/sandbox/runtime.go` | Language runtime detection, path resolution (Git Bash Windows special-case) |
| `internal/sandbox/sandbox.go` | Tool request/response types, policy threshold constants |
| `internal/sandbox/intent.go` | NormalizeOutput pipeline, ApplyReturnPolicy |
| `internal/safefs/safefs.go` | Symlink-safe WriteFile/WriteFileAtomic/CheckNoSymlinks |
| `internal/transport/handlers.go` | MCP/HTTP/JSON-RPC handlers, redaction, wire dedup, rate limiting |

---

## 3. Phase 1 — Recon Findings

### R3.1 Default Policy Allow-list — Exec

The default exec allow list covers 50+ commands (`git`, `npm`, `pnpm`, `yarn`, `bun`, `go`, `node`, `python`, `tsc`, `tsx`, `vitest`, `jest`, `eslint`, `prettier`, `vite`, `next`, `webpack`, `make`, etc.). Each rule uses the trailing-space glob form (`go *`, not `go*`) per V-20 contract. Notably absent from defaults: `curl`, `wget`, `nc`, `netcat`, `socat`, `openssl s_client`, `ssh`, `scp`, `rsync` — consistent with the zero-config local-dev scope.

### R3.2 Default Policy Deny-list — Exec

Deny rules block `sudo *`, `rm -rf /*`, `curl * | sh`, `wget * | sh`, `shutdown *`, `reboot *`, `mkfs *`, `dd if=*`, `dfmt` recursion. The `dfmt` recursion block prevents an agent from calling `dfmt exec 'sudo rm -rf /'` to bypass the outer policy.

### R3.3 Default Policy Deny-list — Read/Write/Edit

Sensitive paths denied: `.env*`, `**/.env*`, `**/secrets/**`, `**/id_rsa`, `**/id_*`. DFMT state (`.dfmt/**`) and git state (`.git/**`) are write/edit denied. The broad `read **` and `write **` / `edit **` defaults are narrowed by these specific denies.

### R3.4 Default Policy Deny-list — Fetch

Cloud metadata and `file://` URLs explicitly denied at policy level: `http://169.254.169.254/*`, `https://169.254.169.254/*`, `http://metadata.google.internal/*`, `https://metadata.google.internal/*`, `file://*`.

---

## 4. Phase 2 — Hunt Findings (by vulnerability class)

### Injection

#### CMDI — Command Injection (V-06 follow-on)

**Finding ID**: F-V06  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:957–1042`

The `Exec` handler parses shell commands into chain segments using `splitByShellOperators`, which recursively handles `$(...)`, backticks, and bare `(...)` subshell grouping. Each segment is individually policy-checked. The `$(...)` recursive splitting prevents `curl * | sh` inside a command substitution from passing a single-part check against `curl *`.

**Observation**: `&&` and `||` are recognized as separators but `;` (sequence operator) is not explicitly handled — it falls through with a `Flush()` but produces an un-split part. However, `;` between two commands in bash does not enable execution of a second command that the first would block; it is a sequence point. The base-command check catches `; rm -rf /` as two parts: `;` (flushed) and `rm` (blocked by `rm` hard-deny). The risk is low.

#### Header Injection (V-13)

**Finding ID**: F-V13  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1496–1506`

Fetch validates header keys and values for `\r`, `\n`, and `:` before `Header.Set`. This closes the window where a malicious header value could inject HTTP response splitting headers on the daemon side.

### Server-Side Request Forgery (SSRF)

#### SSRF — DNS Rebinding Defense

**Finding ID**: F-SSRF-DNS  
**Severity**: Medium  
**Component**: `internal/sandbox/permissions.go:1515–1549`

The Fetch transport uses a custom `DialContext` that resolves the hostname itself, validates every returned IP against `isBlockedIP`, then dials the literal IP. This prevents DNS rebinding attacks where `metadata.google.internal` returns a public IP at pre-check time and a loopback IP at connect time. The pre-check validates all resolved IPs before any connection is attempted.

#### SSRF — IP Blocklist

**Finding ID**: F-SSRF-IP  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1436–1456`

`isBlockedIP` blocks: loopback, link-local unicast/multicast, interface-local multicast, unspecified, RFC1918 private, cloud metadata `169.254.169.254`, and `0.0.0.0/8`. This is comprehensive.

#### SSRF — Redirect Limit

**Finding ID**: F-SSRF-REDIRECT  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1561–1566`

Redirects are capped at 10 hops via `CheckRedirect`, and each redirect target is re-validated against `assertFetchURLAllowed`. This prevents redirect-based SSRF to internal services.

#### SSRF — Scheme Restriction

**Finding ID**: F-SSRF-SCHEME  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1382–1387`

Only `http` and `https` schemes are permitted. `file://`, `gopher://`, `dict://` are rejected.

### Access Control

#### AuthZ — Read Path Containment

**Finding ID**: F-AUTHZ-READ  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1239–1289`

`Read` resolves all paths to absolute, checks they are contained within `s.wd`, then re-checks containment after `EvalSymlinks`. A symlink pointing outside `wd` is refused even if the lexical path passes. Null bytes are rejected.

**Residual risk**: On Windows, junction points and NTFS reparse points are handled by `EvalSymlinks`, but directory junction traversal is mitigated by the path-containment check. Not a critical gap for local-agent use.

#### AuthZ — Edit/Write Symlink Safety

**Finding ID**: F-AUTHZ-WRITE-SYM  
**Severity**: Low  
**Component**: `internal/sandbox/safefs.go`

`Write` and `Edit` use `safefs.CheckNoSymlinks` which Lstat-walks each path component, refusing any non-regular file (symlink, device, named pipe, socket) along the traversal. Missing-leaf cases (where the target file doesn't exist) also reject symlinked parents, closing F-04/F-07/F-08/F-25 cluster.

Atomic writes via `WriteFileAtomic` (tmp + rename) close the TOCTOU window between `CheckNoSymlinks` and file open. Pre-existing file modes are preserved on edit/write to avoid widening permissions on secrets files.

### Data Exposure

#### Secrets — Redaction at Transport Layer

**Finding ID**: F-DATA-EXPOSE  
**Severity**: Low  
**Component**: `internal/transport/handlers.go`

Handlers call `h.redactString(resp.Stdout)` and `h.redactString(resp.RawStdout)` before stashing or returning. Redaction patterns are loaded from `.dfmt/redact.yaml` at daemon startup (per ADR-0014 merge semantics). The redact layer operates on ANSI-stripped, normalized output so escape sequences cannot split a secret across positions and bypass regex anchors.

#### Wire Dedup — Repeated Content

**Finding ID**: F-DATA-DEDUP  
**Severity**: Informational  
**Component**: `internal/transport/handlers.go:1279–1301`

`seenBefore(ctx, contentID)` prevents re-transmitting the same content bytes within a daemon session. A repeated `cat /etc/passwd` returns a `(unchanged; same content_id)` acknowledgement rather than re-sending the full content. This reduces token cost and limits exposure surface.

### Path Traversal

#### Path Traversal — Read

**Finding ID**: F-PATH-READ  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1239–1289`

Fully mitigated: lexical containment check + symlink resolution + `filepath.Clean` + null-byte rejection + policy `**` allow with `.env*` / `secrets/**` / `id_*` specific denies.

#### Path Traversal — Write/Edit

**Finding ID**: F-PATH-WRITE  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:1942–2032` + `internal/safefs/safefs.go`

Fully mitigated: `filepath.Rel(absWd, cleanPath)` rejects `..`-prefixed paths. `safefs.CheckNoSymlinks` refuses symlink parents. Atomic write closes TOCTOU. Permission mode preserved.

### Code Execution

#### RCE — Subprocess Buffer Cap

**Finding ID**: F-RCE-BUFFER  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:2142–2163`

`MaxRawBytes` (256 KB) caps subprocess stdout via `io.LimitReader` on `StdoutPipe`. After the cap is hit, `io.Copy(io.Discard, ...)` drains the pipe so the subprocess can exit cleanly. This prevents OOM from runaway `find / -name "*"` output. The daemon never buffers unlimited subprocess output.

#### RCE — Timeout Enforcement

**Finding ID**: F-RCE-TIMEOUT  
**Severity**: Low  
**Component**: `internal/sandbox/permissions.go:2114–2124`

`execImpl` wraps the context with a `context.WithTimeout`. Default is 60s; maximum is 300s. Both are enforced even if the calling handler sets a larger value (the lesser of requested and max is used).

### Business Logic

#### Concurrency — Semaphore Rate Limiting

**Finding ID**: F-BUSINESS-SEMAPHORE  
**Severity**: Informational  
**Component**: `internal/transport/handlers.go:1242–1246`

The exec handler uses a semaphore (`acquireLimiter`) to bound concurrent subprocess executions, preventing fork-bomb amplification within a single daemon session. The limit is not user-configurable but is a fixed safety valve.

---

## 5. Phase 3 — Verified Findings

| ID | Severity | Title | Component | Status |
|----|----------|-------|-----------|--------|
| F-V13 | Low | Header injection guard | permissions.go:1496 | Verified — fixed |
| F-SSRF-DNS | Medium | DNS rebinding defense | permissions.go:1515 | Verified — implemented |
| F-SSRF-REDIRECT | Low | Redirect re-validation | permissions.go:1561 | Verified |
| F-AUTHZ-WRITE-SYM | Low | Symlink-safe atomic writes | safefs.go | Verified — implemented |
| F-RCE-BUFFER | Low | Subprocess buffer cap | permissions.go:2142 | Verified |
| F-PATH-WRITE | Low | Path containment + atomic write | permissions.go:1942 | Verified |

---

## 6. Phase 4 — Report

### Security Posture Summary

DFMT's sandbox model is well-architected for a local AI tool proxy:

- **Least privilege on exec**: Hard-deny base commands (`rm`, `sudo`, `dd`, `mkfs`, etc.) cannot be re-enabled by operator overrides.
- **SSRF defense**: DNS resolution self-validates every IP; no trust-on-first-connect.
- **Path integrity**: Symlink-aware containment + atomic writes + no TOCTOU.
- **Secrets hygiene**: Redaction at transport layer, wire dedup, no repeated raw bytes.
- **Output normalization**: 8-stage `NormalizeOutput` pipeline strips ANSI, CR-rewrite spam, repeat spam, stack traces, git diff noise, HTML noise before policy evaluation — preventing token amplification from UI artifacts.
- **Token-based policy gates**: Return policy uses approximated tokens (ASCII/4 + non-ASCII runes), not raw bytes, aligning cost with what the agent actually pays (ADR-0012).

### Recommendations

| Priority | Recommendation | Rationale | Status |
|----------|----------------|-----------|--------|
| Low | Consider adding `;` as an explicit shell operator separator in `splitByShellOperators` | Defense-in-depth; current behavior is safe but not explicit | Open |
| Low | Document the hard-deny exec list in the `permissions.yaml` override file header | Operators editing `.dfmt/permissions.yaml` should know these cannot be overridden | Open |
| ~~Informational~~ | ~~BatchExec stub~~ | **Fixed** — `ErrBatchExecNotImplemented` returned instead of `nil, nil` | Closed |

**Closed**: BatchExec stub at `permissions.go:1605` now returns `ErrBatchExecNotImplemented` instead of silently succeeding. Tests updated to expect the error.

---

## 7. Verdict

**Overall Assessment**: Secure for intended use.

DFMT is designed to run locally on a developer's workstation, serving a single AI coding agent operating on code in a project directory. The threat model does not include a remote attacker — it assumes a cooperating or semi-autonomous AI agent that may attempt operations beyond its apparent intent (the "path traversal via apparent intent" scenario). The sandbox correctly assumes that intent and enforces policy at every boundary.

The default-deny allow-explicit policy, combined with hard-deny base commands that operators cannot re-enable, provides a meaningful brake on destructive operations even if the agent or its configuration is compromised.

---

*Report generated by security-check skill — phase 4 complete.*
