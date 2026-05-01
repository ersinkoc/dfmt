# sc-secrets - Hardcoded Secrets Scan Results

**Date:** 2026-05-01
**Scope:** DFMT codebase at `D:\Codebox\PROJECTS\DFMT`
**Skill:** sc-secrets (hardcoded secrets detection)

---

## Summary

No live, production-grade secrets were found in this repository. The codebase is clean of hardcoded API keys, private keys, database passwords, or credentialed connection strings that could expose real services.

All matches are **test fixtures with clearly fake values** (placeholders, redaction pattern targets, or intentionally-leaked test secrets). These are not security findings.

---

## Method

Scanned for:
- Known API key patterns (AWS `AKIA...`, GitHub tokens `ghp_...`, Slack `xoxb-...`, Google `AIza...`, Stripe `sk_live_...`, Twilio `SK...`, SendGrid `SG....`)
- Private key PEM headers (`-----BEGIN RSA PRIVATE KEY-----`, etc.)
- Generic secret/password assignments (`password=`, `secret=`, `token=`, `api_key=`, `auth.*=`)
- Connection strings with embedded credentials (`mongodb://...:...@`, etc.)
- Environment variable reads that may indicate runtime secret loading
- JWTsecret / session token patterns
- Secret file patterns across the entire repo (`*.pem`, `*.key`, `*.crt`, `*.p12`, `*.pfx`, `.env*`, `*credentials*`, `*secret*`, `*token*`, `*.ak`, `*.sk`)

---

## Key Findings

### Files Scanned (by category)

| Category | Files |
|----------|-------|
| Go source | `internal/**/*.go`, `cmd/**/*.go` |
| Config | `.dfmt/config.yaml` |
| Not present | `.dfmt/permissions.yaml` (file does not exist) |
| Secret patterns | Glob for `*.pem`, `*.key`, `*.crt`, `*.p12`, `*.pfx`, `.env*`, `*credentials*`, `*secret*`, `*token*`, `*.ak`, `*.sk` — **zero matches** |

### Findings

| # | File | Line | Pattern | Type | Assessment |
|---|------|------|---------|------|------------|
| 1 | `internal/redact/redact_test.go` | 17-20, 235, 252, etc. | `[AWS_KEY]`, `[GITHUB_TOKEN]`, `[STRIPE_KEY]`, `[GOOGLE_API_KEY]`, `[SLACK_TOKEN]`, `[TWILIO_KEY]`, `[SENDGRID_KEY]`, `[MAILGUN_KEY]` | Redaction pattern test fixtures | **FALSE POSITIVE** — placeholder tokens used to test the redaction engine. Intentionally fake values matching the redactor's own pattern names. Exempted in `.github/secret_scanning.yml`. |
| 2 | `internal/redact/redact_fuzz_test.go` | 21-22 | `[AWS_KEY]`, `[GITHUB_TOKEN]` | Fuzz test inputs | **FALSE POSITIVE** — redactor fuzz harness inputs; same placeholder values. |
| 3 | `internal/sandbox/sandbox_test.go` | 1860 | `const secret = "API_KEY=sk-DO-NOT-LEAK"` | Test fixture | **FALSE POSITIVE** — intentionally leaky test value used to verify that exec output containing secrets is properly handled. Value is `sk-DO-NOT-LEAK` which is obviously not a real key. |
| 4 | `internal/transport/handlers_sandbox_test.go` | 821 | `const secret = "API_KEY: [REDACTED]"` | Test verification | **FALSE POSITIVE** — test verifying that the handler correctly redacts API_KEY values before logging. `[REDACTED]` is the expected output of the redaction, not a real secret. |
| 5 | `internal/transport/handlers_test.go` | 60 | `payload: "export API_TOKEN=[GITHUB_TOKEN]"` | Test payload | **FALSE POSITIVE** — test payload using the `[GITHUB_TOKEN]` placeholder to verify env_export redaction path. |
| 6 | `internal/redact/redact_test.go` | 505-643 | Various fake secret strings (`AbCdEfGhIjKlMnOpQrSt`, `XYZ_123-456`, etc.) | Redactor test cases | **FALSE POSITIVE** — test cases for `IsSensitiveKey` and secret detection logic. Values are fake/generated for testing the detection threshold. |
| 7 | `internal/redact/redact_test.go` | 611, 623 | `postgres://[REDACTED]:[REDACTED]@db.internal/prod`, `redis://[REDACTED]:[REDACTED]@localhost:6379/0` | Redaction test inputs | **FALSE POSITIVE** — already-redacted connection strings used to verify the redaction pattern catches them. |

### Environment Variable Reads (runtime-only secrets — not hardcoded)

The following are **NOT hardcoded secrets**. They are runtime environment variable reads and are the correct pattern for secret injection:

| File | Line | Read | Purpose |
|------|------|------|---------|
| `internal/cli/dispatch.go` | 162 | `os.Getenv("DFMT_PROJECT")` | Project override |
| `internal/cli/dispatch.go` | 1400 | `os.Getenv("LOCALAPPDATA")` | Platform path |
| `internal/client/client.go` | 50 | `os.Getenv("DFMT_SESSION")` | Session token — runtime read, not hardcoded |
| `internal/client/client.go` | 150 | `os.Getenv("DFMT_DISABLE_AUTOSTART")` | Flag for test binaries |
| `internal/config/config.go` | 249 | `os.Getenv("XDG_DATA_HOME")` | Config directory |
| `internal/logging/log.go` | 69 | `os.Getenv("DFMT_LOG")` | Log level override |
| `internal/setup/setup.go` | 55, 260 | `os.Getenv("HOME")`, `os.Getenv("XDG_DATA_HOME")` | Home/path discovery |
| `internal/sandbox/permissions.go` | 2301-2318 | Multiple `os.Getenv("PATH")`, `os.Getenv("TMP")`, `os.Getenv("USERPROFILE")`, etc. | Sandbox environment passthrough |
| `internal/transport/mcp.go` | 31 | `os.Getenv("DFMT_MCP_LEGACY_CONTENT")` | Feature flag |

None of these embed actual secret values — they read from environment at runtime.

### JWT / Session Tokens

Per `security-report/sc-jwt-results.md:21`:
> "sc-jwt is **not applicable** to this codebase. DFMT uses stateless opaque bearer token authentication, not JWTs."

No JWTsecret, JWT signing key, or session token hardcoding was found.

### Private Keys / Certificates

Zero matches for:
- `-----BEGIN RSA PRIVATE KEY-----`
- `-----BEGIN EC PRIVATE KEY-----`
- `-----BEGIN OPENSSH PRIVATE KEY-----`
- `-----BEGIN PGP PRIVATE KEY BLOCK-----`
- `-----BEGIN DSA PRIVATE KEY-----`
- `-----BEGIN PRIVATE KEY-----`

Zero matches for `*.pem`, `*.key`, `*.crt`, `*.p12`, `*.pfx` files committed to the repo.

### `.dfmt/config.yaml`

No hardcoded credentials. Contains only operational configuration (capture settings, storage durability, path ignore rules). The `exec.path_prepend` section is commented-out and empty.

### `.dfmt/permissions.yaml`

**File does not exist.** This is not a finding — permissions are managed through a different mechanism.

---

## Conclusion

**No actionable secrets found.** All matches are test fixtures, redaction pattern targets, or runtime environment reads. The repository is clean of hardcoded credentials per sc-secrets methodology.

The codebase uses the correct secret-injection pattern: secrets are read from environment variables at runtime (e.g., `os.Getenv("DFMT_SESSION")`), not embedded at compile time.

---

## Prior Scan References

This scan builds on prior results documented in:
- `security-report/sc-secrets-results.md` (prior scan — same conclusion)
- `security-report/sc-secrets-crypto-results.md` (cryptography scan — no live keys found)
- `security-report/sc-jwt-results.md` (JWT scan — not applicable, no JWTs used)
- `.github/secret_scanning.yml` (GitHub Push Protection exemption for redact test files)
