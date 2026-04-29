# Security policy

DFMT is a local-only Go daemon that mediates between AI coding
agents and a developer workstation. It executes shell commands,
reads and writes files, fetches HTTP resources, and persists an
audit trail — all under a permission-gated sandbox. A
vulnerability in DFMT can therefore translate into arbitrary code
execution, exfiltration of secrets, or destruction of local state
on the host that runs it. We take that seriously.

## Supported versions

| Version | Status                       |
|---------|------------------------------|
| 0.2.x   | **Supported** (current line) |
| 0.1.x   | Pre-release; not supported   |

Security fixes are issued on the latest 0.2.x patch release.
Upgrading to the latest patch is the supported remediation for
any reported issue.

## Reporting a vulnerability

**Please do not file a public GitHub issue for security bugs.**
The two recommended channels:

1. **GitHub Security Advisories (preferred).** Open a private
   advisory at
   `https://github.com/ersinkoc/dfmt/security/advisories/new`.
   This route is encrypted, lets you propose a draft fix, and
   feeds straight into the CVE issuance flow if a CVE is
   warranted.

2. **Email backup.** If you cannot use the GHSA flow, email
   `ersinkoc@gmail.com` with a subject line beginning
   `[DFMT-SECURITY]`. PGP is not currently published; if you
   need encrypted transport, ask in a non-sensitive first
   message and we will arrange a key exchange.

When you report, include — where applicable:

- The DFMT version (`dfmt --version`) and the host OS / arch.
- A minimal reproduction (a command sequence, an MCP tool call,
  or a malformed input file).
- The expected vs. observed behavior, and the impact you have
  in mind (RCE, secret exfiltration, denial of service, audit
  trail tampering, …).
- Whether you would like credit in the advisory.

## Disclosure timeline

DFMT follows **90-day coordinated disclosure**:

- **Day 0** — report received, acknowledged within 72 hours.
- **Day 7** — initial triage shared back: severity assessment,
  whether a fix is feasible, expected patch window.
- **Day ≤ 90** — fix released as a 0.2.x patch + advisory
  published. If a CVE is appropriate we file it; you are
  credited unless you ask otherwise.

If a fix is not viable within 90 days (e.g. the bug requires an
upstream change in Go itself or an architectural rework), we
will discuss an extension with the reporter rather than make a
unilateral call. Conversely, if a vulnerability is being
actively exploited in the wild, we will **shorten** the timeline
and disclose as soon as a fix is shippable.

If a reporter does not need attribution and is not seeking a
CVE, we accept a **30-day grace** flow: fix in current,
advisory at next minor.

## Threat model

The full architecture overview lives in
[`docs/ARCHITECTURE.md` § 16](docs/ARCHITECTURE.md#16-security-posture).
Summary of what is in scope vs. out of scope:

**In scope:**

- The daemon process and its public RPC surface (MCP over stdio,
  HTTP loopback, Unix-domain socket, TCP loopback).
- Sandbox permission enforcement (`internal/sandbox/permissions.go`)
  including SSRF defense, recursive-`dfmt` deny, env-block-list,
  and the read/write/edit deny rules for secrets and `.dfmt/` /
  `.git/`.
- The journal and content store on disk
  (`.dfmt/journal.jsonl`, `.dfmt/content/<id>.json.gz`),
  including the `safefs` symlink-safe write helper.
- Setup-time changes to user files
  (`~/.claude/mcp.json`, `~/.claude.json` patches, project
  `.claude/settings.json` merges, agent instruction-block
  injection in `CLAUDE.md` / `AGENTS.md` / `.cursorrules` /
  …).
- The auto-update / installer scripts (`install.sh`,
  `install.ps1`).
- The redactor's pattern set
  (`internal/redact/redact.go`); a missed leak class is a
  reportable issue.

**Out of scope:**

- Vulnerabilities in the Go standard library, `golang.org/x/sys`,
  or `gopkg.in/yaml.v3`. Report those upstream; we will pull a
  patched toolchain or dependency once available.
- Bugs in third-party AI agents (Claude Code, Cursor, Codex,
  …). DFMT relies on the agent honoring the routing block we
  inject; an agent that ignores its `CLAUDE.md` / `AGENTS.md`
  routing rules sidesteps DFMT entirely. That is a property
  of the agent, not a DFMT vulnerability.
- Multi-tenant / shared-host scenarios beyond what
  `safefs` + Unix socket mode 0600 + `umask 0o077` already
  defend. DFMT does not target shared infrastructure.
- Attacks requiring local user privilege escalation that DFMT
  does not itself enable. If you can already write to
  `$HOME/.claude/` or kill the user's daemon process, you
  could already do worse than DFMT can amplify.
- Physical access. Disk encryption is the user's
  responsibility.
- Vulnerabilities in models or prompt-injection content the
  user passes through DFMT — DFMT is a transport for those, not
  a defense against them. The redactor mitigates well-known
  secret shapes on a best-effort basis only (see
  ARCHITECTURE.md § 16 for the regex set and the
  best-effort caveat).

## Past audits

The internal validation pass that closed F-04 / F-05 / F-07 /
F-08 / F-11 / F-19 / F-21 / F-25 / F-29, plus F-G-LOW-1 /
F-G-LOW-2 / F-G-INFO-2 / F-R-LOW-1 / F-A-LOW-1, is documented in
[`security-report/SECURITY-REPORT.md`](security-report/SECURITY-REPORT.md).
That report is the historical record for those findings; it is
not maintained as a live tracker. Open issues tracked there are
either closed (the common case) or moved into ROADMAP.md when
they need a forward-looking owner.

## Hardening posture

DFMT ships these defensive measures by default — a regression
in any of them is a reportable issue:

- Symlink-safe atomic writes
  (`internal/safefs.WriteFileAtomic`) for every project-managed
  write path.
- `flock(2)` (Unix) / `LockFileEx` (Windows) per-project
  exclusivity — only one daemon writes a given `journal.jsonl`.
- Listener loopback enforcement; non-loopback HTTP binds fail
  fast.
- `Host` and `Origin` validation on every non-health-probe HTTP
  request (DNS-rebinding defense, F-17 closure).
- Curated subprocess environment (`HOME` / `USER` / `PATH` /
  `LANG` / `TERM`); loader and interpreter overrides
  (`LD_*`, `DYLD_*`, `NODE_*`, `PYTHON*`, `RUBY*`, `BUNDLE_*`,
  `GEM_*`, `JAVA_*`, …) are blocked from agent-supplied env.
- Curated SSRF deny set: `169.254.169.254`,
  `metadata.google.internal`, `metadata.goog`, plus a custom
  `DialContext` that classifies each candidate IP at connect
  time so DNS rebinding cannot smuggle a request past the
  policy.
- Per-resource concurrency caps (`exec`/`fetch`/`read`/`write`
  semaphores) bound the daemon's spawn rate even under a
  prompt-injected agent.
- Server-side priority floor and source override (`Source = mcp`,
  agent's `priority` claim coerced to ≤ P3) so a prompt-injected
  agent cannot promote noise to P1 or forge `source: githook`.
- Journal write events log `sha256 + size`, never the raw body
  — keeps the audit trail from doubling as a leaked-secret
  store.

When in doubt: file a private GHSA, we will figure out the rest
together.
