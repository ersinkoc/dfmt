# ADR-0024: What Memory Keeps, What a Job May Cost, and What a Read Returns

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-07-30 |
| Deciders | Ersin Koç |
| Related | [ADR-0012](0012-token-aware-budgets.md), [ADR-0015](0015-config-knob-consolidation.md), [ADR-0023](0023-real-deadlines-concurrent-proxy-and-rpc-auth.md) |

## Context

ADR-0023 fixed controls that did not hold. This one is about limits that held perfectly well and were set against the wrong thing.

### 1. The index forgot from the wrong end

`Index.Add` evicts at `MaxIndexDocs` by popping the smallest ULID — the oldest document, whatever it is. On any real journal that is the wrong document. This repository's own journal, at the time of writing, was **193 automatic `tool.*` events to 1 deliberate note**; at a cap, "oldest first" retires the earliest decisions — the ones that explain why the code looks the way it does — while keeping every recent `tool.read`.

The failure is quiet in a specific way: the note is still in the journal, so `dfmt_recall` keeps showing it. Only `dfmt_search` forgets, because search is index-backed. Memory that decays from its most valuable end while continuing to look intact.

The same function was also the cost problem. `removeLocked` walked **every** posting list in `stemPL` and `trigramPL`, rebuilt each one, and then recomputed `avgDocLen` across every document — per insert, once at the cap. Measured: 739 µs/insert at a 2 000-document cap, 6 215 µs at 20 000. That growth (8.4× cost for 10× size) is the shape of a full scan.

### 2. A five-minute ceiling on a nine-minute job

`MaxExecTimeout` was 300 s. Removing the transport-level cap (ADR-0023) made it the real ceiling — and it is below the length of ordinary work. This repository's `go test ./...` takes ~531 s. A cold `go build`, an `npm install` on a large lockfile, a container image pull: none of these reliably fit in five minutes.

### 3. Recall re-read the world on every call

`Recall` streams the entire journal every time: every rotated segment, every line unmarshalled, and — via `streamFile` — every event's signature recomputed (canonical re-marshal plus SHA-256). Two recalls with no intervening event do exactly the same work twice.

### 4. `dfmt_read` spoke bytes

`offset` and `limit` were byte quantities, and the response said nothing about which lines came back. Nothing else in an agent's world uses bytes: stack traces, compiler errors, editors, review comments, and DFMT's own `Matches[].Line` all speak lines. To look at a function it knew started "around line 400", an agent had to guess a byte offset, and could not cite `file:line` from what it received. Observed during the review that produced this ADR: the reviewing agent fell back to the native Read tool for every code-reading step.

## Decision

**Eviction is priority-first, age-second.** One min-heap per tier; eviction drains the lowest-priority non-empty tier. The tier comes from the *same* `core.Classifier` that Recall uses, so the tag vocabulary (`summary`/`decision`/`strengths`/`ledger` → P2, `audit`/`finding`/`followup`/`preserve` → P3) governs what survives in the index exactly as it governs what survives a recall budget. Two different answers to "how important is this event" would be worse than either answer alone.

Documents loaded from a persisted index carry no tier (metadata is not serialized, by the same argument that keeps excerpts out of the file) and go to the lowest tier: they are both the oldest and the least identifiable, and leaving the heaps empty would send every eviction through an O(N) fallback scan.

**Removal is reverse-mapped.** `docStems` / `docTrigrams` record which terms a document contributed to, so removal visits those and nothing else; `docLenSum` keeps `avgDocLen` arithmetic; front deletion re-slices instead of memmoving. Result: 8.7 µs and 13.3 µs at the two caps above — 85× and 467× — with cost growth of 1.5× for 10× size instead of 8.4×.

**`MaxExecTimeout` is 900 s.** A build or a test suite is the normal shape of an agent-driven exec. The ceiling exists to stop a runaway from pinning an exec slot, and 15 minutes still does that, alongside the tree-kill deadline, `execSem`'s cap of 4, and the caller's own (usually much lower) `timeout`.

Rejected here, deliberately: an **async job handle** (submit → poll → fetch output). It is the right answer for work that outlives any ceiling — migrations, soak tests, watch modes — but it is a new MCP tool, a job store with its own lifetime and cleanup rules, and a cancellation story, inside a `tools/list` payload that is already byte-budgeted. That is a feature with its own ADR, not a constant, and shipping the constant now does not foreclose it.

**Recall memoizes against the journal cursor.** Keyed on (project, budget, format), validated against `Journal.Checkpoint`. Invalidation is exact rather than time-based: one appended event moves the cursor and the next call rebuilds. A TTL would have been simpler and wrong in both directions — stale inside the window, needlessly cold outside it. An empty cursor is never cached, because it cannot distinguish two journal states; a redactor swap clears the cache, because cached snapshots were rendered through the old one while the cursor stayed put.

**`dfmt_read` is line-oriented.** `offset` is the 1-based first line (0 and 1 both mean the top, so no agent has to know which convention we picked), `limit` is a line count, and the response carries `start_line` / `end_line` / `total_lines`.

Two sub-decisions worth naming:

- **No line numbers in the content.** Numbered text is a trap for the next step in the loop: an agent that lifts an anchor out of it produces an `old_string` that cannot match the file, and `dfmt_edit` refuses it (or, before ADR-0023's uniqueness check, silently edited the wrong site). Line numbers travel in the metadata and in `Matches[].Line`.
- **`total_lines` is 0 when unknown.** A read that stopped at its line limit or at `MaxSandboxReadBytes` never established the file's length; reporting the lines it happened to see as the total would be indistinguishable from the truth and wrong.

The scan streams rather than slurping, so a window deep inside a file larger than `MaxSandboxReadBytes` is still reachable — skipping costs I/O, not memory.

## Consequences

`offset`/`limit` change meaning on a wire surface. Pre-1.0 and worth it: the old semantics were unusable enough that agents routed around the tool, which is a worse compatibility outcome than a documented break. Two tests that pinned byte behavior were rewritten.

Index memory grows by the reverse maps — roughly one string header per (document, term) pair, on top of posting lists that already store the same strings. That is the price of not scanning the dictionary on every eviction, and it is bounded by `MaxIndexDocs`.

An index persisted before this change loads with no tier information, so its documents are evicted first. That is the same order the old code used, so nothing regresses; the tiering starts applying to everything indexed after load.

A 15-minute exec holds one of four exec slots for 15 minutes. An agent that starts three such jobs will queue the fourth — visible as latency, not failure, and preferable to the previous behavior of failing at five minutes with the work already done.

One caution for future work on `removeFromPostingList`: its binary search assumes posting lists are in ULID order, which holds because they are append-only in insertion order. It falls back to a linear scan when the search misses, so an out-of-order list stays *correct* while getting slower — a silent performance cliff rather than a bug. The first version of this change tripped exactly that: a legacy-path guard that always fired left every removal doing a full sweep plus a linear search, and every test still passed. It took a CPU profile to see it.
