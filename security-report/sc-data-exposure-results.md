# sc-data-exposure — Sensitive Data Exposure Scan Results

Scope: redaction coverage in `internal/redact/`, journal/log paths, error message content, debug-mode flags, response-body shapes returned to the calling agent.

## Summary

The redactor (`internal/redact/redact.go`) is comprehensive for provider tokens (Anthropic, OpenAI, GitHub, AWS, Slack, Stripe, Discord, Twilio, SendGrid, Mailgun), DB connection strings, JWTs, PEM private-key blocks, env-export `NAME=value` lines, and Bearer/Basic auth headers. Two material gaps remain that affect data flowing into the journal (`.dfmt/journal.jsonl`) and through `dfmt_recall` into agent transcripts.

## Findings

### EXPOSE-01 — `password:` / `password=` in non-env-export shapes is not redacted
- **Severity:** Medium
- **CWE:** CWE-532 (Insertion of Sensitive Information into Log File)
- **File:** `internal/redact/redact.go:33-147` (commonPatterns)
- **Description:** `commonPatterns` has explicit redaction for `(api_key|secret_key|access_token|auth_token)\s*[:=]\s*…` and a separate `env_export` form that matches `^[NAME]=value$` lines. Plain `password` (or `passwd`, `pwd`) is **not** in the generic_secret regex, so a journaled string like `{"password": "hunter2"}` (JSON) or `password: hunter2` (YAML, indented) bypasses redaction. The `IsSensitiveKey` helper does flag `password` as sensitive, but it only fires on the env-export line shape (`^password=...$`), not on JSON/YAML/log-line `password: …` forms.
- **Impact:** A developer's local config blob, a stack trace including `req.body = {"password":"…"}`, or a redacted-output Fetch response that surfaces a `password` field will land verbatim in `journal.jsonl` and re-emerge through `dfmt_recall` — including into agent transcripts that may be persisted by the host harness. Exactly the "PII in logs" pattern (CWE-532).
- **Evidence:**
  ```go
  // internal/redact/redact.go:121
  {name: "generic_secret", regex: regexp.MustCompile(
      `(?i)(api[_-]?key|secret[_-]?key|access[_-]?token|auth[_-]?token)\s*[:=]\s*['"]?([A-Za-z0-9_/+=.-]{20,})['"]?`),
   repl: "$1: [REDACTED]"},
  // No "password" / "passwd" / "pwd" alternation in this regex.
  ```
- **Remediation:** Extend `generic_secret` to cover `password`, `passwd`, `pwd`, `pass`, plus the existing key/token forms; lower the body-length floor for these (passwords as short as 6 chars need redacting) and accept both `=` and `:` separators with optional surrounding quotes. Suggested:
  ```go
  regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|access[_-]?token|auth[_-]?token|password|passwd|pwd)\s*[:=]\s*['"]?([^'"\s,;}]{6,})['"]?`)
  ```
- **Confidence:** High — confirmed by reading the `commonPatterns` slice end-to-end and the `sensitiveTokens`/`IsSensitiveKey` use-sites; only env-export lines reach `IsSensitiveKey`.

### EXPOSE-02 — Bare 40-char AWS secret without marker is not redacted (documented residual)
- **Severity:** Low
- **CWE:** CWE-200 (Information Disclosure)
- **File:** `internal/redact/redact.go:60-77` (aws_secret pattern)
- **Description:** The `aws_secret` rule requires a `secret(_access)?_key` marker within 80 characters of a 40-char base64 token. A standalone 40-char base64 string — paste of just the credential, no label — is not redacted. The package doc-comment at lines 167-179 explicitly accepts this trade-off because of the false-positive cost on legitimate base64 hashes.
- **Impact:** Already documented as residual; only relevant if a developer pastes a bare AWS secret into a sandboxed exec/read body without any surrounding `aws_secret_access_key` text.
- **Evidence:** Comment at `internal/redact/redact.go:170-179`.
- **Remediation:** No code change. Consider adding the gap to the `redact.yaml` operator-extension docs so security-conscious teams add a stricter project-level pattern.
- **Confidence:** High (acknowledged in source).

### EXPOSE-03 — `Project` field on event leaves absolute filesystem paths unredacted
- **Severity:** Low
- **CWE:** CWE-200 (Information Disclosure)
- **File:** `internal/transport/handlers.go:551`, `internal/core/event.go:88`
- **Description:** Every event written to the journal carries `Project: h.getProject()` — an absolute path like `D:\Codebox\PROJECTS\DFMT` or `/Users/alice/work/customer-acme`. `RedactEvent` walks `Data` but does not touch `Project`. When `dfmt_recall` builds a snapshot the project path is in every event — leaking the developer's home directory layout and any customer-name in path back to the agent (and possibly to a remote LLM transcript).
- **Impact:** Privacy leak in shared transcripts; minor compared to credential leakage but visible on every recall.
- **Evidence:**
  ```go
  // internal/transport/handlers.go:548-551
  e := core.Event{
      …
      Project:  h.getProject(),
      …
  }
  ```
- **Remediation:** Either redact `Project` at journal-write time when it contains user-home segments, or surface a config knob `transport.recall.scrub_project_paths` that replaces the prefix with `~`/`<project>` in recall output only.
- **Confidence:** Medium — depends on threat model; some operators may want full paths for debugging.

### EXPOSE-04 — fswatch event path is redacted but raw `os.Stat`/`os.Open` errors in stderr can include unredacted paths
- **Severity:** Low
- **CWE:** CWE-209 (Information Exposure Through an Error Message)
- **File:** `internal/daemon/daemon.go:546`, `internal/transport/handlers.go:453`
- **Description:** Sandbox handler errors reach stderr verbatim: `fmt.Fprintf(os.Stderr, "logEvent: journal append: %v\n", err)` and `fmt.Fprintf(os.Stderr, "fswatch journal append: %v\n", err)`. The error chain may include an absolute path that was not redacted (the redactor runs over event Data, not over the daemon's own error strings). Local-only sink, so impact is bounded to whoever can read the daemon's stderr (typically the same user), but in CI the daemon's stderr can be captured into pipeline logs.
- **Impact:** Path disclosure to whoever captures the daemon's stderr.
- **Evidence:** stderr fprintf with `%v` of an unredacted error in `internal/daemon/daemon.go:546`, `internal/transport/handlers.go:258, 270, 453`.
- **Remediation:** Route these through `logging.Warnf` (which is the project's centralised sink) and consider an optional pass through the redactor for the formatted message. At minimum, scrub absolute home/project prefixes before log emission.
- **Confidence:** Medium.
