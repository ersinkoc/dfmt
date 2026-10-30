# ADR-0032: An Honest Token Ledger

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

DFMT's pitch is that it does not waste the agent's tokens. The numbers
behind that pitch did not hold up against our own journal: this repository's
`dfmt stats` showed 486 KB raw against 485 KB returned (0.2 % saved). The
README's 40–90 % comes from synthetic `dfmt-bench` scenarios only. Reasons:

- Output under `InlineTokenThreshold` (1024 tokens) passes through
  untouched, and most real calls are that small.
- Stats counted bytes only, and only for exec/read/fetch. Grep and glob left
  no byte record, so the ledger could not show what they cost.
- `token_savings` is an alias of `cached_tokens` — API prompt-cache hits the
  caller reports by hand — and read as if it were DFMT's own saving.
- The fixed cost of the tool declarations (`tools/list`, paid once per
  session) was nowhere in the accounting.

## Decision

`StatsResponse` gains additive fields; nothing is removed or renamed.

- `est_raw_tokens`, `est_returned_tokens`, `est_tokens_saved`: the byte
  totals divided by four, rounded up. They are labelled estimates and work
  retroactively on existing journals, which store bytes, not tokens.
- `tools_list_bytes`, `tools_list_tokens`: the serialized size of the built-in
  tool declarations. Reported **beside** the savings, not subtracted: the
  journal carries no session id, so the number of sessions a total spans is
  unknown and any subtraction would be invented.
- `tool.grep` and `tool.glob` events record `raw_bytes == returned_bytes`.
  A native grep prints the same lines, so no saving is claimed; the call
  becomes visible in `tool_bytes` and the ratio is no longer flattered by
  leaving the cheap-to-claim tools out.
- `token_savings` stays on the wire for compatibility; its comment now says
  it is cache hits, not DFMT's saving.

`dfmt stats` prints the estimate, the fixed cost, and the pass-through
caveat.

## Consequences

- The headline ratio can go *down* on real journals, because grep/glob now
  count at 0 %. That is the point: a ratio that only counts the tools that
  compress is not a ratio.
- A bytes/4 estimate is wrong for CJK-heavy bodies (`ApproxTokens` is exact
  per-body, but the journal keeps only bytes). Recording tokens at event time
  would fix it and is left for a later change.
- No counterfactual exists for edit/write (a native Edit is as small), so
  they stay out of the ledger.
