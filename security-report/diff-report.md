# Security Diff Report

**Branch:** `main`
**Base (last full scan):** `2039f8b` — "fix: round 13 audit (R13-1..R13-6) — sandbox bypass, permissions, dispatch hardening"
**Head:** `893fd55` — "fix: round 14 audit phase A (R14-1, R14-2) — broaden secret redaction, SHA-pin third-party Actions"
**Date:** 2026-04-25
**Files Changed:** 4
**Files Scanned:** 4

| File | Change | Skills Activated |
|------|--------|------------------|
| `.github/workflows/ci.yml` | modified | sc-ci-cd |
| `.github/workflows/release.yml` | modified | sc-ci-cd |
| `internal/redact/redact.go` | modified | sc-lang-go, sc-secrets, sc-data-exposure |
| `internal/redact/redact_test.go` | modified | sc-lang-go (test context) |

## Summary

| Category | New | Existing | Total |
|----------|-----|----------|-------|
| Critical | 0 | 0 | 0 |
| High     | 0 | 0 | 0 |
| Medium   | 0 | 0 | 0 |
| Low      | 0 | 1 | 1 |
| Info     | 0 | 3 | 3 |

## Verdict

**PASS** — No new Critical, High, or Medium findings. The diff resolves prior finding V-5 (softprops/action-gh-release tag-rewrite risk) from the previous report and broadens secret-redaction coverage; both are net-positive security improvements.

## Findings That Were Closed by This Change

### CLOSED-1: V-5 — Tag-rewrite risk on softprops/action-gh-release
- **Source:** previous full scan, `SECURITY-REPORT.md` line 117
- **Status:** **resolved** by R14-2.
- **Evidence:** `release.yml:56` now pins `softprops/action-gh-release@3bb12739c298aeb8a4eeaf626c5b8d85266b0e65 # v2`. `actions/checkout@v4`, `actions/setup-go@v5`, and `golangci/golangci-lint-action@v7` are also SHA-pinned across both workflows.
- **Note:** SHA-pinning prevents a tag rewrite from retroactively changing what the workflow runs. The `# v2` comment preserves human readability, satisfying the trade-off called out in the previous report.

## New Findings (Introduced by This Change)

None.

## Existing Findings (Pre-existing in Touched Files)

### DIFF-001: CI workflow has no explicit top-level `permissions:` block
- **Severity:** Low (defense-in-depth)
- **Confidence:** 90/100
- **Classification:** EXISTING (not introduced by this diff, but visible in a touched file)
- **File:** `.github/workflows/ci.yml` (no `permissions:` key at workflow or job level)
- **CWE:** CWE-732 (Incorrect Permission Assignment)
- **Description:** The CI workflow inherits the repository default `GITHUB_TOKEN` scope, which on legacy repositories can be permissive (`contents: write` plus more). For a pure test-and-lint workflow that needs only `contents: read`, leaving the scope implicit is a defense-in-depth gap — a compromised step (e.g. a malicious test fixture or third-party action update before the next SHA bump) could exfiltrate or mutate repo state it doesn't need.
  - `release.yml` already does this correctly (`permissions: contents: write` at line 8).
  - `ci.yml` runs on `pull_request` from forks; GitHub forces read-only there, so the practical blast radius from forked PRs is bounded. The risk is on `push` to `main` and on internal PRs, where the inherited token applies.
- **Remediation (suggested for a future round):**
  ```yaml
  # at top of .github/workflows/ci.yml, above `jobs:`
  permissions:
    contents: read
  ```
- **Why not block this PR:** Pre-existing condition unchanged by this diff. The diff only touched the `steps:` blocks (SHA pins). Worth a one-line follow-up.

### DIFF-002 (INFO): `db_url_creds` regex does not cover `amqps://`
- **Severity:** Info
- **Confidence:** 95/100
- **File:** `internal/redact/redact.go:99`
- **Pattern:** `(postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqp)://[^:\s/]+:[^@\s]+@`
- **Description:** The TLS variant of AMQP (`amqps://`) is not in the alternation. A connection string `amqps://user:pass@broker/vhost` will not be redacted by this pattern (it would still be partially caught by `IsSensitiveKey` if assigned to a `*_URL` env var, but free-form prose like `"connecting to amqps://user:pass@broker"` slips through).
- **Suggested fix:**
  ```go
  regexp.MustCompile(`(?i)(postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqps?)://[^:\s/]+:[^@\s]+@`)
  ```
  No security regression — strictly broadens coverage. Add a positive test for `amqps://` in `TestRedactExpandedProviders`.

### DIFF-003 (INFO): `mailgun_key` and `twilio_key` may false-positive on hex-prefixed strings
- **Severity:** Info
- **Confidence:** 70/100
- **Files:** `internal/redact/redact.go:81` (`SK[a-f0-9]{32}`), `:87` (`key-[a-f0-9]{32}`)
- **Description:** Both patterns require exactly 32 lowercase hex chars after a short prefix (`SK` / `key-`). 32 lowercase hex = 128 bits = the natural length of an MD5 digest, which appears in many non-credential contexts (cache keys, ETags, content-addressable IDs). A line like `"cache hit on key-d41d8cd98f00b204e9800998ecf8427e"` (md5 of empty string) would be redacted to `"cache hit on [MAILGUN_KEY]"`.
- **Risk:** **Over-redaction is the safer failure mode** for this module — false negatives leak secrets, false positives only obscure logs. Listing for awareness, not as a defect.
- **If tightened later:** anchor with a leading boundary that excludes typical cache contexts, or add a "saw key-* in url path" anti-pattern to `TestRedactExpandedNoFalsePositives` (the existing safe case `"https://example.com/path/key-results-q3"` only excludes alphabetic suffixes, not hex).

### DIFF-004 (INFO): Pattern coverage gaps not addressed by this round
- **Severity:** Info
- **File:** `internal/redact/redact.go`
- **Description:** R14-1 broadened coverage substantially but a few high-value formats remain absent:
  - **Azure storage account keys** (88-char base64).
  - **GCP service-account JSON keys** (`-----BEGIN PRIVATE KEY-----` is caught by `private_key`, but the surrounding `client_email` / `private_key_id` JSON fields are not labelled).
  - **Mongo / Postgres URLs with credentials in the query string** (`?password=…`) rather than userinfo. The current `db_url_creds` only catches inline `user:pass@` form.
- **Recommendation:** scope-bump for a future round; not a regression.

## Verification Detail (Phase 3 lite)

For each new pattern in `redact.go` I ran the standard verifier checks:

| Check | Result |
|-------|--------|
| **Reachability** | All patterns reach prod via `Redactor.Redact()` (called by journal write path, sandbox content store, MCP RPC response shaping). Confirmed by `RedactEvent` recursion through nested map/slice values. |
| **ReDoS / catastrophic backtracking** | **Not possible.** Go's `regexp` package uses RE2 (linear-time matching, no backtracking). Even the broad `[^@\s]+` and `{40,}` quantifiers run in O(n). Verified by reading `regexp.MustCompile` usage — no `(?P<name>…)` recursion or arbitrary lookahead, both rejected by RE2 anyway. |
| **Pattern ordering / label correctness** | Anthropic before OpenAI (line 38 vs 43); confirmed by `TestRedactAnthropicBeforeOpenAI`. AWS prefix list before generic_secret. Provider-specific patterns before `env_export` so per-provider labels survive (where the value is on a non-NAME=value line). |
| **False-positive coverage** | `TestRedactExpandedNoFalsePositives` covers six adversarial inputs (URL paths containing "key-", bare prefixes without bodies, etc.). Tests pass. |
| **Concurrency** | `commonPatterns` is a package-level `[]*redactPattern` shared by all `Redactor` instances. `*regexp.Regexp` is documented concurrent-safe; sharing is correct and intentional. |
| **Test coverage** | New tests add 24 positive cases + 6 negative cases + 1 ordering case. All pass under `go test ./internal/redact/...`. |

For the workflows:

| Check | Result |
|-------|--------|
| **SHA pin format** | All four pins are full 40-char hex SHAs followed by `# v…` tag comment. Format matches the GitHub-recommended pattern. |
| **Pinned action authenticity** | Cannot verify SHAs against GitHub without web access in this scan; trusting that the developer pulled them from each action's commit history. **Recommend:** confirm each SHA appears on the official tag's commit (e.g. `gh api /repos/actions/checkout/commits/34e114876b0b11c390a56381ad16ebd13914f8d5`) the next time the network is available. |
| **`GITHUB_TOKEN` handling** | Only `release.yml` references `secrets.GITHUB_TOKEN`; passed via env to `softprops/action-gh-release` (now SHA-pinned). No other secrets used. |
| **Workflow-level concurrency** | No `concurrency:` block on either workflow. Two pushes to `main` could run two CI jobs in parallel; two tag pushes could create racing release jobs. **Reliability concern, not security.** |

## Dependency Changes

No `go.mod`, `go.sum`, `package.json`, or other manifest changes in this diff.

## Changed Files Not Scanned

None — all four files were in scope.

## PR Comment Format

```markdown
## Security Scan Results

PASS ✓

**New findings:** 0
**Existing findings in touched files:** 1 Low + 3 Info (defense-in-depth and pattern-coverage gaps; none are regressions)
**Closed findings:** V-5 (softprops/action-gh-release tag-rewrite risk) — resolved by R14-2

Suggested follow-up (not blocking):
- Add `permissions: contents: read` at the top of `.github/workflows/ci.yml`.
- Extend `db_url_creds` regex to include `amqps://`.
- Verify each pinned action SHA against its upstream tag commit before the next release.
```
