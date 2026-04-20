# ADR-0002: MIT License

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

DFMT ships as a single-binary developer tool. The license choice affects who can adopt it, how it's bundled, whether competitors can rebrand-and-redistribute it, and whether a future managed offering is possible.

## Decision

**MIT License.**

## Alternatives Considered

### A. Apache-2.0

Nearly equivalent to MIT for practical adoption, but adds patent grant and NOTICE file handling. Chosen by most Go ecosystem projects.

Rejected because MIT is shorter, more recognizable, has no NOTICE requirement, and the patent grant is not a meaningful concern for a tool with no patents to grant.

### B. BUSL-1.1 (Business Source License)

Source-available, with a time-delayed conversion to Apache-2.0. Prevents competing managed offerings during the BUSL window.

Rejected because:
- No managed offering is currently planned.
- BUSL creates adoption friction; enterprise legal reviews flag it.
- The usage at which BUSL would pay off (someone actually wrapping DFMT as a paid SaaS) is highly unlikely for a local daemon.

### C. Elastic License v2 (ELv2)

Source-available, with a narrower definition of permitted use than Apache/MIT. Prevents rebranding and reselling as a managed service, but accepts most other uses.

Rejected for the same reasons as BUSL. Additionally, ELv2 has a narrower precedent and more legal ambiguity than BUSL, making enterprise legal reviews even more likely to flag it.

### D. GPL-3.0 / AGPL-3.0

Copyleft. Derivative works must remain open.

Rejected because:
- DFMT is designed to be embedded or called by proprietary AI agents. Copyleft creates real adoption barriers with commercial tooling.
- Go's static linking model makes GPL attribution particularly fraught.
- The ecosystem DFMT lives in (Go CLIs, MCP servers) is overwhelmingly permissive.

## Consequences

### Positive

- Any agent, extension, or CI tool can bundle DFMT without legal friction.
- Contributors face the lowest possible bar — no CLA needed beyond what MIT implies.
- The `ersinkoc` namespace and brand identity do the differentiation work that a restrictive license would otherwise do. Users looking for DFMT find DFMT; a fork under another name has to build its own reputation from zero.

### Negative

- A well-funded fork could take DFMT, rebrand it, and offer a hosted version. This is theoretically possible but unlikely for a tool whose value is specifically that it runs locally.
- No legal recourse if someone integrates DFMT into a closed product without attribution. MIT requires attribution only in redistributions of the source, not in binary-only integrations.

## Implementation Notes

- `LICENSE` file at repository root: standard MIT template with `Copyright (c) 2026 ECOSTACK TECHNOLOGY OÜ`.
- Copyright header in source files is optional and discouraged (noise in every file). Copyright is established by the `LICENSE` file alone.
- SPDX identifier `MIT` in `go.mod` (via comment) and package metadata.

## Revisit

Revisit only if:
- A credible managed offering becomes part of the product roadmap, at which point BUSL-1.1 could be considered for new code only (dual-licensing existing MIT code is impossible without all contributors' consent).
- A fork gains meaningful traction and causes market confusion, at which point trademark enforcement (not license change) is the correct response.
