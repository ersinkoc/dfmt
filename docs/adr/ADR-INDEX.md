# Architecture Decision Records

This folder documents structural decisions about DFMT. Each record captures the context, the decision, the alternatives considered, and the consequences — so future contributors can understand *why* the code looks the way it does, not just *what* it does.

See [ADR-0000](0000-adr-process.md) for the process governing how ADRs are written, versioned, and superseded.

## Active Decisions

| # | Title | Status | Summary |
| --- | --- | --- | --- |
| [0000](0000-adr-process.md) | ADR Process and Lifecycle | Accepted | Light MADR-style process with explicit supersession. |
| [0001](0001-per-project-daemon.md) | Per-Project Daemon Model | Accepted | One daemon per project, auto-start, idle-exit. |
| [0002](0002-mit-license.md) | MIT License | Accepted | MIT for maximum adoption; brand protects identity. |
| [0003](0003-jsonl-and-custom-index.md) | JSONL Journal + Custom Index | Accepted | Append-only JSONL + in-memory inverted index + gob persistence. No SQLite. |
| [0004](0004-stdlib-only-deps.md) | Stdlib-First Dependency Policy | Accepted | Only `stdlib`, `x/sys`, `x/crypto`, `yaml.v3`. Everything else bundled. |
| [0005](0005-multi-source-capture.md) | Multi-Source Capture Layer | Accepted | MCP + FS + git + shell + CLI, all independent. Agent-agnostic baseline. |
| [0006](0006-sandbox-scope.md) | Sandboxed Tool Execution In Scope | Accepted | Reverses earlier NG4. Sandbox is first-class alongside session memory. |
| [0007](0007-content-store-separation.md) | Content Store ≠ Event Journal | Accepted | Two distinct stores with shared index infrastructure; different lifecycles. |
| [0008](0008-html-parser.md) | Bundled HTML Parser | Accepted | ~350 lines bundled; don't take `x/net/html` dependency. |
| [0009](0009-cross-call-content-dedup.md) | Cross-Call Content Dedup | Accepted | Strip payload to `content_id` + `(unchanged)` summary when the same body was emitted earlier in the daemon's lifetime. |
| [0010](0010-structured-output-awareness.md) | Structured-Output Awareness | Accepted | `NormalizeOutput` detects JSON bodies and drops a small noise-field set (`created_at`, `*_url`, `_links`, `etag`, `node_id`). |
| [0011](0011-per-session-wire-dedup.md) | Per-Session Wire Dedup Scoping | Accepted | Session ID flows through `context.Context`; `Handlers.sentCache` keys per-session. Closes the deferred risk in ADR-0009. |
| [0012](0012-token-aware-budgets.md) | Token-Aware Policy Budgets | Accepted | Inline / medium / tail policy gates compare against approximated token counts (heuristic), not raw bytes. CJK and English bodies now hit the same agent-cost threshold. |
| [0013](0013-drop-unwired-levenshtein.md) | Drop Unwired Levenshtein Scaffolding | Accepted | Remove `core.Levenshtein` + `core.FuzzyMatch` and their tests; the `fuzzy` Search layer remains accepted for forward compatibility but returns no results. |
| [0014](0014-operator-override-files.md) | Operator Override Files (permissions.yaml + redact.yaml) | Accepted | `.dfmt/permissions.yaml` and `.dfmt/redact.yaml` now wired at daemon + CLI startup. Permissions merge has a hard-deny invariant (override `allow:exec:rm *` and friends are silently masked); redact is additive YAML with per-entry resilience. |
| [0015](0015-config-knob-consolidation.md) | Config Knob Consolidation | Accepted | Each Config field classified Wired / Reserved (v0.4). No deletes in v0.3; per-field comments in source flag silent no-ops with a v0.4 wire-or-delete commitment. |
| [0016](0016-metrics-endpoint.md) | Prometheus `/metrics` Endpoint | Accepted | In-tree Prometheus text-format emitter on `/metrics`. v0.3 publishes daemon-level gauges (uptime, MemStats, goroutines), scrape counter, per-tool counters (`dfmt_tool_calls_total{tool,status}`), dedup-hit counter, and index / wire-dedup / content-dedup size gauges. Duration histograms + journal-byte total deferred to v0.4. No new dependency. |

## Superseded Decisions

_None yet. When an ADR is superseded, it moves here with a note explaining what replaced it._

## Writing a New ADR

```
make adr-new TITLE="Content Cache Warming Strategy"
```

This creates `docs/adr/NNNN-content-cache-warming-strategy.md` with the template from ADR-0000. Fill in, submit as part of the PR that implements the decision (or precedes it, if the ADR is setting direction).

CI lint checks run on every ADR change: required fields, numbering contiguity, supersession reference validity.
