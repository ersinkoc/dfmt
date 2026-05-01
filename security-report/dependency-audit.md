# Dependency Audit — DFMT

**Scan date:** 2026-04-29
**Source of truth:** `go.mod`, `go.sum`

## Direct runtime dependencies

| Module | Version | Risk |
|---|---|---|
| `golang.org/x/sys` | v0.43.0 | **Low.** Maintained by the Go core team, MIT-style license. Used for syscall wrappers (umask, signals, file modes). v0.43.0 is current as of 2026-04. No known CVEs at the time of this scan. |
| `gopkg.in/yaml.v3` | v3.0.1 | **Medium-historical, low-current.** v3.0.0 had GO-2022-0956 (panic on malformed input → DoS). v3.0.1 is the fixed version. No newer versions exist as of this scan. The library is in maintenance mode (go-yaml/yaml#876). yaml is used in `internal/config` and `permissions.go` to read **operator-controlled config files**, never untrusted network input — DoS surface is local. |

## Indirect / test dependencies

| Module | Version | Notes |
|---|---|---|
| `gopkg.in/check.v1` | v0.0.0-20161208181325-20d25e280405 | Test-only transitive of yaml.v3. Not compiled into release binaries. |

## Supply-chain posture

- **Stdlib-first policy** (CLAUDE.md) — adding any new module requires an ADR. The runtime tree is intentionally minimal, dramatically reducing the attack surface a typical Go service exposes.
- **Module checksums recorded** — `go.sum` is committed; `go mod download` in CI verifies against it.
- **CI workflow** (`.github/workflows/ci.yml`) — `permissions: contents: read` at workflow level. Third-party actions (`actions/checkout`, `actions/setup-go`, `golangci-lint-action`) are pinned by **40-char SHA**, not floating tag. Tag rewrite cannot retroactively swap the action.
- **Release workflow** (`.github/workflows/release.yml`) — `permissions: contents: write` (needed for release upload). `softprops/action-gh-release` pinned by SHA. `CGO_ENABLED=0` and `-trimpath -ldflags="-s -w"` for reproducible cross-compiles. SHA-256 checksums computed and uploaded alongside binaries.

## Findings

### D-01 (info) — `gpg.in/yaml.v3` is in maintenance mode
**Severity:** Info
**Status:** Acceptable
The upstream `go-yaml/yaml` repo announced "no new feature work" in 2024. v3.0.1 remains the latest tag. If a new yaml CVE lands, an alternative (e.g. `goccy/go-yaml`) may be required; otherwise this dependency is fine. No action needed today.

### D-02 (info) — No release artifact signing (Sigstore / SLSA / cosign)
**Severity:** Info
**Status:** Documented
Releases include `sha256sums.txt` but are not signed with cosign and no SLSA provenance is generated. Acceptable for a developer-machine tool but worth tracking if DFMT ever ships through package managers (Homebrew, scoop, apt) where users cannot reasonably inspect the SHA.

## Conclusion

Supply-chain risk is **low**. Two well-vetted dependencies, both at fixed versions with checksums, in maintenance mode but not vulnerable. CI is hardened (read-only token, SHA-pinned actions). Release pipeline produces reproducible builds with checksums (signing is the only realistic future hardening).
