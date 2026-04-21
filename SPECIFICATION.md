# DFMT — Don't Fuck My Tokens

**Specification**

| Field | Value |
| --- | --- |
| Document | `SPECIFICATION.md` |
| Status | Draft — v0.7 (design-complete, POC-ready) |
| Maintainer | Ersin Koç (ECOSTACK TECHNOLOGY OÜ) |
| Language | Go (stdlib-first, `#NOFORKANYMORE`) |
| License | MIT |
| Date | 2026-04-20 |
| Repository | `github.com/ersinkoc/dfmt` (proposed) |

---

## 1. Executive Summary

DFMT is a local, agent-agnostic daemon that solves two related problems for AI coding agents: uncontrolled context-window consumption during a session, and loss of working state at session boundaries.

For the first problem, DFMT exposes a set of **sandboxed tools** that any AI agent can use in place of its native file-reading, shell, and web-fetching tools. These sandboxed tools execute the same operations but keep the raw output out of the agent's context: large results are indexed locally and returned as summaries or intent-matched excerpts. A 45 KB log file, a 56 KB page snapshot, a 60 KB API response — none of them need to enter the context window in full.

For the second problem, DFMT captures session events (file edits, tasks, decisions, git operations, errors) from multiple independent sources, persists them to an append-only JSONL journal, and produces token-budgeted snapshots that the agent can retrieve after a compaction or at the start of a fresh session. The agent resumes with a working picture of what was happening, without re-asking the user.

Both capabilities run in the same daemon, share the same storage and search infrastructure, and are reachable over MCP, Unix socket, HTTP, and CLI. The daemon is per-project, auto-started on demand, and idle-exits when unused. It ships as a single static Go binary with no runtime dependencies beyond the OS kernel, installable with one command on any developer machine.

A companion command, `dfmt setup`, detects installed AI agents (Claude Code, Cursor, Codex CLI, Gemini CLI, VS Code Copilot, OpenCode, Zed, Continue.dev, Windsurf, and any MCP-capable client) and writes the appropriate configuration for each — MCP server registration, intercept hooks where supported, and tool-preference instruction files — making DFMT usable across a developer's toolkit without per-agent manual setup.

---

## 2. Problem Statement

AI coding agents operate under a finite context window. Two distinct failure modes consume that budget:

**Failure mode 1: tool output floods the window during a session.**

Every tool call — file read, shell command, web fetch, DOM snapshot, API response — returns its full raw output directly into the context window, where it stays for the remainder of the session. A single access log (~45 KB), a browser snapshot (~56 KB), or a listing of recent issues (~60 KB) each consume a meaningful fraction of the available budget, usually to answer a question for which a two-sentence summary would have sufficed. By the time an hour of real work has passed, a large share of the context is stale intermediate data the agent no longer needs but cannot forget.

**Failure mode 2: working state is lost at session boundaries.**

When the context fills up, the agent compacts the conversation: older messages are dropped or summarized to free space. In that process, the working state of the session goes with them — which files were being edited, what tasks are in progress, what decisions the user made, and what the user last asked for. Even if the user opens a fresh conversation deliberately, the same information loss happens in a more visible way. The agent then either re-asks the user, or silently drifts from the intended work.

The two failure modes reinforce each other. Tool output bloat accelerates compaction. Compaction destroys the state that would have made a fresh retrieval of that same data unnecessary. A developer working with AI assistance for several hours finds the agent asking the same questions it asked an hour ago.

Solving either problem alone leaves the other one intact. Solving both at once — with a single local daemon that sits between the agent and its tools, and that remembers what happened — addresses the root cause.

Doing this well requires three properties:

1. **Agent independence.** Every AI agent has a different extension model: different hook points, different tool-calling conventions, different session-lifecycle events. A solution that depends on one specific agent's extension surface covers only that agent's users and breaks when the agent changes.
2. **Runtime portability.** Developer tools that require non-Go runtimes, native libraries, CGo-bound databases, or platform-specific build steps hit friction — especially on minimal environments (Alpine containers, restricted workstations, CI runners). A session-memory and sandboxed-tool daemon should install with one command on any developer machine.
3. **Transport flexibility.** AI agents are the primary consumer, but not the only one. Editor extensions, CI jobs, terminal tools, and custom scripts also benefit from access to session memory and sandboxed execution when the interface supports them. Coupling to one transport (e.g., stdio-only) forecloses those uses.

DFMT is designed to satisfy all three properties across both failure modes.

---

## 3. Goals and Non-Goals

### 3.1 Goals

- **G1. Agent-agnostic capture.** The system captures useful events even with zero agent integration. A developer editing files and making commits produces a reconstructable session.
- **G2. Agent-agnostic retrieval.** Snapshots are requestable over MCP, HTTP, Unix socket, or CLI. Any client can build against any channel.
- **G3. Zero-deployment distribution.** A single static binary, no shared libraries, no package manager beyond `go install` or a release download.
- **G4. Byte-budgeted output.** Consumers specify a byte budget; DFMT returns the best snapshot that fits under that cap. Bytes are measured on the rendered output (UTF-8). Token-based budgets are not primary — clients that need token counts convert bytes to tokens on their side using their model's tokenizer.
- **G5. Inspectable storage.** The journal is human-readable (JSONL). A developer debugging a session can `cat`, `grep`, and `jq` against it with standard tools.
- **G6. Deterministic performance.** Query latency under 5 ms p99 for 10,000 events. Snapshot build under 10 ms. Write latency under 1 ms.
- **G7. Privacy by default.** Nothing leaves the host unless the user opts in. No telemetry, no cloud, no accounts.
- **G8. Strict dependency hygiene.** Pure Go stdlib plus `golang.org/x/sys`, `golang.org/x/crypto`, and `gopkg.in/yaml.v3`. Nothing else.
- **G9. Tool output compression.** A sandboxed tool layer keeps raw outputs (shell, file read, HTTP fetch) out of the agent's context window. Large outputs are indexed locally and returned as summaries or intent-matched excerpts. Target: ≥90% reduction versus passing raw output through.
- **G10. One-command multi-agent adoption.** A single `dfmt setup` command detects installed AI agents, writes the appropriate MCP, hook, and instruction configuration for each, and produces a working integration without manual per-agent steps.

### 3.2 Non-Goals

- **NG1. Not a full RAG system.** DFMT handles session memory and in-session content capture, not a knowledge base over source code. Code search is out of scope; use `ripgrep` or a dedicated indexer.
- **NG2. Not a replacement for the MCP protocol.** DFMT exposes an MCP surface but does not implement every MCP capability (prompts, resources, sampling).
- **NG3. Not a multi-user system.** DFMT is single-user, single-machine, per-project. Team sharing is explicitly out of scope for v1.
- **NG4. Not cloud-syncing by default.** Remote backends are an opt-in extension, not part of the core.
- **NG5. Not a container runtime.** The sandbox layer (see §7.5) uses OS subprocess isolation only. No namespaces, no seccomp, no cgroups manipulation. Users who need stronger isolation should run DFMT itself inside a container.

---

## 4. Design Principles

**P1. Capture and transport are orthogonal.** Any capture source can feed any transport. Adding a new agent platform adds a capture adapter, not a rewrite.

**P2. The journal is the source of truth.** Every derived artifact (index, snapshot, stats) is rebuildable from the journal alone. If the journal is intact, the system recovers.

**P3. Append-only at the storage layer.** No in-place mutations. Deletions are tombstones. This makes crash-safety trivial and enables time-travel.

**P4. Pull over push for the retrieval API.** Consumers ask for what they need, with the budget they have. DFMT never pushes unsolicited state into a conversation.

**P5. Plaintext by default.** JSONL, YAML config, XML snapshots. A curl and a text editor must be enough to debug any state.

**P6. One writer, many readers.** A single daemon owns writes. Clients go through the daemon over socket/HTTP/MCP. No multi-writer locking dance.

**P7. Fail open on capture, fail closed on retrieval.** A broken FS watcher must not kill writes from MCP. An incomplete index must return an error, not stale results.

**P8. Stdlib first, then `x/sys`, then `yaml.v3`. No other dependencies.** Every external dependency is a maintenance liability. Porter stemming fits in 400 lines. BM25 is 30 lines. FSEvents/inotify/ReadDirectoryChangesW are available through `x/sys`.

---

## 5. System Architecture

DFMT is organized in five horizontal layers, with strict interfaces between them.

```
┌────────────────────────────────────────────────────────────────┐
│  TRANSPORT                                                     │
│    MCP stdio    │    HTTP :auto   │   Unix socket   │   CLI    │
├────────────────────────────────────────────────────────────────┤
│  SANDBOX (§7.5)                                                │
│    exec runtimes (bash, node, python, go, ...)                 │
│    file reader (summarize + index)                             │
│    URL fetcher (HTML/JSON/text → markdown chunks)              │
│    security policy (deny/allow, credential passthrough)        │
├────────────────────────────────────────────────────────────────┤
│  RETRIEVAL                                                     │
│    Snapshot builder (budget-aware, priority-tiered)            │
│    Search (BM25 → trigram → Levenshtein)                       │
│    Content search (same infra, ephemeral store)                │
├────────────────────────────────────────────────────────────────┤
│  CORE                                                          │
│    Event journal (JSONL append-only, flock)                    │
│    Event index (inverted, in-memory, gob-persisted)            │
│    Content store (ephemeral, chunked, in-memory)               │
│    Classifier (priority tier assignment)                       │
│    Stemmer (Porter, bundled)                                   │
├────────────────────────────────────────────────────────────────┤
│  CAPTURE                                                       │
│    MCP tool │ FS watcher │ Git hook │ Shell hook │ CLI         │
└────────────────────────────────────────────────────────────────┘
```

Data flow: capture → core/events (writes), sandbox → core/content (writes), transport → retrieval → core (reads). The event path and content path share infrastructure (tokenizer, Porter, BM25) but have separate storage: events are durable and session-spanning; content is ephemeral and query-local.

The **daemon process** owns the core and sandbox layers and hosts all transports. Capture sources run either in-process (MCP, CLI over stdio) or as external processes that speak to the daemon over the socket (FS watcher subprocess, git hook scripts, shell precmd).

---

## 6. Data Model

### 6.1 Event

The fundamental unit is the **event**. An event is an immutable, timestamped, typed record of something that happened during a session.

```
type Event struct {
    ID       string          // ULID, 26 chars, monotonic within daemon
    TS       time.Time       // RFC3339Nano, UTC
    Project  string          // absolute path to project root
    Type     EventType       // see 6.2
    Priority Tier            // see 6.3
    Source   Source          // "mcp" | "fswatch" | "githook" | "shell" | "cli"
    Actor    string          // agent identifier if known ("claude-code", "cursor", ...)
    Data     map[string]any  // type-specific payload, schema below
    Refs     []string        // related event IDs
    Tags     []string        // free-form, user-assigned
    Sig      string          // SHA-256 of canonical JSON, first 16 hex chars (integrity)
}
```

On disk, each event is one line of JSON in the journal. The ULID provides a monotonic, lexicographically sortable ID with millisecond precision and per-millisecond randomness. The `Sig` field allows detection of accidental corruption and manual edits.

### 6.2 Event Types

| Type | Meaning | Typical Source | Default Tier |
| --- | --- | --- | --- |
| `file.read` | Agent read a file | MCP, FS | P2 |
| `file.edit` | File was modified | MCP, FS | P1 |
| `file.create` | File was created | MCP, FS | P1 |
| `file.delete` | File was deleted | MCP, FS | P1 |
| `task.create` | New task registered | MCP, CLI | P1 |
| `task.update` | Task status or body changed | MCP, CLI | P1 |
| `task.done` | Task completed | MCP, CLI | P1 |
| `decision` | User-stated preference or correction | MCP | P1 |
| `error` | Tool failure, non-zero exit, panic | MCP, Shell | P2 |
| `git.commit` | Commit made | git hook | P2 |
| `git.checkout` | Branch change | git hook | P2 |
| `git.push` | Push succeeded | git hook | P2 |
| `git.stash` | Stash operation | git hook | P3 |
| `git.diff` | Large diff observed (>N lines) | git hook, MCP | P3 |
| `env.cwd` | Working directory changed | Shell | P2 |
| `env.vars` | Relevant env var changed | Shell | P2 |
| `env.install` | Package install (npm, go, pip, cargo) | Shell, MCP | P2 |
| `prompt` | User message submitted to agent | MCP | P1 |
| `mcp.call` | MCP tool invoked | MCP | P3 |
| `subagent` | Delegated work started or finished | MCP | P3 |
| `skill` | Skill or slash command invoked | MCP | P3 |
| `role` | Persona or behavioral directive set | MCP | P3 |
| `intent` | Session mode classification | MCP | P4 |
| `data.ref` | Large user-pasted data referenced | MCP | P4 |
| `note` | User-authored note (via `dfmt note`) | CLI | P2 |
| `tombstone` | Logical deletion of prior event | CLI, MCP | — |

The type namespace is hierarchical (dot-separated). New types are added by appending; existing types are never repurposed. A consumer encountering an unknown type MUST preserve it and MAY ignore it.

### 6.3 Priority Classification

Priority is a four-level enum used to order events during snapshot construction under a byte budget:

- **P1 Critical** — the absence of this event meaningfully breaks resumption. Last user prompt, active task, last file edited, user decisions, project rules.
- **P2 High** — valuable state whose absence degrades resumption but does not break it. Recent git ops, unresolved errors, environment changes.
- **P3 Normal** — useful color. MCP tool histograms, subagents, skills, role.
- **P4 Low** — informational. Session intent, data references.

Classification is performed at capture time by the **classifier** component (see 8.1.5). The defaults in the table above can be overridden per-project by a YAML rules file.

### 6.4 Journal Format

The journal is a single file: `<project>/.dfmt/journal.jsonl`.

Each line is a canonical JSON encoding of one `Event`, followed by `\n`. Fields are emitted in a fixed order (ID, TS, Project, Type, Priority, Source, Actor, Data, Refs, Tags, Sig). Canonical encoding ensures the `Sig` field is reproducible.

The file is opened with `O_APPEND | O_CREATE | O_WRONLY` and protected by an advisory `flock` (`LOCK_EX`) for the duration of each write. `fsync` is called after each append in durable mode (default), or deferred by `max_batch_ms` in fast mode.

A **journal segment** is rotated when it exceeds `journal.max_bytes` (default 64 MB). Rotated segments are named `journal.<ULID>.jsonl.gz` and gzip-compressed. The active file is always `journal.jsonl`. Segments are immutable.

### 6.5 Index Format

The inverted index is an in-memory structure:

```
type Index struct {
    Terms      map[string]PostingList // term -> sorted event IDs
    DocLengths map[string]int          // event ID -> token count
    AvgDocLen  float64
    N          int                     // total events
}

type PostingList struct {
    IDs    []string  // sorted, ULID order
    TFs    []uint16  // term frequencies
}
```

The index is rebuilt from the journal on daemon start. After a full rebuild, the index is serialized to `index.gob` via `encoding/gob`. Subsequent starts load the snapshot and replay any journal entries newer than the snapshot's high-water mark. Rebuild from scratch of 10,000 events takes under 200 ms on a 2023 laptop; loading the gob snapshot takes under 50 ms.

The index is rebuilt in full every `index.rebuild_interval` (default 1 hour) while the daemon runs, to reclaim tombstoned entries and renormalize `AvgDocLen`. The rebuild happens in a goroutine; queries continue against the old index until the new one is swapped in atomically via pointer replacement.

### 6.6 Project Identity and Daemon Scope

A project is identified by the absolute path of its root. Discovery rules, in order:

1. If `DFMT_PROJECT` is set, use its value.
2. Else walk upward from CWD until a `.dfmt/` directory is found.
3. Else walk upward from CWD until a `.git/` directory is found.
4. Else use CWD.

**Monorepo handling.** Rule 2 fires before rule 3, so monorepo sub-projects with their own `.dfmt/` are discovered correctly even when a parent `.git/` exists. To opt into per-sub-project isolation, the user runs `dfmt init --sub` inside the subdirectory; this creates a `.dfmt/` there, scoping discovery below it without touching the parent. A monorepo structured like this:

```
my-monorepo/
├── .git/
├── .dfmt/              # parent project, optional
├── apps/
│   ├── web/
│   │   └── .dfmt/      # sub-project (added via `dfmt init --sub`)
│   └── api/
│       └── .dfmt/      # sub-project
└── packages/
    └── core/           # no .dfmt/, events flow up to parent
```

…yields one daemon per `.dfmt/` directory present. A terminal opened in `apps/web/` discovers `apps/web/.dfmt/`; a terminal opened in `packages/core/` walks up, finds `my-monorepo/.dfmt/` (if present) or `.git/` (if not).

**Shared capture across sub-projects is not a design goal.** Two sub-projects running daemons are two independent universes; events from one do not flow to the other. This matches ADR-0001's per-project-daemon position. Users who want a single session memory across the whole monorepo simply don't run `dfmt init --sub` — one `.dfmt/` at the monorepo root covers everything.

**DFMT runs one daemon per project.** The daemon process is bound to a single project root at startup and never serves another. Its Unix socket, PID file, and log file all live inside the project's `.dfmt/` directory. Cross-project state is deliberately absent; a second project running concurrently runs an entirely separate daemon process with its own memory, index, and socket.

A global registry lives at `$XDG_DATA_HOME/dfmt/projects.jsonl`. It maps each known project path to a short ID (first 8 chars of the SHA-256 of the path) and records whether a daemon is currently running for it. The registry is used only by enumeration commands (`dfmt list`). No operational state is kept globally; losing the registry means `dfmt list` misses projects until they are next touched, but no capture or retrieval is affected.

### 6.7 Content Chunks

Separate from events, DFMT also stores **content chunks** produced by sandboxed tool executions. Where an event records "what happened," a content chunk records "the bytes that came out of what happened."

```
type Chunk struct {
    ID       string           // ULID, matches producing event when applicable
    ParentID string           // chunk set identifier: shared by chunks from the same output
    Index    int              // position within the parent's sequence
    Kind     ChunkKind        // "markdown", "code", "json", "text", "log-lines"
    Heading  string           // if kind=markdown, the heading of this section
    Lang     string           // if kind=code, the language tag
    Path     string           // if kind=json, the key path
    Body     string           // the chunk content, UTF-8
    Tokens   int              // rough token count
    Created  time.Time
}

type ChunkSet struct {
    ID       string           // parent ID
    Kind     string           // "exec-stdout" | "file-read" | "fetch" | ...
    Source   string           // command or path or URL that produced it
    Intent   string           // if provided by the caller at creation
    Chunks   []string         // chunk IDs in order
    Created  time.Time
    TTL      time.Duration    // auto-prune after this (default: daemon lifetime)
}
```

Chunks live in the **content store**, which is distinct from the event journal:

- **Lifecycle.** Chunks are ephemeral by default — they live only for the daemon's lifetime. Optional persistence (`ttl: forever` in the producing call) writes them to `.dfmt/content/` as gzipped JSONL, scoped by chunk-set ID.
- **Size.** The content store is bounded (default: 64 MB resident, configurable). When full, the least-recently-used chunk set is evicted.
- **Index.** Chunks share the same inverted-index infrastructure as events, in a separate index instance. Search queries can target events, content, or both.

This separation matters. Events are the permanent record of a session's shape; their storage must be durable, compact, and inspectable. Content is the raw bulk output of tools; its storage must be capacious and fast, and it should not pollute the permanent record when the session ends.

See §7.5 for how sandboxed tools produce chunks, and ADR-0007 for the reasoning behind the separation.

---

## 7. Component Specifications

### 7.1 Core Layer

#### 7.1.1 Journal

Responsibilities: append events, stream events (from a cursor), flush, rotate, recover.

Interface (Go):

```
type Journal interface {
    Append(ctx context.Context, e Event) error
    Stream(ctx context.Context, from string) (<-chan Event, error) // from = ULID cursor
    Checkpoint(ctx context.Context) (string, error)                // returns current cursor
    Rotate(ctx context.Context) error
    Close() error
}
```

Durability modes:

- **durable** (default): `O_SYNC` on open, `fsync` after each append. ~500 writes/sec on SSD.
- **batched**: group fsyncs over `max_batch_ms` (default 50 ms). ~50,000 writes/sec, bounded data loss on crash.

The durability mode is a per-project config setting. DFMT defaults to durable; agents that need throughput (e.g., high-frequency FS watcher) can request batched at startup.

**Critical-event override.** Regardless of the configured mode, the following event types **always fsync immediately** and never batch:

- `decision` — user corrections and preferences are high-value and low-volume; losing the last 50 ms of these to a crash defeats the session-memory promise.
- `prompt` — the user's most recent message is the anchor for resumption snapshots.
- `task.create`, `task.done` — task state is structurally important for resumption.
- `error` — unresolved errors should not vanish on a crash that might be related.
- `role`, `rules` — role and project-rules events that configure the agent's behavior.

Other types (`file.edit`, `git.*`, `mcp.call`, `env.*`, `data.ref`) honor the configured mode. This hybrid default keeps the critical session-memory content safe while allowing high-throughput sources to amortize fsync cost. The override is not configurable — critical events are always durable by DFMT's design contract.

In-flight batched events pending fsync are also flushed immediately when any critical event arrives, so the durability of a critical event implies the durability of all events written before it.

Recovery: on startup, the journal is scanned linearly. Any line that fails to parse, or whose `Sig` does not match, is logged and placed in a `journal.corrupt.jsonl` quarantine file. The daemon starts with the last valid cursor.

#### 7.1.2 Indexer

Responsibilities: tokenize an event's searchable text, update the inverted index, maintain document-length statistics.

Searchable text for an event is constructed by concatenating (a) selected fields from `Data` according to a per-type extractor, (b) `Tags`, (c) `Type`. The extractor is a small table in `core/index.go` mapping event type to a function that returns a string slice.

Tokenization pipeline:

1. Lowercase.
2. Split on `[^\p{L}\p{N}_]+` (Unicode letter, number, underscore).
3. Drop tokens shorter than 2 or longer than 64.
4. Drop stopwords (small English + Turkish stoplist, configurable).
5. Porter-stem each token (English stemmer; Turkish tokens pass through unchanged in v1).
6. Emit both stemmed token and all its 3-grams to separate posting lists.

The two posting lists (stemmed terms and trigrams) support the three-layer retrieval fallback (BM25 on stems → substring on trigrams → Levenshtein correction).

#### 7.1.3 BM25 Scorer

Okapi BM25 with parameters `k1 = 1.2`, `b = 0.75` (Lucene defaults, overridable in config).

For a query `Q = {q1, ..., qn}` and document `D`:

```
score(Q, D) = Σ IDF(qi) * (f(qi, D) * (k1 + 1)) / (f(qi, D) + k1 * (1 - b + b * |D| / avgdl))

IDF(qi) = ln((N - df(qi) + 0.5) / (df(qi) + 0.5) + 1)
```

Query tokens are processed through the same tokenization pipeline as documents. The scorer returns the top-K documents by score, filtered by an optional minimum score threshold. Heading-like tokens (all-caps, or starting an event's searchable text) are boosted by a factor of 5 at index time (term frequency multiplier); the boost is empirically tuned to favor navigational queries ("decision about X") over content-density queries.

#### 7.1.4 Stemmer

Porter's 1980 algorithm for English, bundled as a single ~400-line pure Go file. No stemming for non-ASCII tokens in v1. A pluggable `Stemmer` interface allows dropping in Snowball or a Turkish-specific stemmer in a later version without changing callers.

#### 7.1.5 Classifier

Assigns a `Priority` tier to an event at capture time. Default rules are the table in 6.2. A per-project YAML file `priority.yaml` may override:

```
overrides:
  - match:
      type: "file.edit"
      path_glob: "internal/dns/**"
    priority: p1
  - match:
      type: "git.commit"
      message_regex: "^wip"
    priority: p3
```

Rules are evaluated top-to-bottom; first match wins. If no override matches, the default applies.

### 7.2 Capture Layer

Each capture source is a `Capturer` that produces events and forwards them to the core. Capturers are independent; any can be disabled in config.

```
type Capturer interface {
    Name() string
    Start(ctx context.Context, sink EventSink) error
    Stop(ctx context.Context) error
}

type EventSink interface {
    Submit(ctx context.Context, e Event) error
}
```

#### 7.2.1 MCP Capture

Runs as part of the daemon's MCP transport. When the agent calls `dfmt.remember`, the capturer builds an event and submits it. This is the primary capture for `decision`, `prompt`, `role`, `intent`, and `data.ref` events, which require language understanding to extract.

The MCP capture also exposes a tool-wrapper mode: an agent can route its outbound tool calls through `dfmt.log_tool_call(name, input, output)`, producing `mcp.call`, `subagent`, and `skill` events. This is optional and enabled only if the agent (or its hook config) chooses to use it.

#### 7.2.2 FS Watcher

A subprocess `dfmt watch` (or an in-daemon goroutine, configurable) using `x/sys/unix.Inotify*` on Linux, `x/sys/unix.Kqueue` on macOS and BSD, and `x/sys/windows.ReadDirectoryChangesW` on Windows.

Watches are computed from a glob list in `config.yaml`:

```
capture:
  fs:
    watch:
      - "**/*.go"
      - "**/*.md"
      - "**/*.ts"
      - "**/*.sql"
    ignore:
      - "node_modules/**"
      - ".git/**"
      - "dist/**"
      - ".dfmt/**"
    debounce_ms: 500
```

Emits `file.edit`, `file.create`, `file.delete` events. Debounces rapid sequences (a single `git checkout` can trigger thousands of events in milliseconds; these are coalesced into one `git.checkout` event if the git-hook capturer is also active, or into grouped `file.edit` events otherwise).

The FS watcher is the key to agent-independence. Even with zero MCP integration, `file.edit` events flow, which means the resumption snapshot always contains "files touched recently."

**OS resource-limit handling.** Filesystem watch APIs have per-user/per-process limits: Linux inotify defaults to 8192 watches per user (via `/proc/sys/fs/inotify/max_user_watches`); macOS kqueue requires one file descriptor per watched directory and shares the process FD limit (default 256–10240); Windows ReadDirectoryChangesW has no hard watch count but each handle consumes kernel resources. A large monorepo with deep `**` glob patterns can exhaust these limits.

DFMT handles this in three tiers:

1. **At startup**, the FS watcher probes the OS limit (`/proc/sys/fs/inotify/max_user_watches` on Linux; `ulimit -n` on macOS/BSD). It counts how many directories match the `watch` globs. If the count exceeds 70% of the limit, it emits a `warning` log line and — on Linux — a remediation hint pointing to how to raise `max_user_watches` via sysctl.
2. **During operation**, if an `ENOSPC` error occurs adding a watch, the FS watcher switches to **polling mode** for the offending subtree: rather than inotify watches, it stats matching files at an interval (default 5 s, configurable via `capture.fs.poll_interval`) and synthesizes `file.edit` events from mtime changes. Polling is slower and less precise but guarantees no events are silently lost.
3. **Reports status** through `dfmt doctor` and the `dfmt.stats` endpoint: the output shows `watches_active`, `watches_requested`, `polling_subtrees`, and any ENOSPC events since daemon start. Users can see at a glance whether their watcher is operating in degraded mode.

Partial degradation — some subtrees watched via inotify, others via polling — is always preferable to either silent failure or a daemon that refuses to start. Polling is never the default because it misses sub-interval changes and consumes more CPU; it is a fallback, not a first choice.

#### 7.2.3 Git Hook

`dfmt install-hooks` writes small shell scripts into `.git/hooks/`:

- `post-commit` → `dfmt capture git commit "$(git log -1 --format=%H %s)"`
- `post-checkout` → `dfmt capture git checkout "$PREV $NEW $IS_BRANCH"`
- `pre-push` → `dfmt capture git push ...`
- `post-merge` → `dfmt capture git merge ...`

Each script is idempotent, calls `dfmt capture` over the Unix socket, and exits in under 20 ms. If the daemon is not running, the script degrades to a silent no-op; no git operation is ever blocked by DFMT.

Hooks are versioned via a comment header. `dfmt install-hooks` follows this decision tree when encountering an existing hook file:

- **No existing hook:** write DFMT's hook.
- **Existing hook with current DFMT version marker:** no-op (idempotent).
- **Existing hook with older DFMT version marker:** regenerate from the latest template.
- **Existing hook with foreign content (no DFMT marker):** DFMT detects known hook managers — Husky (`.husky/` script references), lefthook (`lefthook` binary reference or config header), pre-commit (`pre-commit-framework` signature), git-flow, commitlint — and **chains** rather than overwriting. The existing hook is renamed to `<hook>.user`, and DFMT's hook is written with a trailing `sh "$(dirname "$0")/<hook>.user" "$@"` invocation so the user's original hook runs after DFMT's capture. This preserves both behaviors.
- **Foreign content DFMT cannot recognize:** refuse to write. Exit with a message explaining the situation and the `--force` flag (which backs up the foreign hook to `<hook>.dfmt-backup-<ISO>` and writes DFMT's hook alone).

Chain order matters: DFMT's hooks are non-blocking and background-safe (they exit ~20 ms whether the daemon is running or not), so running them first adds negligible latency to the user's existing hook chain. User hooks that fail with non-zero exit codes do propagate that failure to git — DFMT never masks a user-hook failure.

`dfmt install-hooks --uninstall` reverses the operation: if a `<hook>.user` exists, it is renamed back to `<hook>`. If no chained user hook exists, the DFMT hook is simply removed.

#### 7.2.4 Shell Integration

Optional. `dfmt shell-init zsh >> ~/.zshrc` emits a small snippet using `precmd` and `chpwd` to emit `env.cwd`, `error` (on non-zero `$?`), and `env.install` (on matching command patterns).

```
# zsh snippet, emitted by `dfmt shell-init zsh`
autoload -U add-zsh-hook
_dfmt_precmd() {
  local st=$?
  [[ $st -ne 0 ]] && dfmt capture error "exit=$st cmd=$(fc -ln -1 | tr -d '\n')" 2>/dev/null
}
_dfmt_chpwd() { dfmt capture env.cwd "$PWD" 2>/dev/null; }
add-zsh-hook precmd _dfmt_precmd
add-zsh-hook chpwd _dfmt_chpwd
```

Equivalent snippets exist for bash and fish. PowerShell integration is deferred to v1.1.

#### 7.2.5 CLI Capture

`dfmt remember <type> <body>`, `dfmt note <body>`, `dfmt task <body>`, etc. The CLI is both a capture source and a control plane. See section 15.

#### 7.2.6 Cross-Source Event Deduplication

Multiple capture sources can observe the same real-world event. A developer edits `main.go` in their editor: the FS watcher sees a `file.edit` via inotify. If they edited it through the AI agent calling `dfmt.exec sed -i ...`, the sandbox layer also produces a `file.edit` event. Both report the same event; we must not produce two journal entries.

**Deduplication policy:**

1. Every incoming event computes a **dedup key** from `(type, primary_subject, coarse_ts)`:
   - `file.edit`: `(file.edit, absolute_path, ts_rounded_to_2s)`
   - `git.commit`: `(git.commit, commit_hash, —)` — commit hash is unique; no timestamp needed
   - `git.checkout`: `(git.checkout, target_ref, ts_rounded_to_2s)`
   - `env.cwd`: `(env.cwd, new_path, ts_rounded_to_5s)`
   - Other types: `type` + primary data field + coarse timestamp (2–10 s bucket depending on type)
2. The daemon maintains a **recent-events bloom filter** sized for 10,000 events over a rolling 60-second window. On submit, the dedup key is checked against the filter.
3. On hit: the newer event is **merged** into the existing one rather than appended:
   - Sources combine (`sources: ["fswatch", "mcp"]` instead of one).
   - Data maps merge (keys from the later event not in the earlier one are added; existing keys preserved).
   - No second journal entry is written.
4. On miss: the event is written normally and added to the filter.
5. Source priority for merged events: `mcp > shell > githook > fswatch > cli`. Higher priority means the primary source on the merged record; lower-priority additional sources are listed but don't override `Data` fields the higher-priority source set.

**Why bloom filter:** events arrive at up to 100/second during bursts (a `git checkout` touching hundreds of files). A precise dedup cache would require a hash map of recent events sized for burst, with its own memory budget. A bloom filter answers "is this key possibly already seen?" in O(1) with bounded memory (under 100 KB for a 10k-element filter at 0.01% false-positive rate). False positives cause a rare legitimate second-observation to be dedup'd instead of appended — acceptable; the filter's 60 s window means the lost observation reappears on the next action if it matters.

**Coalescing windows for bursty events:**

- A `git.checkout` event opens a 2-second coalescing window. FS edit events during the window are not recorded individually; they are counted into the checkout event's `Data.files_touched`.
- A `git.merge` or `git.rebase` opens an equivalent window of 10 seconds (merges can touch files over a longer interval).
- A `env.install` (package manager detected in shell) opens a 60-second window during which edits in `node_modules/`, `vendor/`, `.venv/`, `target/` are suppressed.

Coalescing is implemented as an in-memory flag consulted by the FS watcher before submitting events.

### 7.3 Retrieval Layer

#### 7.3.1 Snapshot Builder

Takes a byte budget and a format, and returns the best snapshot that fits under the budget.

Algorithm:

1. Determine session window (default: events since the last `prompt` event, or the last 24 hours, whichever is smaller).
2. Pull all events in the window from the journal.
3. Group by priority tier.
4. Within each tier, apply **deduplication**: multiple `file.edit` events on the same path within the window collapse to one "edited N times, last at T" event.
5. Pack events into the budget in tier order (P1 first, then P2, then P3, then P4). Greedy fill; break when next event would exceed budget.
6. Always include the last `prompt` event regardless of budget (reserved bytes).
7. Render in the requested format.

Formats:

- **xml** — structured `<session_snapshot>` with `<tier>` and `<event>` elements. Default for Anthropic-family agents. DTD in Appendix A.
- **json** — JSON object with the same structure. For programmatic consumers.
- **md** — human-readable markdown with H2 sections per tier. For CLI/TUI display.
- **guide** — a prose "Session Guide" rendering organized into narrative sections (last request, active tasks, key decisions, files modified, unresolved errors, git activity, project rules, MCP tools used, subagent work, skills invoked, environment, data references, session intent, user-assigned role). Suitable for direct injection into a model's context after a compaction or fresh-session boundary.

The snapshot is cached in `.dfmt/cache/snapshot-<hash>.<ext>` keyed by `(format, budget, session_window_end)`.

**Cache invalidation.** Each new event marks the cache as stale. But during event bursts (a `git checkout` landing 500 `file.edit` events in 100 ms), naive per-event invalidation would force a full rebuild on the next `dfmt.recall`, and 500 sequential events would each trigger rebuild potentially if recall were also bursty. Two protections are in place:

1. **Lazy rebuild.** Invalidation is a flag flip. The cache is rebuilt only on the next `dfmt.recall` call, not during the burst itself. 500 events flip the flag once (idempotently).
2. **Event batching window.** If a `dfmt.recall` arrives within 200 ms of an event that invalidated the cache, the builder waits for the window to complete before rebuilding. This prevents the classic "client hammers recall during a burst" scenario from producing 500 rebuilds for eventual-convergence data.

The 200 ms window is configurable via `retrieval.rebuild_debounce_ms`. Setting it to 0 disables batching for users who want strictly fresh snapshots at the cost of burst-time rebuild overhead.

#### 7.3.2 Search Engine

Query handler. Takes a query string, returns ranked results.

Three-layer fallback:

1. **BM25 on stemmed terms.** Default. Returns results above the minimum score.
2. **Trigram substring.** Activated if layer 1 returned fewer than N results. Queries the trigram posting list, ranks by overlap.
3. **Fuzzy correction.** Activated if layer 2 returned fewer than N results. Runs Levenshtein (distance ≤ 2) against the term dictionary, substitutes corrected terms, re-runs layer 1.

Each layer returns results tagged with its layer number so the UI can explain why a result matched. Results from higher layers (worse quality) are appended below layer-1 results, never interleaved.

Progressive throttling protects the budget of the caller's context window against runaway search loops: the first three queries in a session return the full top-K; calls 4–8 return top-K/2 with a warning in the response metadata; calls 9+ return a redirect to `dfmt.batch_search` so the caller can consolidate its queries. Throttle counts reset whenever a new `prompt` event arrives — a new user turn is always treated as a fresh search budget.

### 7.5 Sandbox Layer

The sandbox layer runs operations on behalf of the agent — shell commands, file reads, HTTP fetches — without the raw output entering the agent's context window. Large results are chunked into the content store (§6.7) and returned as a summary with a handle for on-demand retrieval; small results are returned inline.

The layer is the direct answer to Failure Mode 1 in §2. When an AI agent uses `dfmt.exec` instead of its native Bash tool, the 56 KB of stdout from a `playwright snapshot` stays in the daemon's content store and the agent's context sees only "page titled 'Login', 14 forms, 23 links" plus a search vocabulary.

#### 7.5.1 Execution Model

Each `dfmt.exec` invocation spawns a subprocess with the specified runtime (bash, node, python, go, ruby, perl, php, R, elixir, or raw shell). The subprocess inherits:

- The current working directory of the agent's project.
- A curated set of environment variables. By default: `HOME`, `USER`, `PATH`, `LANG`, `TERM`, plus any `DFMT_EXEC_*` prefixed vars the user has set, plus credential-passthrough vars for common authenticated CLIs (see 7.5.4).
- A signed `DFMT_JOB_ID` variable identifying this exec for audit logs.

It does not inherit:

- Unrelated environment variables (API keys the user has in their shell but not marked for passthrough).
- Parent process file descriptors other than the three standard streams.
- Signals from the parent except `SIGTERM` on daemon shutdown.

Stdout and stderr are captured separately. Exit code is captured. Resource limits (wall time, CPU time, max memory, max output bytes) are enforced by setrlimit on Unix and job objects on Windows.

**Concurrency.** The daemon enforces a per-project concurrent-exec cap (`sandbox.exec.max_concurrent`, default 4). Incoming exec/read/fetch requests over the cap wait in a bounded queue up to `sandbox.exec.queue_timeout` (default 30 s); queue-wait timeouts return HTTP 429 / MCP error `rate_limited` with a `Retry-After` hint. Fetch has an independent cap (`sandbox.fetch.max_concurrent`, default 4). The caps are per-daemon-per-project, not global; one daemon per project is the architecture (ADR-0001) so this is effectively a per-project limit. Security policy evaluation is synchronous before the subprocess is spawned, so rules cannot be raced.

#### 7.5.2 Output Handling

When the execution finishes, the daemon handles the output based on size and intent:

- **Small output (< `exec.inline_threshold`, default 4 KB)** returns verbatim as the response body.
- **Medium output (4–64 KB)** is stored in a chunk set and returned as: first 1 KB + summary ("exit=0, 1823 lines, 14 warnings, top phrases: ...") + chunk-set ID for retrieval via `dfmt.search_content` or `dfmt.get_chunk`.
- **Large output (> 64 KB, or always if `intent` is provided)** is indexed immediately. The response is: intent-matched excerpts from the indexed chunks + a vocabulary of searchable terms + chunk-set ID. The full raw output is never returned.

The caller can override these thresholds per call: `return: "raw"` forces raw return (up to `exec.max_raw_bytes`, default 256 KB); `return: "summary"` forces summary even for small output.

**Stdout and stderr are treated as separate streams.** Each goes through the size disposition independently: stdout may return inline while stderr is chunked, or vice versa. When chunking occurs, stdout and stderr produce two distinct chunk sets, each with its own set ID, and both carry a common `parent_exec_id` field linking them to the originating exec call. The `ExecResp` carries `stdout_chunk_set` and `stderr_chunk_set` fields when applicable; inline returns use `stdout` and `stderr` string fields. Callers can `search_content` against either set or use `scope: parent_exec_id:<id>` to search across both streams of one exec.

**Execution model: batch in v1, streaming in v1.1.** An exec call blocks until the subprocess exits or the wall timeout fires (default 60 s). During execution, no partial output is returned to the caller — full stdout and stderr are captured, size-dispositioned, and returned in a single response. This matches the JSON-RPC request-response semantics that MCP and Unix socket transport use.

Long-running commands (`npm install`, `docker build`, integration test suites) often exceed 60 s. DFMT's handling:

- **For commands the caller expects to exceed the default timeout**, the caller passes `timeout` explicitly (up to `sandbox.exec.wall_timeout` config ceiling, default 300 s / 5 minutes).
- **For timeouts that fire mid-execution**, the subprocess is terminated (SIGTERM, then SIGKILL after 3 s grace). Captured output up to that moment is returned with `exit: -1` and `timed_out: true`. Partial output is still chunked and indexed — the caller sees "what happened before the timeout" rather than nothing.
- **For genuinely long processes** (a 10-minute build), the user can raise the per-project ceiling in `config.yaml`. DFMT does not arbitrarily cap what the user configures; the 5-minute default is a safety net, not a limit.

**v1.1 streaming mode** (specified here, implemented in 1.1): `dfmt.exec_stream` will return a streaming response. Under MCP this uses `notifications/progress`; under HTTP, Server-Sent Events. The content store accepts streamed chunks as they arrive, so intent-matched queries can run mid-execution. Deferred to 1.1 because it requires UI changes in each agent — the batch model works identically across every MCP client today; streaming requires both DFMT's side and matching agent consumer code, and agent support varies.

#### 7.5.3 File and Fetch Operations

`dfmt.read` and `dfmt.fetch` follow the same pattern:

- **Read** opens a file, applies the same size thresholds. For files, `intent` is particularly valuable: "find the auth middleware" on a 200-line Express server returns the relevant 20 lines, not the whole file.
- **Fetch** makes an HTTP GET (or specified method), follows redirects (with a bound), detects content type, converts HTML to markdown via a bundled minimal converter (no dependency), then applies the same pipeline.

Both operations produce chunk sets with `kind` set appropriately so that `dfmt.search_content` queries can be scoped by origin type.

#### 7.5.4 Security Policy

Security for the sandbox is defined in `.dfmt/permissions.yaml`:

```
version: 1

deny:
  - exec: "sudo *"
  - exec: "rm -rf /*"
  - exec: "curl * | sh"
  - read: ".env*"
  - read: "**/secrets/**"
  - read: "id_rsa"
  - read: "id_*"
  - fetch: "http://169.254.169.254/*"   # cloud metadata
  - fetch: "file://*"

allow:
  - exec: "git *"
  - exec: "npm *"
  - exec: "pytest *"
  - read: "src/**"
  - read: "docs/**"
  - fetch: "https://api.github.com/*"
  - fetch: "https://registry.npmjs.org/*"

credential_passthrough:
  gh:     ["GH_TOKEN", "GITHUB_TOKEN", "~/.config/gh/hosts.yml"]
  aws:    ["AWS_*", "~/.aws/credentials", "~/.aws/config"]
  gcloud: ["CLOUDSDK_*", "~/.config/gcloud/"]
  kubectl:["KUBECONFIG", "~/.kube/config"]
  docker: ["DOCKER_*", "~/.docker/config.json"]

limits:
  exec:
    wall_timeout: 60s
    cpu_timeout:  30s
    max_memory:   512m
    max_output:   10mb
  fetch:
    timeout:      30s
    max_body:     10mb
    max_redirects: 5
```

Rule evaluation:

1. Commands chained with `&&`, `;`, or `|` are split; each part is checked independently.
2. **deny** wins over **allow** at the same specificity level.
3. More-specific rules (project-level) override less-specific (global) rules.
4. No rules file = a conservative default policy (deny all `sudo`, deny reads of files matching common secret patterns, allow local filesystem reads within the project root, allow fetches to public internet but not RFC1918 or cloud metadata endpoints).
5. Non-shell language executions (python, node, etc.) are scanned for shell-escape patterns (`os.system`, `child_process.exec`, `subprocess.run`, etc.) and denied if found — the caller must use `bash` runtime explicitly if they want shell semantics.
6. **Symlinks are resolved before policy check.** For `dfmt.read` and for any deny/allow rule matching a file path: the path is resolved through all symlinks using `filepath.EvalSymlinks` before matching against the rule patterns. A symlink named `safe.txt` pointing to `../../etc/passwd` is checked against `../../etc/passwd` — so a deny rule on `**/.ssh/**` cannot be bypassed by `ln -s ~/.ssh/id_rsa safe.txt && dfmt.read safe.txt`. If resolution fails (broken symlink, loop), the read is denied with a specific error. For FS watcher paths, symlinks outside the project root are not followed — the watcher only observes within the resolved project directory.
7. **Path escape prevention.** After symlink resolution, any path that escapes the project root via `..` or absolute paths is checked against the deny/allow rules explicitly. There is no implicit "project-local = safe" exemption.

The daemon's security module reads the policy at startup and reloads on file change (SIGHUP or inotify on the file).

**Preset permissions templates.** `dfmt init` accepts a `--preset` flag that writes a pre-made `permissions.yaml` appropriate to the project type. Detection is automatic based on repository markers: `package.json` → Node.js, `Cargo.toml` → Rust, `go.mod` → Go, `pom.xml` / `build.gradle` → JVM, `Gemfile` → Ruby, `requirements.txt` / `pyproject.toml` → Python, `composer.json` → PHP. When multiple markers are present (monorepo, polyglot), the user is prompted to select. `--preset none` writes no `permissions.yaml`, leaving DFMT to use its conservative default policy.

Bundled presets (embedded via `go:embed`):

- **node** — allows `npm *`, `yarn *`, `pnpm *`, `npx *`, `node *`; denies reads of `.env*`, `**/*.pem`, `**/id_rsa*`; allows fetches to `registry.npmjs.org`, `nodejs.org`.
- **python** — allows `pip *`, `pipx *`, `python *`, `pytest *`, `poetry *`, `uv *`; denies reads of common secrets patterns plus `**/.env*`; allows fetches to `pypi.org`, `files.pythonhosted.org`.
- **go** — allows `go *`, `gofmt *`, `golangci-lint *`, `goreleaser *`; denies reads of `**/.env*`, `secrets/**`; allows fetches to `proxy.golang.org`, `sum.golang.org`.
- **rust** — allows `cargo *`, `rustc *`, `rustup *`, `rustfmt *`; denies typical secret paths; allows fetches to `crates.io`, `static.crates.io`.
- **ruby-rails** — allows `bundle *`, `rails *`, `rake *`, `rspec *`; denies reads of `config/master.key`, `config/credentials/*.key`, `storage/**`; allows fetches to `rubygems.org`.
- **jvm** — allows `mvn *`, `gradle *`, `./gradlew *`, `java *`; denies reads of `application-*.properties` where it matches credential patterns, `**/.env*`; allows fetches to Maven Central, `repo.maven.apache.org`.
- **php** — allows `composer *`, `php *`, `artisan *`; denies `.env*`, `storage/framework/sessions/*`; allows fetches to `packagist.org`, `repo.packagist.org`.
- **docker** — allows `docker *`, `docker-compose *`, `podman *`, `kubectl *`, `helm *`; denies reads of `~/.docker/config.json` (except through credential passthrough), `**/.kube/config`; fetches to registry hosts.
- **strict** — starts from the conservative default and adds nothing. Recommended for unknown or untrusted project types.
- **permissive** — allows all exec and read by default; fetches still denied to RFC1918 and cloud metadata. For trusted local development only; not recommended for shared or networked environments.

Users can combine presets (`--preset node,python` for a project with both) — the merged policy uses the union of allows and the union of denies, with denies winning. A project-specific `permissions.yaml` can be hand-edited after `dfmt init --preset` to fine-tune.

**Container-aware behavior.** When DFMT runs inside a container (Docker, Podman, container runtime derived from these), additional hardening is redundant — the host has already isolated the environment. DFMT detects containerization at startup by checking, in order:

1. `/.dockerenv` file presence (Docker).
2. `/run/.containerenv` file presence (Podman).
3. `/proc/1/cgroup` contents for `docker`, `kubepods`, `containerd`, `lxc`, or `garden` patterns.
4. `container` environment variable (set by systemd-nspawn, Podman).

When detected, DFMT logs the container runtime and adjusts three defaults:

- **Resource limits relax 2×.** Default `max_memory` goes from 512 MB to 1 GB, `max_output` from 10 MB to 20 MB. Container users typically have smaller pods and want DFMT to use what's allotted, not artificially cap below it.
- **Network deny rules soften.** RFC1918 addresses are typically internal-service access in container deployments. The default "deny RFC1918 fetch" rule is replaced with "allow RFC1918 fetch but log." Cloud metadata endpoints (169.254.169.254 etc.) remain denied by default.
- **CPU limits respect cgroups.** Rather than setting an arbitrary CPU timeout, DFMT reads `/sys/fs/cgroup/cpu.max` (cgroup v2) or `cpu.cfs_quota_us`/`cpu.cfs_period_us` (v1) and uses the cgroup budget as the upper bound for per-exec CPU time.

Users retain full control: `.dfmt/permissions.yaml` in the project always wins over defaults. A container-aware default is a starting point, not a mandate. Users running DFMT in containers specifically to isolate untrusted code execution can tighten the policy in their project config.

DFMT never runs its subprocess sandbox inside an *additional* container — it does not spawn Docker-in-Docker. If the enclosing environment is already a container, subprocesses run directly within that container's namespace.

#### 7.5.5 Intent-Driven Filtering

The `intent` parameter on exec/read/fetch calls is the mechanism through which large outputs produce small context impact. When provided:

1. The full output is chunked into the content store.
2. The chunks are indexed.
3. BM25 search runs the intent as a query against just the chunks from this call.
4. Top-matching chunks are returned, with position hints so the caller can request adjacent chunks if needed.
5. A vocabulary — the top-K most distinctive terms in the full output that were *not* in the intent query — is returned alongside, so the caller can formulate targeted follow-up queries without needing to read the whole content.

This makes DFMT's context behavior predictable: the caller states what they're looking for, the daemon finds it, the context window sees only the answer and a map of what else is available.

**Vocabulary computation.** "Distinctive" is measured against the chunk set of this specific call, not a global corpus. Algorithm: tokenize the full output (same pipeline as indexing), compute term frequencies (TF) per token. Exclude stopwords, the intent query's tokens, and tokens appearing in only one chunk (noise). Rank the remainder by raw TF. Take the top 10 (configurable via `sandbox.exec.vocabulary_size`). Rationale for local-only scoring: a global corpus would bias against recurring technical terms a user's codebase uses often, and DFMT has no stable global corpus anyway. Call-local scoring treats each output as its own universe, which is what the caller wants when asking "what else is interesting in this output."

#### 7.5.6 Content Retrieval

After an exec/read/fetch produces a chunk set, the caller can retrieve more from it using:

- `dfmt.search_content(query, scope?)` — BM25 search scoped to a specific chunk-set ID, or "latest" to target the most recent, or global across the session's content store.
- `dfmt.get_chunk(chunk_id)` — fetch a specific chunk verbatim by ID. Used when search hits revealed a relevant chunk ID that the caller wants in full.
- `dfmt.list_chunks(set_id)` — enumerate chunks in a set (IDs, headings, sizes). Used to navigate large outputs structurally.

All three operations run against the ephemeral content store and return nothing once the daemon idle-exits (unless chunks were stored with `ttl: forever`).

#### 7.5.7 Compliance Nudging

Even with intercept hooks installed, models under cognitive load drift away from preferred tools. A model 20 tool-calls deep in a task may start using native `Bash` instead of `dfmt.exec`, wasting the sandbox infrastructure the user configured. The hook layer (§7.2.6 and the per-agent hook dispatchers) monitors compliance and nudges when drift is detected.

**Drift signals** — tracked in a rolling 20-tool-call window:

- **Native-tool usage ratio.** If the ratio of native `Bash`/`Read`/`WebFetch` calls to `dfmt.exec`/`dfmt.read`/`dfmt.fetch` calls exceeds 0.3 (i.e., more than ~25% of data-heavy calls went through native tools), drift is flagged.
- **Large output events.** A single native tool call returning >16 KB counts as a strong drift signal on its own — the user has lost budget that DFMT was configured to save.
- **Absence of intent.** Sandbox calls made without an `intent` argument when the output would exceed the medium threshold are flagged as "suboptimal usage." Logged but do not escalate to nudge.

**Nudge actions** — graduated:

1. **First flag in a session:** no visible action. Logged only.
2. **Second flag within 10 calls of the first:** next `PostToolUse` hook response injects a short system-message-style note into the agent's context: *"Heads up: recent tool calls are bypassing DFMT's sandbox. Consider `dfmt.exec` with an `intent` argument for outputs that may exceed a few KB. Native tools are fine for small, known-small outputs."*
3. **Third flag:** same nudge plus a compact stats line showing savings-to-date in this session (e.g., "Session budget saved by sandbox: 14 KB of 62 KB possible").
4. **Beyond third:** no further nudges for 50 calls (avoid nagging).

Nudges are transport-level decorations added to hook responses, not ordinary events. They are never stored in the journal. Users who find them annoying can disable via `intercept.nudge: false` in config; the default is enabled.

The drift-detection logic lives in the hook dispatcher, keyed by session — the daemon tracks per-session (identified by a session-ID header the agents carry) compliance counters. Sessions older than 2 hours with no activity are evicted from the counter map.

### 7.6 Transport Layer

All three transports serve the same set of operations. Wire formats differ; semantics do not.

#### 7.6.1 MCP Server

JSON-RPC 2.0 over stdio. DFMT implements a minimal MCP server: `initialize`, `tools/list`, `tools/call`, `shutdown`. Prompts, resources, and sampling are out of scope.

Tools exposed: `dfmt.remember`, `dfmt.recall`, `dfmt.search`, `dfmt.stats`, `dfmt.forget`, `dfmt.note`, `dfmt.task`, `dfmt.log_tool_call`, `dfmt.batch_search`. Full schemas in section 9.1.

The MCP server is a thin client of the daemon. When invoked as `dfmt mcp`, it connects to the Unix socket and forwards tool calls. If the daemon is not running, `dfmt mcp` starts one in the background and waits for it to be ready.

#### 7.6.2 HTTP Server

**Opt-in, off by default.** Because DFMT runs one daemon per project and a fixed port would collide across concurrent projects, the HTTP transport is disabled unless the project's `config.yaml` explicitly enables it with an explicit bind address:

```
transport:
  http:
    enabled: true
    bind: "127.0.0.1:0"   # 0 = pick a free port; daemon writes the chosen port to .dfmt/daemon.port
```

If multiple projects enable HTTP, each picks its own free port. The chosen port is written to `.dfmt/daemon.port` so clients can discover it. Binds outside `127.0.0.1` are rejected unless `--allow-nonlocal` is passed at daemon start and the user has enabled `privacy.allow_nonlocal_http: true` in global config.

Primary transports remain MCP stdio and the Unix socket. HTTP exists for VS Code extensions, CI integrations, and custom scripts that cannot spawn MCP stdio clients. No authentication in v1 (localhost-only); tokens planned for v1.1 when non-local binds are considered.

Endpoints in section 9.2.

#### 7.6.3 Unix Socket

`<project>/.dfmt/daemon.sock` on Linux and macOS, named pipe `\\.\pipe\dfmt-<project-id>` on Windows. Line-delimited JSON-RPC 2.0. Used by CLI and git hooks for low-latency local calls. This is the primary control plane; every other client (including the MCP stdio server) ultimately speaks to the daemon over this socket.

---

## 8. Protocol Specifications

### 8.1 MCP Tools

All tool names are prefixed `dfmt.` to avoid collisions with other MCP servers. All tools return JSON.

#### 8.1.1 `dfmt.remember`

Record an event. Used by agents to capture semantic state (decisions, prompts, roles) that FS watchers cannot see.

Input schema:

```
{
  "type": "object",
  "required": ["type", "body"],
  "properties": {
    "type":     { "type": "string", "pattern": "^[a-z]+(\\.[a-z]+)*$" },
    "body":     { "type": "string", "maxLength": 4096 },
    "priority": { "enum": ["p1", "p2", "p3", "p4"] },
    "tags":     { "type": "array", "items": { "type": "string" } },
    "refs":     { "type": "array", "items": { "type": "string" } }
  }
}
```

Output: `{ "id": "<ULID>" }`.

#### 8.1.2 `dfmt.recall`

Return a snapshot suitable for injection.

Input:

```
{
  "budget":      { "type": "integer", "default": 2048, "minimum": 256, "maximum": 16384 },
  "format":      { "enum": ["xml", "json", "md", "guide"], "default": "xml" },
  "since":       { "type": "string", "description": "ULID or duration like '30m'" },
  "include":     { "type": "array", "items": { "type": "string" }, "description": "event types to include" },
  "exclude":     { "type": "array", "items": { "type": "string" } }
}
```

Output: `{ "snapshot": "<rendered>", "bytes": N, "events": N, "tiers": { "p1": N, "p2": N, ... } }`.

#### 8.1.3 `dfmt.search`

Query the index.

Input:

```
{
  "query":      { "type": "string", "maxLength": 512 },
  "limit":      { "type": "integer", "default": 5, "maximum": 50 },
  "types":      { "type": "array", "items": { "type": "string" } },
  "since":      { "type": "string" },
  "min_score":  { "type": "number", "default": 0 }
}
```

Output:

```
{
  "results": [
    {
      "id": "<ULID>",
      "type": "file.edit",
      "ts": "...",
      "score": 4.82,
      "layer": 1,
      "snippet": "...",
      "data": { ... }
    }
  ],
  "throttled": false,
  "next_layer_available": false
}
```

#### 8.1.4 `dfmt.batch_search`

Run multiple queries in one call. Used when the progressive throttle redirects repeated single queries. Input is an array of search inputs; output is an array of search outputs. Max 10 queries per batch.

#### 8.1.5 `dfmt.stats`

Return session statistics. Input: `{}`. Output:

```
{
  "project":      "<abs path>",
  "project_id":   "<8-hex>",
  "events_total": 1247,
  "events_by_type": { "file.edit": 420, ... },
  "events_by_tier": { "p1": 82, "p2": 310, ... },
  "journal_bytes": 1854392,
  "index_bytes":   248000,
  "session_started": "2026-04-20T09:12:00Z",
  "last_prompt":     "2026-04-20T10:47:33Z",
  "uptime_seconds":  6081,
  "throttle_state":  { "recent_searches": 2, "next_tier": "normal" }
}
```

#### 8.1.6 `dfmt.forget`

Emit a tombstone for one or more events. Does not remove the original event; downstream consumers see the tombstone and exclude the referenced ID.

Input: `{ "ids": ["<ULID>", ...], "reason": "<string>" }`. Output: `{ "removed": N }`.

#### 8.1.7 `dfmt.note`, `dfmt.task`, `dfmt.log_tool_call`

Convenience wrappers over `dfmt.remember` with fixed `type` and structured body. Full schemas in the reference docs.

#### 8.1.8 `dfmt.exec`

Run code in a sandbox. The primary tool agents should use in place of their native shell/bash tool.

Input:

```
{
  "code":     { "type": "string" },
  "lang":     { "enum": ["bash","sh","node","python","go","ruby","perl","php","r","elixir"] },
  "intent":   { "type": "string", "maxLength": 256 },
  "timeout":  { "type": "integer", "description": "seconds, max exec.wall_timeout" },
  "env":      { "type": "object", "description": "additional env vars (subject to policy)" },
  "return":   { "enum": ["auto","raw","summary","search"], "default": "auto" }
}
```

Output:

```
{
  "exit":      0,
  "stdout":    "…",           // inline if small, or chunk reference if large
  "stderr":    "…",
  "chunk_set": "01HY…",       // absent for inline returns
  "summary":   "exit=0, 120 lines, 3 warnings",
  "matches":   [ … ],         // intent-matched excerpts when intent provided
  "vocabulary":["auth","jwt","redis"], // searchable terms in the full output
  "duration_ms": 340
}
```

#### 8.1.9 `dfmt.read`

Read a file. The replacement for an agent's native file-read tool when handling potentially large files.

```
{
  "path":    { "type": "string" },
  "intent":  { "type": "string", "maxLength": 256 },
  "lines":   { "type": "object", "properties": { "start":{"type":"integer"}, "end":{"type":"integer"} } },
  "return":  { "enum": ["auto","raw","summary","search"], "default": "auto" }
}
```

Output shape mirrors `dfmt.exec` minus exit/stderr.

#### 8.1.10 `dfmt.fetch`

Fetch a URL. Replacement for an agent's native web fetch.

```
{
  "url":     { "type": "string", "format": "uri" },
  "method":  { "enum": ["GET","POST","PUT","DELETE","HEAD"], "default": "GET" },
  "headers": { "type": "object" },
  "body":    { "type": "string" },
  "intent":  { "type": "string" },
  "return":  { "enum": ["auto","raw","summary","search"], "default": "auto" }
}
```

Subject to the fetch deny/allow list and the redirect cap. HTTPS is the default; HTTP to localhost is allowed for development.

#### 8.1.11 `dfmt.batch_exec`

Run multiple exec/read/fetch operations in a single call to save round-trips. Input is an array of items, each with a `kind` field (`"exec"`, `"read"`, `"fetch"`) and the matching body. Output is an array of results in the same order. Max 10 items per batch.

#### 8.1.12 `dfmt.search_content`

Query the ephemeral content store produced by exec/read/fetch operations.

```
{
  "query":   { "type": "string" },
  "scope":   { "type": "string", "description": "chunk-set ID, 'latest', or 'all'" },
  "limit":   { "type": "integer", "default": 5 }
}
```

Output: matching chunks with their set ID, chunk ID, heading (if any), and snippet. Returns nothing after the daemon idle-exits unless content was stored with `ttl: forever`.

#### 8.1.13 `dfmt.get_chunk`

Retrieve a specific chunk verbatim by its ID.

```
{ "chunk_id": { "type": "string" } }
```

Output: `{ "chunk": { ...full Chunk record... } }`.

#### 8.1.14 `dfmt.list_chunks`

Enumerate chunks in a set without fetching their bodies.

```
{ "set_id": { "type": "string" } }
```

Output: `{ "set_id": "…", "kind": "exec-stdout", "chunks": [ { "id":…, "index":…, "kind":…, "heading":…, "tokens":… } ] }`.

### 8.2 HTTP API

Parallel to the MCP surface, with REST-style endpoints. Request and response bodies are JSON.

| Method | Path | Maps to |
| --- | --- | --- |
| POST | `/v1/events` | `dfmt.remember` |
| GET | `/v1/snapshot` | `dfmt.recall` |
| GET | `/v1/search` | `dfmt.search` |
| GET | `/v1/stats` | `dfmt.stats` |
| DELETE | `/v1/events/{id}` | `dfmt.forget` |
| GET | `/v1/events` | streaming journal tail (SSE) |
| POST | `/v1/exec` | `dfmt.exec` |
| POST | `/v1/read` | `dfmt.read` |
| POST | `/v1/fetch` | `dfmt.fetch` |
| POST | `/v1/batch_exec` | `dfmt.batch_exec` |
| GET | `/v1/content/search` | `dfmt.search_content` |
| GET | `/v1/content/chunks/{id}` | `dfmt.get_chunk` |
| GET | `/v1/content/sets/{id}` | `dfmt.list_chunks` |
| GET | `/v1/healthz` | `{ "ok": true }` |

All responses set `X-DFMT-Version`, `X-DFMT-Project-Id`, and `X-DFMT-Event-Cursor`.

### 8.3 Unix Socket

Line-delimited JSON-RPC 2.0. Each message is a single line ending in `\n`. Methods are the MCP tool names, unprefixed (so `recall` instead of `dfmt.recall`). Parameters are the same as the MCP tool inputs.

```
{"jsonrpc":"2.0","id":1,"method":"remember","params":{"type":"decision","body":"use slog"}}
{"jsonrpc":"2.0","id":1,"result":{"id":"01HY..."}}
```

---

## 9. Storage Layout

### 9.1 Per-Project

Every DFMT-enabled project has one `.dfmt/` directory. This directory is the complete world of one daemon instance.

```
<project>/.dfmt/
├── journal.jsonl             # active append-only journal
├── journal.<ULID>.jsonl.gz   # rotated segments
├── index.gob                 # serialized index snapshot
├── index.cursor              # high-water mark ULID
├── config.yaml               # project config
├── priority.yaml             # optional priority overrides
├── daemon.sock               # Unix socket (named pipe on Windows)
├── daemon.pid                # PID of the running daemon, or absent
├── daemon.log                # rotating daemon log, max 3 x 10 MB
├── cache/
│   ├── snapshot-<hash>.xml
│   ├── snapshot-<hash>.md
│   └── snapshot-<hash>.json
├── quarantine/
│   └── journal.corrupt.jsonl # malformed or unsigned lines
└── lock                      # flock target (prevents two daemons per project)
```

`.dfmt/` should be listed in `.gitignore`. A helper in `dfmt init` appends it automatically if not present.

### 9.2 Global

Global state is minimal by design. No daemon runs here; no sockets live here.

```
$XDG_DATA_HOME/dfmt/          # $HOME/.local/share/dfmt on Linux
├── projects.jsonl            # registry of known projects (for dfmt list)
└── config.yaml               # user-global defaults, merged under project config
```

On macOS, `$XDG_DATA_HOME` defaults to `$HOME/Library/Application Support`. On Windows, `%LOCALAPPDATA%\dfmt`. A user with no DFMT-enabled projects has no global state at all; these files are created lazily on first project init.

### 9.3 Config Schema

```
version: 1

capture:
  mcp:
    enabled: true
  fs:
    enabled: true
    watch:
      - "**/*.go"
      - "**/*.md"
    ignore:
      - ".git/**"
      - "node_modules/**"
      - ".dfmt/**"
    debounce_ms: 500
  git:
    enabled: true
  shell:
    enabled: false

storage:
  durability: durable      # durable | batched
  max_batch_ms: 50
  journal_max_bytes: 67108864   # 64 MB
  compress_rotated: true

retrieval:
  default_budget: 2048
  default_format: xml
  throttle:
    first_tier_calls: 3
    second_tier_calls: 5
    results_first_tier: 5
    results_second_tier: 2

index:
  rebuild_interval: 1h
  bm25_k1: 1.2
  bm25_b:  0.75
  heading_boost: 5.0
  stopwords_path: ""

transport:
  mcp:
    enabled: true
  http:
    enabled: false             # opt-in, off by default (see 7.6.2)
    bind: "127.0.0.1:0"        # 0 = pick a free port
  socket:
    enabled: true

sandbox:
  enabled: true
  exec:
    default_lang: bash
    inline_threshold: 4096       # bytes under which stdout returns raw
    medium_threshold: 65536      # bytes over which output is indexed eagerly
    max_raw_bytes:   262144      # hard cap on raw returns
    wall_timeout:    60s
    cpu_timeout:     30s
    max_memory:      512m
    max_output:      10mb
    max_concurrent:  4           # per-daemon concurrent exec cap; excess returns 429
    queue_timeout:   30s         # if excess waits longer than this, returns 429
  fetch:
    timeout:         30s
    max_body:        10mb
    max_redirects:   5
    user_agent:      "dfmt/1.0"
    max_concurrent:  4
  content:
    max_resident_bytes: 67108864  # 64 MB content store cap
    default_ttl:        session   # "session" = daemon lifetime; or duration
  runtimes:
    # When a runtime is unavailable, exec with that lang returns error 424.
    # `dfmt doctor` reports which are detected.
    detect: true                  # auto-probe PATH for runtimes
    override: {}                  # e.g. { python: "/opt/homebrew/bin/python3" }

lifecycle:
  idle_timeout: 30m            # daemon exits after this much idleness
  shutdown_timeout: 5s         # graceful drain window

privacy:
  telemetry: false
  remote_sync: null

logging:
  level: info              # debug | info | warn | error
  format: text             # text | json
```

Unknown top-level keys are preserved on disk so that newer daemons can read older configs. Unknown keys within known sections cause a warning.

A separate file, `.dfmt/permissions.yaml`, holds the deny/allow security policy for the sandbox (schema in §7.5.4 and Appendix D). It is kept separate from `config.yaml` because security policy has different edit semantics (often reviewed, sometimes signed off by someone other than the developer) and deserves a change history in git.

---

## 10. Concurrency and Daemon Lifecycle

### 10.1 Concurrency Model

- **One daemon per project.** The daemon takes a `LOCK_EX` on `.dfmt/lock` at startup. A second daemon attempting to start for the same project fails to acquire the lock and exits with code 2. Different projects run independent daemons, each holding its own lock in its own `.dfmt/`.
- **Single writer.** Within a project, the daemon is the only process that writes the journal. Capture sources outside the daemon (git hooks, shell integration, `dfmt capture` CLI calls) submit events over the Unix socket; the daemon serializes writes.
- **Readers are unbounded.** Search and snapshot operations read from the in-memory index under a read lock. Index swaps (after rebuild) use atomic pointer replacement; readers always see a consistent index.
- **Graceful shutdown.** On SIGTERM/SIGINT, the daemon stops accepting new writes, drains in-flight writes, flushes the journal, writes an up-to-date `index.gob`, removes the socket and PID files, and exits. Drain timeout is `shutdown_timeout` (default 5 s); after that, remaining writes are dropped with an error log.
- **Crash recovery.** If the daemon dies uncleanly, on restart it scans the journal from the `index.cursor` high-water mark, rebuilds the delta, and continues. No manual intervention required.

### 10.2 Daemon Lifecycle

The daemon is lazy: it is started by client calls and exits when idle. Users do not need to run `dfmt daemon` manually in normal use.

**Auto-start.** Any `dfmt <command>`, MCP stdio invocation, or socket connection attempt follows this sequence:

1. Resolve the project root (section 6.6).
2. Attempt to connect to `.dfmt/daemon.sock` with a 50 ms timeout.
3. On success, forward the request.
4. On connection refusal or missing socket:
   a. Check `.dfmt/daemon.pid`. If present, read the PID and send signal 0 (`kill -0`) to test for liveness. If the process does not exist or the signal fails with ESRCH, the PID is **stale**; unlink `daemon.pid` and `daemon.sock` (if present) before proceeding. On Windows, use `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` for the equivalent liveness check.
   b. Acquire a startup-race lock at `.dfmt/startup.lock` (flock `LOCK_EX`).
   c. Re-check the socket (another client may have spawned the daemon in between).
   d. Double-fork a detached daemon process, passing `--project <path>` and closing all inherited FDs.
   e. Poll the socket for up to 2 s (25 ms intervals).
   f. Forward the request, or error out if the daemon failed to start.

Step 4a is important: without it, a crashed daemon leaves a stale `daemon.pid` that would cause the new daemon's lock acquisition to succeed (the flock is not held after crash) but subsequent commands checking the PID would be confused. The explicit staleness check and cleanup ensures a clean state for the auto-start attempt.

The client library `internal/client` encapsulates this sequence. All capture sources and CLI commands use it; none implement their own connect logic.

**Idle-exit.** The daemon tracks the timestamp of the last request on any transport. A background ticker checks every minute; if the daemon has been idle for more than `idle_timeout` (default 30 minutes) **and** no FS watcher has fired in that window, it initiates graceful shutdown. This keeps memory use bounded to the set of projects actively being worked on, not the lifetime set of projects ever touched.

The FS watcher's activity counts as "not idle" because edits represent ongoing work even when no agent is calling. If the user is coding in the editor, the daemon stays up. If the editor is closed and no commands arrive for 30 minutes, it exits.

**Foreground mode.** `dfmt daemon --foreground` runs the daemon in the current terminal without double-forking, logs to stdout/stderr, and disables idle-exit. Useful for debugging and for running under a supervisor like systemd or launchd.

**Enumeration.** `dfmt list` reads `$XDG_DATA_HOME/dfmt/projects.jsonl`, checks each entry's `.dfmt/daemon.pid`, verifies the PID is live, and prints a table of running daemons with uptime, event count, and memory. Stale registry entries are silently pruned.

### 10.3 Startup Cost

Per-project daemon design means each new project pays a one-time startup cost on first command:

- Cold start with existing `index.gob`: ~50 ms p50, ~150 ms p99.
- Cold start with full journal rebuild (10k events): ~200 ms p50, ~500 ms p99.

The client auto-start sequence absorbs this latency transparently. A git hook invoked via `post-commit` on a cold daemon sees ~100 ms added to the hook execution; this is below the threshold at which users notice git hook slowness.

---

## 11. Performance Targets

All targets measured on a 2023 laptop (M2 Pro or equivalent x86) with NVMe SSD.

| Operation | Target (p50) | Target (p99) |
| --- | --- | --- |
| `dfmt.remember` end-to-end over Unix socket | 500 μs | 2 ms |
| `dfmt.remember` in durable mode (fsync included) | 1.5 ms | 5 ms |
| `dfmt.search` @ 10k events, BM25 layer | 1 ms | 5 ms |
| `dfmt.search` @ 100k events, BM25 layer | 10 ms | 30 ms |
| `dfmt.recall` @ budget=2048, 10k events | 3 ms | 10 ms |
| Daemon cold start (load `index.gob`) | 50 ms | 150 ms |
| Daemon cold start (full rebuild, 10k events) | 200 ms | 500 ms |
| Client auto-start overhead (first call on cold daemon) | 100 ms | 250 ms |
| Binary size (stripped, UPX off) | ≤ 8 MB | — |
| Resident memory per daemon @ 10k events | ≤ 50 MB | — |
| Resident memory per daemon @ 100k events | ≤ 300 MB | — |
| Idle daemon count (user working on N projects) | N | — |

These are acceptance criteria. The build fails if benchmarks in `bench/` regress targets by more than 20%.

---

## 12. Security and Privacy

### 12.1 Threat Model

DFMT operates as a local service under the user's account. The threat model is limited:

- **In scope:** accidental data leakage (secrets in journals), local privilege confusion (other local users reading the socket), malformed input (parser DoS).
- **Out of scope:** remote attackers with kernel access, sidechannel attacks, defending against a compromised agent process (if the agent is compromised, so is the session context anyway).

### 12.2 Controls

- **Localhost-only HTTP.** Default bind is `127.0.0.1`. Config rejects any non-loopback bind without an explicit opt-in flag.
- **Unix socket permissions.** `0700` on the socket file; `0700` on its containing directory. On Windows, the named pipe ACL grants access only to the owner.
- **Secret redaction.** A single redaction module runs over every piece of textual data DFMT stores, regardless of origin. Patterns match common secret formats: AWS access keys (`AKIA[0-9A-Z]{16}`), GitHub tokens (`ghp_*`, `gho_*`, `ghu_*`, `ghs_*`, `ghr_*`), OpenAI keys (`sk-[A-Za-z0-9]{40,}`), Anthropic keys (`sk-ant-[A-Za-z0-9-]{90,}`), Google API keys (`AIza[0-9A-Za-z-_]{35}`), Stripe keys (`sk_live_*`, `pk_live_*`), generic JWTs (three base64 segments separated by dots), URL-embedded credentials (`https?://[^:]+:[^@]+@`), and any string matching a user-configured regex in `.dfmt/redact.yaml`. Matches are replaced with `<REDACTED:<kind>>` before persistence. Redaction applies to **three write paths**:
  - Event `body` and `Data` fields, before journal append.
  - Sandbox stdout, stderr, and fetched HTTP response bodies, before chunking into the content store.
  - File contents in `dfmt.read`, after reading from disk and before chunking.
  
  Redaction does not apply to the `path` or `url` fields of events (users need to see which path was involved), nor to command text passed to `dfmt.exec` (the user wrote it deliberately). The redactor operates on UTF-8 byte streams; binary content is skipped entirely. A configurable whitelist (`redact.allow: [...]`) can mark patterns that should not be redacted for development-context false positives, but the defaults are conservative.
- **Path scope.** The FS watcher ignores paths outside the project root. Glob patterns that would resolve outside the root are rejected at config load.
- **No network by default.** The daemon opens no outbound sockets unless `privacy.remote_sync` is explicitly configured.
- **No telemetry.** Ever. Not even opt-in in v1.
- **Deletion.** `dfmt purge` removes the entire `.dfmt/` directory for a project. `dfmt purge --global` additionally removes the daemon registry entry. No cloud state exists to coordinate.

### 12.3 Data at Rest

Journal and index are unencrypted on disk in v1. Users concerned about at-rest encryption should rely on full-disk encryption. An opt-in `age`-based encryption layer (using `x/crypto/chacha20poly1305`) is planned for v1.2.

---

## 13. CLI Surface

The `dfmt` binary is both the daemon and the client. Mode is chosen by subcommand.

### 13.1 Daemon and lifecycle

All lifecycle commands operate on the current project unless `--project` or `--all` is given.

```
dfmt daemon [--foreground]         # start daemon for current project
dfmt stop [--all]                  # stop daemon for current project (or all)
dfmt status                        # current project: running? uptime? events?
dfmt list                          # all known projects; which have daemons running
dfmt doctor                        # diagnostic checks for current project
```

In normal use, users never run `dfmt daemon` explicitly. The daemon auto-starts on any client call (see 10.2). `dfmt stop` is useful when the user wants to force a state flush before backing up `.dfmt/`, or to free memory on constrained machines.

### 13.2 Capture

```
dfmt remember <type> <body>        # emit an event
dfmt note <body>                   # shorthand for remember note
dfmt task <body>                   # shorthand for remember task.create
dfmt task done <id>                # emit task.done tombstone-like event
dfmt capture git <subcmd> <args>   # used by git hooks
dfmt capture env.cwd <path>        # used by shell hook
```

### 13.3 Retrieval

```
dfmt recall [--budget N] [--format xml|json|md|guide] [--since DUR]
dfmt search <query> [--limit N] [--type T]
dfmt stats
dfmt tail [--follow]               # stream new events
```

### 13.4 Sandbox (agent-initiated operations from the CLI)

```
dfmt exec <code> [--lang L] [--intent I]   # run code in sandbox
dfmt read <path> [--intent I] [--lines S:E]
dfmt fetch <url> [--intent I] [--method M]
dfmt content search <query> [--scope S]
dfmt content get <chunk-id>
dfmt content list <set-id>
```

Intended mainly for debugging and scripting; the MCP and HTTP surfaces are the primary channels.

### 13.5 Administration

```
dfmt init                          # create .dfmt/ in current project
dfmt setup                         # detect agents and configure them (§14.6)
dfmt install-hooks                 # install git hooks
dfmt shell-init <shell>            # emit shell integration snippet
dfmt config get <key>
dfmt config set <key> <value>
dfmt rotate                        # force journal rotation
dfmt reindex                       # force full index rebuild
dfmt purge [--global]              # delete all DFMT data for this project
```

### 13.6 Integration helpers

```
dfmt mcp                            # run MCP stdio server (for agent configs)
dfmt export [--format json|ndjson]  # dump journal
dfmt import <file>                  # replay journal into current project
```

All commands support `--project <path>` to override project discovery, and `--json` to force JSON output on stdout regardless of TTY.

---

## 14. Error Handling and Recovery

Errors are classified into four categories, each with defined behavior:

| Category | Example | Behavior |
| --- | --- | --- |
| **User error** | Invalid config, bad glob, unknown event type | Fail closed with clear message; exit non-zero on CLI; return JSON-RPC `-32602` |
| **Capture error** | Disk full, broken watcher | Log, disable the offending capturer, continue serving retrieval |
| **Core error** | Corrupt journal line, failed index rebuild | Quarantine the bad data; continue with rest; surface in `dfmt doctor` |
| **Transport error** | Socket bind failure, HTTP bind failure | Log; fall back to remaining transports; exit only if all transports fail |

The daemon never panics on input. All handlers recover from panics and convert them to errors. A single bad event never stops the daemon.

---

## 15. Versioning and Migration

- **Wire formats** follow SemVer at the protocol level. Minor versions add fields only. Major versions are reserved for breaking changes and are not planned.
- **Journal format** carries a `version: 1` header event as the first line on first write. Future versions can migrate forward by reading the header and transforming older lines at read time.
- **Config** carries a top-level `version: N`. The daemon migrates forward and writes the new version on save. Migrations are irreversible.
- **Index** is disposable: if the format changes, the daemon rebuilds from the journal. No index migration logic is maintained.

---

## 16. Dependency Policy

In alignment with `#NOFORKANYMORE`:

- **Permitted:** Go standard library, `golang.org/x/sys`, `golang.org/x/crypto`, `gopkg.in/yaml.v3`.
- **Forbidden:** everything else. No `github.com/fsnotify/fsnotify` (use `x/sys` syscalls directly). No `github.com/mark3labs/mcp-go` (MCP is JSON-RPC 2.0 over stdio, ~300 lines of code). No SQLite. No Bleve. No Cobra/urfave-cli (use `flag` plus small dispatcher). No logrus/zap/zerolog (use `log/slog`).
- **Bundled:** Porter stemmer (single 400-line file, public-domain reference implementation, hand-translated to Go). Stopword lists (text files in `internal/stopwords/`). ULID generator (100 lines).

Any proposed new dependency requires explicit justification in an ADR (`docs/adr/NNNN-*.md`) and is default-rejected.

---

## 17. Build and Distribution

### 17.1 Build

```
go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
  -o dist/dfmt ./cmd/dfmt
```

`CGO_ENABLED=0` always. Release builds cross-compile to:

- `linux/amd64`, `linux/arm64`
- `darwin/amd64`, `darwin/arm64`
- `windows/amd64`, `windows/arm64`
- `freebsd/amd64`

### 17.2 Distribution

- **GitHub Releases.** Prebuilt binaries, checksums, signatures (cosign keyless via GitHub OIDC).
- **`go install`.** `go install github.com/ersinkoc/dfmt/cmd/dfmt@latest`.
- **Homebrew tap.** `ersinkoc/tap/dfmt` for macOS and Linux.
- **Scoop bucket.** For Windows.
- **No npm, no Docker-as-primary.** A Docker image is published for CI convenience but is not the recommended install path.

### 17.3 Third-Party Licenses

DFMT is MIT-licensed. Transitively, its binary includes code under other permissive licenses via its permitted dependencies:

- **Go standard library** — BSD-3-Clause
- **`golang.org/x/sys`** — BSD-3-Clause
- **`golang.org/x/crypto`** — BSD-3-Clause (reserved; pulled in v1.2+ for at-rest encryption)
- **`gopkg.in/yaml.v3`** — MIT (the YAML parser itself) and Apache-2.0 (some upstream parts inherited from libyaml)
- **Bundled Porter stemmer** — the original Porter 1980 algorithm is in the public domain; our Go port is original work under DFMT's MIT
- **Bundled Snowball stopword lists** (English, Turkish) — BSD-3-Clause (derived from Snowball project's public stoplist)
- **Bundled HTML tokenizer** — original work under DFMT's MIT (ADR-0008)

A `LICENSE-THIRD-PARTY.md` file at the repository root records these, with the full license text for BSD-3-Clause and Apache-2.0. The release workflow regenerates this file before each tag so it stays in sync with actual binary contents. Release binaries embed the full third-party license text accessible via `dfmt --licenses`.

### 17.4 Install script

A single-line install:

```
curl -fsSL https://dfmt.dev/install.sh | sh
```

The script verifies the binary signature before placing it in `$HOME/.local/bin` and prints the next-step command.

---

## 18. Platform and Agent Compatibility

DFMT's effectiveness on a given agent depends on three capabilities of that agent:

1. **MCP support** — can the agent call DFMT's sandbox and memory tools directly?
2. **Hook/plugin support** — can DFMT programmatically intercept the agent's native tool calls (Bash, Read, WebFetch) and redirect them to the sandbox, *before* they consume context?
3. **Instruction-file support** — does the agent read a project-level directives file (CLAUDE.md, AGENTS.md, copilot-instructions.md, .cursorrules) that can persuasively nudge the model toward DFMT's tools?

The first two are the leverage points. MCP lets the agent explicitly reach DFMT. Hooks force the agent to use DFMT even when the model forgets. Without hooks, DFMT relies on the instruction file alone — this works, but the model's compliance varies under cognitive load, and a single unrouted Bash call can wipe out a session's worth of context savings.

| Agent | MCP | Hooks / Plugin | Instruction File | Sandbox Routing | Session Memory |
| --- | --- | --- | --- | --- | --- |
| Claude Code | ✓ | ✓ (`PreToolUse`, `PostToolUse`, `PreCompact`, `SessionStart`, `UserPromptSubmit`) | `CLAUDE.md` | **~95%** (hooks + instructions) | Full |
| Gemini CLI | ✓ | ✓ (`BeforeTool`, `AfterTool`, `PreCompress`, `SessionStart`) | `GEMINI.md` | **~95%** | High (no prompt hook) |
| VS Code Copilot | ✓ | ✓ (`PreToolUse`, `PostToolUse`, `SessionStart`, `PreCompact`) | `copilot-instructions.md` | **~95%** | High |
| OpenCode | ✓ | ✓ (TS plugin: `tool.execute.before`, `experimental.session.compacting`) | `AGENTS.md` | **~95%** | High (no SessionStart) |
| Cursor | ✓ | partial (limited intercept) | `.cursorrules` | **~70%** (instruction-dominated) | Limited |
| Zed | ✓ | — | `AGENTS.md` (planned) | **~65%** (instruction only) | Limited |
| Continue.dev | ✓ | — | continue config prompt | **~65%** | Limited |
| Windsurf | ✓ | — | rules file | **~65%** | Limited |
| Codex CLI | ✓ | — (no hook support) | `AGENTS.md` | **~65%** | — (no session hooks) |
| Any other MCP client | ✓ | depends | depends | ≥60% | depends |
| **No agent at all** | — | — | — | — | Baseline (FS + git) |

"Sandbox routing" is the percentage of native tool calls that are successfully redirected to DFMT's sandbox in a real session. The number depends on (a) whether hooks can programmatically block native calls, (b) how comprehensively the instruction file has been written, and (c) the model's general compliance under load. These numbers come from an expected-behavior model, not from benchmarks; real figures will be measured after M2.

"Session memory" reflects how completely the session-event capture works for that agent. Values:

- **Full** — all hook points available, all event categories captured.
- **High** — missing one or two hook points; most events captured, some categories (e.g., user decisions) limited.
- **Limited** — agent-initiated MCP capture only; external capture (FS, git) still works, so the baseline is never zero.
- **Baseline** — user working without an AI agent. FS watcher and git hooks alone produce a reconstructable session record.

A key property of DFMT's design: **no agent is "not supported."** The coverage varies along a continuum. Even Codex CLI, which offers no hooks for DFMT to latch onto, gets the sandbox tools (via MCP) and the full FS + git capture (via external watchers). A developer can use DFMT meaningfully on any of the listed agents; the quality of the integration differs, but the baseline is always useful.

### 18.1 One-Command Multi-Agent Setup

Manually writing the correct MCP registration, hook configuration, and instruction file for each agent a developer uses is friction that would discourage adoption. DFMT addresses this with the `dfmt setup` command:

```
dfmt setup                   # auto-detect installed agents, configure all of them
dfmt setup --agent claude    # configure only Claude Code
dfmt setup --dry-run         # print what would change without writing
dfmt setup --uninstall       # remove DFMT's configuration from all agents
```

On execution:

1. Probes the filesystem for each supported agent's config directory (e.g., `~/.claude/`, `~/.gemini/`, `~/.codex/`, etc.).
2. For each detected agent, writes the MCP server registration in the agent's expected format.
3. Where hook support exists, writes the hook configuration with commands routed through `dfmt hook <agent> <event>`.
4. Writes the agent-appropriate instruction file (in the project, if `cwd` is inside one; otherwise to the agent's global instructions directory with the user's consent).
5. Prints a summary: what was changed, what needs a restart, and any manual steps (e.g., "add `dfmt setup` output to your `.bashrc`").

The command is idempotent: running it twice on the same machine produces no new changes. It is conservative: it never overwrites foreign configuration without `--force`. It writes a manifest at `$XDG_DATA_HOME/dfmt/setup-manifest.json` recording everything it changed, enabling clean `--uninstall`.

Full per-agent details — config paths, file formats, hook command shapes, instruction file templates — live in `AGENT-INTEGRATION.md` alongside this spec.

---

## 19. Observability

- **Structured logs.** `log/slog` with `text` or `json` handler. Log level via config or `DFMT_LOG_LEVEL`.
- **Metrics endpoint.** `GET /v1/metrics` returns a Prometheus-compatible text exposition of: event counts by type and tier, write latency histogram, search latency histogram, index size, journal size, throttle counters, sandbox exec count/latency/exit-code distribution, content store size/evictions.
- **`dfmt doctor`.** Runs a checklist: daemon running? socket reachable? journal writable? index loadable? FS watcher healthy? git hooks installed? shell integration detected? Each check reports `ok`/`warn`/`fail` with a remediation hint.
- **`dfmt tail --follow`.** Streams the journal as events arrive, with format selectable. Useful for debugging capture issues live.

### 19.1 Crash Handling

Daemon crashes are observable through three paths:

1. **Stderr to `daemon.log`.** The daemon's log writer routes all output — including panic stack traces — to `<project>/.dfmt/daemon.log`. Rotation: `daemon.log` → `daemon.log.1` → `daemon.log.2` (deleted), each capped at 10 MB. Rotation triggers on size; there is no time-based rotation.
2. **Panic recovery.** Each goroutine handling client requests recovers from panics, logs the stack trace with event context, and continues serving. A panic in one request never takes down the daemon. The recovered panic is also recorded as an `error` event in the journal with `type: error`, `data.kind: internal-panic`, so it surfaces in recall snapshots.
3. **Fatal exits.** If the daemon exits non-zero (lock contention, unrecoverable FS error), the exit code is written to `<project>/.dfmt/daemon.last-exit` along with a timestamp and a short reason string. The client's auto-start sequence reads this file on next invocation and surfaces it ("last daemon exited with code 2 at T — 'flock failed, another instance may be running'").

### 19.2 `dfmt bundle`

For bug reports, users run:

```
dfmt bundle [--output /path/to/bundle.tar.gz]
```

This produces a tarball containing:

- `daemon.log` and rotated logs
- `daemon.last-exit`
- `config.yaml` (with secrets redacted per `redact.yaml` + sandbox defaults)
- `permissions.yaml` (redacted)
- Last 1000 events from `journal.jsonl` (redacted)
- `index.cursor`
- `dfmt doctor --json` output
- Daemon version, runtime versions, OS/arch, uptime

**Redaction is applied to the bundle.** The same patterns that redact writes (§12.2) are applied to the bundle output — secret keys, tokens, passwords in URLs, user-configured patterns. `file.edit` paths are kept (diagnostic value); `file` contents are not in the bundle regardless.

The bundle is explicitly **never transmitted automatically**. DFMT has no telemetry. Users attach the bundle to issues they open themselves. A generated bundle has a filename header listing what is and isn't included, so the user can decide whether to share.

### 19.3 Log Location Summary

| Path | Contents | Rotation |
| --- | --- | --- |
| `<proj>/.dfmt/daemon.log` | all daemon output, panics, warnings | size, 10 MB × 3 |
| `<proj>/.dfmt/daemon.last-exit` | last exit code + reason + timestamp | overwritten per exit |
| `<proj>/.dfmt/quarantine/journal.corrupt.jsonl` | malformed journal lines | append-only, review manually |
| `$XDG_DATA_HOME/dfmt/setup.log` | `dfmt setup` history and changes | size, 5 MB × 2 |

---

## 20. Testing Strategy

- **Unit tests.** Each core component has >80% line coverage. Tokenizer, stemmer, BM25, classifier, snapshot builder.
- **Property tests.** BM25 properties (idempotence under duplicate terms, monotonicity in term frequency). Journal round-trip (write/read preserves bytes). ULID monotonicity.
- **Integration tests.** A test harness spins up a real daemon against a tempdir project, exercises each transport (MCP, HTTP, socket), asserts end-to-end behavior.
- **Cross-platform tests.** GitHub Actions matrix over `linux/darwin/windows` x `amd64/arm64`.
- **Benchmarks.** `bench/` contains Go benchmarks for write latency, search latency, and snapshot build. Performance targets in section 11 are CI gates.
- **Corpus replay.** A golden corpus of recorded sessions (1k, 10k, 100k events) is replayed against each release to detect regressions in ranking quality.
- **Agent smoke tests.** For every supported agent in §18, a pre-release smoke test exercises DFMT end-to-end through that agent's real MCP/hook surface. The matrix:

  | Tier | Agents | Automation | Frequency |
  | --- | --- | --- | --- |
  | Tier 1 — containerized | Claude Code (via Anthropic CLI image), Codex CLI, OpenCode | CI, every PR to `main` | automatic |
  | Tier 2 — VM harness | Gemini CLI, VS Code Copilot | CI weekly + before each release | automatic |
  | Tier 3 — manual | Cursor, Zed, Continue.dev, Windsurf | checklist run by maintainer before each release | manual |

  Tier 1: a Dockerfile per agent produces a reproducible environment. The test script runs `dfmt setup --agent <name>`, invokes the agent with a fixture prompt, asserts that DFMT's sandbox tools are called and that events appear in the journal. Fully automated.

  Tier 2: requires a GUI agent (VS Code Copilot) or proprietary binary (Gemini CLI's headless mode is limited). A long-running VM with the agent pre-installed runs the same test script; results posted back to CI. Failures in Tier 2 block release; Tier 1 failures block merge.

  Tier 3: agents without reliable automation. A release checklist lists the smoke steps: install DFMT, run `dfmt setup`, open the agent, run three fixture prompts, verify behavior, check `dfmt stats`. The maintainer runs these manually, attaches results to the release PR.

  A regression in any tier blocks the release until addressed or the agent is explicitly marked "experimental" with reduced support guarantees in SPEC §18.

---

## 21. Out of Scope (explicit)

- Code sandboxing or subprocess execution. Belongs in a separate tool.
- Remote/team memory sharing. Maybe v2; not v1.
- Semantic / vector embeddings. BM25 is enough for session memory; embeddings belong in a code RAG system.
- Web UI dashboard. A minimal TUI (`dfmt tail`, `dfmt stats`) is the entire UI surface.
- Full MCP server (prompts, resources, sampling). DFMT exposes tools only.
- Authentication / multi-user. Localhost single-user only in v1.
- Binary-file capture (images, PDFs). Only textual events are supported.

---

## Appendix A — XML Snapshot Document Type

```
<session_snapshot
    version="1"
    project_id="<8-hex>"
    generated_at="<RFC3339>"
    budget_bytes="<int>"
    used_bytes="<int>"
    event_count="<int>">

  <last_prompt ts="<RFC3339>">
    <!-- user's most recent message to the agent; always present if any exists -->
  </last_prompt>

  <tier name="critical">
    <event id="<ULID>" type="<EventType>" ts="<RFC3339>" priority="p1">
      <!-- type-specific inner content -->
    </event>
    ...
  </tier>

  <tier name="state">
    <event id="..." type="..." ts="..." priority="p2">...</event>
    ...
  </tier>

  <tier name="context">
    <event id="..." type="..." ts="..." priority="p3">...</event>
    ...
  </tier>

  <tier name="info">
    <event id="..." type="..." ts="..." priority="p4">...</event>
    ...
  </tier>

  <trailer
      tiers_truncated="p3,p4"
      events_omitted="147" />

</session_snapshot>
```

Inner content of each `<event>` varies by type. Schemas are in `docs/schemas/events/<type>.md`.

---

## Appendix B — ULID Format

DFMT uses ULIDs (Universally Unique Lexicographically Sortable Identifiers) as event IDs.

- 26 characters, Crockford's base32.
- First 10 chars encode a 48-bit millisecond timestamp.
- Last 16 chars encode 80 bits of randomness.
- Lexicographic sort order equals timestamp sort order.
- Monotonic within the same millisecond is guaranteed by incrementing the random tail (Section 3.3 of the ULID spec).

Implementation: 100 lines of Go, bundled in `internal/core/ulid.go`. No dependency.

### Clock Skew Handling

System clocks can move backward (NTP correction, manual set, suspended-laptop resume). A naive ULID generator that reads `time.Now()` during such a skew would emit IDs that sort earlier than previously-generated ones, breaking the monotonic-sort property that the journal relies on.

DFMT's ULID generator enforces **monotonic-high-water-mark**: it caches the timestamp of the last emitted ULID and, if the system clock ever reports a lower value, it advances the cached high-water-mark by 1 millisecond and uses that instead of the system clock. The random tail is freshly generated. This preserves strict monotonicity within the daemon at the cost of briefly emitting ULIDs with "future" timestamps (by up to the magnitude of the backward skew, which is typically milliseconds).

The high-water-mark is in-memory only. If the daemon restarts after a backward clock skew, the first new ULID reads from the (now lower) system clock. In that case, the journal is still append-only and ordered by file position, not by ULID alone — `Stream()` iterates in file order, which is guaranteed monotonic because writes are serialized through one daemon holding a write lock. So even if ULIDs briefly go non-monotonic across a restart-with-skew, replay semantics remain correct.

---

## Appendix C — Open Questions

### Resolved

- **Q1. License.** **MIT.** Rationale: maximize adoption; project identity is protected by naming and brand consistency within the `ersinkoc` portfolio rather than by license terms.
- **Q5. Daemon model.** **Per-project, auto-started, idle-exit.** See section 10 for full specification. ADR-0001.
- **Q-Turkish-stemming.** **Porter English stemmer only in v1.** Non-ASCII tokens pass through unchanged — Turkish queries match exact-token only. v1.1 will add a Turkish Snowball stemmer (bundled if feasible under ADR-0004; otherwise evaluated via an ADR). Rationale: English is the dominant language in AI agent interactions and in developer tooling vocabulary; Turkish support is additive value, not a blocker.
- **Q-MCP-protocol-version.** **Target MCP `2025-06-18` baseline.** Supports every agent listed in SPEC §18 at time of spec freeze. Newer MCP revisions adopted in patch releases as individual agents ship support — DFMT does not adopt a newer baseline unless a target agent requires it.
- **Q-Retention-policy.** **Events retained forever by default.** No automatic time-based pruning. Manual pruning via `dfmt prune --older-than <duration>` (added in Phase 6, CLI cmd). Rationale: the journal is small (median session produces <10 MB of events over weeks of work), disk cost is negligible, and accidental loss of long-term context is worse than the disk cost. Users concerned about privacy run `dfmt purge` or `dfmt prune` explicitly.
- **Q-Durability-default.** **Durable mode by default.** fsync after every append. Rationale: reliability over throughput for the common case; high-frequency capture sources (FS watcher) don't generate enough writes per second for throughput to dominate (<100 writes/sec in steady-state developer work). Batched mode available for users who opt in, documented in config.
- **Q-Idle-exit-default.** **30 minutes of no requests and no FS activity.** Revisit target: after one month of real usage, collect distribution of session durations and inter-session gaps. If users frequently respawn within 30 min (wasting startup cost) or daemons stay up for days (wasting memory on abandoned projects), adjust.

### Open (revisit after measurement, not ship-blocking)

None. All v1 ship decisions are made; the three measurement-pending defaults above are flagged for post-launch revisit without blocking the release.

---

*End of specification.*
