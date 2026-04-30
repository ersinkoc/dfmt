# ADR-0013: Drop Unwired Levenshtein Scaffolding

- **Status:** Accepted
- **Date:** 2026-04-30
- **Supersedes:** —
- **Superseded by:** —
- **Related:** ADR-0003 (JSONL + Custom Index)

## Context

`internal/core/levenshtein.go` shipped a Levenshtein edit-distance
function plus a `FuzzyMatch` wrapper, intended to back the `fuzzy`
case in the Search RPC's `layer` parameter (`bm25` / `trigram` /
`fuzzy`). The implementation was small (~47 lines), fully tested, and
in-tree per the stdlib-only dependency stance (ADR-0004).

It was also unused. `internal/transport/handlers.go::Search` accepts
`layer="fuzzy"` but returns no results — the wiring from request to
`core.Levenshtein` was never built. The scaffolding sat as "reserved
code" for the entire v0.1 → v0.2 cycle; ARCHITECTURE.md §18 listed it
as a tripwire and ROADMAP.md flagged it as a v1.0 decision point
("wire it with an ADR or remove it with a removal ADR").

The decision point came due. We picked remove.

## Decision

Delete `internal/core/levenshtein.go` and the four associated tests
(`TestLevenshtein`, `TestFuzzyMatch`, `TestLevenshteinEdgeCases`,
`TestFuzzyMatchEdgeCases`) from `internal/core/core_test.go`. Update
ARCHITECTURE.md (§8.4 trigrams note, §8.5 fuzzy layer, §17.3 in-tree
list, §18 package map, §18 reserved-code table) and ROADMAP.md
(reserved-code v1.0 checklist) to reflect the removal.

The Search RPC keeps accepting `layer="fuzzy"` for forward
compatibility — same shape, empty result set, `Layer: "fuzzy"`
echoed back. This preserves the wire shape so callers that already
pass `layer="fuzzy"` (treating it as an opt-in upgrade) don't break.

## Alternatives Considered

### Wire the scaffolding (do nothing now, build fuzzy in v0.3)

Levenshtein + the trigram dictionary already in the index would let
us implement the textbook three-layer fallback (BM25 → trigram →
fuzzy correction). Two reasons we passed:

1. **Trigram fallback already closes the failure mode.** The class
   of misses Levenshtein would catch (token typos, near-matches that
   the Porter stemmer mangles) is largely handled by the trigram
   layer that landed in the BM25-zero-hits fallback path. Real-world
   miss rate after that fallback is low; spending two more weeks on
   a third layer for a marginal recall win is poor ROI relative to
   v0.3's bigger items (permissions/redactor wiring, config knob
   consolidation, observability).
2. **Fuzzy correction has known scaling problems.** Edit-distance
   scoring against the term dictionary is O(|dict| · |query_term|)
   per query — fine for small corpora, costly as the journal grows.
   Adding it now means re-tuning later when journal sizes climb.

### Keep the file as documented dead code

ADR-0006 and ADR-0007 set the precedent that reserved code is OK if
the §18 tripwire row stays current. But §18's tripwire premise is
"a future commit will make this load-bearing." After two release
cycles with no caller and no concrete plan, the tripwire becomes
noise. The reserved-code list shrinks; future readers see a smaller
"unwired but in-tree" surface.

## Consequences

### Positive

- Reserved-code surface in §18 shrinks by one entry; the §17.3
  "in-tree implementations" list loses `levenshtein` (the row is
  cosmetic, not load-bearing).
- 47 lines + 67 lines of tests deleted. Future BM25/trigram
  refactors don't need to keep `Levenshtein` API stable.
- The "wire or remove" decision blocking v1.0 (ROADMAP.md reserved-
  code section) closes for this row.

### Negative

- If we want fuzzy correction back in v0.4+, the scaffolding has to
  be re-implemented from `git show <pre-removal>:internal/core/
  levenshtein.go`. The function is small enough that this is a
  ~30-minute exercise; a follow-up ADR would re-add it.
- ADR-0003's *Consequences > Positive* bullet 6 lists "Levenshtein
  correction" as an example of an optimization we control. The ADR
  is left untouched (per the immutable-ADR convention from
  ADR-0000); the reference is now historical, true at the time the
  decision was written.

## Implementation Notes

The Search RPC's `layer="fuzzy"` branch returns an empty hit set
without error. This is identical to the pre-removal behavior — the
old branch never reached `core.Levenshtein` either, because the
glue from `handlers.go` to the dictionary scan was missing. So no
client-visible change.

If a caller depends on `fuzzy` returning **non-empty** results, that
caller was already broken; this ADR does not regress them.

## Migration

None for end users. Operators upgrading across this boundary do not
need to re-index or re-configure. The `.dfmt/index.gob` schema is
unchanged (trigram posting list still populated; `core.Levenshtein`
is no longer a marshal target — it never was).
