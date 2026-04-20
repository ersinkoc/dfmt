# ADR-0004: Strict Stdlib-First Dependency Policy

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | SPECIFICATION.md §16 |

## Context

DFMT is part of the `ECOSTACK` portfolio, which operates under the `#NOFORKANYMORE` philosophy: single binary, zero or near-zero dependencies, stdlib-first. The question for DFMT specifically is whether to strictly apply that policy, or relax it for ergonomic libraries (CLI parsing, MCP protocol, filesystem notification, etc.).

Every dependency is a maintenance liability. Every dependency can become abandoned, malicious, or incompatible. Every dependency increases binary size and build complexity. But every dependency reimplementation is engineering effort that could go elsewhere.

## Decision

**Permitted dependencies, exhaustive list:**

- Go standard library (any version supported at time of release).
- `golang.org/x/sys` — OS-specific syscalls (inotify, kqueue, ReadDirectoryChangesW). Effectively stdlib.
- `golang.org/x/crypto` — reserved for future at-rest encryption work (ChaCha20-Poly1305 via `x/crypto/chacha20poly1305`).
- `gopkg.in/yaml.v3` — YAML config parsing. `encoding/json` is insufficient because humans write configs.

**Everything else is forbidden.** No MCP library, no CLI framework, no logging library, no FS notify wrapper, no database driver, no stemmer, no testing library, no HTTP router.

## Alternatives Considered

### A. Typical Go dep set

Pull in `fsnotify`, `mcp-go`, `cobra` or `urfave/cli`, `zerolog` or `zap`, `testify`, etc. This is the default Go project shape.

Rejected because:
- Even a modest Go project pulls 50+ transitive deps through this path.
- Supply chain risk: a compromised dep in the transitive graph is a real attack vector.
- Each dep maintainer's release cadence becomes a problem for our release cadence.
- Most of these libraries offer capabilities DFMT doesn't need.

### B. Hybrid — stdlib-first but allow "obvious wins"

Allow specific libraries that are widely trusted and save significant effort: `fsnotify` for filesystem notification, a well-maintained MCP library, `cobra` for CLI.

Rejected because:
- "Obvious win" is subjective and the bar creeps. Every new dep has someone arguing it's an obvious win.
- The specific libraries under discussion are either thin wrappers over stdlib (`fsnotify` wraps `x/sys/unix.Inotify*`) or have substantial surface area DFMT doesn't need (`cobra` ships help-text templates, bash completion, nested-command machinery).
- Reimplementation cost is bounded. `fsnotify`'s core is ~500 lines of Go when trimmed to DFMT's needs. MCP is JSON-RPC 2.0 over stdio, ~300 lines.

### C. Strict zero-dep

Refuse even `yaml.v3`. Use only stdlib.

Rejected because:
- YAML parsing is not trivial. Writing a YAML parser is a project. Users want YAML config; JSON config is painful to edit by hand.
- `x/sys` is required for OS syscalls and is effectively stdlib in spirit — maintained by the Go team, released as part of Go's ecosystem.

## Consequences

### Positive

- **Supply chain integrity.** DFMT's transitive dependency graph has exactly 4 nodes. An attacker compromising DFMT must compromise our code, Go itself, `x/sys`, `x/crypto`, or `yaml.v3`. Each of these has a high bar.
- **Binary size.** Target <8 MB stripped. Typical Go projects with the standard dep set land at 15-30 MB.
- **Build portability.** `go install github.com/ersinkoc/dfmt/cmd/dfmt@latest` works on any Go-supported platform with zero additional steps. No system packages, no CGo, no C compiler.
- **Upgrade discipline.** Dep updates are events, not noise. We notice every change because every change is rare.
- **Clear mental model.** A new contributor knows exactly where every line of code comes from: stdlib, the three permitted packages, or our own codebase.
- **No transitive surprises.** No dep's v2 that breaks everything. No dep that silently adds a telemetry call. No dep that shifts its license.

### Negative

- **Higher implementation effort.** Porter stemmer, BM25 scorer, MCP server, CLI argument parsing, filesystem watcher, logging — all implemented in-house. Rough estimate: 2000-3000 extra lines of Go compared to a dep-heavy version.
- **Potential reinvention bugs.** Every reimplementation is an opportunity to get something subtly wrong that a mature library got right years ago.
- **Slower to ship features that exist in ready-made form.** Need a feature? Writing it from scratch is slower than `go get`.
- **Narrower contribution surface.** Contributors expecting the familiar Go dep layout see our go.mod and are surprised.

## Implementation Notes

- Bundled code lives in `internal/`. Examples:
  - `internal/core/ulid.go` — ULID generation (~100 lines).
  - `internal/core/porter.go` — Porter stemmer (~400 lines). Reference: Porter's 1980 paper.
  - `internal/core/bm25.go` — BM25 scorer (~30 lines).
  - `internal/transport/jsonrpc.go` — JSON-RPC 2.0 over any `io.ReadWriter` (~250 lines).
  - `internal/transport/mcp.go` — MCP protocol layer over `jsonrpc` (~300 lines).
  - `internal/capture/fswatch_linux.go`, `_darwin.go`, `_windows.go` — platform-specific FS watchers over `x/sys` (~400 lines each).
  - `internal/cli/flags.go` — custom flag handler using stdlib `flag` package with subcommand dispatch (~200 lines).
- Each bundled component has its own tests. Coverage target: >85% for bundled components (higher than the project default of 80%) because we can't lean on a library's tests.
- Any proposed new dependency must have an ADR. The default answer is no.
- This policy is documented in the root `README.md` as a design commitment, so contributors don't submit PRs that add deps and get surprised by rejection.

## Revisit

Revisit per-dependency when:
- A specific stdlib gap is identified that is genuinely hard to fill (e.g., WebSocket support before Go adds it to stdlib).
- The maintenance cost of a specific in-house component exceeds the cost of depending on a mature alternative. This bar is high: we accept significant in-house effort to preserve the policy.

The policy itself is not revisitable. It is a core property of the product, not a debatable tradeoff.
