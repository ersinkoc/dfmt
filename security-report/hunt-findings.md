# Phase 2 — Hunt findings (consolidated)

**Method:** Direct read of every security-critical source file, focused on the four parallel-scope categories. The four scoped subagents originally launched failed to invoke any real tools (each returned `tool_uses: 0` after stalling on the project's "use DFMT MCP tools" rule that wasn't present in their session); the lead auditor performed the hunt directly with native Read/Grep/Bash. Findings here are based on actual code, not subagent hallucination.

**Files reviewed:**
- `internal/sandbox/permissions.go` (1297 lines, full)
- `internal/sandbox/sandbox.go`, `path.go`, `runner_*.go` (spot-checked via grep)
- `internal/transport/http.go` (673 lines, full), `dashboard.go` (full), `socket.go` (full), `mcp.go` (full), `jsonrpc.go` (full), `handlers.go` (spot)
- `internal/daemon/daemon.go` (lines 1-80, 280-410)
- `internal/core/journal.go` (R13 diff), `index.go` (LoadIndex), `index_persist.go` (full)
- `internal/redact/redact.go` (full)
- `internal/cli/dispatch.go` (file-mode call sites and R13 diff)
- `internal/setup/setup.go` (R13 diff)
- `internal/client/client.go`, `registry.go` (full where relevant)
- `internal/project/gitignore.go` (R13-new file, full)
- `.github/workflows/ci.yml`, `release.yml` (full)
- `go.mod`, `go.sum`

---

## Round-13 fix verification (all CLOSED)

| Prior ID | Subject | Fix | Verified at |
|---|---|---|---|
| V-1 | Sandbox bypass via bare `&` | `hasShellChainOperators` now lists `&`; `splitByShellOperators` flushes on bare `&` while preserving `&<digit>` as redirection operand | `permissions.go:426`, `:648-655` |
| V-4 | `~/.{claude,codex,cursor,vscode,gemini,windsurf,zed,.config/zed,.config/continue}` 0o755 | All nine `configure*` funcs now `MkdirAll(dir, 0o700)`; `~/.dfmt/` registry dir same | `dispatch.go:1349,1399,1408,1417,1426,1435,1444,1453,1462`, `client/registry.go:94`, `setup/setup.go:177` |
| V-5 | `.dfmt/config.yaml` 0o644 | Both `runInit` and `autoInitProject` now write 0o600 | `dispatch.go:187`, `client/client.go:193` |
| V-6 | `bytes.Contains(content, ".dfmt/")` false-pos/neg in gitignore detection | New `project.IsDfmtIgnored` walks lines, handles negation, four canonical spellings, comments, `\r\n` | `internal/project/gitignore.go` (35 lines, all new) |
| V-9 | Journal silently dropped malformed JSON lines | `streamFile` and `scanLastID` now call `journalWarnf` with bounded snippet; tests can override the var | `journal.go:39-56,284-289,453-456` |
| V-16 | RPC params decode error discarded → zero-value params | New `decodeRPCParams` returns `-32602 Invalid params` on failure; applied to remember/search/recall/stats/exec/read/fetch + `/api/stats` | `transport/http.go:382-405` and 7 handler call sites |

A regression test for V-1 lives in `internal/sandbox/permissions_test.go` and 92 lines of new tests; for V-9 in `internal/core/journal_v9_test.go`; for V-6 in `internal/project/gitignore_test.go`; for V-16 in `internal/transport/http_test.go`. R13 is well-tested.

---

## Findings

### H-1 — Redaction patterns miss many modern credential formats
**Category:** Data exposure / Secrets
**Severity (initial):** Medium
**File:** `internal/redact/redact.go:26-56`
**CWE:** CWE-200 Information Exposure · CWE-359
**Trigger:** Any string containing a non-allow-listed token format flows through `Redact()` (journal append, recall snapshot, fswatch event, exec stdout, fetch body) without scrubbing.

**Snippet:**
```go
{name: "github_token", regex: regexp.MustCompile(`(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,}`), repl: "[GITHUB_TOKEN]"},
{name: "openai_key",   regex: regexp.MustCompile(`sk-[A-Za-z0-9_]{48,}`),                       repl: "[OPENAI_KEY]"},
{name: "aws_key",      regex: regexp.MustCompile(`AKIA[A-Z0-9]{16}`),                            repl: "[AWS_KEY]"},
{name: "stripe_key",   regex: regexp.MustCompile(`sk_live_[A-Za-z0-9]{24,}`),                    repl: "[STRIPE_KEY]"},
```

**Why it's a finding — concrete gaps:**
1. **Anthropic keys** (`sk-ant-api03-…`): the `openai_key` regex uses `[A-Za-z0-9_]` which excludes `-`. `sk-ant-…` matches `sk-` then bails at the first `-` after `ant`. So a string like `ANTHROPIC_API_KEY=sk-ant-api03-abc...xyz` writes the entire key to the journal. (The `env_export` matcher catches this if the key name contains `key`/`auth`/`token`/etc., but inline mentions outside `KEY=` form do not.)
2. **AWS prefixes**: only `AKIA` matched. Not matched: `ASIA` (STS temp), `AGPA` (group), `AROA` (role), `AIDA` (user), `ANPA` (managed policy), `AIPA` (instance profile), `ANVA`, `ABIA`, `ACCA`.
3. **GitHub fine-grained PAT**: `github_pat_…` (introduced 2022) — not matched.
4. **Slack tokens**: `xoxb-…`, `xoxp-…`, `xoxa-…`, `xoxr-…` — none.
5. **Google API key**: `AIza[0-9A-Za-z_-]{35}` — none.
6. **Discord bot token**: standard `MT…`, `ND…` patterns — none.
7. **Stripe secret/restricted**: `rk_live_…`, `rk_test_…` — none.
8. **Twilio**: `SK[a-f0-9]{32}` — none.
9. **Sendgrid**: `SG\\.[A-Za-z0-9_-]{22}\\.[A-Za-z0-9_-]{43}` — none.
10. **Mailgun**: `key-[a-f0-9]{32}` — none.
11. **DB connection strings**: `(postgres|mysql|mongodb(\\+srv)?|redis|amqp)://[^:]+:[^@]+@…` — passwords inline in URLs are not redacted.
12. **Slack incoming-webhook URL**, **Discord webhook URL** — unredacted.

**Repro:** write `dfmt remember --data '{"key":"sk-ant-api03-test_1234567890_abcdefghijklmnopqr"}'`. Read `.dfmt/journal.jsonl` — the literal token survives. (Replicates trivially in unit test against `Redactor.Redact`.)

**Suggested fix:** add the pattern bank above. Each new pattern must be character-class-bounded (no `.*`) to avoid ReDoS. Also extend the OpenAI/Anthropic matcher to allow `-`:
```go
{name: "anthropic_key", regex: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{40,}`), repl: "[ANTHROPIC_KEY]"},
```
Add a regression suite in `redact_test.go` driving 1-2 known-good and known-bad samples per provider.

---

### H-2 — Socket server has no per-connection idle timeout
**Category:** DoS / Resource Exhaustion
**Severity (initial):** Low
**File:** `internal/transport/socket.go:100-134`
**CWE:** CWE-400 Uncontrolled Resource Consumption · CWE-770 Allocation of Resources Without Limits

**Snippet:**
```go
func (s *SocketServer) handleConn(conn net.Conn) {
    defer ...
    codec := NewCodec(conn)
    for {
        req, err := codec.ReadRequest()  // blocks forever if peer never writes
        if err != nil { return }
        ...
    }
}
```

**Why it's a finding:** A local user (same UID) that opens N Unix-socket connections and never sends a request keeps N goroutines alive, each holding a `bufio.Reader` and connection FD. Bounded by FD ulimit. The HTTP path correctly sets `ReadHeaderTimeout: 5s` and `IdleTimeout: 60s` (`http.go:147-149`); the Unix-socket / TCP raw-codec path does not.

**Threat model:** local-only (Unix socket is mode 0700 in a 0700 dir; same-user attacker only). Realistic adversary is a misbehaving agent or wrongly-configured shell loop.

**Suggested fix:** wrap `conn.SetReadDeadline(time.Now().Add(60 * time.Second))` before each `codec.ReadRequest()`, and reset on each successful read.

---

### H-3 — Unix-socket file-mode TOCTOU window
**Category:** Race / Permissions
**Severity (initial):** Low (Info, mitigated)
**File:** `internal/transport/socket.go:49-56`
**CWE:** CWE-367 TOCTOU

**Snippet:**
```go
ln, err := net.Listen("unix", s.path)   // socket created with default umask
...
os.Chmod(s.path, 0700)                  // tightened later
```

**Why it's a finding:** Between `Listen` and `Chmod` the socket exists with whatever the process umask permits (commonly 0o755 / 0o775). A same-host attacker who races the chmod could `connect()` during the window.

**Mitigation already in place:** the parent directory created at line 40 is mode `0o700`, so other UIDs cannot traverse to the socket regardless of its mode. The race is unreachable for a different-UID attacker; a same-UID attacker has full filesystem access anyway and gains nothing. Severity stays Low / Info.

**Suggested fix (defense in depth):** call `syscall.Umask(0o077)` before `net.Listen` and restore after.

---

### H-4 — `/api/daemons` returns global daemon registry to authenticated callers
**Category:** Information disclosure
**Severity (initial):** Info
**File:** `internal/transport/http.go:588-621`, `internal/client/registry.go:14-22`

**Why it's a finding:** Any caller bearing the daemon's auth token can `GET /api/daemons` and receive the JSON list of *every* DFMT daemon currently running for the same user (project paths, PIDs, ports/socket paths, start time). Tokens of other daemons are not disclosed (those live in per-project `.dfmt/port` files), but an attacker learns:
- Which projects the user has open.
- TCP ports of other daemons (Windows).
- Unix socket paths (other OSes).

The same data is readable from `~/.dfmt/daemons.json` (mode 0o600). Practical impact is identical; the HTTP route just makes it network-reachable to anyone holding *one* daemon's token.

**Threat model:** the bearer token is generated per daemon, written to `.dfmt/port` (mode 0o600), and only readable by the same UID. The endpoint is not reachable cross-user.

**Suggested fix:** strip `project_path` from the `/api/daemons` response and return only port+pid (the dashboard uses path for display only); or require an explicit `--enable-multi-daemon-listing` config flag. No urgency — this is a defense-in-depth nicety, not a real boundary.

---

### H-5 — GitHub Actions pinned by tag, not SHA
**Category:** Supply chain / CI
**Severity (initial):** Info
**File:** `.github/workflows/ci.yml`, `.github/workflows/release.yml`
**CWE:** CWE-1357 Reliance on Insufficiently Trustworthy Component

| Workflow | Action | Pin |
|---|---|---|
| ci.yml | `actions/checkout` | `@v4` |
| ci.yml | `actions/setup-go` | `@v5` |
| ci.yml | `golangci/golangci-lint-action` | `@v7` |
| release.yml | `actions/checkout` | `@v4` |
| release.yml | `actions/setup-go` | `@v5` |
| release.yml | `softprops/action-gh-release` | `@v2` |

`release.yml` runs with `permissions: contents: write` and uploads cross-compiled binaries. A tag rewrite on any of these actions would let a release ship attacker-modified artifacts.

**Suggested fix:** pin every third-party action by 40-char SHA, e.g. `softprops/action-gh-release@9fbf899...` with the human tag preserved as a comment. The `actions/*` line is lower-risk because GitHub controls those repos; `softprops/action-gh-release` is third-party and warrants SHA pinning first.

---

### H-6 — `gopkg.in/yaml.v3` is unmaintained
**Category:** Dependency / Supply chain
**Severity (initial):** Info
**File:** `go.mod` (yaml.v3 v3.0.1, last release 2022-05-27)

**Why it's a finding:** The library is in maintenance mode upstream. No active CVE today, but a future disclosure has no upstream patch path.

**Reachability:** YAML is parsed only on project-local `.dfmt/config.yaml` and `.dfmt/redact.yaml`, both written by `dfmt init` in the user's own working tree. No untrusted YAML enters the daemon, so this is a "monitoring only" item — not a current vulnerability.

**Suggested fix:** track upstream; if a CVE drops, switch to a maintained fork (`go-yaml/yaml.v3` derivatives such as `goccy/go-yaml`) or hand-write a minimal subset parser given the schema is small (~10 keys).

---

### H-7 — No rate limiting on exec/fetch
**Category:** DoS
**Severity (initial):** Info
**File:** `internal/transport/http.go` and `internal/sandbox/permissions.go:498`
**CWE:** CWE-770 Allocation of Resources Without Limits

A misbehaving agent can issue `dfmt.exec` / `dfmt.fetch` in a tight loop. Each call has a default 30 s timeout (`fetch`) or `DefaultExecTimeout` (`exec`). Concurrent calls are unbounded.

**Threat model:** local-only; the agent IS the attacker. Practical impact is "burn the user's CPU and hit their network endpoints faster than expected." Not a security boundary.

**Suggested fix:** semaphore on concurrent `exec` (e.g. max 4 in flight) and on `fetch` (e.g. max 8 in flight). Optional, low priority.

---

### H-8 — `extractBearerToken` returns empty on absent header
**Category:** Auth (verified clean)
**Severity:** N/A — not a finding
**File:** `internal/transport/http.go:222-243`

Documented for completeness: when no `Authorization` and no `X-DFMT-Token` are present, `extractBearerToken` returns `""`. The middleware compares with `subtle.ConstantTimeCompare([]byte(""), []byte(authToken))`. The function returns 0 when lengths differ (it doesn't process `b` at all when `len(a) != len(b)`). The 401 path is taken. This is **correct**; cited only because `subtle.ConstantTimeCompare` lacking length-equality is a common antipattern and worth noting it doesn't apply here.

---

### Categories with no issues found

- **SQL injection** — no SQL anywhere; project uses no DB.
- **NoSQL / GraphQL / LDAP injection** — no relevant clients.
- **XML External Entity (XXE)** — no XML parsing in repo.
- **SSTI** — no template engine consumes user input. The dashboard HTML is a static const with no interpolation; no `text/template` / `html/template` use anywhere user data flows through.
- **JWT** — no JWT implementation; bearer tokens are opaque random.
- **Open redirect** — no handler returns `Location:` from user input.
- **HTTP header injection** — `req.Headers` flows into `httpReq.Header.Set(k, v)` (`permissions.go:904-906`); Go's `net/http` rejects `\r\n` in header values, so CRLF injection is closed by the stdlib.
- **CSRF** — `wrapSecurity` rejects browser cross-origin requests on every non-health endpoint via `Origin` header check; legitimate browser requests must come from the listener's own host:port.
- **Clickjacking** — dashboard sets `X-Frame-Options: DENY` and CSP `frame-ancestors 'none'`.
- **Mass assignment** — RPC param structs are explicit `struct` definitions per method (`ExecParams`, `ReadParams`, etc.); no `interface{}` / `map[string]any` flowing into privileged actions.
- **Insecure deserialization** — JSON only, on user-owned files (`index.json`, `index.cursor`, `daemons.json`, `journal.jsonl`). Not attacker-controlled in threat model.
- **SSRF in `dfmt.fetch`** — robust: scheme allowlist, hostname-level cloud-metadata blocklist, IP-literal check, DNS resolution + per-IP block check, custom `DialContext` re-resolves and dials the literal IP (DNS-rebind safe), redirect handler re-runs `assertFetchURLAllowed`. `isBlockedIP` covers loopback, private (RFC1918/ULA), link-local, multicast, unspecified, `0.0.0.0/8`, and the AWS metadata literal.
- **Path traversal in `dfmt.read`** — `filepath.Clean` + relative-to-wd check + symlink resolve + re-check on resolved path (refuses files inside wd lexically but pointing outside via symlink). Refuses absolute paths when no wd is set.
- **Command-substitution / chain-operator bypasses** beyond V-1 — `splitByShellOperators` covers `&&`, `||`, `&`, `;`, `|`, `>`, `>>`, `<`, `<<`, `\n`, `$(...)`, backticks. The recursive split-and-policy-check approach catches chained commands.
- **Sandbox env injection** — `buildEnv` enforces `sandboxBlockedEnvNames` (PATH, IFS, BASH_ENV, ENV, PS4, PROMPT_COMMAND, HOME, USER, USERPROFILE, APPDATA, LOCALAPPDATA, PATHEXT, COMSPEC, SYSTEMROOT) and prefix list (LD_, DYLD_, GIT_, NODE_, PYTHON, RUBY, PERL5).
- **Goroutine leaks** — `daemon.Stop` orchestrates an explicit ordered shutdown: `shutdownCh` → `idleCh` → fswatch.Stop → wg.Wait → PersistIndex → server.Stop → journal.Close. HTTP server uses `doneCh` so shutdown watcher exits even when the Start ctx isn't cancelled. Idle monitor is intentionally not in `wg` because it can call Stop itself; closes `idleCh` instead.
- **HTTP server timeouts** — `ReadTimeout: 30s`, `ReadHeaderTimeout: 5s` (Slowloris guard), `WriteTimeout: 30s`, `IdleTimeout: 60s`, `MaxHeaderBytes: 16 KiB`, `MaxBytesReader 1 MiB` body. Exemplary configuration.
- **Constant-time auth compare** — `subtle.ConstantTimeCompare` used (`http.go:223`).
- **CSP** — strict and correct on dashboard route: `default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'`. `'unsafe-inline'` is needed only for `<style>` inside the embedded HTML constant; no user data is interpolated.
- **Atomic file writes** — `writeRawAtomic` does `CreateTemp` → `Chmod 0600` → `Write` → `Sync` → `Close` → `Rename`, with `success` flag for cleanup-on-error.
- **JSON-RPC line size cap** — `MaxJSONRPCLineBytes = 1 MiB` (`jsonrpc.go:37,76-78`).
- **Hardcoded secrets in repo** — none. Verified by quick grep for AWS/Stripe/etc. patterns.
- **ReDoS in redact patterns** — every regex uses bounded character classes; no nested quantifiers; non-greedy where needed (`private_key`'s `.*?`). Safe.
- **Constant-time bearer compare** — confirmed (`subtle.ConstantTimeCompare`).
- **Reflection-based dispatch from user data** — none. RPC method dispatch uses `switch req.Method` with explicit case strings.

---

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | **1** (H-1: redaction pattern coverage) |
| Low | **2** (H-2 socket idle timeout, H-3 socket chmod TOCTOU) |
| Info | **5** (H-4 daemon registry, H-5 actions SHA pin, H-6 yaml.v3, H-7 rate limit, H-8 auth check is correct) |

R13 closed every prior High and Low finding from the previous report. The only Medium that remains is the redaction-coverage gap (H-1), and even that is bounded — the journal is mode 0o600 in a 0o700 directory, so a leaked secret only escapes if the agent later includes journal content in something they ship (recall snapshot, copy-paste). Still worth fixing.
