# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`AGENTS.md` is **not** an architecture document — it is the generated
`<!-- dfmt:v1 -->` context-discipline block that `dfmt init` writes into every
project, DFMT's own repo included, and `dfmt init` will overwrite edits to it.
This file is the canonical architecture reference; keep architecture facts
here, and treat `AGENTS.md` as generated output.

## Project

DFMT is a local Go daemon that sits between AI coding agents and their tools. It runs `exec` / `read` / `fetch` / `glob` / `grep` / `edit` / `write` on the agent's behalf, returns intent-matched excerpts instead of raw output, and persists every call in an append-only journal so a future agent can recall what was decided after context compaction.

This repository is DFMT itself. When working on it, you are dogfooding the daemon — its MCP tools must be used in place of native ones (rules below).


## Common commands

| Task | Command |
|---|---|
| Build binaries | `make build` (produces `dist/dfmt`, `dist/dfmt-bench`) |
| Run all tests | `make test` (or `go test ./...`) |
| Run one package | `go test ./internal/core/...` |
| Run one test | `go test ./internal/core -run TestJournalAppend` |
| Race detector | `go test -race ./...` (Linux/macOS; add `CGO_ENABLED=1` on Windows) |
| Lint | `make lint` (`golangci-lint run ./...`) — max line 120, max cyclo 20, shadow-check on |
| Format | `make fmt` |
| Clean local state | `make clean` |
| Token-savings benchmark | `dfmt-bench tokensaving` |
| Wire-up health check | `dfmt doctor` (project state + per-agent MCP files + binary path) |
| Project bootstrap | `dfmt quickstart` (init + setup + verify) |

## Architecture (big picture)

### Entry points

- `cmd/dfmt/main.go` — CLI entry. Parses `--project`, dispatches via `internal/cli.Dispatch()`.
- `cmd/dfmt-bench/main.go` — token-savings benchmark binary.

### Single-binary, self-promoting daemon (v0.6.x)

`internal/cli/dispatch.go` routes subcommands. Local-only commands (`init`, `setup`) run in-process; everything else routes through `acquireBackend` — connect to the running **host-wide global daemon**, or spawn a detached daemon child and connect to it. The transport is a Unix socket (`~/.dfmt/daemon.sock` on Linux/macOS) or loopback TCP (`~/.dfmt/port` on Windows). One daemon process serves every initialized project — first RPC for a project lazy-loads its `config.yaml` / `journal.jsonl` / `index.gob` / `permissions.yaml` / `redact.yaml` into a per-project resource cache; subsequent RPCs reuse the cached handles. Idle-exits after the configured timeout. See [ADR-0019](docs/adr/0019-global-daemon.md) and [ADR-0021](docs/adr/0021-single-binary-self-promotion.md).

**Behavior contract**: any `dfmt <cmd>` brings the daemon up if it is not running. The short-lived command then connects-and-exits in ~30–100 ms; the daemon child survives in the background until SIGINT, `dfmt stop`, or idle-exit (30 min default). There is exactly **one `dfmt.exe` in steady state** — the daemon. The original CLI invocation has already exited by the time you run `tasklist | grep dfmt`.

**One spawn site**: `startGlobalDaemonBackground` (with platform-specific detach: Windows `DETACHED_PROCESS|CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP`, Unix `setsid()`), called from exactly two places — `runDaemon` (explicit `dfmt daemon`) and `ensureGlobalDaemon` (implicit-on-need from `acquireBackend` and from `runStatus` / `runList` / `runDoctor` at function entry). `client.NewClient` has no auto-spawn; the pre-v0.6.0 pattern of every code path triggering its own spawn is gone.

**`dfmt mcp` is a pure proxy**: it is itself a long-lived process (the agent's MCP transport), and it uses `acquireBackendForLongRunner`, which *ensures* a detached daemon is running and connects to it as a client. It never adopts the daemon role — `acquireBackendForLongRunner` returns a nil `*daemon.Daemon` by construction. This is deliberate: under the old in-process self-promote path, the MCP subprocess became the daemon, so closing the agent's stdin killed the daemon for every other project on the host. An agent session therefore shows two processes (`dfmt mcp` + `dfmt daemon`) but still exactly **one daemon**.

**Test-binary short-circuit**: `acquireBackend` checks `isTestBinary()` and `DFMT_DISABLE_AUTOSTART=1`; either falls back to in-process promotion via `acquireBackendForLongRunner` so unit tests don't fork sibling processes (the test binary is not the dfmt binary).

**Daemon identity — why an upgrade takes effect**: the daemon outlives the command that started it and holds the singleton lock, and liveness is a bare dial (`client.fastDialOK`), which *any* build satisfies. Without a version check, rebuilding and reinstalling dfmt did nothing — the old process kept answering, and because its replies were well-formed the skew showed up as silently wrong results rather than errors. (Observed: a May-15 binary served a v0.6.9 checkout and returned empty stdout for every exec.)

So the global daemon publishes `~/.dfmt/daemon.json` — `{version, pid, exe, exe_fingerprint, started_at}` — written by `Daemon.Start` next to the PID file (`internal/daemon/identity.go`), removed by `Stop`. `inspectGlobalDaemon()` evaluates it via `Identity.IsCurrent` and returns the `globalDaemonStale` state; `ensureGlobalDaemon()` handles that by stopping the old daemon and spawning a fresh one.

`IsCurrent` checks **two independent** staleness signals, because they catch different failures:

1. **Version skew** — one installed release replaced by another.
2. **Fingerprint skew** — the daemon's own binary overwritten on disk while it kept running (`exe_fingerprint` is `"<modtime-unixnano>:<size>"` of `exe`, re-statted on each check). This is the one that matters when working on DFMT itself: a contributor rebuilds many times at the same tag, and a version-only check would compare equal every time while the daemon kept serving the code they just replaced. Deliberately mtime+size, not a hash — the question is "was this replaced", not "is it authentic", and hashing a 13 MB binary before every command would cost far more than it tells us.

Invariants:

- A **missing or malformed** `daemon.json` counts as stale — that is what lets the first run of an upgraded binary replace a pre-identity daemon.
- An **unstamped client** (plain `go build`, no `-ldflags`) matches any *version* (`daemon.SameVersion`), so a dev build never restarts the daemon over a version skew alone. The fingerprint check still applies — it is valid however the client was stamped.
- An **unstatable `exe`** (moved/deleted) is *not* stale; guessing otherwise would restart the daemon on every command.
- **One restart per process** (`restartAttempted`); if the fresh daemon still mismatches, warn and continue rather than loop.
- Never restarts under `isTestBinary()` or `DFMT_DISABLE_AUTOSTART=1`.
- `acquireBackend` calls `ensureGlobalDaemon()` **unconditionally** — do not re-add a `if !client.DaemonRunning(...)` guard around it. A stale daemon *is* running, so that guard skips the version check on precisely the path that matters.
- Compute the verdict once via `readGlobalDaemonInfo().Current`, never by comparing versions ad-hoc at a call site — that is how `doctor` could report "fine" about a daemon the next command was about to restart.

On Windows the graceful stop cannot work for a `DETACHED_PROCESS` daemon (no console, so nothing can deliver a console control event), so `stopGlobalDaemon` waits 5 s and force-kills. That is expected and survivable: the journal is append-only and its reader already tolerates a truncated trailing line. The cost is a lost `index.gob` cache, rebuilt by journal replay on next start, once per upgrade.

`readGlobalDaemonInfo()` (`internal/cli/daemon.go`) is the single reader for "what is serving this host"; `status`, `list`, `dashboard`, and `doctor` all go through it so they cannot report different PIDs for the same process.

Legacy per-project daemons from v0.3.x are stopped during `dfmt setup --refresh`. The daemon-side wire carries a `project_id` field on every RPC so one process disambiguates calls from different projects.

### Core domain (`internal/core/`)

- **Events** (`event.go`) — typed events with priority tiers `p1`–`p4` and sources (MCP, fs watcher, git hook, shell, CLI).
- **Journal** (`journal.go`) — append-only JSONL on disk.
- **Index** (`index.go`, `index_persist.go`) — in-memory inverted index with BM25, Porter stemmer, trigrams, English+Turkish stopwords, custom JSON serialization.
- **Classifier** (`classifier.go`) — assigns priority by event type and tags. The "Tag conventions" table above is what this reads.

### Transport (`internal/transport/`)

Three faces, same daemon:
- MCP over stdio — primary agent integration (`dfmt mcp`).
- HTTP JSON-RPC + dashboard at `/dashboard`.
- Unix socket / loopback TCP — CLI ↔ daemon.

### Sandbox (`internal/sandbox/`)

Implements the seven tool primitives. Output is summarized, intent-matched against the BM25 index, and the raw bytes are stashed in `internal/content/` (the ephemeral content store). The default policy in `permissions.go::DefaultPolicy()` allows common dev tools (`git`, `npm`, `pnpm`, `yarn`, `bun`, `npx`/`pnpx`/`bunx`, `tsc`/`tsx`/`ts-node`, `vitest`/`jest`, `eslint`/`prettier`, `vite`/`next`/`webpack`, `make`, `pytest`, `cargo`, `go`, `node`, `python`, `deno`, basic Unix read-only) and denies destructive ones (`sudo`, `rm -rf /`, `curl|sh`). Operators add overrides in `.dfmt/permissions.yaml`. Every denial error ends with a `hint:` line naming the file to edit and the network classes that cannot be opened up (loopback, RFC1918, cloud metadata).

**Allow-rule contract** (V-20). Exec allow rules use the form `allow:exec:<base-cmd> *` — the trailing space + `*` is what makes the boundary the end-of-token. Without it, `allow:exec:git*` would also match `git-shell`, `git-receive-pack`, etc. Always include ` *` on exec allows.

The sandbox runs an 8-stage **`NormalizeOutput` pipeline** before responses reach the policy filter (`internal/sandbox/intent.go`):

1. **Binary refusal** (`binary.go`) — non-UTF-8 / magic-number-detected bodies (PNG, PDF, gzip, …) become a one-line `(binary; type=…; N bytes; sha256=…)` summary.
2. **ANSI strip** — CSI/OSC escape sequences gone.
3. **CR-rewrite collapse** — progress bars and spinner overwrites collapsed to final state.
4. **RLE** — ≥4 identical adjacent lines compacted with a "(repeated N times)" annotation.
5. **Stack-trace path collapsing** (`stacktrace.go`) — Python/Go traces with ≥3 same-path frames replace continuation paths with `"…"` marker.
6. **Git diff index-line drop** (`diff.go`) — `index <hash>..<hash> <mode>` lines stripped from `git diff` bodies.
7. **Structured-output compaction** (`structured.go`) — JSON / NDJSON / YAML noise fields (`created_at`, `*_url`, `_links`, K8s `creationTimestamp`/`resourceVersion`/`selfLink`/`managedFields`, AWS `NextToken`, pagination metadata, null/empty values). Markdown frontmatter stripped from `.md` bodies. ADR-0010.
8. **HTML → markdown** (`htmltok.go` / `htmlmd.go`) — full tokenizer + markdown walker; drops `<script>`/`<style>`/`<nav>`/`<footer>`/`<aside>`/`<head>`/`<noscript>`/`<svg>`/`<form>`/`<button>`/`<iframe>` wholesale; emits markdown for headings, lists, code blocks (with language hint), tables (with GFM separator), blockquotes, definition lists, links, images. ADR-0008.

The policy filter (`ApplyReturnPolicy`) gates inline / summary / big-tier on **approximated tokens, not raw bytes** (ADR-0012). `ApproxTokens(s) = ascii_bytes/4 + non_ascii_runes` — CJK and English bodies hit the same agent-cost threshold. I/O hard caps (`MaxFetchBodyBytes` 8 MiB, `MaxRawBytes` 256 KB Windows truncation) stay byte-based.

Cross-call wire dedup: a `content_id` already emitted to the agent in this session returns `(unchanged; same content_id)` instead of repeating the bytes (ADR-0009 / ADR-0011).

### Capture pipeline (`internal/capture/`)

Five ingestion paths feed the journal: MCP calls (live), CLI commands like `dfmt remember` / `dfmt task` (live), filesystem watcher (opt-in), git hooks (opt-in via `dfmt install-hooks`), shell integration (opt-in via `dfmt shell-init`).

### Recall (`internal/transport/handlers.go::Recall`)

`dfmt_recall` rebuilds a markdown snapshot under a byte budget. Per-tier streaming with FIFO eviction — lower-priority content drops first when the budget tightens. Path interning (Refs table at the top of the snapshot + `[rN]` token references in events, kicks in at ≥3 occurrences) lives in `internal/retrieve/render_md.go` and is wired for `format=json` and `format=xml`, which route through `retrieve.SnapshotBuilder`. The **default markdown path is not interned** — it builds its lines inline in `handlers_recall.go` and never reaches the renderer. Wiring markdown is tracked on the roadmap (see `docs/ROADMAP.md`).

### MCP required-argument validation (`internal/transport/params_validate.go`)

Every tool schema declares a `required` list, but nothing enforced it: `decodeRequiredParams` rejects only a wholly **absent** `arguments` object, so an object that merely omits the required field decoded to a zero value and the tool ran on it. `dfmt_exec` with no `code` handed an empty string to the shell, which exits 0 in ~25 ms having printed nothing, and the caller got `{"exit":0,"duration_ms":25,"timed_out":false}` — a successful-looking result, indistinguishable from a real command that printed nothing. An agent guessing the wrong argument name (`command` for `code`, `file` for `path`) got silence instead of a correction, and retrying the same wrong name never revealed it.

Param types with required fields now implement `Validate() error`; `dispatchTool` calls it after decode and maps failures to `-32602`. Two deliberate divergences from the schema: JSON Schema `required` constrains **key presence, not emptiness**, and `edit.new_string` (empty = deletion) and `write.content` (empty = truncate) have meaningful empty values, so they are not validated. When adding a tool, add its `Validate()` alongside the schema's `required` array.

Note `strictParams()` (`rpc_params.go`) — `DisallowUnknownFields` — is **off** by default despite its doc comment claiming otherwise; enable with `DFMT_MCP_STRICT_PARAMS=1` when debugging a client that seems to be sending fields the server ignores.

`dfmt_search` returns hits with a short `excerpt` field (≤80 bytes, rune-aligned) drawn from the event's `message` / `path` / `type` — agents can decide whether to drill in without a follow-up `dfmt_recall` round-trip.

### Agent setup (`internal/setup/`)

Auto-detects nine agents (Claude Code, Cursor, VS Code, Codex, Gemini, Windsurf, Zed, Continue, OpenCode) and writes their MCP configs. Tracks every change in `~/.local/share/dfmt/setup-manifest.json` for clean uninstall. `setup --uninstall` removes everything DFMT wrote and surgically strips Claude Code's keys from the shared `~/.claude.json`.

### Symlink-safe writes (`internal/safefs/`)

Use `safefs.WriteFile` / `safefs.WriteFileAtomic` for any new write site under a project-managed path. The helper closed an F-04/F-07/F-08/F-25 cluster around symlink traversal.

## Hard invariants

### Dependency policy — strict stdlib-first

Only two third-party Go modules are permitted in the runtime tree:
- `golang.org/x/sys` (syscalls)
- `gopkg.in/yaml.v3` (config)

Everything else — HTML parser, BM25, Porter stemmer, MCP wire format, JSON-RPC 2.0 — is bundled in-tree. Adding a dependency requires an ADR. **Prohibited**: SQLite, ORMs, web frameworks, CLI frameworks, logging frameworks.

### Test coverage thresholds

- `internal/core`       ≥ 90 %
- `internal/transport`  ≥ 85 %
- `internal/daemon`    ≥ 75 %
- `internal/cli`       ≥ 70 %

New functionality requires tests; bug fixes require regression tests.

### ADR required when

Adding a new component, changing component interactions, adopting a dependency, or making a breaking behavior change. ADRs live in `docs/adr/`; use `0000-adr-process.md` as the template.

### Local state (per-project `.dfmt/`)

`config.yaml`, `journal.jsonl`, `index.gob` (JSON payload — `.gob` filename retained for backwards compat with old daemons that may still be running; serialized via `writeJSONAtomic` in `internal/core/index_persist.go`), `port`, `lock`, optional `permissions.yaml` (line format) and `redact.yaml` (YAML). All `0o600`.

Host-wide state lives in `~/.dfmt/` (override: `$DFMT_GLOBAL_DIR`): `daemon.sock` / `port`, `daemon.pid`, `daemon.json` (identity — see above), `lock`, `last-crash.log`, `daemons.json`. Path helpers are in `internal/project/global.go`; never hand-join these paths. `daemons.json` holds **only legacy per-project rows** — the global daemon does not register itself there, and `dfmt list` synthesizes its rows from the resource cache via `/api/all-daemons`, which is what makes one process visibly serve many projects. `.dfmt/` is added to `.gitignore` automatically by `dfmt init`. Both override files are **wired** at daemon + CLI startup — see ADR-0014 for the merge semantics (hard-deny invariant on exec allows, additive-only redact patterns).

### Line endings

Repo is **LF in both index and working tree**. Do not renormalize to CRLF; if drift appears, repair direction is CRLF → LF.

## Pre-authorized actions (Claude Code)

Claude's default behavior is to ask before taking actions that affect shared state (push, PR open/close, etc.). On this repository the following are **pre-authorized** so the agent does not have to interrupt routine work:

- **`git push origin <current-branch>`** — provided the commits about to be pushed pass `go test ./...` and `go vet ./...` cleanly. Failing tests or vet errors block the push; surface the failure and ask first.

This pre-authorization is intentionally narrow. It does NOT extend to:

- Force-push (`--force`, `--force-with-lease`, `+ref` syntax) — always confirm.
- Tag push (`git push --tags`, `git push origin v*`) — always confirm; tags are advertised to package managers.
- Remote branch creation or deletion (`git push origin :branch`, new remote branches) — always confirm.
- Pushing to repos other than this one's `origin`, or to remotes named anything other than `origin`.
- PR / issue creation, comments, closes, merges (any GitHub API mutation).
- `gh` CLI commands that create or modify shared state.

If in doubt, ask. The pre-authorization here is a convenience for the routine "land a green commit" path; it is not a license for novel risky actions.

## Where to find the rest

- **Canonical onboarding (agent-neutral)** → [AGENTS.md](AGENTS.md)
- **Contributing workflow** → [CONTRIBUTING.md](CONTRIBUTING.md)
- **System architecture diagrams** → [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- **Architectural decisions** → [docs/adr/](docs/adr/)

When you change content here, mirror it in `AGENTS.md` in the same commit.

<!-- dfmt:v1 begin -->
## Context Discipline

This project uses DFMT to keep tool output from flooding the context
window and to preserve session state across compactions. When working
in this project, follow these rules.

### Tool preferences

Prefer DFMT's MCP tools over native ones:

| Native     | DFMT replacement | `intent` required? |
|------------|------------------|--------------------|
| `Bash`     | `dfmt_exec`      | yes                |
| `Read`     | `dfmt_read`      | yes                |
| `WebFetch` | `dfmt_fetch`     | yes                |
| `Glob`     | `dfmt_glob`      | yes                |
| `Grep`     | `dfmt_grep`      | yes                |
| `Edit`     | `dfmt_edit`      | n/a                |
| `Write`    | `dfmt_write`     | n/a                |

Every `dfmt_*` call MUST pass an `intent` parameter — a short phrase
describing what you need from the output (e.g. "failing tests",
"error message", "imports"). Without `intent` the tool returns raw
bytes and the token savings are lost.

On DFMT failure, report it to the user (one short line — which call,
what error) and then fall back to the native tool so the session is
not blocked. The ban is on *silent* fallback — every switch must be
announced. After a fallback, drop a brief `dfmt_remember` note tagged
`gap` when practical, so the journal records that a call was bypassed.
If the native tool is also denied (permission rule, sandbox refusal),
stop and ask the user; do not retry blindly.

### Session memory

DFMT tracks tool calls automatically. After substantive decisions or
findings, call `dfmt_remember` with descriptive tags (`decision`,
`finding`, `summary`) so future sessions can recall the context after
compaction.

### When native tools are acceptable

Native `Bash` and `Read` are acceptable for outputs you know are small
(< 2 KB) and will not be referenced again. For everything else, DFMT
tools are preferred.
<!-- dfmt:v1 end -->
