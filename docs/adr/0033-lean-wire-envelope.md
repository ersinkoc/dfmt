# ADR-0033: Lean Wire Envelope for Small Results

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-03 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

A tool result carries bookkeeping next to the payload: `dfmt_read` adds
`size`, `read_bytes`, a 32-hex `content_id` and the line window; `dfmt_exec`
adds `duration_ms`, `timed_out` and `content_id`. For a one-line read that
is ~137 bytes of JSON around 6 bytes of content; `echo hi` is ~108 bytes
around 3. On this repo 93 % of calls are under the 1k-token inline
threshold, so for most calls the envelope is larger than what DFMT "saves"
(ADR-0032: 2.5 % overall). An agent gets nothing from `content_id` on a
body it received whole: the only consumer is the `(unchanged; same
content_id)` dedup acknowledgement, and no tool fetches by id.

## Decision

A response takes the **lean shape** when all hold: the body reached the
agent whole (not excerpted), no summary / matches / vocabulary accompany it,
and the payload is ≤ `leanWireMaxTokens` (1024, `sandbox.ApproxTokens`,
matching the inline threshold). One helper (`internal/transport/wire_lean.go`)
serves read, exec and fetch; grep and glob never carried these fields.

Lean shape drops `content_id`, `size`, `read_bytes`, `duration_ms`, and the
line window — except that a **partial** read (offset/limit set, or the scan
stopped early) keeps `start_line`/`end_line`/`total_lines`, which is what
lets the agent cite file:line and page forward. Never dropped: `exit` (a
silent success is `{"exit":0}`, not `{}`), `timed_out` when true, fetch
`status`/`headers`. An empty read keeps `size`/`read_bytes` (custom
`ReadResponse.MarshalJSON`) so it is not `{}`.

`duration_ms` and `timed_out` become `omitempty` in every shape (false/0
are the absent values). Everything else is unchanged: stash, `seenBefore`/
`markSent`, journal ledger data, dedup acknowledgements (which exist to
carry `content_id`), and the full shape for summaries, excerpts and large
bodies, including `return:"raw"` on a large body.

Measured: 1-line read 137 → 21 bytes, `echo hi` exec 108 → 26 bytes.

## Consequences

- Clients that read `size`, `read_bytes`, `duration_ms` or `content_id` from
  a small result now see them absent/zero. In-tree consumers decode the same
  structs, so they get zero values. Existing tests that asserted a first
  response's `content_id` were updated to read it off the dedup reply.
- The first (lean) response no longer names the `content_id` that a later
  `(unchanged; same content_id)` refers to; the acknowledgement stays
  self-explanatory.
- Threshold is a constant, not config; revisit if the ledger shows
  mid-sized calls still dominated by envelope.
