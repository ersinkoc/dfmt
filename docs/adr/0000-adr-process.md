# ADR-0000: ADR Process and Lifecycle

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

DFMT's design has been captured in a growing set of Architecture Decision Records under `docs/adr/`. ADR-0001 through ADR-0008 cover concrete technical decisions (daemon model, license, storage, dependencies, capture layer, sandbox scope, content store, HTML parser). As the project evolves, some decisions will need to change: a design choice we made for v1 may no longer fit in v3.

Without a process for amending decisions, the ADR folder becomes either stale (ADRs that no longer reflect reality) or contradictory (multiple ADRs on the same topic with unclear relationships). Both undermine the primary purpose of ADRs: being a reliable archaeological record of why the codebase looks the way it does.

This ADR establishes the process.

## Decision

DFMT uses a **light MADR-style process** with an explicit supersession mechanism:

### Lifecycle states

Every ADR has a `Status` field in its header, taking one of:

- **Proposed** — drafted but not yet accepted. Author requesting feedback.
- **Accepted** — active decision. Implementations conform to it.
- **Deprecated** — decision is no longer the preferred approach, but no replacement exists. Still-active code may rely on it.
- **Superseded by ADR-NNNN** — replaced by a later ADR. New code follows the replacement; old code may still reflect the superseded decision during transition.
- **Rejected** — considered and not accepted. Kept for the record so future discussions don't relitigate the same ground.

### Supersession

When an ADR is superseded:

1. A new ADR is written with `Status: Accepted` and a `Supersedes: ADR-NNNN` field listing the ADR(s) it replaces.
2. The superseded ADR is edited: its `Status` becomes `Superseded by ADR-MMMM` where MMMM is the new ADR's number. A note at the top of the superseded ADR's Context section summarizes what changed and why, then leaves the original text intact.
3. The superseded ADR is **never deleted**. Future readers should be able to trace why a decision was reversed.

Example supersession header on an old ADR:

```markdown
# ADR-0003: JSONL Journal with Custom Inverted Index

| Field | Value |
| --- | --- |
| Status | Superseded by ADR-0042 |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |

> **Note 2027-08-15:** ADR-0042 replaces this decision. The custom index
> described below hit scale limits in multi-year journals; ADR-0042 adopts
> a SQLite-backed backend for installations over 100k events. The original
> content is preserved below for historical reference.
```

### Numbering and filenames

- ADR numbers are zero-padded four-digit sequential: `0001`, `0002`, … `0042`.
- Filenames follow `NNNN-<kebab-case-slug>.md` where the slug is a short description (4-8 words).
- Numbers are assigned when the ADR is merged to `main`, not when a draft is opened. Drafts live on feature branches.
- Numbers are never reused. A rejected ADR keeps its number.

### When to write an ADR

Write an ADR when the decision:

1. Affects the public API or wire format.
2. Affects a structural property of the system (storage format, transport, deployment model).
3. Has multiple reasonable options and you want to record why this one was chosen.
4. Excludes future options (dependency policy, license, scope non-goals).
5. Reverses a previous ADR (always requires one).

Do **not** write an ADR for:

- Routine implementation choices (which hash map, variable naming).
- Performance tuning within an already-decided design.
- Bug fixes that don't change design.

### Template

```markdown
# ADR-NNNN: <title>

| Field | Value |
| --- | --- |
| Status | Proposed / Accepted / Deprecated / Superseded by ... |
| Date | YYYY-MM-DD |
| Deciders | <who> |
| Supersedes | ADR-XXXX (if applicable) |
| Related | other ADRs, spec sections |

## Context
<what prompted this decision>

## Decision
<what we decided>

## Alternatives Considered
<what we didn't pick and why>

## Consequences
### Positive
### Negative

## Implementation Notes
<optional, for non-obvious wiring>

## Revisit
<what would change our mind>
```

### Cross-linking

- The `SPECIFICATION.md` references ADRs by number in sections where the ADR is load-bearing (e.g., "§7.5 Sandbox Layer — see ADR-0006").
- ADRs reference back to relevant spec sections in their `Related` field.
- The `docs/adr/README.md` index lists every ADR with status and one-line description, ordered by number; superseded ADRs are visually grouped at the end.

## Alternatives Considered

### A. No formal process

Let the folder grow organically, discussion spread across GitHub issues and PR descriptions.

Rejected because: DFMT's decision archaeology is a marketing advantage. A project that can show, for any design question, exactly when the decision was made and what alternatives were weighed earns trust. Scattered discussions don't provide that.

### B. Full MADR / Nygard-style ADRs with formal review

Structured templates, required reviewers, status transitions through PR.

Rejected because: DFMT is a solo project (for now). Heavy process slows momentum without adding value. The light MADR style above captures the essentials without the overhead.

### C. Version ADRs like code (semantic versioning)

ADR-0003 v2, v3, etc.

Rejected because: ADRs are records of decisions made at a point in time, not living documents. A v2 of an ADR would blur "what did we decide then" with "what do we decide now." The supersession pattern — new ADR + old one marked — preserves both.

## Consequences

### Positive

- Every decision has a findable, dated record.
- Reversing a decision is explicit and traceable.
- The ADR folder serves as onboarding material: "read these to understand why the codebase is this way."
- Light process doesn't slow iteration.

### Negative

- One more convention to remember.
- Potential for ADR proliferation if the bar is set too low. Mitigated by the "when to write an ADR" guidance above.
- Superseded ADRs accumulate in the folder (never deleted). Mitigated by the README index grouping them at the end.

## Implementation Notes

- `docs/adr/README.md` is regenerated from ADR headers by a small script (`scripts/adr-index.sh`) run as part of release preparation. The script reads each ADR's front matter, builds the table, sorts by number, groups superseded entries.
- GitHub Actions CI runs `scripts/adr-lint.sh` on every PR that touches `docs/adr/`, checking: required fields present, status value valid, supersedes references exist, numbering sequential (no gaps).
- A `make adr-new TITLE="…"` Makefile target copies the template to a new file with the next number assigned.

## Revisit

Revisit if:
- The project gains a formal design-review process where multiple maintainers review ADRs before acceptance — the current solo-maintainer assumptions are noted and would update.
- Tooling emerges that makes ADRs more useful in a different format (e.g., structured YAML frontmatter machine-readable for a design-decisions website). Current markdown format is chosen for human readability first.
