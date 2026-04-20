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

## Superseded Decisions

_None yet. When an ADR is superseded, it moves here with a note explaining what replaced it._

## Writing a New ADR

```
make adr-new TITLE="Content Cache Warming Strategy"
```

This creates `docs/adr/NNNN-content-cache-warming-strategy.md` with the template from ADR-0000. Fill in, submit as part of the PR that implements the decision (or precedes it, if the ADR is setting direction).

CI lint checks run on every ADR change: required fields, numbering contiguity, supersession reference validity.
