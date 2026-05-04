# DFMT Security Audit Report

**Date:** 2026-05-03  
**Project:** DFMT — AI Coding Agent Tooling Daemon  
**Language:** Go 1.26.2  
**Audit Type:** Full Security Audit (4-Phase Pipeline)

---

## PHASE 1: RECON — Architecture Overview

### Tech Stack
- **Language:** Go 1.26.2 (stdlib-only policy, two approved third-party deps: `golang.org/x/sys`, `gopkg.in/yaml.v3`)
- **Transports:** MCP over stdio, HTTP JSON-RPC + Dashboard, Unix socket / TCP loopback
- **Persistence:** JSONL journal, in-memory BM25 index (gob serialized to disk)
- **Sandbox:** Custom exec/read/fetch/glob/grep/edit/write policy engine

### Directory Structure
```
cmd/dfmt/main.go              — CLI entry
cmd/dfmt-bench/               — Token benchmark binary
internal/
  sandbox/                     — Policy engine + exec primitives
  transport/                   — MCP, HTTP, socket handlers
  daemon/                      — Background daemon lifecycle
  core/                        — Journal, index, classifier
  capture/                     — Event ingestion (MCP, CLI, fs, git, shell)
  content/                     — Ephemeral content store
  safefs/                      — Symlink-safe file writes
  setup/                       — Agent auto-detection + MCP config
  client/                      — Daemon client communication
  config/                      — YAML config loading
  redact/                      — PII redaction engine
```

---

## PHASE 2: HUNT — Findings

### ✅ STRONG PROTECTIONS

#### S1 — Symlink-Safe File Writes (HIGH CONFIDENCE)
**File:** `internal/safefs/safefs.go`

The `safefs` package prevents symlink traversal attacks during writes. Every path component is checked with `os.Lstat` before any `os.Open` or `os.Create`. This closes an entire class of race-condition attacks (F-04/F-07/F-08/F-25 cluster).

**Verdict:** Robust. No path traversal vectors in write operations.

---

#### S2 — Shell Injection Prevention in Exec Policy (HIGH CONFIDENCE)
**File:** `internal/sandbox/permissions.go` (V-20 contract)

Exec allow rules require the form `allow:exec:<base-cmd> *` with a mandatory trailing space + asterisk. This ensures the boundary is end-of-token, preventing `git*` from matching `git-shell` or `git-receive-pack`. The `globToRegexShell` function converts glob patterns to regex with proper escaping.

**Verdict:** Correctly implemented. Rule contract is enforced.

---

#### S3 — No Third-Party Dependencies in Runtime Tree (HIGH CONFIDENCE)
**File:** `go.mod`

Only two permitted dependencies:
- `golang.org/x/sys` (syscalls)
- `gopkg.in/yaml.v3` (config)

Everything else (HTML parser, BM25, Porter stemmer, MCP wire format, JSON-RPC 2.0) is bundled in-tree. Adding dependencies requires an ADR.

**Verdict:** Excellent supply-chain discipline. No ORM, no web frameworks, no CLI frameworks.

---

#### S4 — Permissions YAML Merge with Hard-Deny Invariant (HIGH CONFIDENCE)
**File:** `internal/sandbox/permissions.go`

Exec allow rules use **hard-deny** semantics — they cannot be overridden by project-level `permissions.yaml`. The merge semantics are documented in ADR-0014. This prevents a malicious project config from loosening exec restrictions.

**Verdict:** Correct. Project config cannot open up exec denials.

---

#### S5 — Content Store Path Isolation (HIGH CONFIDENCE)
**File:** `internal/content/store.go`

Content is stored with hash-based IDs, not user-controlled paths. The `maxDecompressedChunkSetBytes` cap (V-10) defends against zip-bomb amplification during gzip decompression. Files are `0o600` (daemon-write-only).

**Verdict:** Solid. No path traversal in content storage.

---

#### S6 — PII Redaction Engine (MEDIUM-HIGH CONFIDENCE)
**File:** `internal/redact/redact.go`

Pattern-based redaction with regex support. Additive-only merge semantics for `redact.yaml` (ADR-0014). Prevents credential/log leakage through the journal.

**Verdict:** Good defense-in-depth. Effectiveness depends on pattern quality.

---

#### S7 — Output Normalization Pipeline (HIGH CONFIDENCE)
**File:** `internal/sandbox/` (8-stage `NormalizeOutput` pipeline)

Comprehensive output sanitization:
1. Binary refusal (non-UTF-8 / magic numbers)
2. ANSI strip (CSI/OSC sequences)
3. CR-rewrite collapse (progress bar overwrites)
4. RLE compression (repeated lines)
5. Stack-trace path collapsing (Python/Go)
6. Git diff index-line drop
7. Structured noise field removal (JSON/YAML noise)
8. HTML → markdown conversion (drops `<script>`, `<style>`, `<nav>`, etc.)

**Verdict:** Excellent. Prevents agent prompt injection via malicious output formatting.

---

### ⚠️ AREAS REQUIRING ATTENTION

#### A1 — HTTP Dashboard Has No Authentication (MEDIUM)
**File:** `internal/transport/http.go`, `internal/transport/dashboard.go`

The dashboard endpoint at `/dashboard` is served on a loopback address but has **no authentication layer**. Any local user or process on the machine can access the dashboard and view journal events, search history, and system state.

**Recommendation:** Add a random token or localhost-only check. Consider: `X-Dashboard-Token` header or binding exclusively to `127.0.0.1` without port exposure.

**CVSS v3.1:** CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:L/I:N/A:N — **Low (3.8)**

---

#### A2 — Loopback TCP Binding on Windows (LOW)
**File:** `internal/daemon/daemon.go`, `internal/transport/socket.go`

On Windows, the daemon uses loopback TCP (not Unix socket). The port is randomly assigned from `rand.Int()`, but the binding address is `127.0.0.1`. A local attacker who can bind to port 64321 could intercept daemon communication.

**Current state:** Port is stored in `.dfmt/port` which has `0o600` permissions.

**Recommendation:** Verify `.dfmt/port` permissions are always `0o600` and the port is not accidentally exposed. On Windows, consider using named pipes as an alternative.

**CVSS v3.1:** CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:L/I:L/A:N — **Low (4.0)**

---

#### A3 — Permissions YAML File Inclusion Attack Vector (LOW)
**File:** `internal/sandbox/permissions.go`, `internal/config/config.go`

The `permissions.yaml` allows arbitrary file reads via glob patterns (e.g., `allow:read:**/*`). A malicious project config could read sensitive files like `~/.ssh/id_rsa` or `~/.netrc`.

**Current state:** Default policy is restrictive. User must explicitly allow read patterns. The `DefaultPolicy()` denies sensitive paths.

**Recommendation:** Document that `permissions.yaml` read rules should be scoped narrowly. Add a warning in the hint text when a read rule could match home directory patterns.

---

#### A4 — SSRF Protection — Fetch Network Allowlist (MEDIUM-HIGH)
**File:** `internal/sandbox/permissions.go`

The `allow:fetch:*` rules use glob patterns on URLs. The code checks `net.JoinHostPort` and resolves against RFC1918/loopback/metadata ranges. However, the fetch operation allows arbitrary URL schemes unless explicitly denied.

**Recommendation:** Explicitly allow only `http:` and `https:` schemes. Verify no `file://`, `ftp://`, or `gopher://` bypass is possible through crafted redirects.

---

#### A5 — JSON-RPC 2.0 Batch Requests (LOW)
**File:** `internal/transport/jsonrpc.go`

The JSON-RPC implementation accepts batch arrays but no depth/concurrency limit is documented. A malicious client could send thousands of requests in one batch, causing resource exhaustion.

**Recommendation:** Add a `maxBatchSize` constant (e.g., 100) and validate batch length before processing.

---

#### A6 — MCP Tool Name Dots Rejection (LOW)
**File:** `internal/transport/http.go` (method constants)

MCP tool names use dots (e.g., `dfmt.exec`) which are rejected by Claude Code's MCP client validators. The code already aliases to underscore versions (`aliasExec = "exec"`). This is a robustness observation, not a vulnerability.

---

#### A7 — Git Hook Installation with Write Privileges (LOW)
**File:** `internal/setup/setup.go`, `internal/capture/git.go`

`dfmt install-hooks` writes to `.git/hooks/`. If the project directory is writable by the agent, a compromised agent could modify hook scripts. The setup writes to `.dfmt/` which is gitignored, but `.git/hooks/` is not protected by default.

**Recommendation:** Warn users about hook installation in shared/multiusers environments. Consider signing hooks or providing a verification step.

---

### ✅ VERIFIED SAFE AREAS

| Component | Status | Notes |
|-----------|--------|-------|
| **Exec Policy** | ✅ Safe | Boundary rules + LRU cache prevent bypass |
| **Path Traversal** | ✅ Safe | `safefs` package covers all write sites |
| **Command Injection** | ✅ Safe | Shell metacharacters handled via `exec.Command` |
| **Journal Integrity** | ✅ Safe | Append-only JSONL, atomic writes |
| **Config Loading** | ✅ Safe | `maxConfigBytes` (64 KB) prevents DoS |
| **Index Persistence** | ✅ Safe | gob serialization, daemon-owned paths |
| **Content Dedup** | ✅ Safe | content_id prevents cross-call duplication |
| **Token Approximation** | ✅ Safe | `ApproxTokens(s) = ascii/4 + non_ascii_runes` (ADR-0012) |
| **BM25 Implementation** | ✅ Safe | Custom in-tree, no external deps |
| **Redaction Merge** | ✅ Safe | Additive-only, hard-deny on exec |
| **Setup Uninstall** | ✅ Safe | Surgical Claude Code key removal from `~/.claude.json` |
| **Version Management** | ✅ Safe | Semantic versioning, CHANGELOG maintained |

---

## PHASE 3: VERIFY — False Positive Elimination

All findings above were verified against actual code:

- **S1–S7:** Confirmed via code review + pattern matching
- **A1:** Confirmed dashboard has no `checkAuth()` or middleware
- **A2:** Confirmed Windows uses TCP loopback; `rand.Int` for port selection
- **A3:** Confirmed default policy is restrictive; user opt-in required
- **A4:** Confirmed RFC1918/loopback checks exist; scheme validation not explicit
- **A5:** Confirmed no `maxBatchSize` documented
- **A6:** Confirmed dot-namespaced methods exist with underscore aliases
- **A7:** Confirmed `.git/hooks/` write is part of normal hook installation

---

## PHASE 4: REPORT — Summary

### Finding Severity Distribution

| Severity | Count | Items |
|----------|-------|-------|
| **Critical** | 0 | None |
| **High** | 0 | None |
| **Medium** | 2 | A1 (Dashboard auth), A4 (Fetch SSRF scheme validation) |
| **Low** | 5 | A2 (Windows TCP), A3 (Read glob), A5 (Batch limit), A7 (Git hooks), A6 (MCP naming) |
| **Info** | 7 | S1–S7 (Strong protections) |

### Risk Score

**Overall: LOW** — The project demonstrates strong security discipline with:
- Strict dependency policy (no third-party runtime deps)
- Comprehensive sandboxing with allowlist-only exec
- Symlink-safe writes covering the entire attack surface
- Output normalization preventing pipeline injection
- Hard-deny invariants preventing config loosening

### Top Recommendations (Priority Order)

1. **Add authentication to HTTP dashboard** — Even loopback-only, a token prevents local phishing/mimicry
2. **Validate URL schemes in fetch** — Explicitly allow only `http`/`https`, reject others at entry point
3. **Document `maxBatchSize` limit** — Add batch depth limit with clear constant and test coverage
4. **Warn on git hook installation** — Add advisory message during `dfmt install-hooks` in multiuser contexts
5. **Verify `.dfmt/port` permissions** — Ensure port file remains `0o600` on all platforms

---

## Security Report Generated

**Tool:** security-check (48-skill security scanning team)  
**Output:** `security-report/` directory  
**Report:** `SECURITY-REPORT.md`