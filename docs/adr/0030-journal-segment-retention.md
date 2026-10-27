# ADR-0030: Opt-In Retention for Rotated Journal Segments

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-27 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

The journal rotates at `storage.journal_max_bytes` (10 MiB by default),
renaming the active file to `journal.jsonl.<last-id>.jsonl`. Nothing ever
removed a rotated segment, so a project that lives for years accumulates
history without bound. Everything that reads the whole journal pays for it:
recall cache misses, index rebuilds on a lost `index.gob`, and (before
0.9.1) the index tail every three seconds. `storage.compress_rotated` was
meant to blunt this, but it has never been wired (ARCHITECTURE §8.3).

The journal is also the agent's long-term memory. A rotated segment holds
decisions `dfmt_recall` and `dfmt_search` can still surface; deleting it
is not a cache eviction.

## Decision

Add `storage.journal_keep_segments` (int, default **0 = keep all**).
With N > 0, every successful rotation deletes all but the N newest rotated
segments, oldest first (`journalImpl.pruneRotatedLocked`). The active file
is never touched. A segment that cannot be deleted is warned about and
retried at the next rotation.

- **Opt-in.** The default deletes nothing, so no existing install loses
  history on upgrade. The cost of the default is disk; the cost of the
  opposite default would be memory the user did not agree to forget.
- **Count, not age or bytes.** Segments are already size-bounded by
  `journal_max_bytes`, so a count bounds total size (≈ N × max bytes) with
  one knob and no clock dependence.
- **At rotation only.** Rotation is when a segment is created; pruning
  there needs no timer and touches the disk only when it already does.

## Consequences

- With N set, events older than the kept segments are gone from recall,
  search and rebuilt indexes. The config template says so.
- The index may still hold documents from deleted segments until its next
  rebuild; that is harmless (search finds them, recall's journal stream
  does not) and self-correcting.
- `compress_rotated` remains unwired; retention does not depend on it.
