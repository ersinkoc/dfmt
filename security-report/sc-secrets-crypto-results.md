# Secrets / Crypto / Data Exposure — HUNT phase results

Scope: hardcoded credentials (CWE-798), sensitive data exposure (CWE-200),
crypto misuse (CWE-327, CWE-338), redactor correctness/bypasses.

Repository: `D:\Codebox\PROJECTS\DFMT` (branch `main`).
Method: native `Read`/`Grep`/`Glob` review of every file listed in the brief
plus the broader transport/, daemon/, client/, content/, redact/, setup/,
logging/, sandbox/ packages, and a search of the whole tree for high-entropy
credential markers (`Bearer `, `sk-`, `ghp_`, `AKIA`, `Authorization`, etc.).

---

## Summary

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High | 1 |
| Medium | 5 |
| Low | 5 |
| Informational | 4 |

No live credentials, private keys, or hardcoded API tokens are committed to
the repo. Crypto-grade randomness comes from `crypto/rand` and there is no
constant-time comparison gap (because there is currently no auth token in
the network path — see SC-INFO-1). The headline weaknesses are gaps in the
redactor's coverage that allow several recognisable secret classes to slip
through into the journal and into chunk-set storage.

### Top 3

1. **SC-HIGH-1 — `tool.write` content is journaled before pattern-redaction
   gives the agent any meaningful safety net.**
   `internal/transport/handlers.go:1123-1126` — the entire `params.Content`
   is placed under the `content` key and written to the journal. The data
   *does* run through `h.redactData` inside `logEvent`, but only the regex
   patterns in `redact.go` will catch anything; arbitrary user content (a
   `.env`, a config with custom-format secrets, paste of a Slack message,
   etc.) is otherwise persisted verbatim to `.dfmt/journal.jsonl`.
2. **SC-MED-1 — Bearer / Basic regexes use a too-narrow character class
   that lets typical base64 secrets escape after the first `+` or `/`.**
   `internal/redact/redact.go:106-107` — `[A-Za-z0-9_.-]` excludes `/`, `+`,
   `=` (base64 alphabet). A header like `Authorization: Bearer
   abcDEF/GHI+jkl=mnoPQR…` matches up to the first non-allowed char, then
   the trailing portion is journaled in the clear.
3. **SC-MED-2 — `aws_secret` only matches when the value is on the same
   line as the key marker.** `internal/redact/redact.go:61` — a real AWS
   secret in the wild often lives on the next line (YAML/JSON/INI multi-line
   formats) or under a different label (e.g. `secretKey:`), and 40-char
   base64 strings have no provider-specific prefix to fall back on.

---

## Detailed findings

### SC-HIGH-1 — Unbounded sandbox `tool.write` content reaches the journal

- Severity: High
- CWE: CWE-200 (Sensitive Information Exposure), CWE-532 (Insertion of
  Sensitive Information into Log File)
- File: `internal/transport/handlers.go:1110-1132`

The `Write` handler logs the **entire** content of every sandbox write into
the journal under `data.content`:

```go
h.logEvent(ctx, "tool.write", params.Path, map[string]any{
    "path":    params.Path,
    "content": params.Content,
})
```

`logEvent` does call `h.redactData()`, so the regex patterns in `redact.go`
*do* run. But the redactor only catches strings that match a known
high-entropy provider pattern. If an agent uses the dfmt write tool to
materialise a `.env`, a config file, a private SSH key (note: the
`private_key` regex matches PEM blocks but not raw OpenSSH `-----BEGIN
OPENSSH PRIVATE KEY-----` content if the agent strips the headers, and not
.ppk format at all), or any organisation-specific secret format, the full
bytes land in `.dfmt/journal.jsonl` and the index. The journal has 0o600
perms (good), but it is replicated into recall snapshots, chunk-set stores,
and is the artefact `dfmt bundle` would dump (per CLAUDE.md — see
SC-INFO-2).

By contrast, `Read` and `Fetch` log only the byte count and intent — not
the raw content. `Edit` logs only `path`. `Write` is the outlier.

Recommendation: drop `content` from the `tool.write` log event entirely,
or replace it with a hash + length. The intent string is already captured
as the summary tag; the path is sufficient for retrospective debugging.
If full content recall is wanted, pipe it through the same content store
as `Exec`/`Read` (chunk-set with redaction) instead of the journal.

---

### SC-MED-1 — Bearer / Basic auth regex misses base64 `+`, `/`, `=`

- Severity: Medium
- CWE: CWE-327 (Use of an Inadequate Algorithm — incomplete pattern)
- File: `internal/redact/redact.go:106-107`

```go
{name: "bearer_token", regex: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_.-]{20,}`), repl: "Bearer [REDACTED]"},
{name: "basic_auth",   regex: regexp.MustCompile(`(?i)basic\s+[A-Za-z0-9_.-]{10,}={0,2}`), repl: "Basic [REDACTED]"},
```

Bearer tokens in the wild are routinely raw base64 or base64url. The class
`[A-Za-z0-9_.-]` is missing `/`, `+`, `=`. So:

- `Bearer abc/DEF+ghiJKLmnoPQR…` matches up through `abc` and re-engages at
  `DEF` only after the special char; whatever runs to the next non-allowed
  char remains exposed. A constructed POC: `Bearer abcdefghij1234567890+secrets`
  — the `+secrets` tail survives.
- `Basic dXNlcjp+QGFkbWlu` (a base64 with `+`) is partially matched.

Real-world impact is higher for Basic auth: base64 of `user:pass` very
commonly contains `+` or `/`. JWT case is fine because the JWT regex has
its own broader char class.

Recommendation: extend the char class to `[A-Za-z0-9_./+=-]` or
`[A-Za-z0-9._~/+=-]` for both patterns. Add a regression test that uses a
header with `+` and `/` in the credential.

---

### SC-MED-2 — `aws_secret` requires the literal key name on the same line

- Severity: Medium
- CWE: CWE-200
- File: `internal/redact/redact.go:61`

```go
{name: "aws_secret", regex: regexp.MustCompile(`(?i)(aws[_-]?secret[_-]?access[_-]?key|aws[_-]?secret)\s*[:=]\s*['"]?([A-Za-z0-9/+=]{40})['"]?`), repl: "$1: [AWS_SECRET]"},
```

This is a deliberate tradeoff to avoid false-positive 40-char strings, but
in practice AWS secrets show up:

- in YAML across two lines (`aws:\n  secret_access_key: AbCd…40chars`) —
  the `:=` constraint fails because of the newline,
- in `~/.aws/credentials` ini-style: `aws_secret_access_key = AbCd…` —
  matches OK,
- in a tabular dump: `Access Key | Secret`. No marker, no match.

The current behaviour means a `dfmt exec aws sts get-session-token` whose
stdout includes the raw secret will likely make it into the journal under
the credentials block.

Recommendation: add an alternative pattern that triggers only when both an
AWS access key ID (already captured by `aws_key`) and a 40-char base64
string appear within ~5 lines of one another — the joint occurrence is the
real signal.

---

### SC-MED-3 — Redactor walks `[]string` and `[]any` but not `[]byte`, slices of structs, or arbitrary integer/float secrets

- Severity: Medium
- CWE: CWE-200
- File: `internal/redact/redact.go:212-239`

`redactValue` only recurses through `string`, `map[string]any`, `[]string`,
`[]any`, and `[]map[string]any`. Anything else passes through unchanged:

- `[]byte` containing UTF-8 of a token (JSON unmarshalling never produces
  `[]byte` so this is mostly an internal-API risk; the `Content` field of
  Write above is already a string so doesn't hit this case).
- Concrete struct slices defined inside transport types (we reviewed
  `core.Event` — its `Data` is `map[string]any` so safe).
- Numeric secrets — e.g. a 19-digit credit card stored as `int64` would not
  be redacted. (Lower priority since dfmt isn't a payments tool.)

The bigger concrete issue: when an MCP tool's argument contains a
slice-of-strings nested inside a `[]any` (e.g. `"args": ["--token","abc"]`),
the redactor handles it. But if a future caller switches to a typed array
of structs, redaction silently degrades.

Recommendation: either reflect over arbitrary slice/array kinds via
`reflect.Value.Kind()`, or add a comment + linter rule preventing typed
slices of strings from crossing the redactor boundary.

---

### SC-MED-4 — `IsSensitiveKey` substring match flags innocent variables and misses real secrets

- Severity: Medium (false-positive AND false-negative split)
- CWE: CWE-1284 (Improper Validation of Specified Quantity)
- File: `internal/redact/redact.go:256-269`

```go
sensitive := []string{
    "password", "passwd", "secret", "token", "api_key", "apikey",
    "access_token", "refresh_token", "auth", "credential", "private",
    "key", "session", "jwt", "bearer",
}
for _, s := range sensitive {
    if strings.Contains(lower, s) { … }
}
```

`strings.Contains(lower, "key")` triggers on `MONKEY_PATCH=...`,
`KEYBOARD_LAYOUT=...`, `PRIMARY_KEY=...`, `KEYWORDS=...`,
`API_KEY_FORMAT_VERSION=2` — wiping useful debug values.

Conversely, `cred` is missing as a stem — `MY_CRED=...` would be caught (via
`credential`-substring `cred`?  — actually no, `credential` contains `cred`
but the lookup is the other way: `strings.Contains(lower, "credential")`,
so `mycred=...` is NOT caught.) Likewise `sig`, `nonce`, `mac`, `seed`,
`pat`, `pass` (already inside `password`) are missing.

Recommendation: switch from substring-match-on-needles to whole-token match
against a curated list, OR keep the substring approach but precompile a
regex with word boundaries (`\b(password|secret|...)\b`). Add `cred`,
`pat`, `pass`, `pwd`, `sig`, `nonce`, `mac`, `seed` as additional stems.

---

### SC-MED-5 — Redactor does not handle multi-line PEM continuations after CRLF normalisation

- Severity: Medium
- CWE: CWE-20 (Improper Input Validation)
- File: `internal/redact/redact.go:118`

The `private_key` pattern uses `(?s)` so `.` spans newlines. Good. BUT the
non-greedy `.*?` between header and footer means that on Windows-style CRLF
journals, the regex still matches because `.` includes `\r` under `(?s)`.
No real bug here — but the pattern explicitly only covers RSA/EC/DSA/OPENSSH.
It misses:

- `-----BEGIN PGP PRIVATE KEY BLOCK-----` (GnuPG export)
- `-----BEGIN ENCRYPTED PRIVATE KEY-----` (PKCS#8 password-protected)
- `-----BEGIN PRIVATE KEY-----` IS covered (the `(?:...)?` group makes the
  type optional). Verified.

Recommendation: add `(?:RSA |EC |DSA |OPENSSH |ENCRYPTED |PGP )?` — or use
`(?:[A-Z]+ )?` to be future-proof.

---

### SC-LOW-1 — ULID fallback path uses a deterministic mix of pid + counter + nanotime instead of failing closed

- Severity: Low
- CWE: CWE-338 (Use of Cryptographically Weak PRNG)
- File: `internal/core/ulid.go:46-56`

```go
if _, err := rand.Read(lastRandom[:]); err != nil {
    fmt.Fprintf(os.Stderr, "warning: crypto/rand.Read failed for ULID: %v (using fallback seed)\n", err)
    ulidFallbackCtr++
    mix := uint64(os.Getpid())<<32 | ulidFallbackCtr
    binary.BigEndian.PutUint64(lastRandom[:8], mix^uint64(ts.UnixNano()))
    binary.BigEndian.PutUint16(lastRandom[8:], uint16(ulidFallbackCtr))
}
```

The author flagged this in the comment — fallback IDs are unique-per-process
but predictable. ULIDs in dfmt aren't used as security tokens (they're
event IDs and chunk-set IDs; the chunk-set ID *is* used in the URL path of
content fetches, but the content store enforces 0o700 dir + 0o600 file
modes and only validates ID shape). `crypto/rand.Read` on Linux/Windows
basically never fails outside out-of-entropy edge cases on early boot, so
the fallback is unlikely to trigger in production. Still: a predictable
chunk-set ID + a future bug that loosens content-store permissions would
become an exploit chain.

Recommendation: in the fallback, refuse to mint and propagate the error
(or panic) for any ID used as a capability handle. ULIDs that are pure
event PKs can keep the fallback.

---

### SC-LOW-2 — Logging package writes log files with mode 0644

- Severity: Low
- CWE: CWE-732 (Incorrect Permission Assignment)
- File: `internal/logging/logging.go:54-58`

```go
if err := os.MkdirAll(dir, 0755); err != nil { … }
f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
```

On a multi-user host, anything dfmt logs (which today is just stderr-style
warnings, but the logger is a `slog.Logger` ready to receive structured
events) is world-readable. Not currently a leak vector because the logger
isn't wired into the redactor pipeline — see SC-INFO-3 — but if it is, the
defaults will leak.

Recommendation: 0700 / 0600 to match the rest of `.dfmt/`.

---

### SC-LOW-3 — `IsSensitiveKey` is case-folded but env_export pattern emits the value as-is when the var name doesn't match

- Severity: Low
- CWE: CWE-200
- File: `internal/redact/redact.go:156-168`

`redactEnvExport` correctly leaves `HOME=/tmp` and `DEBUG=true` untouched.
But it does **not** then run the per-pattern redactor on the unredacted
line. Example: `MOTD="Welcome, your sk-ant-api03-… key was rotated"`.
Because `MOTD` isn't sensitive, the line falls through. The OUTER loop in
`Redact()` already iterates patterns AFTER env_export, so the inline
`sk-ant-…` does still get caught — verified by reading `Redact()` at
line 142-151. So no actual leak; the concern is a future refactor that
short-circuits after env_export.

Recommendation: add a comment to `Redact()` calling out that env_export
runs *before* the provider patterns deliberately so subsequent passes can
still scrub inline secrets in non-sensitive assignments.

---

### SC-LOW-4 — `dfmt status --json` and `dfmt list --json` echo socket / project paths to stdout, unredacted

- Severity: Low
- CWE: CWE-209 (Information Exposure Through Error Message)
- File: `internal/cli/dispatch.go:641-647`, `:898-904`

Project paths regularly leak sensitive context (`/home/u/work/acme-corp/customer-secrets-2025/...`).
That path is shown to whoever invokes `dfmt status`, and goes into the JSON
output that `dfmt list` produces. Not a network-exposed issue, but worth
calling out: `dfmt bundle` (advertised in CLAUDE.md) would presumably
include this verbatim — see SC-INFO-2.

Recommendation: optionally redact the path to `<project>/.../<basename>` in
`--json` output, gated by an env var or flag. Or do nothing — this is the
least serious of the lot.

---

### SC-LOW-5 — JWT regex is permissive on the signature segment, including dots

- Severity: Low
- CWE: CWE-697 (Incorrect Comparison)
- File: `internal/redact/redact.go:121`

```go
{name: "jwt", regex: regexp.MustCompile(`eyJ[A-Za-z0-9_=-]+\.eyJ[A-Za-z0-9_=-]+\.[A-Za-z0-9_/+=.-]*`), repl: "[JWT]"},
```

Header and payload classes are `[A-Za-z0-9_=-]` — base64url, OK. Signature
class is `[A-Za-z0-9_/+=.-]`, which **includes `.`** so the engine will
greedily eat the next sentence:

> the token `eyJabc.eyJdef.SIG` was leaked

becomes redacted up to `eyJabc.eyJdef.SIG` plus any trailing word with
allowed chars. Mostly harmless (over-redaction beats under-redaction),
but the `/` and `+` in the signature class are wrong for base64url JWTs.
A non-JWT base64 string that happens to start `eyJ.eyJ.` with dots in it
could be over-matched.

Recommendation: tighten to `[A-Za-z0-9_=-]*` for the signature segment
(matching the spec; pure base64url, no `/` `+` `.`).

---

## Informational

### SC-INFO-1 — TCP/loopback "auth token" path is dead code

`internal/transport/http.go:78-86`, `internal/client/client.go:30,747-748`.
The `authToken` field in HTTPServer is documented as currently always
empty, and the client only sets `Authorization: Bearer …` when reading a
non-empty token from the port file. The wire format is reserved for opt-in
auth re-enablement. Therefore the absence of `crypto/subtle.ConstantTimeCompare`
is **not currently a vulnerability** — there is nothing to compare. If
the auth path is reactivated, the token comparison MUST use
`subtle.ConstantTimeCompare` to avoid timing leaks, AND the token MUST come
from `crypto/rand.Read` (24+ bytes, base64url encoded). Add a unit test that
fails if a non-empty token is written without the constant-time check.

### SC-INFO-2 — `dfmt bundle` is advertised in CLAUDE.md but is not implemented

Searched `internal/cli/dispatch.go` and the entire `internal/`: no `bundle`
case in `Dispatch`, no `runBundle` function, no diagnostic-bundle assembler.
CLAUDE.md line ~85 ("`dfmt bundle` for bug reports") is a documentation
phantom. Not a vulnerability today — but if the implementation lands, it
must (a) NOT include `journal.jsonl` raw, only redacted excerpts, (b) NOT
include the port file or any `.dfmt/lock` content, (c) NOT touch
`~/.claude.json` or `~/.aws/`, (d) be written 0o600. File this as a
documentation bug or build the command properly.

### SC-INFO-3 — `internal/logging/logging.go` is plumbed but unused

Grep finds zero callers of `logging.Init`, `logging.Logger`, `logging.With`,
or `slog.Default`-wrapped helpers from the dfmt code paths. Daemon and
handlers all use `fmt.Fprintf(os.Stderr, …)` directly. Net effect: the
0644 mode flagged in SC-LOW-2 has no real-world exposure today, and any
configured log path is silently a no-op. Either wire it up (running
operator output through a redact-aware writer) or delete the package.

### SC-INFO-4 — Process / port-file / .dfmt mode hygiene is good

- `.dfmt/` dir: 0o700 — `internal/cli/dispatch.go:212`, `internal/daemon/daemon.go:86-88`.
- `journal.jsonl` and rotated segments: 0o600 — `internal/core/journal.go:103,404`.
- Content-store dir: 0o700 — `internal/content/store.go:103`.
- Persisted chunk-set files: 0o600 — `internal/content/store.go:316`.
- Port file: atomic write + 0o600 — `internal/transport/http.go:496`.
- Unix socket: chmod 0o700 + restrictive umask — `internal/transport/socket.go:53,63`.
- PID file: 0o600 — `internal/daemon/daemon.go:248`, `internal/cli/dispatch.go:764`.
- `.claude/settings.json` writes: tmp + rename + chmod 0o600 — `internal/cli/dispatch.go:323-345`.
- `~/.claude.json` patch: tmp + rename + chmod 0o600 — `internal/setup/claude.go:176-198`.
- ULID minting: `crypto/rand` first, predictable fallback only on rand failure — `internal/core/ulid.go`.

---

## What was checked and not flagged

- Hardcoded credentials anywhere in the tree: clean. Only matches are in
  `internal/redact/redact_test.go` and `internal/transport/handlers_test.go`,
  both of which are explicitly exempted in `.github/secret_scanning.yml`.
- Use of `math/rand`: zero hits in `internal/`.
- Use of `crypto/md5`, `crypto/sha1`: zero hits in `internal/`.
- Reflect-based JSON deserialisation that bypasses redact: events go
  through `RedactEvent → redactValue` which dispatches on runtime type;
  unknown types pass through unchanged but cannot reach the journal because
  every direct path (`Remember`, `logEvent`, `consumeFSWatch`) calls
  `redactData` on the map first.
- TLS / cert pinning: dfmt makes outbound HTTP via the sandbox `Fetch`
  path, which uses `net/http` defaults (system CAs, TLS 1.2+). No issues
  spotted in this slice.
- `.gitignore`/secret-scanning config: `.github/secret_scanning.yml` is
  narrow — only exempts the redactor's own test files. Verified.

---

## Pointers for the FIX phase

Priority order suggested:

1. SC-HIGH-1 — drop `content` from `tool.write` log event, or hash it.
2. SC-MED-1 — widen Bearer / Basic char classes.
3. SC-MED-2 — add cross-line AWS-secret heuristic.
4. SC-MED-4 — switch `IsSensitiveKey` to word-boundary matching.
5. SC-MED-3 / SC-MED-5 — extend redactor recursion + PEM coverage.
6. SC-LOW-2 — tighten log file perms to 0o600.
7. SC-INFO-2 — delete `dfmt bundle` from CLAUDE.md OR implement it
   (whichever is easier; documentation phantoms breed misconceptions).
8. SC-INFO-1 — when re-enabling auth, do not regress on
   `subtle.ConstantTimeCompare` + `crypto/rand`.
