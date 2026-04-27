# DFMT Architecture

**DFMT** ("Don't Fuck My Tokens") is a local Go daemon that bridges
AI coding agents (Claude Code, Cursor, Codex, Gemini, VS Code,
Windsurf, Zed, Continue, OpenCode) with their execution environment.
It exposes a uniform set of MCP tools for `exec`, `read`, `fetch`,
`glob`, `grep`, `edit` and `write`, returning **intent-matched
excerpts** instead of raw output, and persists every operation in an
append-only journal so that a future agent session can recall the
context after compaction.

This document describes the system from a build-and-debug
perspective: components, data flow, threading model, on-disk layout,
and security posture. It is written for engineers and AI agents that
need a working mental model of the codebase. The canonical onboarding
doc is [`AGENTS.md`](../AGENTS.md); this file is the architectural
deep dive.

> **Editorial note.** Mermaid blocks render in GitHub, GitLab, VS
> Code's preview, and most modern Markdown viewers. ASCII-art
> fallbacks are kept where the diagram type is awkward to express in
> Mermaid (sequence with notes, signed binary layouts, etc.).

---

## Table of contents

1. [System context](#1-system-context)
2. [High-level architecture](#2-high-level-architecture)
3. [Process model](#3-process-model)
4. [Entry points](#4-entry-points)
5. [CLI dispatch](#5-cli-dispatch)
6. [Daemon lifecycle](#6-daemon-lifecycle)
7. [Transport layer](#7-transport-layer)
8. [Core domain](#8-core-domain)
9. [Sandbox](#9-sandbox)
10. [Capture pipeline](#10-capture-pipeline)
11. [Session memory and recall](#11-session-memory-and-recall)
12. [Agent setup](#12-agent-setup)
13. [Configuration](#13-configuration)
14. [On-disk layout](#14-on-disk-layout)
15. [Concurrency model](#15-concurrency-model)
16. [Security posture](#16-security-posture)
17. [Build, test, release](#17-build-test-release)
18. [Package map](#18-package-map)
19. [End-to-end walkthrough](#19-end-to-end-walkthrough)

---

## 1. System context

DFMT sits **between** an AI agent and the operating system. The agent
never speaks to the kernel directly — every shell-out, file read,
HTTP fetch, glob, grep, edit, and write goes through DFMT's MCP
server. That gives DFMT three jobs:

1. **Token thrift.** Filter raw tool output down to what the agent
   asked for (the `intent` parameter). Stash full output in an
   ephemeral content store for later retrieval.
2. **Audit trail.** Append every operation to a per-project journal
   so future sessions can recall what happened.
3. **Policy enforcement.** Apply allow/deny rules per operation and
   per glob — agents cannot read `.env`, write to `.git/`, or shell
   into `sudo`.

```mermaid
C4Context
    title DFMT system context
    Person(user, "Developer", "Edits code through an AI agent")
    System_Boundary(local, "Local workstation") {
        System(agent, "AI coding agent", "Claude Code, Cursor, Codex, etc.<br/>Speaks MCP over stdio")
        System(dfmt, "DFMT daemon", "Per-project Go daemon<br/>MCP server + sandbox + journal")
        System_Ext(os, "OS / filesystem / shell", "Files, processes, network")
    }
    System_Ext(remote, "Remote HTTP endpoints", "Docs, package registries, APIs")

    Rel(user, agent, "Prompts")
    Rel(agent, dfmt, "MCP tool calls", "stdio JSON-RPC 2.0")
    Rel(dfmt, os, "Sandboxed exec / read / write")
    Rel(dfmt, remote, "Sandboxed fetch", "HTTP(S), SSRF-defended")
    Rel(dfmt, user, "Dashboard, CLI", "Browser / terminal")
```

The "system context" claim is enforced socially, not by the kernel.
Agents that ignore `AGENTS.md` and call native `Bash`/`Read` tools
sidestep DFMT entirely. Token savings then collapse, but the journal
still records whatever does come through MCP.

---

## 2. High-level architecture

```mermaid
flowchart TB
    subgraph Agent["AI Agent (Claude Code, Cursor, Codex, …)"]
      A1[MCP client]
    end

    subgraph DFMT["DFMT daemon (per project)"]
      direction TB
      subgraph Transport["Transport layer"]
        T1[MCP / stdio]
        T2[HTTP + Dashboard]
        T3[Unix socket / TCP loopback]
      end

      subgraph Handlers["RPC handlers"]
        H1[Remember]
        H2[Search]
        H3[Recall]
        H4[Stats]
        H5[Exec / Read / Fetch / Glob / Grep / Edit / Write]
      end

      subgraph Core["Core domain"]
        C1[(Journal<br/>append-only JSONL)]
        C2[(Index<br/>BM25 + trigram)]
        C3[Classifier<br/>P1–P4]
        C4[Content store<br/>ephemeral]
      end

      subgraph Sandbox["Sandbox"]
        S1[Permission gate]
        S2[Intent extractor]
        S3[Redactor]
        S4[Runtime: bash/python/…<br/>HTTP client / file IO]
      end

      subgraph Capture["Capture pipeline"]
        K1[MCP calls]
        K2[CLI commands]
        K3[FS watcher]
        K4[Git hooks]
        K5[Shell integration]
      end
    end

    subgraph Disk[".dfmt/ on disk"]
      D1[journal.jsonl]
      D2[index.gob]
      D3[config.yaml]
      D4[port / lock / pid]
    end

    A1 -- "stdio JSON-RPC" --> T1
    Browser((Browser)) -- "GET /" --> T2
    CLI((dfmt CLI)) -- "JSON-RPC" --> T3

    T1 --> Handlers
    T2 --> Handlers
    T3 --> Handlers

    Handlers --> Sandbox
    Handlers --> Core
    Sandbox --> Core
    Capture --> Core

    C1 --> D1
    C2 --> D2
```

The shape is conventional: transports fan in, handlers fan out, the
core owns durable state, the sandbox owns side effects, and the
capture pipeline funnels external events into the same core.

---

## 3. Process model

DFMT runs as a **single daemon per project directory**. The first
CLI or MCP call in a project that finds no daemon listening will
spawn one (auto-start). The daemon idles out after a configurable
timeout (30 minutes by default) and exits cleanly.

```mermaid
stateDiagram-v2
    [*] --> NotRunning
    NotRunning --> Starting: first request
    Starting --> AcquiringLock: project resolved
    AcquiringLock --> Initializing: flock OK
    AcquiringLock --> NotRunning: lock held by other process
    Initializing --> Serving: journal+index loaded
    Serving --> Serving: request resets idle timer
    Serving --> ShuttingDown: idle timeout / SIGINT / Stop()
    ShuttingDown --> NotRunning: journal flushed,<br/>index persisted, lock released
```

The lock is a real `flock(2)` on Unix and a named handle on Windows
(`internal/daemon/flock_*.go`). It is the only reason a second
`dfmt` process gives up gracefully instead of fighting for the
socket.

Auto-start is in `internal/client/client.go`:

1. The CLI client tries to dial the project's socket / port.
2. On failure it spawns `dfmt daemon` as a detached subprocess.
3. It then retries the dial with exponential backoff (50 ms → 1.2 s,
   total budget ≈ 3.9 s).
4. If the daemon never becomes ready it returns an explicit error.

### 3.1 Two journal writers: daemon vs `dfmt mcp`

The auto-start path above is the **CLI's** path — `dfmt exec`,
`dfmt read`, `dfmt search`, etc. dial the daemon's socket / TCP port
and call handlers via JSON-RPC. The daemon owns the durable journal
handle.

The **MCP** path (`dfmt mcp`, what an AI agent launches over stdio)
is structurally different: it does **not** dial the daemon and does
**not** auto-spawn one. Instead `runMCP` (`internal/cli/dispatch.go`)
opens its own per-process `core.OpenJournal` handle on
`.dfmt/journal.jsonl`, builds an in-process `transport.Handlers`, and
serves MCP JSON-RPC entirely without the daemon involved. This is why
an agent works on a project that has never been `dfmt daemon`-started:
the MCP child process is self-contained.

```mermaid
flowchart LR
    subgraph CLI[CLI path — RPC]
        A[dfmt exec<br/>dfmt read<br/>dfmt search …] --> B[client.Client]
        B -.dial.-> Sock[(.dfmt/daemon.sock<br/>or .dfmt/port)]
        Sock --> D[daemon.Daemon<br/>journal handle]
    end

    subgraph MCP[MCP path — in-process]
        E[AI agent stdio] --> F[dfmt mcp]
        F --> G[runMCP<br/>own journal handle]
    end

    D --> J[(.dfmt/journal.jsonl)]
    G --> J
```

Concrete consequences:

- **Concurrent writers.** When the agent's `dfmt mcp` process is
  running and `dfmt daemon` is also running (e.g., the user uses
  the CLI in another terminal), both append to the same journal
  file. Each process serializes its own writes via `sync.Mutex`
  (`internal/core/journal.go`), and append-mode ordering across
  processes relies on POSIX `O_APPEND` atomicity for writes shorter
  than `PIPE_BUF`. There is no cross-process file lock on the
  journal.
- **Per-process indexes.** The MCP path persists its in-memory
  index back to `.dfmt/index.gob` on stdin EOF / SIGINT (via the
  `defer journal.Checkpoint + PersistIndex` block). The daemon
  persists on `Stop()`. A "last writer wins" race on `index.gob` is
  benign — both indexes derive from the same journal — but means
  the on-disk snapshot is never a strict superset of either side's
  view.
- **Hard-coded options on the MCP path.** Where the daemon reads
  journal options from `cfg.Storage.*`, `runMCP` hard-codes
  `MaxBytes: 10 MiB`, `Durable: true`, `BatchMS: 100`,
  `Compress: true`. (The `Compress` field is stored but not used —
  see §13.0.) Configuration drift between the two paths is a known
  asymmetry.

The daemon **does** still play roles the MCP path doesn't, even when
both are running: it owns the FS watcher (`capture.fs`), the HTTP
dashboard, the per-user process registry (`~/.dfmt/daemons.json`),
and the idle-shutdown timer.

---

## 4. Entry points

### 4.1 `cmd/dfmt/main.go` — primary CLI

```mermaid
flowchart LR
    A[dfmt args...] --> B{parse global flags<br/>--version --project --json}
    B --> C[strip global flags from os.Args]
    C --> D[cli.Dispatch]
    D --> E{subcommand}
    E -->|init / setup / doctor| F[local-only path]
    E -->|remember / search / recall| G[talks to daemon via client]
    E -->|exec / read / fetch / …| G
    E -->|daemon| H[blocks running daemon.Daemon]
    E -->|mcp| I[stdio MCP server]
```

Global flags are pulled out before dispatch so subcommands never see
them. `--project` overrides project discovery; `--json` switches the
CLI to machine-readable output.

> **Two version strings.** `cmd/dfmt/version.go` exports
> `var version = "dev"`, set at build time via `-ldflags
> "-X main.version=$VERSION"`. This is the value `dfmt --version`
> prints. Separately, `internal/cli/cli.go` exports a hard-coded
> `const Version = "0.1.0"` used by handlers that want a stable
> identifier (e.g., the MCP `serverInfo.version`). The two can
> diverge — release tags drive the ldflag value while `cli.Version`
> is bumped manually.

### 4.2 `cmd/dfmt-bench/main.go` — benchmarking binary

A separate binary that exercises the same packages directly. Modes:
`tokenize`, `index`, `search`, `exec`, `tokensaving`, `all`.

`tokensaving` is **not** an end-to-end benchmark of real agent
sessions — it is a synthetic comparison of *legacy* vs *modern*
wire bytes for six canonical scenarios baked into
`tokensaving.go::buildScenarios`:

1. small file read (inline tier)
2. `npm install` with progress bar (ANSI noise, no intent)
3. spinner retry-loop spam (no intent)
4. `go test` 200 PASS + 1 FAIL + panic (no intent)
5. `pytest` 200 PASS + 1 FAIL + traceback (no intent)
6. `cargo build` 250 compile + 2 errors (no intent)

The output is a side-by-side byte count and percent-saved per row,
plus a total. The numbers are pure JSON byte counts the MCP
transport would write to the wire — they reflect the savings
pipeline (kind-aware signal extraction, intent-less default
filtering, MCP envelope dedup), not abstract agent-context savings.
Backs the README's "40–90 %" claim **for these scenarios**; real
sessions vary.

---

## 5. CLI dispatch

`internal/cli/dispatch.go` (≈ 3 300 lines) is the largest single
file. It is essentially a giant switch on the subcommand and a
collection of tiny `runX()` functions per command.

```mermaid
mindmap
  root((dfmt))
    Project setup
      init
      quickstart
      setup
      setup --uninstall
      install-hooks
      shell-init
    Daemon control
      daemon
      stop
      list
      status
      doctor
    Memory ops
      remember
      note
      search
      recall
      stats
      tail
    Sandbox ops
      exec
      read
      fetch
      glob
      grep
      edit
      write
    Tasks
      task subject
      task done id
    Capture bridges
      capture git commit
      capture git checkout
      capture git push
      capture env.cwd
      capture shell
      capture tool
    Agent integration
      mcp
      dashboard
    Inspection
      config
      stats
      help
```

Each `runX()` function follows the same shape:

1. Resolve the project (`--project` → `DFMT_PROJECT` → walk up from
   `cwd` looking for `.dfmt/` or `.git/`).
2. Auto-init the project on first use.
3. Open a `client.Client` to the daemon (auto-spawn if needed).
4. Translate flags into RPC params, call the matching handler.
5. Render output as JSON or human prose depending on `--json`.

`buildCaptureParams()` (also in this file) is the bridge from the
git-hook / shell-integration `dfmt capture <kind>` invocations into
`transport.RememberParams`.

> **Subcommand maturity caveats.** Two subcommands shown above are
> partially implemented and worth knowing about before relying on
> them in operator scripts:
>
> - **`dfmt tail`** — `runTail` (`dispatch.go:1920`) prints
>   `"Streaming events..."` and returns; `--follow` prints
>   `"(tail --follow not yet implemented)"`. It does not actually
>   tail the journal yet. Use `dfmt search` or `dfmt recall` for
>   journal inspection in the meantime.
> - **`dfmt config`** — read-only display of three fields
>   (`capture.mcp.enabled`, `capture.fs.enabled`,
>   `storage.durability`). The trailing `args` parameter is reserved
>   for a future `get`/`set` UX; today the only way to change a
>   config value is to edit `.dfmt/config.yaml` directly. Use
>   `--json` to dump the full parsed config for inspection.
>
> All other subcommands in the mind-map are fully wired.

`dfmt stats` from the CLI passes `NoCache: true` to bypass the
daemon's 5 s stats cache — humans interpret unchanged numbers as
"DFMT broke" and the cache TTL would otherwise hold the same value
across two consecutive shell invocations. Dashboard polling, which
hits the same `Stats` handler, leaves `NoCache=false` so the cache
absorbs its high-frequency refresh load.

---

## 6. Daemon lifecycle

`internal/daemon/daemon.go` owns the runtime. The struct holds the
journal, the index, the sandbox, the redactor, the FS watcher, the
transport server, and the goroutine coordination primitives.

### 6.1 Start sequence

```mermaid
sequenceDiagram
    autonumber
    participant CLI as dfmt CLI
    participant D as daemon.Daemon
    participant L as flock
    participant J as journal
    participant I as index
    participant T as transport
    participant W as fswatcher

    CLI->>D: New(projectPath, cfg)
    D->>D: discover project, mkdir .dfmt (0700)
    D->>J: OpenJournal(.dfmt/journal.jsonl, opts)
    D->>I: LoadOrRebuild(index.gob, journal cursor)
    D->>D: build sandbox + handlers + redactor

    CLI->>D: Start(ctx)
    D->>L: AcquireLock(.dfmt/lock)
    alt lock held
        L-->>D: error
        D-->>CLI: ErrLocked
    else lock acquired
        L-->>D: ok
        D->>T: server.ListenAndServe
        T-->>D: actual address
        D->>D: write .dfmt/port (safefs)
        D->>W: start (if capture.fs.enabled)
        D->>D: spawn consumeFSWatch goroutine
        D->>D: spawn rebuildIndexAsync goroutine (if needed)
        D->>D: spawn idleMonitor goroutine
        D-->>CLI: Started
    end
```

### 6.2 Goroutines

| Goroutine            | Lifetime        | Purpose                                                    |
|----------------------|-----------------|------------------------------------------------------------|
| `consumeFSWatch`     | until shutdown  | Drain `fswatcher.Events`, redact, append to journal+index  |
| `rebuildIndexAsync`  | one-shot        | Replay journal into index when `index.gob` is missing/stale |
| `idleMonitor`        | until shutdown  | Tick every `clamp(idleTimeout/10, 1s, 1m)`; call `Stop()` on idleness |
| `transport.Serve`    | until shutdown  | HTTP / socket / TCP accept loop                            |
| `journal sync ticker`| until shutdown  | Periodic `fsync()` in batched mode                         |

Activity is tracked through `lastActivityNs` (atomic int64). Every
RPC entry calls `handlers.Touch()`, which simply stores `time.Now()`
without contention. The idle monitor compares it to the timeout —
no `time.AfterFunc`, no timer goroutine leaks.

### 6.3 Stop sequence

The order in `Stop()` matters and is documented in the source. A
simplified view:

```mermaid
flowchart TB
    A[atomic running := false] --> B[close shutdownCh]
    B --> C[close idleCh]
    C --> D[fswatcher.Stop]
    D --> E[cancel rebuild ctx]
    E --> F[wg.Wait — drain consumeFSWatch + rebuild]
    F --> G{rebuild completed?}
    G -- yes --> H[index.Persist]
    G -- no  --> I[skip persist — keep cursor stale, replay next start]
    H --> J[server.Shutdown]
    I --> J
    J --> K[journal.Close]
    K --> L[remove pid file]
    L --> M[unregister from process registry]
    M --> N[release lock]
```

Skipping `index.Persist()` when rebuild is incomplete is deliberate.
Half-built indices on disk would silently miss documents on the next
start; replaying the journal on the next start is cheap and
correct.

### 6.4 Health checks (`dfmt doctor`)

`runDoctor` (`internal/cli/dispatch.go`) is the operator-facing
diagnostic. It pre-computes daemon liveness once, then runs nine
state checks plus two cross-cutting passes:

| # | Check                                          | Pass criterion                                                      |
|---|------------------------------------------------|---------------------------------------------------------------------|
| 1 | Project exists                                  | `project.Discover(dir)` returns a path                              |
| 2 | Config valid                                    | `config.Load(dir)` returns non-nil and validates                    |
| 3 | `.dfmt` directory                               | exists and is a directory                                           |
| 4 | Go toolchain (build)                            | `runtime.Version() ≥ go1.26.2`; older versions report a **warning** (not a failure) noting unpatched stdlib CVEs (GO-2026-4866 / 4870 / 4946 / 4947) — the binary still works, but a future TLS dashboard would inherit unpatched code |
| 5 | Journal openable                                | `journal.jsonl` opens cleanly (or is missing — also OK)             |
| 6 | Index file readable                             | `index.gob` opens cleanly (or is missing — also OK)                 |
| 7 | Port file consistent with daemon liveness       | port file exists ⇔ daemon alive — stale ports flagged               |
| 8 | PID file consistent with daemon liveness        | pid file exists ⇔ daemon alive — stale PIDs flagged                 |
| 9 | Lock file consistent with daemon liveness       | if file exists but daemon dead, attempt `flock` to detect orphans   |

After the table-driven checks, two additional passes run:

- **Per-agent wire-up** (`checkAgentWireUp`) — for every agent in
  the setup manifest, verifies each recorded MCP config file still
  exists and that the binary it points at is resolvable. Closes
  the silent-rot case where a user moved the dfmt binary or wiped
  the agent's config dir between sessions.
- **Instruction-block staleness** (`checkInstructionBlockStaleness`)
  — diffs the project's `CLAUDE.md`/`AGENTS.md` block body against
  the canonical version baked into this dfmt build. Surfaces a
  warning (not a failure) with the cure: `dfmt init` to refresh.

Exit code is `1` if any of the nine table checks or the per-agent
wire-up reported a hard failure; warnings (Go toolchain old,
instruction block stale) do **not** flip the exit code.

---

## 7. Transport layer

Three protocols, one set of handlers.

```mermaid
flowchart LR
    subgraph Clients
        A[AI agent]
        B[CLI / git hook / shell]
        C[Browser]
    end

    subgraph Wire
        W1["MCP / stdio<br/>JSON-RPC 2.0<br/>line-delimited"]
        W2["Unix socket / TCP loopback<br/>JSON-RPC 2.0"]
        W3["HTTP<br/>POST / + GET /dashboard"]
    end

    H[handlers.Handlers]

    A --> W1 --> H
    B --> W2 --> H
    C --> W3 --> H
```

### 7.1 MCP (`internal/transport/mcp.go`)

Implements MCP 2024-11-05 enough to satisfy strict validators (Cursor,
Continue). Supports:

- `initialize` — capability handshake.
- `tools/list` — eleven tools (table below). Descriptions are
  **self-tuning**: observed compression ratios from past
  exec/read/fetch calls are appended so the agent gets up-to-date
  evidence that `intent` is paying off.
- `tools/call` — dispatches into the handlers.
- `ping` — health check.

| Tool             | Handler   | Required params                | Notes                                                          |
|------------------|-----------|--------------------------------|----------------------------------------------------------------|
| `dfmt_exec`      | Exec      | `code`                         | `lang`, `intent`, `return`, `timeout`. Default lang = bash.    |
| `dfmt_read`      | Read      | `path`                         | `intent`, `offset`, `limit`, `return`.                         |
| `dfmt_fetch`     | Fetch     | `url`                          | SSRF-defended (see §9). `intent`, `method`, `return`, timeout. |
| `dfmt_glob`      | Glob      | `pattern`                      | `intent`.                                                      |
| `dfmt_grep`      | Grep      | `pattern`                      | `files`, `intent`, `case_insensitive`, `context`.              |
| `dfmt_edit`      | Edit      | `path`, `old_string`, `new_string` | Atomic via safefs.                                         |
| `dfmt_write`     | Write     | `path`, `content`              | Logs SHA-256 + size, **not** the content body.                 |
| `dfmt_remember`  | Remember  | `type`                         | LLM token tracking + tags + actor.                             |
| `dfmt_search`    | Search    | `query`                        | BM25 over journal.                                             |
| `dfmt_recall`    | Recall    | (none)                         | `budget` (bytes), `format` (md/json/xml).                      |
| `dfmt_stats`     | Stats     | (none)                         | TTL-cached aggregates.                                         |

The MCP request loop has a `recover()` wrapper so a panic in any one
handler does not kill the daemon.

**Wire envelope (`MCPCallToolResult`).** Tool responses are wrapped
in the MCP-spec `{content, structuredContent, isError}` envelope
(`internal/transport/mcp.go`). DFMT's default emits a tiny sentinel
text block — `"dfmt: see structuredContent"` — in
`content[0].text` and puts the full payload in `structuredContent`.
Modern MCP clients (Claude Code, Cursor, Codex, Cline, Continue)
read `structuredContent`, so duplicating the JSON-stringified
payload into both fields would be a flat ≈ 50 % token tax on every
tool response. Setting `DFMT_MCP_LEGACY_CONTENT=1` re-enables the
duplicated form for any text-only MCP client that ignores
`structuredContent`.

### 7.2 HTTP + dashboard (`internal/transport/http.go`)

```mermaid
flowchart TB
    subgraph Routes
        R1["POST / — JSON-RPC entry"]
        R2["GET /dashboard — HTML page"]
        R3["GET /dashboard.js — embedded JS asset"]
        R4["GET /api/stats — Stats handler JSON"]
        R5["GET /api/daemons — registry snapshot"]
        R6["GET /healthz — text 'ok' 200, liveness"]
        R7["GET /readyz — text 'ok' 200, readiness"]
    end

    R1 -->|JSON-RPC 2.0| H[handlers]
    R2 -->|HTML + CSP sha256| Static[(embedded dashboard.html)]
    R3 -->|application/javascript| Static
    R4 -->|JSON| H
    R5 -->|JSON| Reg[(client.Registry)]
    R6 -->|200 OK| Live[liveness probe]
    R7 -->|200 OK| Ready[readiness probe]
```

The dashboard is fully embedded — no asset pipeline, no remote
`<script src=>` references. Both the HTML and the JS are served
from `internal/transport/dashboard.go` constants (`DashboardHTML`,
`DashboardJS`) and reach the browser via two separate routes
(`/dashboard` and `/dashboard.js`) so the page can use
`script-src 'self'` without inline-hash maintenance.

What the page renders, polling `/api/stats` and `/api/daemons`:

- **Project selector** (top-right dropdown) — switches between all
  daemons listed in `~/.dfmt/daemons.json` so a single browser tab
  can monitor every project.
- **Headline cards** — Total Events, Session Duration.
- **MCP Byte Savings** — Raw Bytes, Returned Bytes, Bytes Saved,
  Compression %, Stash Dedup Hits.
- **LLM Token Metrics** — Input/Output Tokens, Cache Savings,
  Cache Hit Rate (populated only when callers pass token counts to
  `dfmt_remember`).
- **Events by Type / by Priority** — two simple bar charts.
- **Session Info** — Session Start/End timestamps. The full CSP header is:

```text
default-src 'self'; style-src 'self' 'unsafe-inline';
script-src 'self'; img-src 'self' data:; connect-src 'self';
frame-ancestors 'none'; base-uri 'none'
```

`X-Content-Type-Options: nosniff` and `X-Frame-Options: DENY` round
out the headers. Style allows `unsafe-inline` because the dashboard
ships a small embedded `<style>` block; scripts do not.

Listener selection is platform-aware:

- **Windows.** Always HTTP-over-TCP-loopback (`HTTPServer`). Default
  bind is `127.0.0.1:0` (ephemeral); set `transport.http.enabled=true`
  + `transport.http.bind=127.0.0.1:8765` to pin the port for a stable
  dashboard URL. Unix-domain sockets are technically supported but
  not the natural choice for PowerShell-friendly tooling.
- **Unix, default.** Unix-domain socket at `.dfmt/daemon.sock`
  (mode 0600, bound under umask 0o077 — closes F-05). Dashboard
  unavailable on this path because browsers can't dial a Unix
  socket.
- **Unix, opt-in TCP.** Set `transport.http.enabled=true` to switch
  to TCP loopback instead of the socket. Daemon refuses to run both
  simultaneously — the CLI client chooses dial target via presence
  of `.dfmt/port` (TCP) vs `.dfmt/daemon.sock`, and exposing both
  would make that choice ambiguous.

In every TCP path the loopback constraint is enforced inside
`transport.NewHTTPServer`'s listener phase; a non-loopback bind fails
fast. The server writes its actual port to `.dfmt/port`
(`{"port":N}`, mode 0600) so the CLI client can find it without
parsing logs.

### 7.3 Handlers (`internal/transport/handlers.go`)

The longest file in the project (≈ 1 450 lines). It is the actual
business logic:

```mermaid
classDiagram
    class Handlers {
      +index *core.Index
      +journal core.Journal
      +sandbox sandbox.Sandbox
      -mu sync.RWMutex
      -execSem chan struct
      -fetchSem chan struct
      -readSem chan struct
      -writeSem chan struct
      -dedupCache map[string]dedupEntry
      -statsCache *StatsResponse
      +Remember(ctx, params)
      +Search(ctx, params)
      +Recall(ctx, params)
      +Stats(ctx, params)
      +Exec/Read/Fetch/Glob/Grep(ctx, params)
      +Edit/Write(ctx, params)
      +Touch()
    }
```

Important design choices:

- **Concurrency caps.** Buffered-channel semaphores: 4 concurrent
  `exec`, 8 `fetch`, 8 `read`, 4 `write`. An over-eager agent cannot
  DoS the host.
- **Server-side priority floor.** Agent-supplied `priority` on
  `Remember` is coerced to **P3** unless it is exactly `p2`, `p3`,
  or `p4`. The previous behavior — silently accepting any string
  including `p1` — let a prompt-injected agent claim the P1 band
  reserved for `decision` / `task.done` events the recall budget
  refuses to drop. P3 (not P4) is the fallback so legitimately
  surprising notes still beat routine tool calls under tight
  budget; the trade is a known false-elevation surface for malformed
  payloads.
- **Server-side Source override.** Whatever the agent puts in
  `params.Source` is **discarded** — the journal entry is always
  stamped `Source = mcp` because the agent IS calling via MCP and
  Source is a fact, not a parameter. Without this an agent could
  forge `source: "githook"` or `source: "fswatch"` and pass off
  noise as system-captured events.
- **Dedup window.** Identical `(kind, source, body)` triples within
  30 s and ≤ 64 distinct bodies share a single content-store entry.
  Cuts disk writes when an agent re-reads the same file twice.
- **Stats cache.** A 5 s TTL cache fronts `Stats()` because dashboard
  polling is aggressive and the journal walk is O(N).
- **Write hygiene.** `Write` events log SHA-256 + size, not the
  payload. Stops the journal from doubling as a leaked-secret store
  if an agent ever writes credentials.

### 7.4 JSON-RPC codec (`jsonrpc.go`, `rpc_params.go`)

A handwritten JSON-RPC 2.0 codec — one of the reasons DFMT keeps the
dependency list tiny. Strict params decoder splits `-32602`
(invalid params) from `-32603` (internal error) so MCP clients show
the right diagnostic.

---

## 8. Core domain

### 8.1 Events

```mermaid
classDiagram
    class Event {
      +ID string ULID
      +TS time.Time
      +Project string
      +Type EventType
      +Priority Priority
      +Source Source
      +Actor string
      +Data map[string]any
      +Refs []string
      +Tags []string
      +Sig string SHA-256/16
      +ComputeSig() string
      +Validate() bool
    }

    class EventType {
      <<enum>>
      file.read
      file.edit
      file.create
      file.delete
      task.create
      task.update
      task.done
      decision
      error
      git.commit
      git.checkout
      git.push
      git.stash
      git.diff
      env.cwd
      env.vars
      env.install
      shell.cmd
      prompt
      mcp.call
      subagent
      skill
      role
      intent
      data.ref
      note
      tombstone
    }

    class Priority {
      <<enum>>
      p1
      p2
      p3
      p4
    }

    class Source {
      <<enum>>
      mcp
      fswatch
      githook
      shell
      cli
    }

    class DynamicTypes {
      <<runtime-only>>
      tool.exec
      tool.read
      tool.fetch
      tool.glob
      tool.grep
      tool.edit
      tool.write
    }

    Event --> EventType
    Event --> Priority
    Event --> Source
```

The `Sig` field is `sha256(canonical_json)[:16hex]` and is computed on
every `Append`. **`Event.Validate()` exists but is currently called
only from tests** — production read paths (`journal.Stream`, index
replay) do **not** re-verify the signature. Treat the field as a
forward-looking integrity hook: useful evidence for tooling that
imports the journal externally, not a runtime guarantee that the
daemon catches in-place tampering.

> **Two type spaces.** The constants in `core/event.go` (left column
> of the enum above) are the *named* event types used by capture
> sources. Sandbox tool calls are journaled with **dynamic** type
> strings — `tool.exec`, `tool.read`, `tool.fetch`, `tool.glob`,
> `tool.grep`, `tool.edit`, `tool.write` — that are not in the enum
> at all. They flow through the same `Event` struct (`Type` is just
> a string), are always priority **P4**, source **mcp**, and the
> `intent` parameter lands in the `Tags` slice. Stats and the
> dashboard pivot on these dynamic types to compute byte savings,
> so renaming them is a wire-compat change.

> **Orphaned constants.** `EvtShellCmd` (`shell.cmd`) is defined and
> has a `capture.ShellCapture.BuildCommand` helper, but **neither
> is called from production code**. The `dfmt capture shell` CLI
> subcommand exists, but it builds a `note` event (P4, source
> `shell`) with `cmd` / `cwd` in `data` — not an `EvtShellCmd`. And
> the installed bash/zsh/fish hooks emit only `env.cwd`, not shell
> commands. The constant is reserved for a future feature; treat it
> as test-only today.

### 8.2 Classifier

`internal/core/classifier.go::Classify` walks the rule list first and
falls through to a `defaultPriorities` map keyed by event type only
on no rule match. The seeded rule list installed by `NewClassifier`
contains exactly two entries, both gated on `Type: EvtNote` plus a
tag whitelist:

```mermaid
flowchart TB
    A[Event] --> B{rules<br/>first match wins}
    B -- "Type=note AND tag in<br/>{summary, decision, strengths, ledger}" --> P2
    B -- "Type=note AND tag in<br/>{audit, finding, followup, preserve}" --> P3
    B -- no rule match --> D{defaultPriorities table}
    D -- decision / task.done --> P1
    D -- git.commit / git.push / git.checkout / error --> P2
    D -- file.edit / file.create / task.create --> P3
    D -- mcp.call / note / prompt --> P4
    D -- unknown type --> P4

    P1[(P1 — must keep)]
    P2[(P2 — usually keep)]
    P3[(P3 — keep if room)]
    P4[(P4 — drop first)]
```

Note-only tag elevation is what makes `dfmt_remember` worth using
properly. A note tagged `summary` lands in P2 and survives a tight
recall budget; an untagged note stays at P4 and gets dropped first.
The whitelist is closed: a note tagged `important` or `keep-this`
will **not** elevate. To register a new elevation tag, edit
`noteElevateP2Tags` / `noteElevateP3Tags` in `classifier.go`.

The two seeded rules are inserted in priority order (P2 first, P3
second), so a note carrying both kinds of tags lands in P2 — first
matching rule wins. Custom rules added via `Classifier.AddRule`
append after the seeds; if a custom rule should override the
note-elevation defaults, callers need to mutate the slice directly
(no public reorder API today).

### 8.3 Journal

Append-only JSONL at `.dfmt/journal.jsonl`.

```mermaid
flowchart LR
    A[handler] --> B[Append ctx, event]
    B --> C[redact]
    C --> D[encode JSON line]
    D --> E{durability}
    E -- durable --> F[write + fsync]
    E -- batched --> G[write + buffer]
    G --> H{buffer >= batchBytes<br/>or 100ms tick}
    H -- yes --> I[fsync]
    H -- no --> G
    F --> J{file size > 10 MiB}
    I --> J
    J -- yes --> K[Rotate]
    K --> L[write tombstone event]
    L --> M[rename journal.jsonl → journal.jsonl.&lt;ULID&gt;.jsonl]
    M --> N[create new journal.jsonl]
```

> **Compression flag is dead code.** `storage.compress_rotated` and
> the journal's internal `compress bool` are wired into the option
> struct but `Rotate()` never invokes any gzip path. Rotated
> segments stay as plain `.jsonl` on disk regardless of the
> setting. Treat the flag as reserved for a future feature.

Two durability modes:

- **`durable`** — `fsync()` after every write. Survives kernel crash;
  every event is on disk before the handler returns.
- **`batched`** (default) — `fsync()` every 100 ms or every batch
  bytes. ≤ 100 ms data risk, much higher throughput.

A single event is capped at **`maxEventBytes = 1 MiB`**
(`internal/core/journal.go`). `Append` returns `ErrEventTooLarge`
above the cap — agents that try to journal a multi-megabyte stdout
get an explicit refusal, not a silent truncation.

**Recovery on open.** `OpenJournal` reuses any existing
`journal.jsonl` rather than truncating: `scanLastID` walks the file
to find the highest event ID and seeds `hiCursor` so subsequent
`Checkpoint` calls and rotations behave correctly. Crash mid-write
typically leaves a partial trailing line, which the scanner skips
(it scans past trailing junk to keep recovery robust).

### 8.4 Index

`internal/core/index.go` is an in-memory inverted index plus a
trigram fallback. The interesting bits:

```mermaid
flowchart TB
    subgraph Add
        A1[event.Data → searchable text]
        A2[Tokenize<br/>unicode-aware]
        A3[Stopword filter<br/>EN + TR]
        A4[Porter stem]
        A5[Update postings + trigrams]
    end

    subgraph Search
        S1[query → tokens → stems]
        S2[Lookup stem postings<br/>BM25 score k1=1.2 b=0.75]
        S3[Top-K via heap<br/>Layer=1 BM25 only]
    end

    A1 --> A2 --> A3 --> A4 --> A5
    S1 --> S2 --> S3
```

> **Trigrams are indexed but not queried.** `Index.Add` populates a
> trigram posting list alongside the stem postings, and the index is
> persisted with both, but `SearchBM25` only walks `stemPL`. The
> trigram path and `core.Levenshtein` exist for a future fuzzy-search
> layer (see §8.5 Search layers — `trigram` and `fuzzy` cases are
> reserved). Today both are dead weight in the hot path.

Persistence (`index_persist.go`) uses **custom JSON** marshaling.
The original implementation tried `encoding/gob`, but Go's gob can't
encode unexported map fields and the index is mostly unexported by
design. Custom `MarshalJSON` / `UnmarshalJSON` hops through an
internal `indexJSON` struct so unexported fields can travel safely.

The on-disk filename is still `index.gob` for backwards
compatibility with older project directories.

`LoadIndexWithCursor` keeps a small `index.cursor` file with the ID
of the last indexed event **plus a `TokenVer` field** matching the
`TokenizerVersion = 1` constant in `index_persist.go`. On daemon
start, only the journal tail beyond the cursor is replayed — typical
startup re-indexes seconds, not minutes. **Bumping `TokenizerVersion`
forces a full rebuild on the next start** even if the cursor file is
fresh: tokens stemmed by an older Porter implementation would no
longer match new query stems, so silently keeping the index would
break search.

### 8.5 Search layers

The Search RPC accepts an optional `layer` parameter
(`bm25` / `trigram` / `fuzzy`). At present only **`bm25`** is
implemented; `trigram` and `fuzzy` are reserved cases in the switch
that return an empty result set. The default when `layer` is
omitted is `bm25`. The default `limit` when omitted is 10.

Hits carry `id`, `score`, and `layer` (an integer rank, not the
string above). `type` and `source` are populated by the caller's
post-processing where available.

### 8.6 Tokenization

`Tokenize()` is unicode-aware: any letter, digit, or `_` joins the
current token; everything else breaks it. Tokens are kept if their
length is between 2 and 64 bytes. The Porter stemmer runs on the
result. English and Turkish stopwords are baked in as constants —
no stopword files to ship.

---

## 9. Sandbox

`internal/sandbox/` is the side-effect frontier. Seven operations
share one structure:

```mermaid
flowchart TB
    subgraph SB[sandbox]
        A[Operation request] --> B{Permission gate}
        B -- denied --> Z["Error with hint:<br/>add allow:op:pattern to .dfmt/permissions.yaml"]
        B -- allowed --> C[Run]
        C --> D[Capture raw output]
        D --> N[NormalizeOutput<br/>strip ANSI, CR-overwrites, repeat-spam]
        N --> AP[ApplyReturnPolicy<br/>filtered body + raw bytes + matches]
    end

    subgraph TR[transport handler]
        AP --> R[Redact secrets<br/>provider keys, AWS, GitHub, JWT, PEM, …]
        R --> ST[Stash redacted bytes<br/>content store, size-evicted at 64 MiB]
        R --> RT[Return redacted body to caller]
        R --> LJ[journal.Append<br/>with redacted data + tags]
    end
```

Redaction is **not** a sandbox step — the sandbox returns raw bytes
plus the return-policy-filtered body, then the transport handler
runs `h.redactString` / `h.redactData` on each output field before
stashing into the content store and appending to the journal. This
matters when reading the source: `internal/sandbox/permissions.go`
contains no `redact` calls; the redactor reference lives in
`internal/transport/handlers.go` and is held by `Handlers`, not by
`SandboxImpl`.

### 9.1 Default policy (`permissions.go`)

```yaml
allow:
  exec: git, npm, pnpm, pytest, cargo, go, echo, ls, cat, find,
        grep, dir, pwd, whoami, wc, tail, node, python (and "X *" forms)
  read:  **
  write: **
  edit:  **
  fetch: https://*, http://*

deny:
  exec:  sudo *, rm -rf /*, "curl * | sh", "wget * | sh",
         shutdown *, reboot *, mkfs *, dd if=*,
         dfmt, dfmt *           # blocks recursive bypass
  read:  .env*, **/.env*, **/secrets/**, **/id_rsa, **/id_*
  write: same as read   +   .dfmt/**, **/.dfmt/**, .git/**, **/.git/**
  edit:  same as write  (F-29: edit is a write in disguise)
  fetch: http(s)://169.254.169.254/*       # AWS / IMDS
         http(s)://metadata.google.internal/*
         http://metadata.goog/*            # GCP
         file://*
```

A `LoadPolicy(path)` function exists in `permissions.go` to parse
the `allow:exec:base-cmd *` line format from
`.dfmt/permissions.yaml`, but as of this writing **the daemon does
not call it** — `sandbox.NewSandbox(projectPath)` always installs
`DefaultPolicy()` (see §13.4). Every denial error still ends with
a one-line hint pointing at `.dfmt/permissions.yaml`, on the
assumption that the loader will land in a near-term sprint;
operators relying on those hints today will find their overrides
silently ignored.

### 9.2 Recursive bypass

`dfmt` and `dfmt *` are on the deny list. Without that, an agent
could shell out to `dfmt exec '...'` from inside a sandboxed exec
and inherit a fresh, empty policy. The deny rule preserves the
"every operation is sandboxed" invariant.

### 9.3 Fetch SSRF defense

```mermaid
flowchart TB
    A[Fetch url] --> B{Scheme allow-list<br/>http, https only}
    B -- no --> Z[reject]
    B -- yes --> C{Hostname deny list<br/>cloud metadata IPs}
    C -- match --> Z
    C -- ok --> D{Pre-DNS classification<br/>literal IP loopback / RFC1918 / link-local}
    D -- match --> Z
    D -- ok --> E[Custom DialContext<br/>resolve, classify each candidate IP,<br/>refuse rebinds to private space]
    E -- all blocked --> Z
    E -- ok --> F[HTTP request]
    F --> G[Response size cap]
    G --> H[Redact + intent-match]
```

The DialContext step is the part most pure URL-checks miss: a remote
hostname can resolve to RFC1918 today and to public space tomorrow,
or vice versa. Resolution + classification happens at connect time,
so DNS rebinding cannot smuggle a request past the policy.

### 9.4 Intent extraction (`intent.go`)

Output filtering is driven by the `return` parameter
(`auto` / `raw` / `summary` / `search`) and the actual size of the
captured output. Constants are in `sandbox.go`:

| Output size                                  | `auto` (default) behavior                                |
|----------------------------------------------|----------------------------------------------------------|
| ≤ 4 KiB (`InlineThreshold`)                  | inline body only — no summary, matches, or vocabulary    |
| 4 KiB – 64 KiB (`MediumThreshold`)           | summary + 5 BM25 matches + 10 vocabulary terms           |
| &gt; 64 KiB                                  | summary + 10 BM25 matches + 20 vocabulary terms          |
| `raw` mode                                   | full body inline, capped at 256 KiB (`MaxRawBytes`)      |

Matches are scored by BM25 against the `intent` keywords. **Kind-aware
signal promotion** is always on (`internal/sandbox/signals.go`):
anchored regexes for Go test runners (`^---\s+FAIL:\s`, `^panic:\s`,
`^fatal error:\s`), pytest tracebacks, cargo errors, generic
`error:` / `Error:` prefixes, and language-specific exception
headers fire on a trimmed line. Matches earn a synthetic
`SignalScore = 100.0` so they outrank any BM25 keyword hit
(`scoreLine` tops out near ~3.6), with up to `signalCap = 8`
signals merged ahead of the keyword matches. Net effect: a `go test`
run with no intent still surfaces which test failed. Vocabulary is
a small set of distinctive terms the caller can use to refine the
next call.

Tiering is by **output size**, not by event priority. Earlier drafts
of this document conflated the two; the actual tier check is purely
a byte threshold.

### 9.5 Edit / Write atomicity

`internal/safefs/safefs.go` is the symlink-safe write helper. It
writes to a temp file in the destination directory, fsyncs it, and
atomically renames over the target. Symlink targets in protected
paths (e.g. `.dfmt/`) are refused outright — that closed F-08, which
was the case where a malicious symlink turned a write into a path
traversal.

### 9.6 Sandbox environment passthrough and block-list

`buildEnv` (`permissions.go`) is the gate between the daemon's own
process environment and the subprocess that `dfmt_exec` launches.
The base set is **curated, not inherited**:

- Unix: `HOME`, `USER`, `PATH`, `LANG=en_US.UTF-8`, `TERM=xterm`.
- Windows: `PATH`, `TMP`, `TEMP`, `LOCALAPPDATA`, `USERPROFILE`,
  plus `HOME=$USERPROFILE` and `USER=$USERNAME` synthesized so
  Unix-style scripts work in Git Bash.

Every variable from the daemon's environment whose name starts with
`DFMT_EXEC_` is forwarded verbatim — the escape hatch for
operator-managed extras like `DFMT_EXEC_GOFLAGS` or
`DFMT_EXEC_NPM_TOKEN`. Anything else from the daemon's env is
**not** passed through.

Then the caller's `req.Env` map is merged in, but each name is run
through `isSandboxEnvBlocked` first. The block-list closes the
loader-/startup-hook injection vector: an agent that controls these
variables can effectively replace any allowed binary with arbitrary
code.

| Class                    | Blocked names / prefixes                                   |
|--------------------------|------------------------------------------------------------|
| Dynamic loader           | `LD_*`, `DYLD_*`                                           |
| Git internals            | `GIT_*` (covers `GIT_EXEC_PATH`, `GIT_SSH`, `GIT_INDEX_FILE`, …) |
| Node.js                  | `NODE_*` (covers `NODE_OPTIONS`, `NODE_PATH`, `NODE_TLS_REJECT_UNAUTHORIZED`) |
| npm                      | `NPM_CONFIG_*`                                             |
| Python                   | `PYTHON*` (covers `PYTHONSTARTUP`, `PYTHONPATH`, `PYTHONHOME`) |
| Ruby toolchain           | `RUBY*`, `BUNDLE_*`, `GEM_*`                               |
| Perl                     | `PERL5*`                                                   |
| Lua                      | `LUA_*`                                                    |
| PHP                      | `PHP*`, `COMPOSER_*`                                       |
| JVM                      | `JAVA_*`, `_JAVA_OPTIONS`                                  |
| Shell hooks              | `BASH_ENV`, `ENV`, `PS4`, `PROMPT_COMMAND`                 |
| Path / interpreter       | `PATH`, `IFS`, `PATHEXT`, `COMSPEC`                        |
| User identity            | `HOME`, `USER`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `SYSTEMROOT` |

The list is closed by exclusion — an operator who allow-lists a new
interpreter (`ruby`, `php`, `java`) needs the corresponding env
prefix already in `sandboxBlockedEnvPrefixes`, otherwise the agent
can override the loader of that newly-allowed binary. F-G-LOW-2
from the security audit added the npm / bundle / gem / composer /
lua / java / php rows after the original list missed them.

### 9.7 Size and count caps

The sandbox enforces several hard limits the agent cannot raise:

| Constant                | Value      | Effect                                                                |
|-------------------------|------------|-----------------------------------------------------------------------|
| `MaxRawBytes`           | 256 KiB    | upper bound on `exec` stdout returned in `raw` mode                   |
| `MaxSandboxReadBytes`   | 4 MiB      | hard ceiling on `dfmt_read` regardless of the caller's `limit`        |
| `MaxFetchBodyBytes`     | 8 MiB      | response-body cap for `dfmt_fetch`                                    |
| `maxGlobInlineFiles`    | 500        | inline file-list cap; overflow surfaces a "(truncated)" sentinel match |
| grep match cap          | 100        | total `GrepMatch` results returned per call                           |
| `maxGrepLineBytes`      | 200        | per-line truncation in grep matches (minified JS / log lines)          |
| `maxGrepPatternBytes`   | 4096       | upper bound on the user-supplied regex source                         |
| `maxGrepPatternNodes`   | 1024       | parsed-AST node count cap (validates via `regexp/syntax`)             |
| `maxGrepRepeatNesting`  | 3          | nested-quantifier depth cap (`a*b*c*d*` is depth 4 → reject)          |
| HTTP redirect chain     | 10         | `http.Client.CheckRedirect` returns error on the 11th hop              |
| HTTP TLS handshake      | 10 s       | per-connection                                                         |
| HTTP idle conn          | 90 s       | keep-alive recycle window                                              |
| HTTP default timeout    | 30 s       | applied when caller passes `req.Timeout <= 0`                         |

The grep pattern complexity check uses `regexp/syntax.Parse` to
count AST nodes and the deepest repetition nesting, then rejects
patterns that would let an agent push the daemon into expensive
backtracking-style work even though Go's RE2 engine is linear-time
in input length.

### 9.8 Content store

`internal/content/store.go` is the post-sandbox stash that lets
agents fetch the *full* output of a tool call after the
intent-filtered version has already returned. The relationship is:

```mermaid
flowchart LR
    A[handlers.Exec / Read / Fetch] --> B["stashContent(kind, source, intent, redacted body)"]
    B --> C{TTL?}
    C -- "0 (default)" --> D[in-memory + persist to disk]
    C -- "&gt; 0" --> E[in-memory only]
    D --> F[<id>.json.gz<br/>0600, gzip-encoded JSON]
    E --> G[lazy expiry on Get]
    A --> H[returns content_id<br/>chunk-set ID]
    H --> I[agent later: fetch full body via content_id]
```

Key invariants:

| Aspect            | Behavior                                                                                   |
|-------------------|--------------------------------------------------------------------------------------------|
| Default cap       | 64 MiB total in-memory (`StoreOptions.MaxSize`); `evict()` drops oldest sets until the new chunk fits. |
| Persistence       | Sets with `TTL == 0` are persisted to `<dir>/<id>.json.gz` (mode 0600, **gzip-compressed JSON**). Sets with `TTL > 0` are memory-only. |
| Lazy expiry       | `GetChunk` / `GetChunkSet` check `set.Created + set.TTL` on every access; expired sets are dropped before returning a miss. |
| Bulk prune        | `PruneExpired()` walks the table and drops every expired set; `O(\|sets\|)` so cheap in practice. |
| ID validation     | `chunkIDPattern = ^[A-Za-z0-9_-]{1,128}$` — production callers pass ULIDs; the regex stops a caller from smuggling `..` / `/` / drive letters into a filesystem path. |
| Chunk kinds       | `markdown`, `code`, `json`, `text`, `log-lines` (`ChunkKind` enum).                        |
| Dedup at stash    | The transport handler keys `stashContent` by `sha256(kind, source, body)` for 30s with a cap of 64 distinct bodies — re-reads of the same file in a tight loop share one chunk-set ID instead of writing one per call. |

> **Compression scope.** `compress/gzip` *is* used here, despite
> being unwired in the journal-rotation path. The chunk-set
> `<id>.json.gz` files on disk are real gzip envelopes; an
> operator inspecting `.dfmt/content/` should `gunzip -c` to read
> them. The dead `storage.compress_rotated` flag from §13 only
> governs the journal's rotated `.jsonl` segments.

---

## 10. Capture pipeline

DFMT can capture events from five sources. All five funnel into the
same `journal.Append + index.Add` path, so downstream code does not
care where an event originated.

```mermaid
flowchart LR
    subgraph Sources
      M[MCP calls]
      C[CLI commands]
      F[FS watcher]
      G[Git hooks]
      S[Shell integration]
    end

    M --> R[redactor]
    C --> R
    F --> R
    G --> R
    S --> R

    R --> J[(journal)]
    R --> I[(index)]
```

| Source              | Implemented in                                     | Status     | Activation                              |
|---------------------|----------------------------------------------------|------------|-----------------------------------------|
| MCP calls           | `internal/transport/{mcp.go, handlers.go}`         | live       | always                                  |
| CLI commands        | `internal/cli/dispatch.go::buildCaptureParams`     | live       | always                                  |
| FS watcher          | `internal/capture/fswatch{,_linux,_windows}.go`    | live       | `capture.fs.enabled=true`               |
| Git hooks           | `internal/capture/git.go` + `cli/hooks_embed.go`   | live       | `dfmt install-hooks`                    |
| Shell integration   | `internal/capture/shell.go`                        | live       | `dfmt shell-init bash\|zsh\|fish` + `eval $(...)` |

### 10.1 FS watcher

```mermaid
flowchart TB
    subgraph Linux
        L1[inotify_init]
        L2[walk + add watches]
        L3[read events]
    end
    subgraph Windows
        W1[ReadDirectoryChangesW per watch root]
        W2[mod-time cache for static subtrees]
        W3[only re-walk dirs whose mtime changed]
    end

    Events["Events chan<br/>buffered 100"] --> Debounce
    Debounce[Per-path debounce<br/>map path → time<br/>cooldown = capture.fs.debounce_ms] --> Consume[consumeFSWatch]
    Consume --> J[(journal)]
    Consume --> I[(index)]

    L3 --> Events
    W1 --> Events
    W2 --> W3 --> Events
```

The Windows path is heavily optimized because `ReadDirectoryChangesW`
fires for every metadata blip, and naive consumers re-walk the tree
on each event. The mod-time cache means a static tree pays roughly
zero work after the first scan.

Debounce is per-path and non-blocking. Each path keeps a `time.Time`
in a small map; events within `capture.fs.debounce_ms` (default
**500 ms**) of the last one for the same path are dropped. A
cleanup goroutine evicts entries older than `10 × debounce_ms` so
the map cannot grow unbounded.

The `Events` channel is buffered at **100 events**. When
`consumeFSWatch` falls behind (e.g., heavy redact CPU on a big
text-paste burst), `FSWatcher.emit` is non-blocking — overflow
events are dropped and counted in an atomic `droppedEvents`
counter, surfaced via `FSWatcher.DroppedEvents()`. There is no
back-pressure on the OS-level watcher (`inotify` /
`ReadDirectoryChangesW`); kernel-buffer overflow there is reported
as a separate `IN_Q_OVERFLOW` event on Linux.

### 10.2 Git hooks

`dfmt install-hooks` writes three small shell scripts into
`.git/hooks/` from `internal/cli/hooks_embed.go`:

```mermaid
sequenceDiagram
    participant Git
    participant H as .git/hooks/post-commit
    participant CLI as dfmt capture
    participant D as daemon

    Git->>H: post-commit (after commit lands)
    H->>CLI: dfmt capture git commit <hash> <message>
    CLI->>D: client.Remember(EvtGitCommit, P2, ...)
    D->>D: journal.Append + index.Add
    D-->>CLI: ack
    CLI-->>H: exit 0
```

The three hooks pass slightly different positional arguments:

| Hook            | Invocation                                                          |
|-----------------|---------------------------------------------------------------------|
| `post-commit`   | `dfmt capture git commit "$COMMIT_HASH" "$COMMIT_MSG"`              |
| `post-checkout` | `dfmt capture git checkout "$REF" "$IS_BRANCH"` — `IS_BRANCH` is `true`/`false` based on `git show-ref --verify --quiet refs/heads/$REF` |
| `pre-push`      | `dfmt capture git push "$REMOTE" "$BRANCH"` — `BRANCH` from `git symbolic-ref --short HEAD` |

The hooks rely on `command -v dfmt` for runtime PATH resolution —
they are emitted verbatim from the embedded
`internal/cli/hooks/git-*.sh` scripts and are **not** rewritten with
an absolute path at install time (`installHookContent` / 
`installShellHookContent` explicitly ignore the `dfmtBin` argument
they receive). A missing binary degrades each hook to a no-op
rather than failing the commit, and the `dfmt capture …` invocation
is backgrounded with `&` so a slow daemon-start never blocks `git`.

> **Different from MCP configs.** Per-agent MCP configs *do* pin an
> absolute path via `setup.ResolveDFMTCommand()` (§12), because the
> agent process spawns the binary directly with no shell in the
> middle. Git/shell hooks run inside a user shell that already has
> a PATH, so PATH lookup is the simpler and correct choice there.

### 10.3 Shell integration

`dfmt shell-init <shell>` prints a `here-doc` block for the user to
source. The actual hook scripts are embedded in the binary at
`internal/cli/hooks/{bash.sh,zsh.sh,fish.fish}` and are emitted
verbatim, with one substitution: any leading `dfmt` is replaced
with the absolute path of the running binary (resolved by
`os.Executable()`) so sourced hooks invoke the correct dfmt even
when PATH changes later.

Bash (`hooks/bash.sh`) — wires a `PROMPT_COMMAND` callback:

```bash
PROMPT_COMMAND="dfmt_prompt_hook"

dfmt_prompt_hook() {
    if command -v dfmt >/dev/null 2>&1; then
        dfmt capture env.cwd "$PWD" 2>/dev/null
    fi
}
```

> **Note for operators.** The bash hook **overwrites** `PROMPT_COMMAND`
> rather than appending. If you already use `PROMPT_COMMAND` for
> something else, source the dfmt hook first and chain manually.

Zsh (`hooks/zsh.sh`) uses `add-zsh-hook precmd dfmt_precmd_hook` —
non-destructive, sits alongside any existing precmd hooks.

Fish (`hooks/fish.fish`) uses an event handler on directory change.

In all cases the hook fires a P4 `env.cwd` event whenever the
current working directory changes — the cheapest possible
integration that still tells future agents "the user navigated to
this folder".

---

## 11. Session memory and recall

The whole system exists to make `dfmt_recall` good. The recall
algorithm is **per-tier streaming with FIFO eviction** under a byte
budget:

```mermaid
flowchart TB
    A[journal.Stream] --> B{classify by priority}
    B --> P1Q[(P1 ring buffer<br/>cap 5000)]
    B --> P2Q[(P2 ring buffer<br/>cap 1000)]
    B --> P3Q[(P3 ring buffer<br/>cap 500)]
    B --> P4Q[(P4 ring buffer<br/>cap 500)]

    P1Q --> Render
    P2Q --> Render
    P3Q --> Render
    P4Q --> Render

    Render[Greedy fill under budget<br/>default 4096 bytes, no upper cap] --> Out[markdown / json / xml]
```

The ring buffer caps mean the streaming pass uses bounded memory —
even on a multi-million-event journal. The greedy fill walks the
buffers in priority order; when the budget is exhausted, lower-tier
content is left out.

The format defaults to markdown because the consumer is usually an
AI agent that has just been compacted. JSON / XML are available for
tools.

### 11.1 Where recall actually runs

The streaming + FIFO eviction + render loop above all live in
`internal/transport/handlers.go::Recall` (around lines 555–686).
That handler:

1. Streams `journal.Stream` directly into four tier buckets with the
   caps shown in the diagram.
2. Concatenates buckets in priority order, reverse-iterating each
   bucket so newest-within-tier surfaces first.
3. Renders compact markdown lines inline (`- [P1] 04-27 22:38:23
   @actor #tag {data}`) into a single `RecallResponse.Snapshot`
   string, breaking when the byte budget would be exceeded.

The `format` request parameter is accepted but currently informational
only — `handlers.Recall` always emits markdown, regardless of whether
the caller asked for `md`, `json`, or `xml`. The parallel
`internal/retrieve/` package (with `SnapshotBuilder`, `MarkdownRenderer`,
`JSONRenderer`, `XMLRenderer`) is **not imported anywhere outside its
own `_test.go`** — the production wiring went straight through the
transport handler. Treat the `retrieve` package as a reserved, parallel
implementation; its renderers do not participate in either the MCP
`dfmt_recall` path or the CLI `dfmt recall` command.

### 11.2 Session-continuity loop (Claude Code)

`dfmt init` writes a project-local `.claude/settings.json` that
hooks Claude Code's three lifecycle phases. Together they form a
closed loop so a new session can pick up where the previous one
ended even after a context compaction:

```mermaid
sequenceDiagram
    autonumber
    participant CC as Claude Code
    participant H as .claude/settings.json hooks
    participant DF as dfmt CLI
    participant Recall as .dfmt/last-recall.md
    participant J as journal

    rect rgb(240,248,255)
    Note over CC,J: Mid-session — every tool call
    CC->>H: PreToolUse({tool_name, args})
    H->>DF: dfmt capture tool (5s timeout)
    DF->>J: append note event<br/>tags=[<tool_name>]
    end

    rect rgb(255,250,240)
    Note over CC,Recall: Compaction trigger (~80% context)
    CC->>H: PreCompact()
    H->>DF: dfmt recall --save --format md (30s timeout)
    DF->>J: stream + classify by tier
    DF->>Recall: write 0600 markdown snapshot
    end

    rect rgb(245,255,245)
    Note over CC,Recall: Next session start
    CC->>H: SessionStart()
    H->>Recall: cat .dfmt/last-recall.md (10s timeout)
    Recall-->>CC: prepended into new session
    end
```

The three hooks and their commands (templated by OS — POSIX shell
on Unix/macOS, PowerShell on Windows):

| Hook | Command | Timeout |
|------|---------|---------|
| `PreToolUse` (matcher `""` — fires on every tool) | `dfmt capture tool` | 5 s |
| `PreCompact` (fires before Claude Code compacts the conversation) | `dfmt recall --save --format md` | 30 s |
| `SessionStart` (fires when the user opens a new conversation) | POSIX: `if [ -f .dfmt/last-recall.md ]; then echo '--- Previous session summary ---' && cat .dfmt/last-recall.md && echo '--- End of previous session ---'; fi` (PowerShell equivalent on Windows) | 10 s |

The same `.claude/settings.json` carries DFMT's MCP allow-list
(`mcp__dfmt__dfmt_exec` through `mcp__dfmt__dfmt_write`, 11 entries)
so the agent can call DFMT tools without per-call permission
prompts. File mode is `0o600`.

### 11.3 Native vs MCP tool tracking

The `PreToolUse` hook above lets DFMT count *every* tool the agent
invokes — including Claude Code's built-in `Bash` / `Read` /
`Write` / `Glob` / `Grep` / etc. that bypass DFMT's MCP routing.
Each such call lands in the journal as a `note` event with the
tool name in `Tags`. `Stats` walks the journal and pivots the
tags against a known native-tool set:

```go
// internal/transport/handlers.go
var knownNativeTools = map[string]struct{}{
    "Bash": {}, "Read": {}, "Edit": {}, "Write": {}, "Glob": {},
    "Grep": {}, "WebFetch": {}, "WebSearch": {}, "TaskCreate": {},
    "TaskUpdate": {}, "TaskDone": {}, "Agent": {},
}
```

The aggregated counts surface in the `StatsResponse`:

| Field                  | Meaning                                                       |
|------------------------|---------------------------------------------------------------|
| `native_tool_calls`    | map of `tool_name → count` for the 12 built-ins above         |
| `mcp_tool_calls`       | map of `tool.exec` / `tool.read` / etc. → count               |
| `native_tool_bypass_rate` | `sum(native) / (sum(native) + sum(mcp))` as a percentage   |

The dashboard renders all three. A high `native_tool_bypass_rate`
means the agent is doing work that DFMT could have token-saved but
didn't get the chance to — usually because the agent's CLAUDE.md /
AGENTS.md routing instructions slipped or because the user pasted
an instruction that overrode them. This is *measurement*, not
enforcement: DFMT cannot force Claude Code to route through MCP.

---

## 12. Agent setup

`internal/setup/` auto-detects nine agents and writes the MCP
configuration each one expects.

```mermaid
flowchart TB
    A[dfmt setup or quickstart] --> B[setup.Detect]
    B --> C{For each agent}
    C --> D{Detected?}
    D -- no  --> E[skip]
    D -- yes --> F[write MCP config]
    F --> G[record path in manifest]
    G --> H["~/.local/share/dfmt/setup-manifest.json"]
    H --> I{is Claude Code?}
    I -- yes --> J[surgical patch ~/.claude.json<br/>+ ~/.claude/settings.json hooks]
    I -- no  --> K[done]

    L[dfmt setup --uninstall] --> M[load manifest]
    M --> N[strip files / restore backups]
    N --> O[clear manifest]
```

| Agent ID      | Files written                                                         | Notes                                            |
|---------------|-----------------------------------------------------------------------|--------------------------------------------------|
| `claude-code` | `~/.claude/mcp.json` (primary) + `~/.claude.json` (patched, user scope) + project `.claude/settings.json` (init-time hooks) | `.dfmt-bak` backup taken on first patch.        |
| `cursor`      | `~/.cursor/mcp.json`                                                  |                                                  |
| `vscode`      | `~/.vscode/mcp.json`                                                  | Copilot / VS Code MCP.                           |
| `codex`       | `~/.codex/mcp.json`                                                   | Codex CLI.                                       |
| `gemini`      | `~/.gemini/mcp.json`                                                  |                                                  |
| `windsurf`    | `~/.windsurf/mcp.json`                                                |                                                  |
| `zed`         | `~/.config/zed/mcp.json`                                              |                                                  |
| `continue`    | `~/.config/continue/mcp.json`                                         |                                                  |
| `opencode`    | `~/.config/opencode/mcp.json`                                         |                                                  |

Every config carries the same shape:

```json
{
  "mcpServers": {
    "dfmt": {
      "command": "<absolute path to dfmt resolved at setup time>",
      "args": ["mcp"]
    }
  }
}
```

The `command` field is set by `setup.ResolveDFMTCommand()`. Resolution
order: `exec.LookPath("dfmt")` → `os.Executable()` → literal `"dfmt"`.
Writing absolute paths makes MCP launches PATH-independent — the
agent spawns the exact binary that was on PATH at setup time, even
if a later login shell ends up with a different PATH.

`~/.claude.json` is special: it is shared across many Claude
features and the setup code patches it surgically (writing a
`~/.claude.json.dfmt.bak` backup before the first successful patch).
It is intentionally **not** added to the manifest, because the
manifest-based uninstall calls `os.Remove` on every tracked path
and `~/.claude.json` is a user-owned file — only `mcpServers.dfmt`
is stripped on uninstall.

The project-local `.claude/settings.json` is created by `dfmt init`
and carries the DFMT MCP allow-list plus the `PreCompact` and
`SessionStart` hook references. The user-home `~/.claude/settings.json`
is **never** modified — `WriteProjectClaudeSettings` refuses to write
under `$HOME/.claude/`.

`dfmt doctor` is the inspection counterpart: it reports per-agent
wire-up status (does the manifest-recorded file still exist? does
the binary path resolve?), plus project-level health (config valid,
journal openable, index loadable, lock file fresh).

### 12.1 Project instruction file injection

In addition to the per-agent MCP configs above, `dfmt setup` also
injects a marker-delimited DFMT routing block into the project's
agent-instruction files (`internal/setup/projectdocs.go`):

| File                                  | Format            | Marker                             |
|---------------------------------------|-------------------|------------------------------------|
| `CLAUDE.md`                           | Markdown          | `<!-- dfmt:v1 begin --> … <!-- dfmt:v1 end -->` |
| `AGENTS.md`                           | Markdown          | same                               |
| `GEMINI.md`                           | Markdown          | same                               |
| `.github/copilot-instructions.md`     | Markdown          | same                               |
| `.cursorrules`                        | Plain text        | `# dfmt:v1 begin` … `# dfmt:v1 end`|
| `.windsurfrules`                      | Plain text        | same                               |

The block contains the "use `dfmt_*` tools instead of native ones"
contract that makes a session honor MCP routing. Each entry is
recorded in the manifest with `Kind: FileKindStrip` so uninstall
**only removes the block**, leaving the rest of the file (and any
content the user added before or after the markers) untouched. If
the file becomes empty after stripping (DFMT was the sole resident),
it is removed.

Files written from scratch by DFMT (`~/.claude/mcp.json`,
`~/.codex/mcp.json`, etc.) carry `Kind: FileKindDelete` instead, so
uninstall removes them outright (with `.dfmt.bak` restore where one
exists).

Backups are written by `setup.BackupFile(path)`: if `path` exists,
its bytes are copied to `<path>.dfmt.bak` *next to* the original.
If a `.dfmt.bak` already exists from an earlier run, it is rolled
to `.dfmt.bak.<unix-nano>` rather than overwritten — repeated
re-runs preserve every prior version. The helper refuses to follow
symlinks at the backup target (closes a same-host path-traversal
attack where `path.dfmt.bak` is a symlink to e.g. `/etc/cron.d/x`).

### 12.2 Auto-init (`ensureProjectInitialized`)

Every CLI subcommand that needs project state, plus `dfmt mcp`
startup itself, calls `ensureProjectInitialized(dir)` (in
`internal/cli/dispatch.go`). The function is **idempotent and
non-destructive**:

1. Create `.dfmt/` at mode 0700 (no-op if it exists).
2. Write `.dfmt/config.yaml` from `DefaultConfigYAML()` **only if
   missing** — a user-customised config is never clobbered.
3. Append `.dfmt/` to `.gitignore` **only if `.gitignore` already
   exists** — DFMT will not create a `.gitignore` unprompted; that
   is the user's call.
4. Merge DFMT's tool-allowlist + hooks into project-local
   `.claude/settings.json`. The merge is itself idempotent and
   refuses to write to `~/.claude/settings.json` (that would
   silently rewrite a user-scope settings file the agent shares
   across projects).

The auto-init path means an agent dropped into any random folder
gets a working DFMT setup on the first MCP call without the user
having to remember to run `dfmt init`.

---

## 13. Configuration

Configuration is layered: built-in defaults → global
(`$XDG_DATA_HOME/dfmt/config.yaml` or `~/.local/share/dfmt/config.yaml`)
→ project (`.dfmt/config.yaml`). Project values win. The decoder is
strict (`KnownFields(true)`) — typos surface as load errors rather
than silently degrading.

Each config file is capped at **64 KiB** (`maxConfigBytes` in
`internal/config/config.go`). Files above the cap are rejected with
`config file too large: exceeds 65536 bytes` rather than truncated
— a giant config almost always means the file got corrupted or
mis-edited and silently using the first 64 KiB would mask the
problem.

The struct lives in `internal/config/config.go` and the defaults in
`internal/config/defaults.go::DefaultConfigYAML()`. Both `dfmt init`
and the daemon's auto-init use the same `DefaultConfigYAML`, so
there is exactly one source of truth.

### 13.0 Wired vs reserved fields

A non-trivial fraction of the config struct exists for forward
compatibility — the field is defined, gets a default, runs through
`Validate()`, but **no production consumer reads it**. Setting any
of these values in `.dfmt/config.yaml` is silently a no-op.

| Section / key                                    | Status                                                      |
|--------------------------------------------------|-------------------------------------------------------------|
| `capture.mcp.enabled`                            | wired                                                       |
| `capture.fs.enabled / watch / ignore / debounce_ms` | wired                                                    |
| `capture.git.enabled / capture.shell.enabled`    | wired (honored by `install-hooks` / `shell-init`)          |
| `storage.durability / max_batch_ms / journal_max_bytes` | wired (threaded into `OpenJournal`)                  |
| `storage.compress_rotated`                       | **unwired** — `Rotate()` never gzips (see §8.3)             |
| `lifecycle.idle_timeout`                         | wired                                                       |
| `lifecycle.shutdown_timeout`                     | **unwired** — `Stop()` uses a hardcoded `10*time.Second`    |
| `transport.mcp.enabled / http.enabled / socket.enabled` | wired                                                |
| `transport.http.bind`                            | wired                                                       |
| `index.bm25_k1 / bm25_b / heading_boost`         | **unwired** — `NewBM25Okapi()` hardcodes `1.2 / 0.75`       |
| `index.rebuild_interval / stopwords_path`        | **unwired**                                                 |
| `retrieval.default_budget / default_format`      | **unwired** — `Recall` uses hardcoded `4096` and `"md"`     |
| `retrieval.throttle.*` (4 fields)                | **unwired**                                                 |
| `privacy.telemetry / remote_sync / allow_nonlocal_http` | **unwired**                                          |
| `logging.level / logging.format`                 | **unwired** — `internal/logging` ignores them               |

The pattern is consistent: the config layer is more complete than
the daemon's plumbing, by design. New features can land their
config fields ahead of the consuming code, with `Validate()`
already enforcing sensible ranges, and the wiring lands later
without re-reviewing the schema. The flip side is that several
"obvious" knobs (BM25 tuning, default recall budget, log level)
look configurable but aren't yet.

### 13.1 Schema (Go struct → YAML key)

| Section       | Key                              | Type     | Default            | Notes                                              |
|---------------|----------------------------------|----------|--------------------|----------------------------------------------------|
| `version`     | `version`                        | int      | `1`                | Schema version.                                    |
| **capture**   | `capture.mcp.enabled`            | bool     | `true`             | Always recommended.                                |
|               | `capture.fs.enabled`             | bool     | `false`            | Opt-in. Watches the project tree.                  |
|               | `capture.fs.watch`               | []string | `["**"]`           | Globs to include.                                  |
|               | `capture.fs.ignore`              | []string | `[".git/**", "node_modules/**", "__pycache__/**"]` | **Must** include `.dfmt/**` (else self-loop). |
|               | `capture.fs.debounce_ms`         | int      | `500`              | Per-path cooldown.                                 |
|               | `capture.git.enabled`            | bool     | `true`             | Honored by `dfmt install-hooks`.                   |
|               | `capture.shell.enabled`          | bool     | `true`             | Honored by `dfmt shell-init`.                      |
| **storage**   | `storage.durability`             | string   | `"batched"`        | `"durable"` or `"batched"`. Validated.             |
|               | `storage.max_batch_ms`           | int      | `100`              | Max delay between fsyncs in batched mode.          |
|               | `storage.journal_max_bytes`      | int64    | `10485760` (10 MiB)| Trigger for rotation.                              |
|               | `storage.compress_rotated`       | bool     | `true`             | **Unwired** — `Rotate()` never invokes gzip.        |
| **retrieval** | `retrieval.default_budget`       | int      | `4096`             | **Unwired** — `Recall` uses hardcoded 4096.        |
|               | `retrieval.default_format`       | string   | `"md"`             | **Unwired** — `Recall` uses hardcoded `"md"`.      |
|               | `retrieval.throttle.first_tier_calls`   | int | `10`            | **Unwired**.                                       |
|               | `retrieval.throttle.second_tier_calls`  | int | `5`             | **Unwired**.                                       |
|               | `retrieval.throttle.results_first_tier` | int | `20`            | **Unwired**.                                       |
|               | `retrieval.throttle.results_second_tier`| int | `10`            | **Unwired**.                                       |
| **index**     | `index.rebuild_interval`         | string   | `"1h"`             | **Unwired** — no scheduled rebuild path.           |
|               | `index.bm25_k1`                  | float64  | `1.2`              | **Unwired** — `NewBM25Okapi` hardcodes 1.2.        |
|               | `index.bm25_b`                   | float64  | `0.75`             | **Unwired** — `NewBM25Okapi` hardcodes 0.75.       |
|               | `index.heading_boost`            | float64  | `5.0`              | **Unwired** — no scoring path consumes it.         |
|               | `index.stopwords_path`           | string   | `""`               | **Unwired** — stopwords are baked-in constants.    |
| **transport** | `transport.mcp.enabled`          | bool     | `true`             | stdio MCP server.                                  |
|               | `transport.http.enabled`         | bool     | `false`            | Opt-in HTTP loopback.                              |
|               | `transport.http.bind`            | string   | `"127.0.0.1:8765"` | Bind address.                                      |
|               | `transport.socket.enabled`       | bool     | `true`             | Unix socket (Unix only).                           |
| **lifecycle** | `lifecycle.idle_timeout`         | duration | `"30m"`            | Validated parseable by `time.ParseDuration`.       |
|               | `lifecycle.shutdown_timeout`     | duration | `"10s"`            | **Unwired** — `Stop()` uses hardcoded `10*time.Second`. |
| **privacy**   | `privacy.telemetry`              | bool     | `false`            | **Unwired**. DFMT never phones home regardless.    |
|               | `privacy.remote_sync`            | string   | `"none"`           | **Unwired**.                                       |
|               | `privacy.allow_nonlocal_http`    | bool     | `false`            | **Unwired**. Loopback enforcement happens in transport, not via this flag. |
| **logging**   | `logging.level`                  | string   | `"info"`           | **Unwired** — but see callout below: log level is set via `DFMT_LOG` env var, not this field. |
|               | `logging.format`                 | string   | `"text"`           | **Unwired** — output format is hard-coded `<level>: <message>\n` for legacy compatibility. |

### 13.1.1 Logging is configured via env, not config

The `internal/logging` package itself works fine — it has
`Debugf` / `Infof` / `Warnf` / `Errorf`, all writing
`<level>: <message>\n` to stderr. The threshold is read once at
process startup from the **`DFMT_LOG`** environment variable:

| `DFMT_LOG` value             | Threshold                                    |
|------------------------------|----------------------------------------------|
| `debug`                      | `LevelDebug` — verbose                       |
| `info`                       | `LevelInfo`                                  |
| `warn` / `warning`           | `LevelWarn` (default)                        |
| `error`                      | `LevelError`                                 |
| `off` / `none` / `silent`    | `LevelOff` — completely silent               |

The `logging.level` and `logging.format` config fields would be a
nicer place to put this, but the wiring isn't there yet (see §13.0).
Operators silencing daemon stderr in CI dashboards should export
`DFMT_LOG=error` or `DFMT_LOG=off`.

### 13.2 Default `.dfmt/config.yaml` (as written by `dfmt init`)

The default file is intentionally minimal — it sets only the values
where a sane default differs from the zero-value. Everything else
is populated from the struct defaults at load time.

```yaml
# DFMT Configuration
version: 1

capture:
  mcp:
    enabled: true
  fs:
    enabled: false           # opt-in; flip to true to start watching
    watch:
      - "**"
    ignore:
      - ".dfmt/**"           # MANDATORY: prevents self-loop
      - ".git/**"
      - "node_modules/**"
      - "vendor/**"
      - ".venv/**"
      - "venv/**"
      - "__pycache__/**"
      - "*.pyc"
      - "dist/**"
      - "build/**"
      - "target/**"
      - "out/**"
      - ".next/**"
      - ".nuxt/**"
      - ".turbo/**"
      - "coverage/**"
      - ".idea/**"
      - ".vscode/**"
      - "*.swp"
      - "*.swo"
      - "*.log"
      - "tmp/**"

storage:
  durability: batched
  journal_max_bytes: 10485760
```

### 13.3 Validation rules

`Config.Validate()` enforces:

- `storage.durability ∈ {"durable", "batched"}`
- `storage.max_batch_ms ≥ 0`
- `storage.journal_max_bytes ≥ 0`
- `lifecycle.idle_timeout`, `lifecycle.shutdown_timeout` parseable
  by `time.ParseDuration` (when set)
- `index.bm25_k1 ≥ 0`
- `index.bm25_b ∈ [0, 1]`
- `index.heading_boost ≥ 0`
- `retrieval.default_budget ≥ 0`

Invalid values cause `Load` to fail immediately so misconfiguration
never silently degrades behavior.

### 13.4 Optional sidecars (documented, not yet wired)

Two project-level overlay files are referenced throughout the code
and error hints, but as of this writing **neither is read at
daemon startup** — both are reserved for the next milestone:

- `.dfmt/permissions.yaml` — site-specific sandbox allow/deny rules
  in line-format `allow:exec:base-cmd *`. The parser
  (`internal/sandbox/permissions.go::LoadPolicy`) exists and is
  tested, but is **not called** from `daemon.New` — production
  sandboxes always load `DefaultPolicy()`. The denial-error hints
  ("add `allow:exec:<cmd> *` to .dfmt/permissions.yaml") are
  forward-looking.
- `.dfmt/redact.yaml` — additional regex/field redaction patterns.
  The redactor's `AddPattern` method exists, but no loader reads
  this file. Operators that need extra patterns today must build a
  custom binary or call `Redactor.AddPattern` from a fork.

Both files are reserved at 0600 if you create them, but `dfmt init`
does **not** scaffold them and the daemon will not pick them up.
Track [#permissions-loader](docs/adr/) and the redactor wiring
issue before relying on either.

---

## 14. On-disk layout

The canonical filenames are constants in
`internal/project/project.go`. The lock file name is hard-coded in
`internal/daemon/lock.go`. The HTTP port file is written by the
transport layer when `transport.http.enabled=true`.

```mermaid
flowchart TB
    subgraph Project[".dfmt/  — per project, 0700"]
        C[config.yaml<br/>0600]
        J[journal.jsonl<br/>0600 — append-only JSONL]
        Jr["journal.jsonl.&lt;ULID&gt;.jsonl<br/>rotated, plain JSONL (compress flag unwired)"]
        I[index.gob<br/>0600 — JSON payload, .gob filename retained]
        Ic[index.cursor<br/>0600 — last-indexed event ID]
        Sk[daemon.sock<br/>0600 — Unix socket, Unix only]
        Pid[daemon.pid<br/>0600 — running daemon PID]
        Cont["content/<br/>0700 — ephemeral chunk store<br/>64 MiB cap, size-evicted"]
        L[lock<br/>0600 — flock target / Win named handle]
        P[port<br/>0600 — HTTP port, only if http.enabled]
        Ph["permissions.yaml<br/>0600 — reserved, loader not wired"]
        R["redact.yaml<br/>0600 — reserved, loader not wired"]
    end

    subgraph UserXDG["$XDG_DATA_HOME/dfmt (or ~/.local/share/dfmt)"]
        Gc[config.yaml<br/>global overrides]
        M[setup-manifest.json<br/>tracks files installed by setup]
    end

    subgraph UserHome["~/.dfmt/  — note: separate from XDG path above"]
        Reg[daemons.json<br/>0600 — cross-project registry<br/>backs `dfmt list` and /api/daemons]
    end
```

| File                    | Owner / writer                                | Notes                                                  |
|-------------------------|-----------------------------------------------|--------------------------------------------------------|
| `config.yaml`           | `dfmt init` / user-edited                     | Project config; merged onto global + struct defaults.  |
| `journal.jsonl`         | `core.Journal`                                | Active append-only log.                                |
| `journal.jsonl.<ULID>.jsonl` | `core.Journal::Rotate`                  | Older segments. The `storage.compress_rotated` flag is currently a no-op (see §8.3). |
| `index.gob`             | `core.Index::Persist`                         | JSON payload — filename kept for backwards compat.     |
| `index.cursor`          | `core.Index::Persist`                         | Last indexed event ID; enables incremental load.       |
| `daemon.sock`           | `transport.SocketServer`                      | Unix socket, mode 0600 (umask 0o077 at bind).          |
| `daemon.pid`            | `daemon.Daemon::Start`                        | Atomic write via `safefs.WriteFile` (refuses symlinks).|
| `lock`                  | `daemon.AcquireLock`                          | `flock(2)` on Unix; LockFileEx on Windows.             |
| `port`                  | `transport.HTTPServer::writePortFile`         | JSON `{"port":N}`. Removed on shutdown.                |
| `permissions.yaml`      | operator                                      | Site-specific sandbox rules. **Reserved — loader not wired** (see §13.4). |
| `redact.yaml`           | operator                                      | Extra redaction patterns. **Reserved — loader not wired** (see §13.4). |

All project-level files are 0600. `.dfmt/` itself is 0700. `dfmt init`
adds `.dfmt/` to the project's `.gitignore` if it is not already
listed (idempotent merge in `internal/project/gitignore.go`).

User-level state actually lives in **two** distinct directories,
which is a historical wart worth knowing about:

- **`$XDG_DATA_HOME/dfmt/`** (defaults to `~/.local/share/dfmt/` on
  Unix, `%APPDATA%/dfmt/` on Windows) — holds the global
  `config.yaml` overrides and `setup-manifest.json` (per-agent file
  inventory for clean uninstall).
- **`~/.dfmt/`** — holds `daemons.json`, the cross-project registry
  consumed by `dfmt list` and `GET /api/daemons`. The `daemons.json`
  path is hard-coded in `internal/client/registry.go::registryPath`
  and does **not** follow XDG.

The split exists because the registry is a runtime state file (PIDs,
ports, socket paths) the user might also want to grep with `cat
~/.dfmt/daemons.json` from a shell, while the manifest is install
metadata that fits the XDG data convention.

### 14.1 Project ID and socket path fallback

Two derived values from `internal/project/discover.go` are worth
naming explicitly:

- **`ID(projectPath)`** — first 4 bytes of `SHA-256(path)` rendered
  as 8 hex characters. Used in dashboard URLs and as a stable
  short ID across machines for the same path.
- **`SocketPath(projectPath)`** — normally
  `<project>/.dfmt/daemon.sock`. Unix `UNIX_PATH_MAX` is 108 bytes
  on Linux and 104 on macOS, so if the full socket path exceeds
  **100 bytes** the function falls back to
  `<userRuntimeDir>/dfmt-<hash>.sock`, where the hash is 16 hex
  characters (8 bytes of SHA-256). `userRuntimeDir()` resolution:
  `$XDG_RUNTIME_DIR` → `$TMPDIR` → `os.TempDir()` (the last branch
  drops a per-user `dfmt-<uid>/` subdir at mode 0700 if the base
  path is shared like `/tmp`). Closes F-06 (predictable-path
  pre-creation by a same-host attacker on shared `/tmp`).

---

## 15. Concurrency model

```mermaid
flowchart TB
    subgraph Daemon
        D[daemon.Daemon]
        D --- mu[atomic running]
        D --- act[atomic lastActivityNs]
        D --- lk[sync.Mutex lock]
        D --- once[sync.Once stopOnce]
        D --- wg[sync.WaitGroup]
        D --- shut[chan shutdownCh]
        D --- idle[chan idleCh]
    end

    subgraph Goroutines
        G1[transport.Serve]
        G2[idleMonitor]
        G3[consumeFSWatch]
        G4[rebuildIndexAsync]
        G5[journal sync ticker]
    end

    subgraph Handlers
        H[Handlers]
        H --- hmu[sync.RWMutex]
        H --- es[execSem cap=4]
        H --- fs[fetchSem cap=8]
        H --- rs[readSem cap=8]
        H --- ws[writeSem cap=4]
        H --- dc[dedupCache + mu]
        H --- sc[statsCache 5s TTL]
    end
```

Patterns to know:

- **Atomics for hot reads.** `running` and `lastActivityNs` are the
  two values touched on every request. Both are atomic.
- **Channels for shutdown.** `shutdownCh` (close to broadcast) and
  `idleCh` (close to wake the monitor) replace `time.AfterFunc`,
  whose timer goroutine made early prototypes leak under churn.
- **WaitGroup for drain.** Stop waits for `consumeFSWatch` and
  `rebuildIndexAsync` before persisting the index — if rebuild was
  cancelled mid-way the cursor is intentionally left stale.
- **Per-resource semaphores.** Four buffered-channel semaphores in
  `Handlers`, **shared across operation families** rather than
  per-MCP-tool:

  | Semaphore  | Cap | Operations served                  |
  |------------|-----|------------------------------------|
  | `execSem`  | 4   | `Exec`                             |
  | `fetchSem` | 8   | `Fetch`                            |
  | `readSem`  | 8   | `Read`, `Glob`, `Grep`             |
  | `writeSem` | 4   | `Edit`, `Write`                    |

  Acquired through `acquireLimiter(ctx, sem)` which `select`s on
  `sem <- struct{}{}` vs `ctx.Done()`. Client cancellation
  immediately returns the context error without ever holding a
  slot, so an aborted RPC never starves the next one. Release is
  a deferred `<-sem`.

---

## 16. Security posture

DFMT's threat model is "agent inside the box, attacker outside" —
the agent is trusted to run code it intends to run, but the host
must be protected from agent mistakes (and from prompt-injected
agents).

```mermaid
flowchart TB
    subgraph Agent
        A[AI agent intent]
    end

    subgraph DFMT Defenses
        P[Permission gate<br/>op + glob allow/deny]
        SSRF[SSRF defense<br/>scheme / hostname / IP / DNS rebind]
        Red[Redactor<br/>AWS keys, JWTs, bearer tokens]
        SF[safefs<br/>symlink-safe atomic write]
        Sem[Concurrency caps<br/>exec/fetch/read/write semaphores]
        Pri[Server-side priority floor<br/>P1 reserved]
        Hash[Write-content hashing<br/>journal stores SHA-256, not body]
        Lock[Per-project flock<br/>no two daemons]
        UMask[Socket umask 0o077<br/>0700 perms]
    end

    subgraph Host
        OS[OS / FS / shell]
        Net[Network]
    end

    A --> P --> OS
    A --> SSRF --> Net
    OS --> Red --> J[(journal)]
    A --> SF --> OS
    A --> Sem --> OS
    A --> Pri --> J
    A --> Hash --> J
```

A few specific findings the codebase has hardened against:

- **F-04 / F-07 / F-08** — write-path symlink traversal. Closed by
  `internal/safefs`.
- **F-11** — agents writing secrets that then live forever in the
  journal. Closed by hashing write content.
- **F-19** — agent-initiated DoS by spawning many concurrent execs.
  Closed by per-op semaphores.
- **F-21** — agent claiming P1 priority for noise. Closed by the
  server-side priority clamp.
- **F-25** — symlink race in atomic write. Closed in safefs.
- **F-29** — `dfmt_edit` slipping past write deny rules. Closed by
  mirroring write denies into edit + going through a unified
  `PolicyCheck`.

Secret redaction (`internal/redact/`) runs on every event before it
reaches the journal or the agent response. The built-in pattern
table in `redact.go::commonPatterns` is matched in the order shown —
provider-specific prefixes run before broader generic matchers so
the labels stay accurate (`sk-ant-…` is consumed before the `sk-…`
OpenAI matcher; AWS prefix list runs before `generic_secret`). The
24 patterns:

| Group        | Pattern             | Catches                                                                        |
|--------------|---------------------|--------------------------------------------------------------------------------|
| Provider key | `anthropic_key`     | `sk-ant-` + 40+ chars                                                          |
| Provider key | `openai_key`        | `sk-` + 40+ chars (incl. `sk-proj-…` multi-segment keys)                       |
| Provider key | `github_token`      | classic PATs: `ghp_` / `gho_` / `ghu_` / `ghs_` / `ghr_` + 36+ chars           |
| Provider key | `github_fine_pat`   | fine-grained PATs: `github_pat_` + 59+ chars                                   |
| Provider key | `aws_key`           | all 10 AWS access-key prefixes (`AKIA`/`ASIA`/`AGPA`/`AROA`/`AIDA`/`ANPA`/`AIPA`/`ANVA`/`ABIA`/`ACCA`) + 16 chars |
| Provider key | `aws_secret`        | 40-char base64 within ≤ 80 chars of an `aws_secret_*` marker (covers YAML / JSON / tabular AWS-CLI wrapping) |
| Provider key | `google_api_key`    | `AIza` + 35 chars                                                              |
| Provider key | `slack_token`       | `xoxb-` / `xoxp-` / `xoxa-` / `xoxr-` / `xoxs-` / `xapp-` + 10+ chars          |
| Provider key | `stripe_key`        | `sk_live_` / `sk_test_` + 24+ chars                                            |
| Provider key | `stripe_restricted` | `rk_live_` / `rk_test_` + 24+ chars                                            |
| Provider key | `stripe_token`      | `tok_` + 24+ chars                                                             |
| Provider key | `discord_token`     | `M[TWN]…23+.…6+.…27+` (user-id base64, timestamp, hmac segments)               |
| Provider key | `twilio_key`        | `SK` + 32 lowercase hex                                                        |
| Provider key | `sendgrid_key`      | `SG.` + 22-char `.` + 43-char tail                                             |
| Provider key | `mailgun_key`       | `key-` + 32 hex                                                                |
| Webhook URL  | `slack_webhook`     | `https://hooks.slack.com/services/T…/B…/…` (carries implicit auth)             |
| Webhook URL  | `discord_webhook`   | `https://discord(app)?.com/api/webhooks/<id>/<token>`                          |
| DB URL       | `db_url_creds`      | `user:password@` in `postgres(ql)://`, `mysql://`, `mongodb(+srv)://`, `redis(s)://`, `amqp(s)://` |
| Generic      | `generic_secret`    | inline `api_key` / `secret_key` / `access_token` / `auth_token` `=` value (20+ chars) |
| Generic      | `bearer_token`      | `Bearer <20+ chars>` (case-insensitive, full base64 alphabet incl. `+/=~`)     |
| Generic      | `basic_auth`        | `Basic <10+ chars>` HTTP basic-auth header                                     |
| Env line     | `env_export`        | `export NAME=value` / `NAME=value` lines whose NAME is classified sensitive by `IsSensitiveKey` (special-cased — runs `ReplaceAllStringFunc`, not the generic loop) |
| PEM         | `private_key`       | full PEM block (`-----BEGIN [RSA / EC / DSA / OPENSSH ]PRIVATE KEY-----` … `END`); `(?s)` so the body spans newlines |
| Token       | `jwt`               | three `.`-separated base64-url segments (`eyJ…eyJ…`)                            |

Plus a separate **field-name pass** (`Redact` walks `event.Data` map
keys): keys matching `password`, `api_key`, `apikey`, `private_key`,
`secret`, `oauth`, `token`, etc. — case-insensitive substring match,
so `customer_api_key` redacts. The body for those keys is replaced
with `[REDACTED]` regardless of whether its value matches any regex.

Both the regex set and the field-name set are case-insensitive.
Each regex uses bounded character classes and no nested quantifiers
to keep `ReplaceAllString` linear on multi-MB inputs (sandbox
stdout, journal lines).

The package doc is explicit that this is **best-effort**, not a
guarantee: a bare 40-char AWS secret with no nearby `secret_key`
marker is *not* redacted (false-positive cost would be too high),
and project-specific credential shapes need operator-supplied
patterns. Site-specific patterns can be added in code via
`Redactor.AddPattern`, but a `.dfmt/redact.yaml` overlay loader is
not yet wired (see §13.4) — the file path appears in comments and
error hints but the daemon does not read it.

---

## 17. Build, test, release

### 17.1 Make targets

```mermaid
flowchart LR
    A[make build] --> Bin[(dist/dfmt + dist/dfmt-bench)]
    B[make test] --> Cov[per-package coverage]
    C[make lint] --> Gl[golangci-lint]
    D[make fmt] --> Fmt[go fmt ./...]
    E[make release] --> Mat[7 platform binaries]
    F[make install] --> GP[$GOPATH/bin/dfmt]
    G[make clean] --> Rm[rm -rf dist]
```

Cross-compile matrix in `make release`:

| OS       | Arches             |
|----------|--------------------|
| Linux    | amd64, arm64       |
| macOS    | amd64, arm64       |
| Windows  | amd64, arm64       |
| FreeBSD  | amd64              |

The published GitHub Releases (`.github/workflows/release.yml`,
triggered on `v*` tags) build the same matrix but with stricter
flags:

- **`CGO_ENABLED=0`** — pure-Go static binaries, no libc dependency.
- **`-trimpath`** — strips local file paths from the binary for
  reproducibility.
- **`-ldflags "-s -w -X main.version=${TAG}"`** — strips debug info
  and DWARF symbols, embeds the tag name as `version`.
- **`sha256sums.txt`** is generated alongside the binaries and
  uploaded to the release.

Only `dfmt` ships in releases — `dfmt-bench` is a local-development
tool. Third-party actions in both workflows are pinned by full
40-char SHA so a tag rewrite cannot retroactively change CI or
release behavior.

### 17.2 Test coverage targets

| Package              | Target    |
|----------------------|-----------|
| `internal/core`      | ≥ 90 %    |
| `internal/transport` | ≥ 85 %    |
| `internal/daemon`    | ≥ 80 %    |
| `internal/cli`       | ≥ 75 %    |

These are **soft targets documented in `AGENTS.md`** — neither the
`Makefile` `test` target (`go test ./...`) nor the GitHub Actions
workflow (`.github/workflows/ci.yml`) enforces a coverage gate, so
the numbers are aspirational rather than blocking.

New functionality requires tests; bug fixes require regression
tests.

**CI matrix (`.github/workflows/ci.yml`):**

| Job  | OS                                          | Go versions | Notes                                                  |
|------|---------------------------------------------|-------------|--------------------------------------------------------|
| test | ubuntu-latest, macos-latest, windows-latest | 1.24, 1.26  | macOS is `continue-on-error` — informational only. Linux + Windows are required. |
| lint | ubuntu-latest                               | 1.26        | `golangci-lint --timeout=5m`.                          |

Both jobs run `go test ./...` plain — **the CI does not invoke the
race detector** (`-race`). Running with `-race` is a developer-side
option (`go test -race ./...` on Linux/macOS, `CGO_ENABLED=1
go test -race ./...` on Windows). Third-party actions are pinned by
40-char SHA so a tag rewrite cannot retroactively change CI
behavior.

### 17.3 Dependency policy

Two third-party Go modules total:

- `golang.org/x/sys` — syscalls not in stdlib.
- `gopkg.in/yaml.v3` — YAML config.

Everything else (BM25, Porter stemmer, MCP protocol, JSON-RPC 2.0,
HTML parser, ULID, levenshtein) is in-tree. Adding a dependency
needs an ADR in `docs/adr/`.

### 17.4 Dev scripts

- `dev.ps1` — primary developer loop on Windows (the maintainer's
  daily driver). Format, vet, test, lint, build, doctor, smoke-run.
  Warns when the local `go` toolchain is older than the Go 1.26.2
  stdlib CVE line.
- `install.ps1` / `install.sh` — `curl … | sh`-style installers for
  end users. Detect OS/arch, download the matching release binary
  to `~/.local/bin` (override via `DFMT_INSTALL_DIR`), pin a tag
  via `DFMT_VERSION` (default `latest`), or build from source via
  `DFMT_FROM_SOURCE=1`. Set `DFMT_DEBUG=1` for verbose tracing.
  Linux / macOS / FreeBSD on amd64 / arm64 supported.
- `Makefile` — POSIX-friendly mirror of `dev.ps1` (build, test, lint,
  fmt, install, release, clean).
- `scripts/cleanup_user_claude_json.go` — one-shot rescue tool for
  the rare case where a half-finished setup leaves a malformed
  `~/.claude.json`.
- `scripts/test-mcp.ps1` — quick MCP-stdio smoke test against the
  freshly built binary.
- `build-test.ps1` / `run-build-test.{bat,cmd,ps1,sh}` — minimal CI
  smoke shells used by Windows CI runners.
- `run-go-mod-tidy.go` — wrapper that invokes `go mod tidy` with the
  exact toolchain pin.

### 17.5 ADRs

Architecture Decision Records live under `docs/adr/`. They
document the load-bearing choices the codebase will not silently
revisit. Current set:

| ADR  | Title                                  | One-line summary                                                  |
|------|----------------------------------------|-------------------------------------------------------------------|
| 0000 | ADR Process and Lifecycle              | Light MADR-style process with explicit supersession.              |
| 0001 | Per-Project Daemon Model               | One daemon per project, auto-start, idle-exit.                    |
| 0002 | MIT License                            | MIT for adoption; brand protects identity.                        |
| 0003 | JSONL Journal + Custom Index           | Append-only JSONL + in-memory inverted index. No SQLite.          |
| 0004 | Stdlib-First Dependency Policy         | Only stdlib + `x/sys` + `yaml.v3`; everything else bundled.       |
| 0005 | Multi-Source Capture Layer             | MCP + FS + git + shell + CLI, all independent. Agent-agnostic.    |
| 0006 | Sandboxed Tool Execution In Scope      | Reverses earlier exclusion; sandbox is first-class.               |
| 0007 | Content Store ≠ Event Journal          | Two stores, shared index infra, different lifecycles.             |
| 0008 | Bundled HTML Parser                    | ≈ 350 lines bundled; no `x/net/html` dep.                         |

`docs/adr/ADR-INDEX.md` is the always-current index. Add a new ADR
when introducing a component, changing component interactions,
adopting a new dependency, or making a breaking behavior change.

---

## 18. Package map

```text
cmd/
├── dfmt/
│   ├── main.go              CLI entry, global flag parsing
│   └── version.go           build-time version string
└── dfmt-bench/
    ├── main.go              benchmarking driver
    └── tokensaving.go       token-savings report

internal/
├── cli/
│   ├── cli.go               package-level state (global flags)
│   ├── dispatch.go          subcommand router (~3 300 LOC)
│   └── hooks_embed.go       embedded git-hook scripts
├── client/
│   ├── client.go            CLI ↔ daemon RPC + auto-spawn
│   ├── process_unix.go      detached spawn (Unix)
│   ├── process_windows.go   detached spawn (Windows)
│   └── registry.go          cross-process daemon registry
├── config/
│   ├── config.go            YAML loading + validation
│   └── defaults.go          DefaultConfigYAML
├── content/
│   ├── content.go           ChunkSet / Chunk types
│   ├── store.go             ephemeral content store, size-evicted at 64 MiB
│   └── summarize.go         intent-blind summary generator
├── core/
│   ├── core.go              package-level helpers
│   ├── event.go             EventType / Priority / Source / Event
│   ├── classifier.go        priority assignment + tag elevation
│   ├── journal.go           append-only JSONL, batched fsync, rotation
│   ├── index.go             inverted index, BM25, trigrams
│   ├── index_persist.go     custom JSON serialization
│   ├── tokenize.go          unicode tokenizer + stopwords
│   ├── porter.go            Porter stemmer
│   ├── trigram.go           trigram fallback index
│   ├── bm25.go              BM25 scoring math
│   ├── levenshtein.go       edit distance
│   └── ulid.go              ULID generator (sortable IDs)
├── daemon/
│   ├── daemon.go            lifecycle, goroutines, idle monitor
│   ├── lock.go              singleton-lock abstraction
│   ├── flock_unix.go        flock(2) impl
│   ├── flock_windows.go     named-handle impl
│   ├── process_unix.go      pid file management
│   └── process_windows.go   pid file management
├── capture/
│   ├── capture.go           event source constants + helpers
│   ├── fswatch.go           shared FSWatcher logic
│   ├── fswatch_linux.go     inotify
│   ├── fswatch_windows.go   ReadDirectoryChangesW + mtime cache
│   ├── git.go               git-hook submitters
│   └── shell.go             shell submitter
├── sandbox/
│   ├── sandbox.go           operation surface
│   ├── runtime.go           subprocess + HTTP runtime
│   ├── permissions.go       Policy / DefaultPolicy / PolicyCheck
│   ├── intent.go            BM25 intent extraction + size-tiered output
│   └── signals.go           signal forwarding to child processes
├── setup/
│   ├── setup.go             write MCP configs, manifest tracking
│   ├── detect.go            agent auto-detection (9 agents)
│   ├── claude.go            Claude-specific patching of ~/.claude.json
│   └── projectdocs.go       AGENTS.md / CLAUDE.md routing block
├── transport/
│   ├── transport.go         server abstraction
│   ├── mcp.go               MCP 2024-11-05
│   ├── http.go              HTTP server + dashboard
│   ├── handlers.go          RPC business logic (~1 450 LOC)
│   ├── jsonrpc.go           JSON-RPC 2.0 codec
│   ├── rpc_params.go        strict params decoder
│   ├── socket.go            Unix-socket transport
│   ├── socket_umask_unix.go umask 0o077 at bind
│   ├── socket_umask_windows.go (no-op)
│   └── dashboard.go         embedded HTML + CSP
├── project/
│   ├── project.go           project struct, paths
│   ├── discover.go          walk-up project discovery
│   └── gitignore.go         idempotent .gitignore merge
├── retrieve/                ⚠ orphaned — no production importer; see §11.1
│   ├── retrieve.go          package doc comment only (no code)
│   ├── snapshot.go          SnapshotBuilder.Build — priority-tiered greedy fill
│   └── render_md.go         MarkdownRenderer / JSONRenderer / XMLRenderer
├── redact/
│   └── redact.go            pattern-based secret scrubbing
├── safefs/
│   └── safefs.go            symlink-safe atomic writes
└── logging/
    ├── log.go               leveled stderr logger
    └── logging.go           init / configuration
```

---

## 19. End-to-end walkthrough

A worked example: the agent calls `dfmt_exec` with
`code="go test ./..."` and `intent="failing tests"`.

```mermaid
sequenceDiagram
    autonumber
    participant Agent
    participant MCP as transport/mcp.go
    participant H as handlers.go::Exec
    participant Sb as sandbox.Exec
    participant Run as runtime
    participant Red as redactor (held by H)
    participant CS as content store
    participant J as journal
    participant I as index

    Agent->>MCP: tools/call dfmt_exec<br/>{code, lang=bash, intent="failing tests"}
    MCP->>H: Exec(ctx, params)
    H->>H: acquire execSem (cap=4)
    H->>Sb: Exec(req)
    Sb->>Sb: PolicyCheck(allow:exec:go *)
    Sb->>Run: spawn bash -c "go test ./..." with curated env
    Run-->>Sb: stdout (LimitReader-capped) / stderr / exit code
    Sb->>Sb: NormalizeOutput (strip ANSI / CR-overwrites)
    Sb->>Sb: ApplyReturnPolicy → filtered Stdout + RawStdout + Matches + Summary + Vocabulary
    Sb-->>H: ExecResp (raw bytes still present)
    H->>Red: redactString(Stdout / Stderr / RawStdout / Summary)
    Red-->>H: redacted strings
    H->>CS: stashContent("exec-stdout", redacted RawStdout+Stderr, intent)
    CS-->>H: content_id (ULID, dedup by sha256(kind,source,body))
    H->>J: Append type="tool.exec" priority=P4 source=mcp<br/>data=redactData({lang, exit, raw_bytes, returned_bytes, content_id})<br/>tags=[redactString("failing tests")]
    H->>I: Add(event) — tokenize + stem + posting update
    H-->>MCP: structuredContent {matches, redacted summary, content_id}
    MCP-->>Agent: tools/call result
```

Key observations:

- The agent never sees the raw test output. It sees the matches that
  scored highest against `intent="failing tests"`, plus a small
  vocabulary it can use to refine the next query.
- The full output lives in the content store. If the agent really
  needs it, it can pull it back via the `content_id`.
- The journal records `raw_bytes` and `returned_bytes` for this call,
  which feeds the `dfmt_stats` aggregation.
- A future session of the same agent — or a different agent, or the
  same agent after a compaction — can `dfmt_search "failing tests"`
  or `dfmt_recall` and reconstruct enough context to keep going.

That last property is what the project name is about.

---

## Appendix A — Glossary

| Term            | Meaning |
|-----------------|---------|
| **MCP**         | Model Context Protocol — JSON-RPC 2.0 over stdio, the agent ↔ tool wire format. |
| **Intent**      | Free-form short string describing what the agent wants from the tool's output. |
| **ChunkSet**    | A logical, time-bounded collection of content-store chunks for a single tool call. |
| **Tier**        | One of P1–P4. Higher = more important; lower drops first under recall budget. |
| **ULID**        | DFMT's sortable event ID. 16 bytes (48-bit ms timestamp + 80-bit randomness with same-ms monotonic increment), encoded as **32 hex characters**. The name follows the algorithm; the encoding is hex, not Crockford base32. |
| **safefs**      | DFMT's symlink-safe atomic write helper. |
| **Redactor**    | Pattern-based secret scrubber that runs on every event. |
| **Manifest**    | `~/.local/share/dfmt/setup-manifest.json` — record of installed agent files for clean uninstall. |

## Appendix B — Wire format examples

### B.1 MCP `tools/call` request

```json
{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "tools/call",
  "params": {
    "name": "dfmt_exec",
    "arguments": {
      "code": "go test ./internal/core",
      "lang": "bash",
      "intent": "failing tests",
      "timeout": 60,
      "return": "auto"
    }
  }
}
```

### B.2 Journal entry (one line of `journal.jsonl`)

The ID is the 32-hex-char encoding produced by
`internal/core/ulid.go::NewULID`. The `sig` is the first 16 hex
chars (8 bytes) of `sha256(canonical_json(event_without_sig))`.

```json
{"id":"0192d6a8e7f0a1b2c3d4e5f60718a9bc","ts":"2026-04-27T19:21:33.118Z","project":"D:/Codebox/PROJECTS/DFMT","type":"tool.exec","priority":"p4","source":"mcp","data":{"lang":"bash","exit":1,"raw_bytes":18432,"returned_bytes":1812,"content_id":"0192d6a8e801f5d2e3a4b5c6d7e8f901"},"tags":["failing tests"],"sig":"a1b2c3d4e5f60718"}
```

Note that `type` is `tool.exec` (a runtime-only string, see §8.1) and
the `intent` argument lives in `tags`, not in `data`. The `mcp.call`
constant in the enum is reserved for capture-pipeline use, not for
sandbox tool logging.

### B.3 `dfmt_recall` response (markdown excerpt)

The renderer in `handlers.go::Recall` produces a flat list, prefixed
with priority tag and a compact `MM-DD HH:MM:SS` timestamp. Tiers
are emitted in priority order (P1 → P4); within a tier events are
newest-first.

```markdown
# Session Snapshot

- [p1] 04-27 14:08:01 @claude-code  decision: chose batched durability for the journal
- [p2] 04-27 13:14:22  git.commit 8478faf — "fix(dev.ps1): ASCII hyphen …"
- [p2] 04-27 11:05:48 @claude-code #summary  note: F-29 closed: edit mirrors write deny list
- [p3] 04-27 10:21:09  file.edit internal/sandbox/permissions.go
- [p4] 04-27 10:20:55  mcp.call tool=read intent="permission rules" raw_bytes=18432 returned_bytes=412
…
```

Output format is selectable via `format` (`md` / `json` / `xml`) but
the markdown form above is the default and the one most agents
consume after a context compaction.

---

*Document version: DFMT v0.2.x line. Update this file whenever a
package gains, loses, or moves a public surface, or whenever a flow
diagram in §6, §7, §9, §10 or §11 stops matching reality.*
