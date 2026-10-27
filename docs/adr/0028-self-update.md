# ADR-0028: User-Initiated Self-Update

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-27 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

Upgrading dfmt meant re-running `install.sh` / `install.ps1`, or building
from source. Both are easy to forget, and the cost of an old binary is not
an error: since the daemon outlives the command that started it and agents
launch `dfmt mcp` from an absolute path, a stale install keeps answering
with correct-looking, outdated behavior (the failure ADR-0022 describes).
Releases already publish one binary per platform plus a `sha256sums.txt`,
which is everything an in-place update needs.

The design constraints are the project's own: stdlib only (ADR-0004), no
network traffic dfmt initiates by itself (`privacy.telemetry: false`,
`remote_sync: none`), and no step that can leave a user without a working
binary.

## Decision

Add `dfmt update` (alias `self-update`), implemented in
`internal/selfupdate` with `net/http`, `crypto/sha256` and `os` only.

- **Only on request.** dfmt never checks for updates in the background;
  the command is the sole caller. `--check` reports without changing
  anything.
- **Source.** `GET https://api.github.com/repos/ersinkoc/dfmt/releases/latest`
  (or `/releases/tags/<tag>` with `--version`), then the
  `dfmt-<goos>-<goarch>[.exe]` asset — the names `release.yml` builds.
- **Verification.** The asset must match its line in the release's
  `sha256sums.txt`. A missing checksums file or line is a refusal, not a
  warning. The candidate is then executed once with `--version` and must
  report the requested tag, which catches a wrong-platform, truncated or
  mis-stamped binary before anything is replaced.
- **Swap.** Written to a temp file in the binary's own directory and
  renamed over it (atomic on one filesystem). On Windows the running
  binary is first renamed to `<exe>.old` — renaming a running executable
  is allowed there, overwriting it is not — and the next invocation
  deletes it. Any failure before the rename leaves the old binary
  untouched and removes the temp file.
- **Path stays.** The binary is replaced in place, so agent configs, which
  hold its absolute path, keep working. The global daemon is stopped
  afterwards so the next command starts the new one; ADR-0022's
  fingerprint check would also catch it, one command later.
- **Ordering.** Only `vX.Y.Z` tags are releases. A build from a checkout
  (`v0.8.1-3-gabc`, `dev-…`) is refused unless `--force`, because it cannot
  be ordered against a release and is usually ahead of the latest one.
  Without `--version`, an equal or older release is "up to date".

## Consequences

- One command upgrades a host; the trust root is GitHub's TLS plus the
  checksums file in the same release. That is the same trust `install.sh`
  already relies on, not a stronger one: a compromised release could
  publish a matching checksum. Signing releases would close that and is
  left for a later ADR.
- A binary installed somewhere the user cannot write (e.g. `/usr/local/bin`
  by root) cannot self-update; the error names the directory and suggests
  reinstalling. Package-manager installs should be updated by their
  package manager.
- `dfmt mcp` proxies already running under agents keep the old code until
  the agent restarts them. The wire protocol is stable across patch
  releases, so they keep working against the new daemon meanwhile.
