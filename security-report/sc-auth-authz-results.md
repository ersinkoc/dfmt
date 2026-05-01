# sc-auth / sc-authz — Authentication & Authorization Hunt

## Authentication

**Status:** explicit non-feature.

The dfmt daemon assumes a single-user local trust model. Authentication
is not implemented and the codebase contains explicit machinery to
prevent the daemon from accidentally being exposed to untrusted callers:

- **Non-loopback HTTP bind is hard-refused** at startup (F-09 closure).
  An operator who configures `transport.http.bind = "0.0.0.0:8765"` gets
  an error and the daemon refuses to start. The error message says:

  > `non-loopback HTTP bind refused: listener bound to 0.0.0.0 — bearer-token auth not implemented (F-09)`

- The Unix socket is bound under a `0o077` umask and chmodded to `0o700`
  on Unix (`internal/transport/socket_umask_unix.go`,
  `internal/transport/socket.go`).

- The port file `.dfmt/port` is written atomically with mode `0o600` so
  other local users can't enumerate the daemon's port.

- The dead `authToken` plumbing was removed under F-22; current code
  has no auth-token checks at all.

## Authorization

**Status:** Sandbox `Policy` is the only authorization boundary.

The `Policy.Evaluate` model:

1. **Deny rules run first.** Any matching deny rule rejects.
2. If `len(p.Allow) > 0`, only operations matching an allow rule are
   permitted.
3. If `Allow` is empty, all operations are permitted (except those
   denied) — only meaningful when the policy is fully overridden.

The default policy ships with both allow and deny lists populated. The
deny list is **always checked**, even if the operator has expanded
`Allow`.

### Glob → regex compilation

`globToRegex` and `globToRegexShell` translate the glob syntax. Both
use `regexp.QuoteMeta` for literal characters, so a user-authored rule
like `read api.example.com/*` won't have its `.` interpreted as
"any char" (closed under an unnamed prior issue with broadening
matches). Compiled regexes are cached in a 512-entry LRU
(`regexLRUCache`) to bound memory under policy reloads.

## Findings

No auth/authz vulnerabilities identified. The non-feature is
intentional and well-fenced.

### A-LOW-1 — Default `read` allow `**` is broad
**Severity:** Low (by design)
**File:** `internal/sandbox/permissions.go::DefaultPolicy()`
**Description:** Default policy allows `read **` and `write **`. A
malicious agent can read or write any file inside the working directory
(plus, via the symlink-resolve-and-recheck path, refuse symlinks
escaping wd). This is **the intended product behavior** — dfmt is a
tool for AI agents to navigate code — but it means the deny rules
(`.env*`, `secrets/`, `id_rsa`, `.dfmt/**`, `.git/**`) are the only
gate on sensitive files.
**Impact:** None over the design intent. The deny rules cover the
common cases.
**Recommendation:** Document that operators dealing with non-standard
secret stores (`creds/`, `private_keys/`, custom CA bundles) should add
project-level `permissions.yaml` rules. The README and `redact.yaml`
docs should call this out.

## Confidence

High. The code is explicit about the trust model and refuses the
non-loopback bind that would push it outside that model.
