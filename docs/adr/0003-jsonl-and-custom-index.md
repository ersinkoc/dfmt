# ADR-0003: JSONL Journal with Custom Inverted Index

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-20 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | — |

## Context

DFMT needs two storage capabilities: a durable log of all events, and a fast full-text search index for BM25 retrieval. The obvious default in the Go (and broader developer-tooling) ecosystem is SQLite with the FTS5 extension — widely trusted, well-tested, and the canonical answer when someone says "local database plus full-text search."

The decision here is whether to follow that default, or build something simpler from stdlib primitives.

## Decision

**Append-only JSONL for the log, custom in-memory inverted index for search, `encoding/gob` snapshots for persistence.**

- One event per line of JSON in `journal.jsonl`.
- In-memory inverted index with posting lists per stemmed term and per 3-gram.
- Custom BM25 scorer (30 lines of Go).
- Porter stemmer bundled as a single file (400 lines of Go).
- Index serialized to `index.gob` every hour; rebuilt from journal on startup (~200 ms for 10k events).

## Alternatives Considered

### A. SQLite + FTS5

The default for a reason. Mature, fast, well-understood. FTS5 handles tokenization, ranking, and prefix search out of the box. The obvious choice if runtime environment were not a concern.

Rejected because:
- **CGo.** Every SQLite binding for Go either uses CGo (ruling out straightforward cross-compilation) or relies on pure-Go ports that lag upstream and are maintained by small teams. CGo kills static binaries, forces platform-specific build steps, and breaks `go install`. Alpine and musl environments require an additional build toolchain for CGo-SQLite, which is a support cost.
- **Opacity.** Debugging a search or storage issue means attaching to a SQLite CLI and running FTS5 queries. The JSONL approach debugs with `grep`, `jq`, and a text editor — tools every developer already has.
- **Size.** SQLite static-linked adds ~1.5 MB to the binary. DFMT targets <8 MB total.
- **Overkill.** DFMT's queries are stateless, index-local, single-user. Zero of SQLite's serious capabilities (transactions, joins, concurrent writers, WAL recovery) are needed. Using it is paying for features we don't use.
- **FTS5 availability.** FTS5 is a compile-time option. Some distro packages and language bindings ship without it. This introduces a version-compatibility matrix that has to be documented and tested.

### B. Pure-Go SQLite (modernc.org/sqlite)

CGo-free port of SQLite. Good project, impressive work.

Rejected because:
- 7 MB of transpiled C — larger than the rest of DFMT combined.
- Tracks upstream SQLite through a transpilation pipeline, with a lag.
- Still opaque at debug time.
- Violates the zero-external-deps rule (ADR-0004).

### C. BoltDB / Badger

Embedded key-value stores. Would hold the journal and an index.

Rejected because:
- Binary format, not human-readable.
- External dependencies.
- Introduces a transactional model DFMT does not need.
- No full-text search; we'd still need to build the inverted index.

### D. Bleve (full-text search in pure Go)

Blevesearch is the Go analog of Lucene. Full BM25, tokenizers, stemmers, the works.

Rejected because:
- Large dependency surface (~40 transitive packages).
- Designed for much larger corpora than a single session's events. We pay for features we don't use.
- Our access pattern is "events added in strict chronological order, never removed, rarely more than 10k total." A general-purpose search library is mis-sized.

### E. Plain-text log + grep

The minimum viable option: write events as text, search with subprocess calls to `grep`.

Rejected because:
- No ranking. BM25 is a real quality improvement over substring match for session memory.
- Substring queries miss stems. "Caching" wouldn't match "cached."
- Latency acceptable for small corpora but scales linearly.

## Consequences

### Positive

- **Debuggable.** `cat journal.jsonl | jq 'select(.type=="decision")'` gives a readable view of every decision captured. No special tooling needed.
- **Portable.** Journal files are just JSONL. They move between machines, export to analytics tools, feed into AI pipelines, all without a conversion step.
- **Static binary.** No CGo. `go build` on any platform produces a self-contained executable. Alpine, musl, Windows all work with no special steps.
- **Small.** BM25 scorer is 30 lines. Porter stemmer is 400. ULID generator is 100. Total index implementation: under 2000 lines of Go.
- **Fast rebuild.** 10k events rebuild in ~200 ms. This is fast enough that crash recovery is a non-event.
- **Full control.** Every optimization — heading boost, trigram fallback, Levenshtein correction, progressive throttling — is a small patch to our own code, not a fight with someone else's abstractions.
- **Crash semantics are trivial.** Append-only + fsync = uncorruptable. The worst case after a crash is losing the last unfsynced write; a batched daemon might lose up to `max_batch_ms` of events (default 50 ms).

### Negative

- **Engineering effort.** We write the index ourselves. Estimated 1-2 weeks of implementation + testing vs. "shell out to FTS5." This is the main cost.
- **Risk of subtle bugs.** BM25 has well-known gotchas (dividing by zero on empty documents, handling the `avgdl` update race). We must test thoroughly.
- **Scale ceiling.** In-memory index scales to ~1M events comfortably, ~10M with care, beyond that SQLite's on-disk index becomes the right answer. DFMT's target is session memory (10k-100k events), so this ceiling is hypothetical.
- **No SQL for ad-hoc queries.** Users with advanced needs ("group events by day and count types") get a flat JSONL file; they run `jq` or write a script. Most users will never need this.

## Implementation Notes

- The tokenizer is a single function. Changes to it require reindexing, so the tokenizer version is stored in `index.cursor` and a mismatch triggers full rebuild.
- The inverted index struct uses `map[string]PostingList`. Maps are the right data structure; tries were considered and rejected (marginal speedup, major complexity).
- `encoding/gob` is used for index snapshots because it's stdlib, handles our struct types directly, and is compact. JSON would work but is slower to load.
- The journal uses `O_APPEND | O_CREATE | O_WRONLY`, with `O_SYNC` added for durable mode. `flock(LOCK_EX)` guards each write, but the daemon is the only writer, so the lock is mostly a belt-and-suspenders measure.
- The 5x heading boost (SPEC §7.1.3) is implemented at index time by writing heading tokens to the posting list with TF × 5.

## Revisit

Revisit if:
- A user's journal exceeds 1M events and query latency becomes unacceptable. Mitigation: introduce disk-spilled posting lists (mmap) or move to SQLite.
- FTS5 adds a feature (e.g., built-in phrase queries) that becomes a user requirement. Mitigation: implement it ourselves; most FTS5 features are 50-100 lines.
- A compelling pure-Go FTS library emerges that genuinely matches our scale and philosophy. Unlikely, but worth watching.
