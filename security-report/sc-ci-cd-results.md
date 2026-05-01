# sc-ci-cd Results

## Target: `.github/workflows/ci.yml`, `.github/workflows/release.yml`

## Methodology

Per the engagement brief, validated:

1. Third-party action SHA pinning (40-char hex).
2. Token scope: read-only on CI, narrowest necessary on release.
3. `pull_request_target` usage and untrusted-input checkouts.
4. Secret exposure to fork PRs.
5. `${{ github.event.* }}` script-injection in `run:` blocks.
6. Implicit-token actions that could be coerced into pushing to the repo.

Searched both files for `pull_request_target`, `github.event.`, fork-PR
checkouts, and `secrets.*` references.

## Findings

No issues found by sc-ci-cd.

### Verified-clean controls

| Control | Location | Status |
|---|---|---|
| Triggers do not include `pull_request_target` | `ci.yml:3-7`, `release.yml:3-6` | PASS — only `push`, `pull_request` (untrusted), and `push tag v*` |
| Default workflow permissions read-only | `ci.yml:12-13` | PASS — `contents: read` |
| Release-only `contents: write` | `release.yml:8-9` | PASS — narrowest scope for tag-driven release |
| All third-party actions pinned by 40-char SHA | `ci.yml:32,35,49,51,56`, `release.yml:20,23,56` | PASS |
| `actions/checkout@34e1148...` v4 pin | both files | PASS |
| `actions/setup-go@40f1582...` v5 pin | both files | PASS |
| `golangci/golangci-lint-action@9fae48a...` v7 pin | `ci.yml:56` | PASS |
| `softprops/action-gh-release@3bb1273...` v2 pin | `release.yml:56` | PASS |
| `release.yml` does not check out a fork PR | `release.yml:20` (only `actions/checkout` on the tag ref of the main repo) | PASS |
| No `${{ github.event.* }}` interpolation inside `run:` blocks | both files | PASS — no `github.event.pull_request.title`, `.body`, `.head.ref`, `.head.sha` ever spliced into shell |
| `${{ github.ref_name }}` used safely (passed via `env:`, not interpolated into `run:` script) | `release.yml:30-32` | PASS — even if a tag name carried shell metacharacters, the value is read from `${VERSION}` shell-expansion, not from the un-quoted `${{ }}` form |
| Matrix `${{ matrix.os }}` only used in `runs-on:`, not in shell | `ci.yml:17,27` | PASS |
| `secrets.GITHUB_TOKEN` is the only secret referenced; passed via `env:` to `softprops/action-gh-release` only | `release.yml:62-63` | PASS — token never echoed, never used in `run:` shell |
| No long-lived secrets (no PAT, no signing key, no registry creds) | both files | PASS |
| `cache: true` in `setup-go` does not write to repo or expose tokens | `ci.yml:38,52`, `release.yml:26` | PASS |
| `continue-on-error` only on macOS matrix entry, documented | `ci.yml:23-27` | PASS — Linux + Windows still required, so a macOS-only failure cannot mask a real regression on the gating platforms |
| No `workflow_run`, no `repository_dispatch`, no remote-trigger primitives that could promote read-token to write | both files | PASS |

### Tag-injection probe

The release workflow expands `${{ github.ref_name }}` into the env var
`VERSION` (`release.yml:31-32`), which is then used in shell as
`${VERSION}` inside `LDFLAGS="-s -w -X main.version=${VERSION}"`. Tags
in GitHub's data model are validated against `refs/tags/<name>`; even
if a tag like `v1.0.0";rm -rf /;"` were createable (it is not — Git
disallows `;`, `"`, etc. in ref names), the value would be a shell
variable expansion *inside* a double-quoted string and the shell
would treat any embedded shell metacharacters as literals when
expanded into the `-X` ldflag (Go would then fail to compile, not
exec arbitrary code).

### `actions/checkout` post-install hook surface

The pinned SHA versions of `actions/checkout` (v4) and `setup-go` (v5)
do not run repo-controlled scripts on the CI runner. `golangci-lint-
action` runs the linter binary it downloads, which is the same trust
boundary as installing Go itself. Pinning by SHA closes the "tag
rewrite swaps the binary" vector.

### Confidence: High
