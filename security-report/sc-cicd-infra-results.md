# SC-CICD-INFRA: CI/CD, Build/Release, Repo Hygiene Audit

**Scope:** GitHub Actions workflows, release pipeline, repo hygiene, lint coverage,
ADRs, embedded git hooks, accidental secrets. Classes targeted: sc-ci-cd, sc-iac,
sc-docker.

**Methodology:** Native `Read` on all files in scope. Manual review of permissions
blocks, `pull_request_target` / `workflow_run` triggers, gitignore completeness,
ldflags, sha256sum generation, embedded hook scripts. Repo-wide regex sweep for
canonical secret patterns (`AKIA...`, `ghp_...`, `sk-...`, `BEGIN ... PRIVATE KEY`,
`xoxb-`, `xoxp-`, etc.).

**Out of scope, confirmed absent:** No `Dockerfile*`, no `*.tf`, no `k8s/`, no
`docker-compose*`, no `helm/`. DFMT is a single Go binary with no IaC layer; the
sc-iac and sc-docker classes have no surface in this repo.

---

## Findings summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 1 |
| Low | 3 |
| Info | 4 |

---

## Findings

### M-CICD-01 (Medium): Embedded git hook scripts run unsanitised git metadata into a child process

**Files:**
- `internal/cli/hooks/git-post-commit.sh:11-16`
- `internal/cli/hooks/git-post-checkout.sh:6-20`
- `internal/cli/hooks/git-pre-push.sh:6-15`

**What:** The post-commit hook captures `COMMIT_MSG=$(git log -1 --format=%s)`
and passes it as the fourth positional argument to `dfmt capture git commit
"$COMMIT_HASH" "$COMMIT_MSG"`. The post-checkout hook passes `$1` (the ref)
into `git show-ref --verify "refs/heads/$REF"` and into the dfmt invocation.
Pre-push reads `$REMOTE` (`$1` from git) similarly.

**Risk:** When the user runs `dfmt install-hooks`, these scripts execute with
the user's full shell privileges every time someone commits, checks out, or
pushes. A malicious branch name like `'; rm -rf ~; #` or a maliciously crafted
commit message embedded by an upstream collaborator would normally be a concern
because git metadata is attacker-controllable through PR-style workflows.

**Mitigation present:** All interpolations use double-quoted `"$VAR"`, which
preserves them as a single positional argument to the next process — they are
not re-evaluated by `/bin/sh`. `git show-ref --verify` in post-checkout is the
only place a quoted variable becomes part of a non-argv string (`refs/heads/$REF`),
but that string is the *argument* to `--verify`, not a shell command, so git
treats it as a literal ref. Background `&` job nesting is fine: the parent shell
doesn't re-parse the args.

**Verdict:** Not directly exploitable today, but the pattern is fragile. A
future contributor refactoring to `eval`, backticks, or `sh -c "..."` would
turn this into a real RCE. Low likelihood, medium impact.

**Remediation:** Add a comment block at the top of each `git-*.sh` warning that
`$REF`, `$COMMIT_MSG`, etc. are attacker-controlled and must never be passed to
`eval`, `sh -c`, or unquoted command substitutions. Optionally pipe via stdin
(`printf '%s\n' "$COMMIT_MSG" | dfmt capture ...`) instead of argv to remove
even the theoretical concern about platforms with weird argv handling.

---

### L-CICD-01 (Low): `gosec` not enabled in golangci-lint config

**File:** `.golangci.yml:5-18`

**What:** `linters.default: none` is set, then 13 linters are explicitly
enabled. `gosec` (the standard Go static security analyzer — catches G101
hardcoded creds, G204 subprocess injection, G304 path traversal, G401 weak
crypto, etc.) is **not** in the enable list. Likewise `errcheck` IS enabled,
but a wide list of exclusions (lines 38-238) silences errcheck on dozens of
file:source pairs — most of these are legitimate (defer Close, test helpers),
but the cumulative effect is that error-handling regressions in those exact
spots will pass CI.

**Risk:** Security regressions in command-execution paths
(`internal/sandbox/`), credential handling (`internal/redact/`), and path
joining throughout the codebase will not trip the linter. The project relies
on hand-review for every PR. For a tool whose central feature is sandboxed
exec, gosec coverage is a cheap hardening win.

**Remediation:** Add `gosec` to `linters.enable`. Expect a one-time noise
spike; triage with `//nolint:gosec` annotations + comments for legitimate
uses (sandbox.exec, etc). Also consider `revive` for general code quality.

---

### L-CICD-02 (Low): macOS jobs marked `continue-on-error: true`

**File:** `.github/workflows/ci.yml:27`

**What:** `continue-on-error: ${{ matrix.os == 'macos-latest' }}` makes macOS
test failures non-blocking. Comment in the file (lines 23-26) acknowledges
this and lists `$TMPDIR` length issues and Linux-style hardcoded paths as
pre-existing flakes.

**Risk:** A platform-specific security regression on macOS (e.g., a Darwin-only
syscall pathway in `internal/capture/fswatch_darwin.go` — note: that file is
referenced in ADR-0004 line 83 but the watcher list in CLAUDE.md only mentions
Linux+Windows; check whether macOS support is shipped or intentionally skipped)
will not gate merges. The sandbox runs subprocesses with platform-specific
resource limits — a macOS bug in that path could ship unblocked.

**Remediation:** Track the named flakes in an issue, fix them, then drop
`continue-on-error`. Until then the comment is good documentation; the audit
finding is just to ensure visibility.

---

### L-CICD-03 (Low): `dev.ps1` mutates `~/.claude.json` and stops Claude processes by default

**File:** `dev.ps1:72-101, 172-199, 232`

**What:** The dev script — invoked by maintainers as `.\dev.ps1` — by default:
- Force-kills every running Claude/Claude Code process (`Stop-Process -Force`).
- Modifies `~/.claude.json` to remove "stale" project entries matched by
  `[\\/]Temp[\\/]dfmt[-_]` regex.
- Patches `mcpServers.dfmt.command` to point at the freshly built binary.
- Writes to user PATH.
- Uses `-X main.version=0.1.0-dev+$gitRev` ldflags (no `-s -w` strip in dev,
  acceptable; release.yml does strip).

**Risk:** Not a CI/CD risk per se (this script does not run in CI), but it is
an unsigned PowerShell script that destroys live editor sessions. If a
contributor copies it as a starting point for a setup script published to
end-users, the kill-claude-and-rewrite-config behaviour would be inappropriate.

**Remediation:** Add a banner at the top of `dev.ps1` reinforcing that this is
maintainer-only, and confirm `dev.ps1` itself is not referenced from any user
docs or installer flow. Out-of-scope for this audit but worth noting.

---

### I-CICD-01 (Info): GitHub Actions hygiene — well done

**Files:** `.github/workflows/ci.yml`, `.github/workflows/release.yml`

Confirmed positives:
- Workflow-level `permissions: contents: read` on CI (line 12-13), `contents:
  write` only on release (line 8-9). No write on PR-triggered runs.
- All third-party actions pinned by full 40-char SHA with version tag in a
  trailing comment (`actions/checkout@34e114876b...# v4` etc).
- No `pull_request_target` trigger anywhere. No `workflow_run` trigger. PR code
  never executes with elevated permissions.
- Matrix tests run on `ubuntu-latest`, `macos-latest`, `windows-latest` against
  Go 1.24 + 1.26.
- Release uses `-trimpath -ldflags "-s -w -X main.version=..."` (release.yml:34,
  39): symbols stripped, build paths trimmed, version pinned to git tag. Good
  reproducible-build hygiene.
- `sha256sum * > sha256sums.txt` runs in the same job that uploads the files
  (release.yml:50-53), so an attacker swapping a binary would also have to
  regenerate the checksum — not a defense in depth issue, but documented in
  the comment at release.yml:15-19. This is the standard expectation; SLSA-3
  provenance attestation (e.g., via `actions/attest-build-provenance`) would
  be the next-tier upgrade.

---

### I-CICD-02 (Info): `.gitignore` is comprehensive

**File:** `.gitignore`

Confirmed excluded: `*.exe`, `*.dll`, `*.so`, `*.dylib`, `dist/`, `tmp/`,
`bench/latest/`, `.dfmt/`, `coverage.out`, `coverage.html`, `*.out`, `*.log`,
`logs/`, `.idea/`, `.vscode/`, `.claude/`, `/security-report/`, `/run-build-test.*`,
`/build-test.ps1`, `/run-go-mod-tidy.go`, `*.tmp`, `*.bak`, `*.backup`. Notably
NOT explicitly ignored: `.env*` files (no .env files exist in the repo today —
verified with `Glob` — but adding `.env*` to the gitignore as a future
defense-in-depth is recommended given the threat model). Verified `.dfmt/`
exists locally with `journal.jsonl` and `config.yaml` but is correctly
ignored.

**Recommendation (low priority):** Add `.env`, `.env.*`, `*.pem`, `*.key`,
`id_rsa*`, `id_ed25519*`, `*.p12`, `*.pfx` to `.gitignore` as a paranoid
default. Cost: one diff line. Benefit: prevents an unrelated `.env` from a
test harness slipping in.

---

### I-CICD-03 (Info): No accidentally committed secrets

Repo-wide regex sweep for canonical secret patterns:
- `AKIA[0-9A-Z]{16}` — only hits in `internal/redact/redact_test.go`
  (canonical AWS doc example `AKIAIOSFODNN7EXAMPLE`) and
  `internal/transport/handlers_test.go` (test fixture using fabricated
  bodies). Both are test corpora for the redactor itself, explicitly
  exempted in `.github/secret_scanning.yml`.
- `ghp_[A-Za-z0-9]{36}` — only `ghp_abc123xyz456def789...` test fixtures.
- `sk-[A-Za-z0-9]{48}` — only `sk-abcd1234efgh5678...` test fixtures.
- `BEGIN ... PRIVATE KEY` — only the pattern string itself in redact_test.go.
- `xoxb-` / `xoxp-` etc — only the regex literals in `internal/redact/redact.go`
  and the documentation list in `README.md:308`.

The `.github/secret_scanning.yml` paths-ignore (`internal/redact/*_test.go`)
is well-scoped — it does NOT exempt production code under
`internal/redact/redact.go`, so a real leak landing in the regex constants
would still be flagged.

---

### I-CICD-04 (Info): ADRs reviewed for accepted-risk language

Read all 9 ADRs (0000-0008 + INDEX). Searched for "we accept this risk",
"known issue", "trade-off", "for now", "deferred". Findings:

- **ADR-0006 (Sandbox Scope):** Explicitly calls out "Security surface. A tool
  that runs arbitrary code is a tool that must be careful... A bug here is not
  'returns wrong answer,' it's 'runs wrong command.'" (lines 87-88). This is
  honest design documentation, not a quietly-accepted vulnerability — it
  motivates the security policy in §7.5.4 of the spec.
- **ADR-0006 line 105:** "A security incident in the sandbox necessitates
  rearchitecting the isolation model. Mitigation: optional container-based
  sandbox as a future extension, with the current subprocess sandbox becoming
  the 'trusted' tier." — deliberate scope limit, not a bug.
- **ADR-0001:** `flock` on `.dfmt/lock` for daemon singleton; the doc notes
  this is "mostly a belt-and-suspenders measure" because the daemon is the
  only writer — fine, not a finding.
- **ADR-0003:** "BM25 has well-known gotchas (dividing by zero on empty
  documents, handling the avgdl update race). We must test thoroughly." —
  not a security risk, just a quality note.
- **ADR-0008 line 84:** "No HTML5 error recovery. Severely malformed HTML may
  parse differently than a browser parses it. Acceptable for our use case."
  — acceptable scope limit, no security implication for the agent (markdown
  output is what gets indexed; mismatched HTML5 parsing doesn't enable XSS
  because the consumer is not a browser).

**No ADR documents an accepted security weakness that should be elevated to
a finding.**

---

## Methodology notes

- Tools used: native `Read`, `Grep`, `Glob` (per the audit prompt's "Use native
  Read" instruction). DFMT MCP tools were preferred per CLAUDE.md but the audit
  rubric explicitly required native Read.
- Searched: every `.github/**/*.yml`, every `docs/adr/*.md`, every
  `internal/cli/hooks/*`, `Makefile`, `dev.ps1`, `scripts/*`, `.gitignore`,
  `.golangci.yml`, `.github/secret_scanning.yml`.
- Confirmed absence: no IaC (`*.tf`), no Dockerfile, no `docker-compose*`,
  no `k8s/`, no `helm/`. assets/ contains only `banner.png` as expected.
