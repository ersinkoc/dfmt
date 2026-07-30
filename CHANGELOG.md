# Changelog

All notable changes to **DFMT** ("Don't Fuck My Tokens") are
documented here. Format follows [Keep a Changelog
1.1.0](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

The wire surfaces under SemVer guarantees today are:

- The MCP tool names (`dfmt_exec`, `dfmt_read`, `dfmt_fetch`,
  `dfmt_glob`, `dfmt_grep`, `dfmt_edit`, `dfmt_write`,
  `dfmt_remember`, `dfmt_search`, `dfmt_recall`, `dfmt_stats`).
- The shape of `tools/call` request and response payloads
  (`structuredContent` envelope, the existing field set on each
  tool's input arguments).
- The CLI subcommand surface (`dfmt init`, `dfmt setup`,
  `dfmt daemon`, `dfmt mcp`, `dfmt doctor`, `dfmt remember`,
  `dfmt search`, `dfmt recall`, `dfmt stats`, `dfmt exec`,
  `dfmt read`, `dfmt fetch`, `dfmt glob`, `dfmt grep`, `dfmt edit`,
  `dfmt write`, `dfmt list`, `dfmt status`, `dfmt stop`,
  `dfmt install-hooks`, `dfmt shell-init`, `dfmt capture …`).
- The on-disk shape of `.dfmt/journal.jsonl` event records (`id`,
  `ts`, `project`, `type`, `priority`, `source`, `actor`, `data`,
  `refs`, `tags`, `sig`).

Internal package shapes (`internal/...`) are NOT covered by SemVer.

## [Unreleased]

A full read-through of the code base, in two passes.

The first pass found controls that existed, looked implemented, and did
not hold — the same failure shape as 0.7.3's, with no error anywhere to
notice them by
([ADR-0023](docs/adr/0023-real-deadlines-concurrent-proxy-and-rpc-auth.md)).

The second found limits that held perfectly well and were set against
the wrong thing: an index that forgot its most valuable events first, a
five-minute ceiling on nine-minute work, and a read tool that spoke a
unit nothing else uses
([ADR-0024](docs/adr/0024-retention-ceilings-and-line-oriented-reads.md)).

The third closed the one gap the second deliberately left open: work
that outlives any ceiling at all
([ADR-0025](docs/adr/0025-async-exec-jobs.md)).

### Fixed

- **`dfmt_exec`'s timeout stopped nothing.** `exec.CommandContext`
  kills the process it started; `bash -c "a; b"` forks, so the
  grandchild survived the deadline and kept the inherited stdout pipe
  open, leaving the read blocked until the command finished on its
  own. Measured with `Timeout: 3s`: returned at **20.7 s**,
  `timed_out=false`, and the output printed before the kill was
  discarded. The deadline now kills the whole process tree (`Setpgid`
  + `kill(-pgid)` on Unix, `taskkill /T /F` on Windows), `WaitDelay`
  bounds `Wait` afterwards, partial output survives, and `timed_out`
  is finally set — it had been `false` on every call ever made.

- **Every tool call was capped at 30 seconds by the HTTP server.**
  `WriteTimeout` is absolute from request-header time, so it bounded
  handler duration regardless of the timeout the agent asked for
  (`dfmt_exec` advertises 60 s by default, 300 s max). `sleep 25`
  returned; `sleep 45` died with a bare `EOF` while the command kept
  running server-side. The same deadline killed the dashboard's SSE
  stream every 30 seconds. Removing it makes the sandbox's own
  `MaxExecTimeout` the real ceiling — see the Changed section, where
  that ceiling is raised to 900 s so a build or test suite fits. Past
  it, a job now fails as a clean `timed_out` verdict with partial
  output rather than a bare `EOF`.

- **`dfmt_glob` did not recurse.** `filepath.Glob` has no `**` — the
  second star adds nothing to `filepath.Match` — so `**/*.go` matched
  exactly one level of depth and returned **2 of this repo's 278 Go
  files**, silently, while the tool schema advertised that very
  pattern. Recursive patterns now walk with doublestar semantics
  (`**/` is zero-or-more segments, so root-level files match);
  non-`**` patterns keep the stdlib path unchanged.

- **`dfmt_edit` silently edited the first of N matches.** The
  implementation was `strings.Replace(..., 1)`, and the anchors an
  agent picks — an error string, a field name, a repeated call — are
  exactly the text that repeats. An `old_string` that is not unique is
  now refused with its occurrence count; `replace_all` opts into a
  sweep and the response reports `replacements`.

- **One slow tool call froze the whole MCP session.** The stdio loop
  was `read → handle → write`, one request at a time; a `dfmt_read`
  was observed waiting more than two minutes behind a running
  `go test`. Requests are still read serially but dispatched to a
  bounded worker pool with a mutex-guarded writer.

- **The RPC endpoint never checked the token it minted.** The daemon
  generated a 32-byte bearer token, wrote an *empty* string into the
  port file, and ignored the `Authorization` header its own clients
  were sending. On Windows the transport is loopback TCP with no ACL,
  so `/` — exec, write, edit, fetch — was reachable by any process on
  the host. The token is now published in the 0600 port file and
  verified in constant time on `/`. The dashboard and read-only `/api`
  endpoints are unchanged (a browser has no token to present).

- **`dfmt_remember` wrote fine and retrieved badly.** The write path
  was sound (journal + index + redaction + signature), but nothing
  about retrieval favoured a deliberate note over the automatic tool
  events around it — 193 of 202 events in this repo's own journal.
  `SearchHit.Type` and `SearchHit.Source` were declared on the wire
  struct and never assigned, so every hit arrived unlabelled; tool
  events carried an excerpt of their own type name (`"tool.grep"`)
  because only `data["path"]` was consulted; and a query for
  `timeout` returned six tool events and *not* the note that
  discusses timeouts at length, because BM25 length normalization
  favors short documents and the corpus is almost entirely short
  documents. Hits now carry type and source, excerpts fall back to
  the command/pattern/URL the event recorded, and `dfmt_search`
  accepts a `type` filter (`type: "note"` searches only your own
  memories). The scoring itself is unchanged — the corpus is
  lopsided, not the ranking — so the fix is to let the caller say
  which corpus it means rather than to invent a weight.

- **The tag vocabulary that governs retention was undocumented at the
  point of use.** `summary`/`decision`/`strengths`/`ledger` elevate a
  note to P2 and `audit`/`finding`/`followup`/`preserve` to P3, which
  decides what survives a tight recall budget — but the `tags`
  parameter said only "Tags for categorizing the event", and the tool
  description advertised `dfmt_remember` as a token-telemetry
  recorder. Both now say what they do, within the `tools/list` byte
  budget.

- **Recall labelled events with the wrong priority.** The tier came
  from the classifier, the printed `[pN]` came from the stored value,
  and `dfmt_remember` coerces every agent-written event to p3 — so a
  note elevated to P2 by its tags rendered as `[p3]` sorted above
  `[p2]` lines, which reads as a sorting bug and hides the one signal
  the tag vocabulary exists to produce.

- **`dfmt_remember` was missing from the tool-call metrics.** Every
  other tool records duration and errors (ADR-0018); the hole was
  exactly where session memory is written.

### Added

- **Async exec jobs.** `dfmt_exec {code, async: true}` submits and
  returns a `job_id` immediately; `{job_id}` polls it; `{job_id,
  cancel: true}` stops it. For work no synchronous ceiling fits — a
  migration, a soak test, a watch build — where the alternative was
  raising the timeout until it stopped bounding anything. Jobs run at
  their own concurrency (2, separate from the interactive exec slots so
  a `git status` never queues behind an hour-long job), are capped at
  32 outstanding with a 30-minute retention on finished ones, and are
  canceled on daemon shutdown rather than left as subprocesses nobody
  owns. Cancel goes through the same tree-kill as a timeout: verified
  end to end, a canceled `sleep 120; echo … > file` leaves no file.
  Also available from the CLI: `dfmt exec -async`, `-job`, `-cancel`.

  Async is parameters on `dfmt_exec` rather than a separate tool
  because `tools/list` is paid on every session start and "run a
  command" is one capability whose result you may want now or later.
  This is the first deliberate raise of the tools/list byte budget
  (6 KiB → 6.75 KiB); every other addition in this release was paid for
  by trimming.

### Changed

- **`dfmt_read` takes lines, not bytes.** `offset` is now the 1-based
  first line (0 and 1 both mean the top) and `limit` a line count, and
  the response reports `start_line` / `end_line` / `total_lines`. Bytes
  are a unit nothing else an agent works with uses — stack traces,
  editors, review comments and DFMT's own `matches[].line` all speak
  lines — so reading a function that starts "around line 400" meant
  guessing an offset and getting back something you could not cite.
  Line numbers are deliberately NOT prefixed onto the content: an agent
  that copies an anchor out of numbered text builds an `old_string`
  that cannot match the file. `total_lines` is 0 when the read stopped
  early, because a partial count presented as the total is worse than
  silence. Deep windows stay reachable in files larger than the read
  ceiling. This changes the meaning of two existing arguments; see
  [ADR-0024](docs/adr/0024-retention-ceilings-and-line-oriented-reads.md).

- **`MaxExecTimeout` raised from 300 s to 900 s.** With the transport
  cap gone (above), 300 s became the real ceiling — below the length of
  ordinary work. This repository's own `go test ./...` takes ~531 s. A
  runaway is still bounded: the deadline kills the whole process tree,
  four exec slots, and the caller's own timeout is usually far lower.
  A job that outlives even this now has an async job handle instead of
  a larger constant — see Added.

### Performance

- **The index forgot from the wrong end.** At `MaxIndexDocs` eviction
  popped the smallest ULID — the oldest document, whatever it was. On a
  journal that is 193 automatic tool events to 1 deliberate note, that
  retires the earliest decisions while keeping every recent
  `tool.read`. Quietly: the note stays in the journal so `dfmt_recall`
  still shows it, and only `dfmt_search` forgets. Eviction now drains
  the lowest-priority tier first, oldest-first within a tier, using the
  same classifier (and therefore the same tag vocabulary) that recall
  uses.

- **Removing one document scanned the whole index.** `removeLocked`
  walked every stem and trigram posting list and recomputed
  `avgDocLen` over every document — per insert, once at the cap. With
  per-document reverse maps, a running length sum, and front-deletion
  by re-slicing: **739 µs → 8.7 µs** at a 2 000-document cap and
  **6 215 µs → 13.3 µs** at 20 000. Cost growth for a 10× larger index
  went from 8.4× to 1.5×.

- **Recall re-read the entire journal on every call**, unmarshalling
  every line and recomputing every event signature, even when nothing
  had been appended since the last one. Snapshots are now memoised
  against the journal cursor — exact invalidation, not a TTL.

### Fixed

- **Smaller correctness fixes.** Recall lines rendered event fields in
  map-iteration order, so identical data produced different output on
  every call — undiffable, and it invalidated the agent's prompt cache
  on a tool whose entire job is context economy; ordering is now
  deterministic and prefers identifying fields (`message`, `path`)
  over byte counts. A failed journal rotation left the file handle
  closed, so every later append failed until the daemon restarted; it
  now reopens. UTF-16 surrogate pairs (emoji, astral-plane CJK) from a
  Windows shell were encoded as CESU-8 and arrived as replacement
  characters. A truncated journal read no longer ends the stream
  silently.

## [0.7.3] — 2026-07-30

Four fixes. Two of them made a tool return confident, well-formed,
useless output rather than an error — which is why they survived so
long: nothing looked broken from the outside.

### Fixed

- **`dfmt_grep` searched build output and could not find code.** The
  walker was a bare `filepath.WalkDir` with no exclusions of any kind —
  no VCS skip, no build-output skip, no binary detection. On dfmt's own
  repo (130 MB, of which **98 MB is `dist/`**: eight cross-compiled
  release binaries, plus 14 MB of `.git`) that inverted the entire point
  of the project. Measured before the fix:

  | pattern | where the 100 match slots went |
  |---|---|
  | `runtime` | **58 to `dist/dfmt.exe`**, 2 to `.git/objects/pack/*.pack`, **0 to `internal/` or `cmd/`** |
  | `error` | 10 to `.git/hooks/fsmonitor-watchman.sample`, 0 to `internal/` |

  `WalkDir` is lexical, so `dist` is reached before `docs` and
  `internal`: build output claimed the match cap before the walker ever
  saw source. The agent paid twice — once for a compiled binary's string
  table at ~55 approximated tokens per line (against ~22 for a source
  line; `ApproxTokens` charges one token per non-ASCII rune), and again
  when it got no usable signal and fell back to native grep for the same
  search.

  Grep also matched **its own journal**: a search for a nonexistent
  token returned exactly one hit — the previous grep's query, recorded
  in `.dfmt/journal.jsonl` moments earlier. The journal grows with every
  call, so that was a feedback loop.

  Two exclusions now apply, both O(1) per entry:

  - **Directory pruning by basename** (`fs.SkipDir`, so a pruned subtree
    is never stat'ed): VCS, build output, dependency trees, tool caches,
    editor state, and `.dfmt` itself. Never applied to the search root,
    so `path: "dist"` still searches `dist`.
  - **Binary detection from a 512-byte prefix**, reusing `detectBinary`
    so the walker and the output pipeline share one definition of
    "binary". Unconditional, since an executable's string table is
    unusable however explicitly it was requested. This also removed a
    pathological read: `grepFileLines` slurps whole files, so each 13 MB
    artifact meant a 13 MB allocation plus a `strings.Split` over it.

  Plus an 8 MiB per-file cap for large *text* (logs, generated dumps)
  that binary sniffing passes.

  Measured after, same four patterns: **23% fewer tokens overall, 43% on
  `runtime`**, and 62 garbage lines → 0. The percentage understates it —
  the point is that `runtime` now returns `internal/osutil/os.go` and
  `internal/config/config.go` instead of a binary's string table. One
  pattern (`error`) went *up* 50 tokens, because the excluded `.git/hooks`
  matches were replaced by real documentation matches.

  Exclusions are reported in the result summary (`(skipped 4 ignored
  dirs, 2 binary)`) rather than applied silently — an unreported skip is
  indistinguishable from "no matches there", which is precisely how this
  hid for so long. The note costs nothing when nothing was skipped.

  `dfmt_glob` had the same hole on a separate code path (it filters
  `filepath.Glob` output rather than walking) and got the same
  exclusions, including in its intent-matching read loop, which was
  feeding binary bodies through `MatchContent`.

  The skip list is a hardcoded basename set mirroring the ignore list
  already shipped for the fs watcher (`capture.fs.ignore`) — the same
  knowledge, never wired to the walkers. Honoring `.gitignore` instead is
  the principled version and is now tracked on the roadmap; it needs an
  in-tree matcher and its own ADR.

- **The `SessionStart` hook failed on every session start on Windows.**
  `dfmt init` branched on `GOOS` and wrote a PowerShell body —
  `if (Test-Path .dfmt/last-recall.md) { Write-Host …; Get-Content … }` —
  into `.claude/settings.json`. But Claude Code does not hand hook bodies
  to the host shell; it runs them through bash (`/usr/bin/bash -c`, the
  bundled Git Bash on Windows). So the body was a syntax error, not a
  portability win, and every new conversation opened with

  ```
  SessionStart:startup hook error [if (Test-Path .dfmt/last-recall.md) …]:
  /usr/bin/bash: -c: line 1: syntax error near unexpected token `('
  ```

  The hook's whole job is to prepend the previous session's snapshot, so
  Windows users silently lost cross-session recall — the `PreCompact` half
  kept writing `.dfmt/last-recall.md`, and nothing ever read it back.

  All hook bodies are now POSIX shell on every platform. The POSIX form
  already worked on Windows; only the PowerShell branch was ever broken.

- **Upgrading did not repair an affected config.** `mergeClaudeHook` is
  keyed on the exact command string, so a re-run of `dfmt init` / `dfmt
  setup` on a machine carrying the PowerShell body left it in place and
  registered the POSIX one *beside* it — two `SessionStart` entries, one
  of which still errored. A new legacy kind (`LegacyPowerShellHook`)
  purges the fossil, dropping a group left with no hooks and an event left
  with no groups so upgrades stop accumulating dead scaffolding. Detection
  is anchored on dfmt's own `last-recall.md` artifact, so a hand-written
  user hook that happens to call `Test-Path` is untouched. Picked up
  automatically by `dfmt init`, `dfmt setup --clean`, and `dfmt
  quickstart`, for both the project and user-scope settings files.

- **Every non-ASCII Latin-1 character came back as `�`.** The UTF-16LE
  decoder used by the Windows exec path took `hi == 0` to mean "ASCII"
  and wrote the low byte through untouched. That is only true below
  `0x80`: for U+0080–U+00FF the branch emitted a raw byte that is not
  valid UTF-8, and `encoding/json` then replaced it with U+FFFD on the
  way to the agent. So `ç ö ü ş ğ é ñ` — every accented character in
  Turkish, and much of Western European text — arrived mangled from a
  Windows Git Bash. The general branch below already handled these
  correctly; the fast path was simply wrong about its own precondition.
  Now gated on `hi == 0 && lo < 0x80`.

- **`dfmt setup` wrote an impossible binary path on WSL.**
  `wslPathToWindowsPath` sliced with `p[5:]` to strip `/mnt/c`, which is
  six characters — so the drive letter survived and `/mnt/c/Users/foo`
  became `C:c\Users\foo`. That value went into every agent's MCP config
  whenever the dfmt binary lived on a Windows drive, so the agent could
  not launch dfmt at all. Also generalized past the hardcoded `c`: a
  binary on `/mnt/d` used to fall through and be returned as an
  unconverted Unix path, failing the same way but more quietly. The
  drive letter must now be its own path segment, so `/mnt/c` and
  `/mnt/c/...` convert while `/mnt/config` is left alone.

### Internal

- Coverage tests for previously unexercised paths in `internal/client`
  (RPC surface), `internal/sandbox`, `internal/capture` (fs watcher
  units), `internal/cli` (Windows detach, reconnect errors),
  `internal/config` (save), `internal/project` (identity paths) and
  `internal/setup` (project init, WSL path conversion). No behavior
  change.

## [0.7.2] — 2026-07-29

Findings from a full-project scan. Both fixes address failures that were
certain rather than unlikely, and both had been silently degrading sessions
for some time.

### Fixed

- **Every multi-line command was rejected, blaming a rule that does not
  exist.** Go's `.` does not match `\n`, so a glob of `**` compiled to
  `^.*$` and could not match any multi-line text. Under the documented
  default-permissive policy — `allow:exec:**`, with *no* exec deny rules at
  all — a two-line script, a heredoc, or any `dfmt_exec` with an embedded
  newline failed its ALLOW check and was reported as "blocked by deny rule",
  accompanied by a hint stating that all exec commands are allowed by
  default. Both halves were true; the message was not.

  All glob-derived patterns now carry `(?s)`. For deny rules this can only
  ever match *more* text, which is the safe direction, and a regression test
  pins that a denied token on a second line is still caught.

  `Policy.EvaluateReason` now distinguishes `ReasonExplicitDeny` from
  `ReasonNoAllowMatch`, because the remedies are opposites: remove a deny
  rule, or add an allow rule.

- **`dfmt mcp` never recovered when the daemon went away.** The proxy
  resolved its backend once at startup and never revalidated it. The daemon
  idle-exits after 30 minutes and agent sessions routinely idle longer, so
  from that point every tool call returned a raw platform dial error for the
  rest of the session, recoverable only by reconnecting the MCP server by
  hand.

  `cli.reconnectingBackend` re-runs `ensureGlobalDaemon`, rebuilds the
  client, and retries once. Detection deliberately does not parse the error
  — dial failures are wrapped, platform- and locale-specific strings — but
  asks the liveness question it can actually answer. **Only a vanished
  daemon is retryable**: `dfmt_exec` and `dfmt_write` are not idempotent, so
  a policy denial or a failing command surfaces immediately and unchanged.
  `StreamEvents` is exempt from retry (re-subscribing would restart the
  event position).

### Changed

- **`dfmt doctor`** probes the shell's POSIX userland (ls, cat, sed, awk)
  separately from the language toolchains. The v0.7.1 Windows PATH bug hid
  because go/node/python resolve from the Windows PATH while the coreutils
  did not, so every toolchain check passed while the sandbox was unusable.

### Removed

- `registerProjectWithGlobalDaemon` — dead since v0.5.0 turned `dfmt mcp`
  into a proxy, as its own doc comment anticipated. No callers remained.

### Documentation

- [ADR-0022](docs/adr/0022-daemon-identity-and-resilient-proxy.md) records
  the daemon identity mechanism shipped in v0.7.0 and the proxy reconnect
  added here, including the alternatives rejected (warn-only, refuse-to-
  serve, hashing the binary, a shutdown RPC, retrying every failed call).

## [0.7.1] — 2026-07-29

### Fixed

- **Windows: every POSIX command in the sandbox returned exit 127.**
  `lookPath` deliberately resolves `bash` to Git Bash at
  `<root>\usr\bin\bash.exe` — the same directory that holds ls, cat, grep,
  sed, awk, find, which and the rest of the coreutils. A stock Git for
  Windows install keeps that directory *off* the Windows PATH so its POSIX
  tools don't shadow Windows commands, and Git Bash re-adds it when you open
  a terminal. The sandbox never gets that step: it runs `bash.exe -c` with an
  environment it builds itself, whose PATH is the Windows PATH. So bash
  started fine and then could not find a single one of its own utilities.

  `shellCompanionDirs` now prepends the resolved shell's companion
  directories (`usr\bin`, `mingw64\bin`, `usr\local\bin` when present) for
  shells located inside a Git-for-Windows tree. Prepended rather than
  appended, matching an interactive Git Bash session: the interpreter is
  bash, so `find . -name '*.go'` must mean POSIX find, not Windows
  `find.exe`. Shells outside a Git tree contribute nothing, so no unrelated
  directory is ever injected.

  The bug survived because the existing toolchain probe checks go, node and
  python — all of which *do* live on the Windows PATH — so they resolved
  happily while `ls` was broken. `dfmt doctor` now probes the coreutils
  separately and reports a shell that cannot find its own userland.

## [0.7.0] — 2026-07-29

The release that makes an upgrade actually take effect.

The daemon is a singleton that outlives the command which started it, and
liveness was decided by a bare socket dial — a check *any* build passes. So
rebuilding and reinstalling DFMT did nothing: the old process kept the
host-wide lock and kept answering. Because its replies were well-formed, the
skew surfaced as silently wrong results rather than errors. Observed in the
wild: a months-old binary serving a current checkout and returning empty
stdout for every `dfmt_exec`, with no error anywhere to explain it.

Two independent defects produced that same silent-wrong-answer signature, and
both are fixed here. The daemon now publishes its build identity and is
replaced when it no longer matches; and MCP tool calls now enforce the
`required` arguments their schemas always declared.

### Added

- **Daemon identity file** `~/.dfmt/daemon.json` —
  `{version, pid, exe, exe_fingerprint, started_at}`, written by
  `Daemon.Start` next to the PID file and removed by `Stop`. Written by the
  daemon itself rather than the HTTP server, so it exists on every transport
  including a socket-only daemon with HTTP disabled.
- **Automatic stale-daemon restart.** `inspectGlobalDaemon` gained a
  `globalDaemonStale` state; `ensureGlobalDaemon` stops the outdated daemon
  and spawns a fresh one. Staleness is two independent signals: a **version**
  skew (one release replaced by another), and a **fingerprint** skew — the
  daemon's own binary overwritten on disk while it kept running. The second
  is what catches a rebuild at an unchanged tag, the everyday case when
  developing DFMT itself, which a version-only check compares equal forever.
  Bounded to one restart per process; never fires under a test binary or
  `DFMT_DISABLE_AUTOSTART=1`; an unstamped dev build is exempt from the
  version half but not the fingerprint half.
- **`dfmt doctor` binary-consistency check.** Compares the `dfmt` on `PATH`,
  the command each agent's MCP config actually launches, and the running
  daemon's `exe` + `version`, and reports any divergence. Nothing is deleted —
  the operator chooses which copy to keep.
- **`dfmt status`** now reports the serving daemon's PID and build, and warns
  when it is stale and due to be restarted. `--json` gains `daemon_pid`,
  `daemon_version`, `cli_version`, `daemon_exe`, `daemon_started_at`.

### Fixed

- **MCP tool calls silently ignored missing required arguments.** Every tool
  schema declares a `required` list that nothing enforced: `decodeRequiredParams`
  rejects only a wholly *absent* `arguments` object, so an object that merely
  omits the required field decoded to a zero value and the tool ran on it.
  `dfmt_exec` without `code` handed an empty string to the shell, which exits
  0 in ~25 ms having printed nothing, returning
  `{"exit":0,"duration_ms":25,"timed_out":false}` — indistinguishable from a
  real command that printed nothing. A caller guessing the wrong argument name
  (`command` for `code`, `file` for `path`) got silence instead of a
  correction. Param types now implement `Validate()`; failures map to
  JSON-RPC `-32602` naming the missing field.
- **`ExecResp.Stderr` was never populated.** The field existed and the
  transport already read and redacted it, but the sandbox never assigned it —
  so every compiler error, stack trace, and test-failure banner arrived as an
  empty body with a bare non-zero exit code and nothing to act on. Stderr is
  captured under a `MaxRawBytes` cap, normalized like stdout, and deliberately
  *not* passed through the return-policy filter: excerpting the channel that
  explains a failure is how the one line naming the failing file gets lost.
- **Windows: every `dfmt_exec` through a detached daemon returned nothing.**
  A `DETACHED_PROCESS` daemon owns no console, and spawning a console
  application from it without `CREATE_NO_WINDOW` makes Windows wedge the child
  at 0 CPU seconds — it never runs its command and never writes a byte. No
  unit test caught this because a test binary always has a console. Child
  processes are now created with `CREATE_NO_WINDOW`.
- **`dfmt doctor` reported the wrong binary for agent configs**, showing the
  path the *current* process would write on the next `dfmt setup` rather than
  what the configs contain. The row therefore always agreed with "this CLI"
  and concealed the exact drift the check exists to surface.

### Changed

- **`dfmt list`** now prints `N project(s) served by 1 daemon` instead of
  `N daemon(s) running`. Under the global daemon every row is the same
  process serving a different project, so the old wording contradicted the
  design and read as a singleton violation.
- **Single source of truth for daemon state.** `status`, `list`, `dashboard`,
  and `doctor` now read `readGlobalDaemonInfo()` instead of each
  reconstructing liveness from a different subset of `~/.dfmt/*` in a
  different order. They can no longer disagree about whether a daemon is up
  or which PID it has.
- **`acquireBackend` always calls `ensureGlobalDaemon`.** The previous
  `if !client.DaemonRunning(...)` guard skipped the freshness check on
  precisely the path that mattered — a stale daemon *is* running.

### Notes

- On Windows a graceful stop cannot reach a `DETACHED_PROCESS` daemon (no
  console, so no console control event can be delivered), so `stopGlobalDaemon`
  waits 5 s and force-kills. This is survivable: the journal is append-only
  and its reader already tolerates a truncated trailing line. The cost is a
  discarded `index.gob` cache, rebuilt by journal replay on the next start,
  once per upgrade.
- `strictParams()` / `DisallowUnknownFields` remains **off** by default; its
  doc comment previously claimed the opposite. Enable with
  `DFMT_MCP_STRICT_PARAMS=1` when debugging a client whose fields appear to be
  ignored.

## [0.6.9] — 2026-05-17

### Fixed

- **Daemon stability**: `dfmt mcp` no longer adopts the daemon role via
  `PromoteInProcess`. It now connects to the global daemon as a pure
  proxy — when the MCP stdin loop exits, only the proxy process terminates;
  the global daemon keeps running for other callers. Previously, closing
  the agent's MCP transport killed the daemon, causing orphaned processes
  and dashboard downtime.

### Changed

- **Daemon idle timeout**: `idleTimeout=0` in config now explicitly disables
  the idle monitor. The daemon runs indefinitely until `dfmt stop` or
  `SIGINT/SIGTERM`. Previously, a zero/empty timeout fell back to the 30-min
  default; now it is a first-class "never exit" value.

### Internal

- **Timeout constants**: all scattered timeout literals (`5 * time.Second`,
  `30 * time.Second`, `60 * time.Second`) consolidated into a single
  `internal/timeouts` package. One place to tune, everywhere to use.
- **sync.Pool for buffers**: `strings.Builder` and `bytes.Buffer` now
  reuse pooled instances via `sync.Pool` in `internal/sandbox/pool.go`,
  reducing allocations in high-throughput exec paths.
- **Path interning in renderers**: `buildPathRefs` path interning (ref
  tokens like `[r1]`) is now active in `JSONRenderer` and `XMLRenderer`
  as well as `MarkdownRenderer`, reducing repeated path string overhead.

## [0.6.8] — 2026-05-16

A refactor-and-test release. No wire contract changes; every entry below is
internal cleanup that closes the `refactor.md` audit list. The MCP tool
shapes, on-disk journal format, and CLI subcommand surface are unchanged.

### Changed — file structure (Wave 2)

- `internal/cli/dispatch.go` split into nine files
  (`dispatch.go`, `daemon.go`, `doctor.go`, `setup.go`, `project.go`,
  `recall.go`, `config.go`, `dashboard.go`, `tools.go`, `mcp.go`).
- `internal/sandbox/permissions.go` split into eight files by
  responsibility (path, exec, fetch, runtime, etc.).
- `internal/transport/handlers.go` split into seven `handlers_*.go`
  files keyed by tool group.

### Changed — god-function decompositions (Wave 3 + §9 closure)

- `handleToolsList` 302 → 20 LOC (MCP tool schemas extracted to
  `mcp_schemas.go`).
- `runDoctor` 344 → 76 LOC (`coreChecks` + `runChecks`).
- `daemon.New` 261 → 110 LOC (`loadJournalAndIndex`, `loadSandbox`,
  `buildServer`).
- `handleToolsCall` 228 → 82 LOC. Generic `dispatchTool[T, R]` helper
  collapses 11 nearly-identical tool-dispatch cases. The Stats case's
  bare `json.Unmarshal` is normalized to `decodeParams`; non-strict
  mode is functionally identical and Stats now also rejects trailing
  tokens, matching every other tool.
- `handleAPIProxy` 244 → 79 LOC. Five helpers split out:
  `writeProxyError` (15 duplicate error-encode sites → 1),
  `readDaemonRegistry`, `resolveProxyTarget`,
  `forwardProxyOverUnix`, `forwardProxyOverHTTP`. Wire contract
  (status codes, JSON-RPC error codes, body caps) byte-identical.
- `splitByShellOperators` 186 → 118 LOC. Three helpers
  (`scanParensClose`, `scanBacktickClose`, `appendInnerSplits`)
  deduplicate the five paren / backtick / subshell scanners that were
  inlined separately. A bug fix to paren counting now lands in one
  place instead of five.
- `sandbox.Grep` 170 → 77 LOC. Four helpers (`compileGrepPattern`,
  `resolveGrepSearchRoot`, `grepFileLines`, `filterMatchesByIntent`)
  make file-level grep behavior independently unit-testable. Same
  100-match cap, same 200-byte line truncation, same intent-filter
  "preserve unfiltered when filter empties the set" semantics.

### Changed — duplication kills

- New `internal/osutil` package consolidates 6 copies of the
  `goosWindows` constant and ~35 inline `runtime.GOOS == "windows"`
  sites. Cross-package `SamePath` / `SameCleanPath` helpers replace
  four near-duplicate path-equality functions.
- Named context timeouts: `rpcTimeout` (5 s, short metadata calls),
  `toolTimeout` (30 s, interactive tool calls),
  `perProjectShutdownTimeout` (5 s, per-project journal +
  index shutdown work) — names the 4 inline 5 s budgets in
  `projectres.go` and the 10 inline 5 s/30 s call sites in `cli`.
- `cli.ParseGlobals(args)` consolidates global-flag parsing
  (`stripGlobalFlags` / `SetGlobalJSON` / `SetGlobalProject` were
  three views of the same logic). `cmd/dfmt/main.go` now calls one
  parser; strict-mode `--project` error returns surface to the
  operator instead of silently dropping. The lenient
  `stripGlobalFlags` stays as a one-line wrapper preserving the
  historical Dispatch-by-tests tolerance.
- `cli.resolveSpawnExePath` consolidates the test-binary refusal
  dance (4 sites across `startGlobalDaemonBackground` and
  `startDaemonBackground`) into one resolution point.

### Added — sentinel errors

- `sandbox.ErrPolicyDenied` — every "operation denied by policy"
  error now wraps this sentinel via `%w`. Callers / tests can
  `errors.Is(err, sandbox.ErrPolicyDenied)` instead of substring-
  matching the formatted message.
- `sandbox.ErrPathContainsNullByte` — load-bearing path-sanitization
  refusal, previously an inline `errors.New` at three sites.
- `core.ErrJournalClosed` — Append / Read after Close. Lets callers
  distinguish post-shutdown writes from generic I/O errors.
- `transport.ErrServerAlreadyRunning` — same identity on
  `HTTPServer.Start` and `SocketServer.Start`. Supervisor logic can
  now branch on the sentinel.

### Removed

- `cli.SetGlobalJSON` and `cli.SetGlobalProject` exported setters.
  Replaced by `cli.ParseGlobals` which writes the package-private
  flag state directly.

### Fixed

- `--help` / `-h` no longer mutates state on `dfmt task` and `dfmt
  install-hooks`. Pre-fix `dfmt task --help` journaled an event whose
  subject was the literal string "--help", and `dfmt install-hooks
  --help` rewrote `.git/hooks/{post-commit,post-checkout,pre-push}`
  unconditionally because `runInstallHooks` ignored its argv. Both
  subcommands now route through a FlagSet (install-hooks) or an
  explicit `helpRequested` check (task) so help requests
  short-circuit before any side effect runs.
- `dfmt note --help` prints "Usage of note:" rather than the
  misleading "Usage of remember:". The shared `runRemember` now
  takes the invocation verb and names its FlagSet accordingly.
- `dfmt stats|status|list|stop --help` print usage instead of
  silently running the underlying command.
- `dfmt shell-init --help` / `dfmt capture --help` / `dfmt config
  get --help` print usage rather than the previous confusing errors
  ("unknown shell: --help", "unknown capture type: --help", "unknown
  config key '--help'").
- Global `-json` / `--json` / `--project` flags survive direct
  `Dispatch` calls — external callers and the test suite that import
  `internal/cli` and call `Dispatch` directly now behave the same as
  `cmd/dfmt`.
- `errors.Is` at two remaining sentinel-equality sites: the
  `errSkipCapture` check in `cli/setup.go` and the `path_prepend`
  stat error in `sandbox/permissions.go` (now `%w`-wrapped).

### Tests

- Wave 4 coverage push: cli 63.4 → 69.3%, transport 82.4 → 83.1%,
  daemon 75.0 → 75.2%. New `setupInProcessDaemon` helper uses
  `daemon.PromoteInProcess` to give CLI tests an in-process daemon
  without sibling-process fork.
- Wave 5 cli push: tests for `loadedProjectsViaAPI` (five branches
  via `httptest`) and `signalStopProcess` panic-safety edges
  (PID 0, PID outside kernel range).
- New `TestHelpFlagDoesNotMutateState_{Task,InstallHooks}` regression
  guards. `TestRunInstallHooks{Bash,Zsh,Fish}Shell` renamed to
  `TestRunInstallHooksRejectsBogusShellFlag*` and corrected to
  assert exit 2 (the previous `want 0` only passed because
  `runInstallHooks` silently ignored its argv).

## [0.6.7] — 2026-05-08

### Fixed

- **`globalConfigPath()` now respects `XDG_DATA_HOME`**: The function
  was ignoring the environment variable and always returning
  `~/.dfmt/config.yaml` on all platforms. Tests and production
  behavior now agree.
- **Stale test defaults corrected**: `transport.http.enabled` default
  (`true`), `transport.http.bind` default (`127.0.0.1:3490`), and
  `newTestConfig()` HTTP enabled flag all corrected to match `Default()`.
- **`TestRunSetupVerifyMissingFilesOps` isolation**: Test now sets
  `XDG_DATA_HOME` to its temp dir so the global manifest doesn't leak
  between test cases.

## [0.6.6] — 2026-05-07

### Security

- **AUTHZ-01**: Exec deny rules (e.g. `deny:exec:git *`) now match case-insensitively, so `GIT` or `Git` invocations are correctly blocked. Previously only lowercase `git` matched.
- **AUTHZ-01**: Deny rules with leading directory (e.g. `/usr/bin/sudo`) are now also normalized, closing a bypass vector.
- `detach_windows.go`: Removed redundant `HideWindow` flag that was causing terminal window flicker on Windows. `CREATE_NO_WINDOW` already prevents console window creation.

## [0.6.4] — 2026-05-07

Test coverage push with targeted new tests across core, safefs, transport,
daemon, and cli packages. No behavioral changes to the public API.

### Added

- `Journal.StreamN` — `Stream(ctx, from, n)` variant that drains at most
  `n` events before closing the channel. The existing `Stream` signature
  is unchanged.
- `DFMT_MCP_STRICT_PARAMS=1` env var enables strict JSON-RPC params
  validation, rejecting unknown fields that would previously be silently
  ignored.
- `dispatch_extra_test.go` — suite of `TestDispatch*` and `TestRun*` tests
  covering error paths, flag handling, and subcommand routing.
- symfsafe file operation tests: `WriteFileAtomic` error paths
  (`WriteError`, `SyncError`, `CloseError`, `RenameError`), permission
  behavior on Windows, and `OpenReadNoFollow` for symlink-safe file opens.
- HTTP handler direct-call tests: `handleDropProject`,
  `handleDashboardCSS`, `handleFavicon`, `handleMetrics`,
  `handleAPIDaemons`, `handleAPIAllDaemons`, `handleAPIStream`,
  `handleAPIProxy`, `handleAPIStats`.
- `TestHTTPServerBindGetter`, `TestHTTPServerPortFileGetter`,
  `TestHTTPServerSocketPathGetter` for HTTPServer field accessors.

### Changed

- `internal/cli/dispatch.go` — expanded subcommand routing and dispatch
  helpers.
- `internal/sandbox/runtime.go` — exec pipeline error handling
  improvements.
- `internal/setup/claude.go` — agent detection refinements.

### Fixed

- `runDaemon` now calls `config.Load("")` instead of `config.Default()`,
  so `transport.http.bind` in `~/.dfmt/config.yaml` is actually honored.
  Previously the daemon always bound an ephemeral port regardless of the
  configured bind address.
- `globalConfigPath` now returns `~/.dfmt/config.yaml` instead of
  `~/.local/share/dfmt/config.yaml`, so the global config survives
  `dev.ps1` wipes (which preserve the `~/.dfmt/` directory itself).
- `dev.ps1` no longer deletes `~/.dfmt/config.yaml` during the wipe
  step.
- `config.merge` now auto-creates `~/.dfmt/config.yaml` when the file
  is missing, so the daemon always starts with a known-good
  configuration after a wipe.
- Default `transport.http.enabled=true` and `transport.http.bind=127.0.0.1:3490`
  so the daemon binds a stable port on first boot without any config
  present.

## [0.6.3] — 2026-05-07

Reverses the v0.6.2 "no detach" decision. Every `dfmt` subcommand now
brings the daemon up if it is not running AND returns to the shell
prompt promptly — the daemon role lives on in a detached background
child. The user-visible contract is now: _whatever command you run,
it opens the daemon once, and unless killed the daemon always exists_.

### Changed

- `acquireBackend` rewritten: when no daemon is running, spawn a
  detached `dfmt daemon` child via `startGlobalDaemonBackground`,
  poll the listener for liveness up to 4 s, then connect as a client.
  Short-lived commands no longer block the terminal on idle-exit.
- `startGlobalDaemonBackground` now uses platform-specific detachment
  (Windows `DETACHED_PROCESS | CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP`,
  Unix `setsid()`) so the daemon child survives the parent's exit.
  Pre-v0.6.3 the spawned daemon inherited the parent's console / process
  group; closing the launching terminal could kill the daemon.
- `dfmt status`, `dfmt list`, `dfmt doctor` now call the new
  `ensureGlobalDaemon` helper at function entry. Pre-v0.6.3 these three
  reported "Daemon: not running" without trying to bring the daemon up,
  which contradicted the rest of the CLI's "self-promote on demand"
  story.

### Preserved from v0.6.2

- `runMCP` keeps its in-process self-promotion path
  (`acquireBackendForLongRunner`). MCP-driven sessions still show
  exactly one `dfmt.exe` in `tasklist` because MCP itself is the
  long-lived process.
- `client.NewClient` still has no auto-spawn. The single spawn site is
  `startGlobalDaemonBackground`, called from `runDaemon` (explicit) or
  `ensureGlobalDaemon` (implicit-on-need). Test binaries and
  `DFMT_DISABLE_AUTOSTART=1` short-circuit to in-process promotion.

### Notes

- Verified end-to-end on Windows: `dfmt status` from a clean state
  returns in ~100 ms with the daemon spawned; subsequent
  `stats` / `list` / `remember` / `search` calls return in ~30–70 ms;
  `tasklist` shows exactly one `dfmt.exe` between commands; `dfmt stop`
  → next command respawns cleanly.

### Recommended workflow

The single workflow now collapses to: run any `dfmt <command>`. That
command brings the daemon up if it is not running (~100 ms latency,
one-time per `dfmt stop` cycle) and returns to the prompt. The daemon
persists in the background until SIGINT, `dfmt stop`, or the configured
idle-exit timer.

For the v0.6.2 in-process semantics (no detached child, daemon role
lives in the foreground process), set `DFMT_DISABLE_AUTOSTART=1`. The
in-process promote path is preserved as the documented escape hatch.

## [0.6.2] — 2026-05-06

Documentation release. Closes the v0.6.x roadmap by explicitly
deciding **not** to implement the terminal-detach helper. The OS-level
trade-off it would have required (fork-and-exit, breaking the
"single dfmt.exe" promise that v0.6.0/v0.6.1 just established)
outweighed its benefit for the typical workflow.

### Recommended workflow

The shortest path to a non-blocking, single-process experience:

- **With Claude Code (the typical case)**: open the project, Claude
  Code spawns `dfmt mcp` automatically, that process self-promotes
  to daemon, and stays alive for as long as the agent session runs.
  Other dfmt invocations (`dfmt stats`, `dfmt list`, etc.) from any
  terminal connect-and-exit immediately. Zero terminal blocking.

- **Standalone CLI use without Claude Code**: run `dfmt daemon` once
  at session start. The host-wide global daemon comes up, the
  terminal returns to the prompt (Windows `start /B dfmt daemon`,
  Unix `dfmt daemon &` for explicit shell-backgrounding). Subsequent
  `dfmt stats` / `dfmt search` / `dfmt recall` calls connect-and-exit
  fast.

- **Quick one-shot from a fresh terminal with no daemon**: `dfmt
  stats` self-promotes, prints, and the process keeps running until
  Ctrl+C or the configured idle-exit timer (30 minutes default).
  Terminal blocks for the duration. This is the only case where the
  "no detach" decision is visible to operators.

### Why not implement detach

The OS reality on both Windows and Unix is that the parent shell
(PowerShell, cmd, bash, zsh) waits for the **child process to exit**
before returning the prompt — closing stdio is not enough. To return
the prompt while a daemon keeps running, the daemon role must live
in a **separate process** that survives the original process's exit.
That separate process is, by definition, a spawn — `os.StartProcess`
of `os.Args[0]` with a marker flag, or `exec.Command(self, "daemon",
"--foreground")`. Either way it has the same shape as the
v0.5.x auto-spawn pattern that v0.6.0/v0.6.1 deliberately retired.

The net steady-state result of fork-and-exit detach would be one
`dfmt.exe` (the daemon child), so the "tek dfmt.exe" eyeball test
still passes. But the brief two-process window during the handoff,
plus the lock-handoff coordination (parent stops listener → spawns
child → child binds listener) is meaningful complexity for a UX gap
that only affects a rare workflow (standalone CLI use without a
prior `dfmt mcp` or `dfmt daemon` invocation).

ADR-0021 captures the full decision tree and the alternatives
considered. The detach helper is officially **out of scope** for the
v0.6.x cycle; revisited only if a real operator workflow surfaces
that needs it.

### Changed

- `docs/adr/0021-single-binary-self-promotion.md` — adds an
  "Explicitly out of scope: terminal detach" section with the OS
  reasoning and the workflow guidance above.
- `CLAUDE.md` / `AGENTS.md` — short note on the recommended
  daemon-up-first pattern for standalone CLI sessions.
- `CHANGELOG.md` — v0.6.2 entry covering the deliberate
  non-implementation.

### Notes

- No code changes. `internal/version.Current` bumped to `v0.6.2`
  for the release tag only.
- v0.6.x is now feature-complete; future improvements will land on
  the v0.7.x track.

## [0.6.1] — 2026-05-06

The cleanup release that closes v0.6.0's "Notes / known limitations".
Every dfmt subcommand now self-promotes the daemon in-process when
none is running, and the legacy auto-spawn path inside
`client.NewClient` is gone for good.

### Changed

- **Tool-call wrappers self-promote.** `dfmt exec`, `dfmt read`,
  `dfmt fetch`, `dfmt glob`, `dfmt grep`, `dfmt edit`, `dfmt write`
  use `acquireBackend` instead of `client.NewClient` directly. Same
  pattern as the read/write commands shipped in v0.6.0: connect to
  existing daemon, or become it. `dfmt exec` and `dfmt read` keep
  their direct-sandbox fallback for the truly-degraded case.
- **`client.NewClient` no longer spawns.** `ensureDaemon`,
  `startDaemon`, and the test-binary guard `isTestBinary` retired
  from `internal/client`. The legacy per-project port/socket
  fallback remains so surviving v0.3.x daemons keep working; the
  pre-v0.6.0 `exec.Command(self, "daemon", "--global")` is gone.

### Removed

- `internal/client.ensureDaemon` (unexported).
- `internal/client.startDaemon` (unexported).
- `internal/client.isTestBinary` (unexported; CLI side has its own
  copy in `internal/cli/dispatch.go`).
- The `DFMT_DISABLE_AUTOSTART` env-var opt-out is now a no-op since
  there's nothing left to disable. Documentation references it for
  back-compat with operator runbooks but the code path is gone.

### Notes

- The `exec.Command(self, ...)` count in the dfmt source is now
  zero outside the explicit `dfmt daemon` background-mode path
  (`startGlobalDaemonBackground` in `dispatch.go`). That one is
  opt-in by user invocation, not implicit auto-spawn.
- Terminal-blocking for short-lived commands (`dfmt stats`,
  `dfmt search`, etc. when no daemon is running) is still queued
  for v0.6.x. The detach helper (`FreeConsole` / `setsid+fork`) is
  the next item on the v0.6.x roadmap.

## [0.6.0] — 2026-05-06

The single-binary release. `dfmt mcp` and the main user-facing CLI
commands no longer spawn a separate `dfmt daemon` child via
`exec.Command`. When no daemon is running, the calling process
self-promotes via `daemon.PromoteInProcess`, binds the host-wide
listener, serves its own command using the in-process Handlers as
its Backend, and (for long-lived foreground commands) keeps running
until SIGINT or the idle-exit timer fires.

Net result: one `dfmt.exe` in `tasklist` instead of the v0.5.x pair
(MCP subprocess + spawned daemon). The user-visible promise: any
`dfmt <command>` invocation either connects to the existing daemon
or *becomes* the daemon, no second binary launched.

### Added

- **`daemon.PromoteInProcess(ctx, cfg)`** — constructs and starts a
  global daemon in the current process. Returns `*LockError` when
  another process already holds the host-wide lock so the caller
  can `errors.As` it and fall back to a thin RPC client.
- **`Daemon.Done() <-chan struct{}`** — closed on Stop / idle-exit.
  Long-lived subcommands block on it after their primary work
  completes.
- **`Daemon.Handlers() *transport.Handlers`** — exposes the live
  handler set so promoted-in-process callers can use it as a
  `transport.Backend` directly without an IPC roundtrip.
- **`cli.acquireBackend(projectPath)`** (internal) — single seam
  every daemon-touching subcommand goes through. Returns either a
  `client.ClientBackend` (existing daemon) or the in-process
  Handlers (promoted), plus the owned `*Daemon` when this process
  must keep it alive.

### Changed

- **`dfmt mcp`** no longer calls `client.NewClient`'s auto-spawn
  path. On daemon-not-running, the MCP subprocess self-promotes;
  when stdin EOF closes the MCP transport (Claude Code closed),
  the daemon role keeps serving inbound RPCs from other dfmt
  invocations on the same host until SIGINT or idle-exit.
- **`dfmt stats`, `dfmt search`, `dfmt recall`, `dfmt remember`,
  `dfmt tail`** apply the same self-promotion pattern. Tests that
  previously asserted "no daemon → return 1" now accept either 0
  (promoted + served) or 1 (legitimately degraded).

### Notes / known limitations

- **Terminal blocking for short-lived commands.** When `dfmt stats`
  or `dfmt search` runs without an existing daemon, it self-promotes
  and the daemon role keeps the process alive after the command
  output prints. The user's terminal blocks until SIGINT or the
  idle-exit timer (30 min default). Workarounds: shell-background
  the invocation, or run `dfmt mcp` / `dfmt daemon` first as a
  long-lived foreground process. A `FreeConsole` (Windows) /
  `setsid+fork` (Unix) detach helper is queued for v0.6.x.
- **Tool-call wrappers** (`dfmt exec`, `dfmt read`, `dfmt fetch`,
  `dfmt glob`, `dfmt grep`, `dfmt edit`, `dfmt write`) still use
  `client.NewClient` directly. They typically run through MCP
  rather than the CLI; the auto-spawn path inside `NewClient`
  remains alive until they migrate too. v0.6.x cleanup.
- **`client.NewClient`'s `startDaemon` / `exec.Command`** is still
  alive for the tool-call wrappers above. v0.6.x removes it once
  every caller routes through `acquireBackend`.

## [0.5.0] — 2026-05-06

The architectural cleanup release. `dfmt mcp` is now a thin proxy over
the global daemon — no more duplicate journal handles, no more
in-memory index drift between MCP subprocess and daemon. Per-project
filesystem capture, `dfmt remove` cache eviction, live SSE event log
on the dashboard, and a bounded multi-project cache.

### Breaking

- **`dfmt daemon --legacy` is gone.** The per-project daemon mode
  (kept on life support during the v0.4.x deprecation window) is
  removed. `dfmt daemon` always brings up the host-wide global
  daemon. Operators upgrading from v0.3.x must run `dfmt setup
  --refresh` (which already migrates legacy daemons in v0.4.x) and
  update any hand-rolled systemd / launchctl units to drop `--legacy`.

- **`dfmt mcp` requires a daemon.** The subprocess no longer opens
  its own journal/index/sandbox state. When the daemon is unreachable
  (and auto-spawn fails within ~4 s), tool calls return JSON-RPC
  error `-32603 daemon unreachable` instead of falling back to local
  state. There is no silent local mode — this is the deliberate
  trade-off behind closing the duplicate-journal-handle borç.

### Added

- **MCP proxy mode.** `dfmt mcp` forwards every tool call through
  `client.NewClient` to the daemon. The MCPProtocol now reads from a
  `transport.Backend` interface; the local `*Handlers` and the new
  `client.ClientBackend` adapter both satisfy it. Effects: zero
  duplicated journal handles, zero per-startup journal replay in the
  MCP subprocess, all per-project state owned by one process.

- **Per-project filesystem watcher.** Extra projects served by the
  global daemon now get their own `FSWatcher` when their config opts
  in via `capture.fs.enabled: true`. `ProjectResources.startFSWatch`
  spawns the consume goroutine; teardown order in
  `closeExtraProjects` is fswatch → index-tail → journal close so a
  shutting-down project never appends past its own journal close.
  v0.4.x only ran fswatch for the default project; secondary projects
  fell back to MCP/CLI events alone.

- **Live SSE journal tail.** `/api/stream?follow=true` now keeps the
  connection open after historical replay drains, polls every 2 s for
  events strictly after the last cursor, and forwards them to the
  client. The dashboard's project switcher opens an `EventSource`
  with `follow=true` so the new "Live Events" panel surfaces appends
  without a page reload. Without `follow=true` the endpoint stays in
  its pre-v0.5.0 one-shot replay contract — `dfmt tail` and
  `Client.StreamEvents` both depend on the channel closing at HEAD.

- **`dfmt remove` daemon cache eviction.** When a daemon is reachable,
  `dfmt remove` calls `dfmt.drop_project` so the running daemon
  evicts its `ProjectResources` cache entry (Checkpoint + Persist +
  Close, then drop) before .dfmt/ is deleted on disk. Without this,
  the daemon kept open file handles to the removed journal/index, the
  dashboard switcher continued to list the dead project, and the next
  `Resources(P)` call would race against the missing directory. New
  RPC method on the JSON-RPC surface (also accessible via the
  `drop_project` alias).

- **LRU eviction in extra-project cache.** The `extraProjects` map
  caps at 8 entries (`extraProjectsMaxEntries`). On a cache miss
  when full, the project with the oldest `LastActivityNs` is evicted
  before the new bundle loads. Eviction teardown runs in a goroutine
  after the cache entry is removed under the write lock, so concurrent
  `Resources()` lookups never block on the slow flush. A long-running
  global daemon serving 50+ projects no longer accumulates unbounded
  memory.

### Changed

- **`compressionStats` reads the journal via Backend.StreamEvents.**
  The MCP-side stats summary used to reach into `m.handlers.journal`
  directly; it now goes through the same Backend interface as every
  other tool call. Local mode reads the daemon's journal as before;
  proxy mode reads the daemon's journal via the network call.

### Notes

- `Client.NewClient`'s legacy port-file fallback (Step 2/2b/2c) is
  intentionally still present. Removing it requires updating ~20
  `TestClient_*` tests that seed legacy port files to assert behavior;
  that follow-up is queued for v0.5.x. User-visible behavior is
  unchanged: a v0.3.x port file still gets reaped on first auto-spawn
  and the global daemon takes over.

- Default project's filesystem watcher continues to use the existing
  `Daemon.fswatcher` / `Daemon.consumeFSWatch` path. Migrating it to
  `defaultRes` is a cleanup for a follow-up.

## [0.4.7] — 2026-05-06

The biggest pre-v0.5.0 correctness fix: the daemon's in-memory search
index no longer drifts away from on-disk journal state when another
process (most importantly `dfmt mcp` subprocesses) appends events.

### Fixed

- **MCP-subprocess index drift.** The `dfmt mcp` subprocess opens its
  own journal handle to the same `.dfmt/journal.jsonl` file as the
  daemon and appends events directly. The daemon's in-memory index
  was never updated for those events — `dfmt_search` results stayed
  stale until the next daemon restart (which on a global daemon
  serving long sessions could be hours away). A new per-project tail
  goroutine in `ProjectResources` polls the journal every 3 s and
  `Add()`s new events to the index. `Index.Add` is idempotent so
  events the daemon itself appended are safely re-processed as
  no-ops. Wired for both the default-project (legacy daemons) and
  extra-project (global daemon) paths. Closes drift within ~3 s.

### Note

This is the first piece of the v0.5.0 architectural cleanup landed
as a v0.4.x patch — additive only, no API or wire changes. The
larger MCP-proxy refactor (eliminating the duplicate journal handle
entirely) remains queued for v0.5.0.

## [0.4.6] — 2026-05-06

Patch release with two more global-daemon UX fixes after v0.4.5.

### Fixed

- **`dfmt status` works from any directory.** Previously errored out
  with `error: no project found` when invoked outside a DFMT-
  initialized directory, hiding the host-wide global daemon from any
  fresh terminal. The missing-project case is now a degraded report:
  the project line is annotated with the resolution error, but daemon
  liveness, dashboard URL, transport endpoint and last-crash all
  still render. JSON adds a `project_error` field so scripts can
  distinguish the no-project case from a real failure.
- **`dfmt setup --uninstall` stops the global daemon up front.** Was
  stripping agent MCP configs and removing manifest files but
  leaving the host-wide daemon running for up-to-the-idle-timeout,
  with stale handles open for every cached project. Also closed the
  awkward window where agent configs no longer pointed to dfmt but
  the daemon was still serving requests routed through them.
  Falls through with a warning if the stop fails so uninstall still
  completes.

## [0.4.5] — 2026-05-06

Patch release closing two more global-daemon UX gaps caught after
v0.4.4 shipped.

### Fixed

- **Dashboard initial load + refresh.** The page fired `loadStats()`
  with empty params before any project was selected, so the first
  `/api/stats` POST returned `-32603` (errProjectIDRequired) and the
  page showed an error before the user had done anything. Refresh
  button had the same problem — it always called `loadStats()`,
  silently navigating back to the empty-params view even after the
  user picked a project. `loadDaemons` now returns the first project
  path so init can preselect it; refresh routes through a wrapper
  that reads the selector value and falls back to bare `loadStats()`
  only for legacy single-project daemons.
- **`dfmt status --json` socket field.** Was always emitting the
  per-project legacy socket path under `socket`, regardless of which
  daemon was actually answering RPCs. In global daemon mode the
  per-project path is a stale v0.3.x artifact — the real listener is
  `~/.dfmt/daemon.sock` (Unix) or `~/.dfmt/port` (Windows). Operators
  debugging "why won't my client connect" got pointed at the wrong
  file. Now resolves the global path first when a host-wide daemon
  is up.

## [0.4.4] — 2026-05-06

Patch release closing four cross-project correctness gaps in the
host-wide daemon's resource cache. Each is a real bug that hits
users running one DFMT daemon for multiple projects (the v0.4.0
default).

### Fixed

- **Per-project redactor routing.** Every redactor-touching helper
  (`redactString`, `redactData`, `redactEventForRender`,
  `redactMatches`) read the default project's redactor on every call.
  In global daemon mode that meant project A's `redact.yaml` patterns
  scrubbed project B's tool output, and project B's own patterns
  never fired against its own bytes — a two-direction privacy leak.
  New `redactorFor(ctx)` helper resolves the per-project redactor
  via the bundle; legacy single-project daemons keep working through
  the fallback path.
- **Per-project content store + dedup key.** `stashContent` read
  `h.getStore()` (default project's store, nil in global mode) so
  every Exec / Read / Fetch returned `content_id=""` and the
  dashboard's "show content" links broke for every project. The
  dedup cache was also project-blind: identical bytes from project
  A and B collapsed to one chunk-set ID pointing into A's store,
  so B's lookup 404'd. Both fixed by routing through
  `bundle.ContentStore` + `bundle.ProjectPath` and prepending
  project ID to the dedup key.
- **Per-project config.** `loadProjectResources` reused the daemon's
  startup cfg for every cached extra project. A global daemon
  serving project A and B silently used A's retention / budget /
  path_prepend on every B call regardless of B's `.dfmt/config.yaml`.
  Each project now loads its own config; daemon-level cfg is the
  fallback when project YAML is missing or malformed.
- **Index persistence on shutdown.** `closeExtraProjects` (the Stop
  path for the per-project resource cache) closed each cached
  journal but never persisted its index. Every restart of a global
  daemon serving N projects forced a full journal replay per project
  on the next run — user-visible as a cold-recall pause that scaled
  with journal size. Persist now happens before Close, gated on
  `NeedsRebuild=false` to avoid writing a cursor=HEAD that would
  silently mark older events as indexed.

### Internal

- **Daemon registry honors `DFMT_GLOBAL_DIR`.** The `~/.dfmt/daemons.json`
  path now resolves on every save/load instead of caching at first
  `GetRegistry()` call, so test sandboxes and migration tooling get
  the right path. Test pollution of the developer's real registry
  from `internal/daemon/{TestRegister,TestUnregister}` closed via
  `t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())` + a deferred
  `unregister()` cleanup.

## [0.4.3] — 2026-05-06

Patch release closing three more global-daemon visibility gaps
caught in the v0.4.2 audit pass.

### Fixed

- **`dfmt mcp` registers project with the host-wide daemon at
  startup.** MCP-only projects (no other CLI command had touched
  them yet) were invisible to the dashboard's project switcher
  even though events were being recorded. `runMCP` now fires an
  async best-effort Stats RPC at startup so the daemon's
  Resources(projectID) lazy-loads the project into extraProjects.
  Never spawns a daemon, never blocks startup.
- **`dfmt list` shows the global daemon.** Was reporting "No
  running daemons" against a live host-wide daemon because the
  on-disk registry stays empty in global mode. Now synthesizes
  rows from `/api/all-daemons` so every project the daemon's
  resource cache holds appears in the listing.
- **`dfmt doctor` port + PID checks see the global paths.** Both
  rows flagged red under the global daemon because they only
  checked `<proj>/.dfmt/{port,daemon.pid}`. They now check the
  global paths first and fall back to per-project for v0.3.x
  straddle setups.

### Note

The deeper architectural issue — `dfmt mcp` writing to its own
journal/index handles instead of forwarding through the daemon —
is preserved for v0.5.0. The MCP startup ping is a v0.4.x
visibility fix, not the proxy refactor.

## [0.4.2] — 2026-05-06

Patch release closing two more audit findings against the global
daemon. v0.4.0 shipped the host-wide daemon; v0.4.1 wired the
dashboard cross-project switcher; v0.4.2 closes the gaps in the
SSE stream endpoint and the `dfmt stop` command.

### Fixed

- **`/api/stream` accepts `project_id` query param.** The SSE
  endpoint used by `Client.StreamEvents` and any future dashboard
  live-event view broke against the global daemon: Stream(ctx,...)
  was called with no project_id pushed in, so resolveBundle
  returned `project_id required: daemon has no default project`
  and every connection failed. `Client.StreamEvents` now appends
  the client's project_id to the URL on both Unix-socket and TCP
  paths.
- **`dfmt stop` actually stops the global daemon.** v0.4.0/v0.4.1
  read only `<proj>/.dfmt/daemon.pid`, saw no PID against a host-
  wide daemon, deleted per-project lock/socket files (which were
  never there), and printed "Daemon stopped" while the process
  kept running. `runStop` now short-circuits to a global-aware
  shutdown when `globalDashboardURL` is non-empty: SIGINT (or
  taskkill /T on Windows), 5 s graceful window, escalate to
  SIGKILL / taskkill /F if needed, then remove ~/.dfmt/{port,
  daemon.pid, lock, daemon.sock} once the process is gone.

## [0.4.1] — 2026-05-06

Patch release for the dashboard's cross-project switcher. Phase 2's
project_id routing already worked in v0.4.0 (the daemon serves every
project from one process, every RPC carries `project_id`), but the
dashboard's project-selector dropdown was reading from the on-disk
daemon registry — which the host-wide daemon doesn't populate. The
result was an empty dropdown that broke the user-visible promise of
"every project monitored from one dashboard."

### Fixed

- `/api/all-daemons` now enumerates the daemon's in-process project
  cache when the host daemon installs a `ProjectsLister` (global
  mode). Falls back to the on-disk registry only when no lister is
  installed (back-compat for v0.3.x straddle setups).
- Dashboard JS `loadStatsForProject` no longer routes through
  `/api/proxy` (which was designed for cross-daemon forwarding in the
  per-project model). It POSTs `/api/stats` with `project_id`
  stamped in params and lets the daemon's resolveBundle route to the
  right cache. `/api/proxy` is preserved on the daemon side for
  operators still running per-project daemons.

### Added

- `Daemon.LoadedProjects()` — returns the union of
  `defaultProjectPath` and the keys of `extraProjects` for the
  cross-project view.
- `Handlers.SetProjectsLister` / `LoadedProjects()` — parallel seam
  to `SetResourceFetcher` / `resolveBundle`.

## [0.4.0] — 2026-05-06

Phase 2: host-wide global daemon. The "tek daemon" rework — one DFMT
process per host serving every project, one stable dashboard URL, no
multi-process clutter in `tasklist` / `ps`. ADR-0019 (supersedes
ADR-0001) documents the architectural reversal.

### Breaking

- **One daemon, not N.** A single `dfmt daemon --global` process binds
  at `~/.dfmt/{port|daemon.sock,daemon.pid,lock}` and serves every
  project via lazy per-project resource caching. The legacy
  per-project daemon mode (`dfmt --project <p> daemon`) is preserved
  through the v0.4.x series for back-compat but operators should
  migrate via `dfmt setup --refresh`. v0.5.0 will remove it.
- **`flock` singleton.** Two `dfmt daemon --global` invocations on the
  same host cannot both bind. The second exits with a `LockError`
  carrying the global directory path.
- **Wire field.** Every JSON-RPC params struct now carries an
  `omitempty` `project_id` field. v0.3.x clients that omit it fall
  back to the daemon's `defaultProjectPath` (the legacy single-
  project pin) — backward compatible. New clients stamp it from the
  CLI's `--project` resolver or the MCP subprocess's session-bound
  cwd.

### Added

- `dfmt daemon --global` flag — host-wide daemon mode.
- `dfmt setup --refresh` migration step that stops legacy
  per-project daemons and removes their per-project transport
  scaffolding (port, daemon.sock, daemon.pid, lock). Project
  state (config, journal, index, content/) is preserved.
- `~/.dfmt/last-crash.log` — the global daemon writes a structured
  panic record (timestamp, version, panic value, full stack) on any
  unhandled panic via `safefs.WriteFileAtomic`. `dfmt doctor` and
  `dfmt --json status` surface the file's age and path.
- `internal/project/global.go` — `GlobalDir`, `GlobalSocketPath`,
  `GlobalPortPath`, `GlobalPIDPath`, `GlobalLockPath`,
  `GlobalCrashPath` helpers with `DFMT_GLOBAL_DIR` env override for
  tests / sandboxed environments.
- `daemon.NewGlobal(cfg)` constructor — daemon comes up with no
  per-project journal/index pre-loaded; resources resolve on each
  RPC via `Daemon.Resources(projectID)`.

### Changed

- `client.NewClient` connection order is now: probe global at
  `~/.dfmt/{port|daemon.sock}` → fall back to legacy per-project
  endpoint → auto-spawn global. The auto-spawn path uses
  `dfmt daemon --global`, so a fresh project's first call lands
  on the host-wide daemon.
- ADR-0001 (Per-Project Daemon Model) marked Superseded by ADR-0019.

### Migration

```sh
# v0.3.x → v0.4.0 upgrade path
dfmt setup --refresh   # stops legacy daemons, prints summary
dfmt status            # next call spawns the global daemon; URL stable
```

The migration is idempotent. Project journals, indexes, and
configuration files are never touched — only daemon transport
scaffolding (`<project>/.dfmt/{port,daemon.sock,daemon.pid,lock}`)
is removed.

## [0.3.2] — 2026-05-06

Security audit remediation cycle. The 4-phase pipeline (Recon → Hunt
→ Verify → Report) closed 26 findings: 0 critical, 4 high, 12 medium,
5 low, 5 informational. No exploitable issue reached production.

### Security

- **Recall snapshots are re-redacted before render** (V-01) — the
  recall path streamed events through markdown / JSON / XML
  formatters without re-applying the redactor, so a value redacted
  at journal-append time but updated by a later patch could leak
  through `dfmt_recall`. Redact now runs in both the inline-markdown
  and structured render paths.
- **Log file sink is `0o600`, not `0o644`** (V-02) — pre-fix the
  log file was world-readable; mkdir mode was `0o755` instead of
  `0o700`. Both tightened.
- **Deny rules normalized; reserved-name check hoisted into Write/
  Edit; default-policy doc aligned with default-permissive design**
  (V-03, V-05, V-15) — `extractBaseCommand` strips leading directory
  and treats tab/newline as IFS separators so `/usr/bin/sudo\twhoami`
  no longer slips past `deny:exec:sudo *`. `globToRegexShell` maps a
  literal space to `[ \t]+`. `safefs.CheckNoReservedNames` is now
  invoked on the Write/Edit hot path so `dfmt_write path="NUL"` is
  refused on Windows.
- **Setup writers preserve foreign MCP entries** (V-04) — pre-fix
  `dfmt setup` clobbered each agent's `mcp.json` with a single-key
  `{"mcpServers":{"dfmt":{...}}}`, silently destroying any other
  MCP servers (playwright, context7, github, …) the user had
  configured. New `MergeMCPServerEntry` splices our entry in and
  preserves every other key. A one-shot `<path>.dfmt.bak` pristine
  backup is captured on first patch.
- **Markdown injection in render pipeline closed** (V-06) — table
  cell pipes are escaped, code fence lengths grow with the body's
  longest backtick run, and recall ref-token forgery (`[r12]` from
  agent-controlled text) is escaped before render.
- **HTML tokenizer hardened** (V-07, V-08) — drop-set widened to
  cover `object`/`embed`/`applet`/`link`/`template`/`frame`/`frameset`/
  `math`/`portal`/`meta`. Raw-text scan switched to a windowed case-
  fold compare (was `strings.ToLower(t.src[t.pos:])` — O(N²) on
  pathological input). Token cap (200_000) and tag-depth cap (1024)
  added.
- **Sandbox Read closes TOCTOU via `O_NOFOLLOW`** (V-09) — Unix
  uses the syscall flag directly; Windows Lstat-then-Open with a
  reparse-point check on the leaf.
- **JSON decoders depth-capped on every agent-controlled path**
  (V-10) — new `internal/safejson` package; HTTP body decode (3
  call-sites), JSON-RPC envelope, journal lines, persisted index,
  and cursor file all run through `safejson.Unmarshal` with a
  64-deep nesting limit.
- **HTTP body and connection caps** (V-11, V-12) — `/api/proxy`
  bodies capped at 1 MiB (`MaxBytesReader`); HTTP and Unix-socket
  listeners wrapped with `LimitListener` (max 128 concurrent
  connections each).
- **In-memory index bounded** (V-13) — `MaxIndexDocs` (100_000)
  with FIFO eviction by oldest event ID. ULID time-sortable IDs
  give the eviction stable lexicographic ordering.
- **Setup integrity follow-ups** (V-14) — manifest now persisted
  BEFORE the agent file write so a save failure can never leave an
  injected file with no uninstall row. Claude trust flags
  (`hasTrustDialogAccepted`, `hasClaudeMdExternalIncludesApproved`,
  `hasClaudeMdExternalIncludesWarningShown`) are now captured to
  `<state>/claude-trust-prior.json` on first patch and restored on
  uninstall. Capture is idempotent so a re-patch doesn't lose the
  original state.
- **Redactor unicode-aware** (V-16) — bearer / generic-secret /
  basic / password patterns switched from ASCII `\s` to
  `[\s\p{Z}]` so NBSP / ZWSP / YAML whitespace-only emits don't
  bypass redaction. Truncated-PEM pattern added.
- **Fetch / exec timeouts clamped** (V-17) — pre-fix
  `time.Duration(params.Timeout) * time.Second` could overflow on
  large user-supplied values; the post-multiply `<= 0` floor reset
  to default but pinned a fetch semaphore slot for arbitrarily
  long. Now clamped to ceiling before multiplication.
- **MCP `tools/call` decoder is strict** (V-18) — was bare
  `json.Unmarshal`; now uses `decodeParams` (DisallowUnknownFields,
  trailing-token reject) plus `decodeRequiredParams` for tools whose
  schema needs at least one field. Agent-side typos (`limt: 10`)
  surface as -32602 instead of silently running with limit=0.
- **`safefs.WriteFile` clamps mode to user-only bits** (V-19) — the
  helper had been trusting caller-passed mode; one accidental
  `0o644` would have shipped world-readable secrets.
- **Setup writers all routed through safefs** (V-20) — `.dfmt/
  config.yaml` seed and the setup manifest itself were still using
  `os.WriteFile` (no symlink protection). Both now use
  `safefs.WriteFileAtomic`.
- **`/api/proxy` Unix-branch tightened** (V-21) — switched to
  `io.ReadAll(io.LimitReader(conn, 16<<20))` so multi-packet
  responses are read fully and oversized bodies surface explicitly
  rather than silently truncating; the post-write `err` shadowing
  is fixed.
- **Default response headers strict everywhere** (V-I1) —
  `wrapSecurity` now sets `X-Frame-Options: DENY` and a strict CSP
  (`default-src 'none'; frame-ancestors 'none'; base-uri 'none'`)
  on every non-health endpoint, not just `/dashboard`.
- **RFC 6598 CGNAT range blocked in SSRF defense** (V-I2) —
  `100.64.0.0/10` was uncovered by Go's `IsPrivate()` but routes to
  internal infrastructure on AWS NAT-gateway-fronted hosts.

## [0.3.1] — 2026-05-05

### Changed

- **DFMT no longer injects `deny` entries into Claude Code's
  `.claude/settings.json`** — pre-v0.3.1 init/setup added
  `permissions.deny: ["Bash", "WebFetch", "WebSearch"]` so the host
  agent could not call those tools natively. That call belongs to the
  user, not DFMT. Init/setup now register the MCP server and the
  PreToolUse routing hook only; the deny list is left untouched.
- **Stale legacy deny entries are pruned** — on the first post-upgrade
  init/setup, if `permissions.deny` contains the exact triple
  `{Bash, WebFetch, WebSearch}` that older DFMT versions injected,
  those three are removed. Any user-added entries (anything else, or
  any partial subset) are left in place. The heuristic deliberately
  errs on the side of preserving the user's policy.
- **HTTP transport: bearer-token auth removed** — the auth gate added
  in v0.2.7 is dropped. All HTTP endpoints (dashboard + JSON-RPC) are
  publicly accessible on the bound loopback port. Re-enable per-deployment
  with a reverse proxy if needed; the daemon binds 127.0.0.1 by default.

### Fixed

- **dev.sh / dev.ps1 build the right version string** — both scripts
  now stamp `v0.3.1` into `internal/version.Current`. dev.sh was
  previously frozen at `v0.2.7-dev`.

## [0.3.0] — 2026-05-05

### Changed

- **Default-permissive exec policy** — sandbox exec is now fully allowed by
  default. All commands (`gh`, `curl`, `sudo`, `rm`, etc.) pass without
  configuration. Operators can restrict specific commands via
  `.dfmt/permissions.yaml` if needed.
- **Default-permissive read/write/edit** — all read, write, and edit
  operations are allowed by default. Operators can restrict paths via
  `.dfmt/permissions.yaml`.
- **Hard-deny list cleared** — `hardDenyExecBaseCommands` is now empty.
  SSRF protections (cloud metadata IPs, file:// scheme) remain enforced via
  fetch deny rules.
- **`dfmt remove` command** — new `dfmt remove` (alias: `dfmt teardown`)
  undoes `dfmt init`: removes `.dfmt/`, strips DFMT block from
  `.claude/settings.json`, CLAUDE.md, and AGENTS.md. Does NOT touch
  agent MCP configs — use `dfmt setup --uninstall` for that.
- **Fallback mapping documented** — AGENTS.md and CLAUDE.md now explicitly
  list the fallback tool for each dfmt_* MCP tool.

## [0.2.7] — 2026-05-05

Security release: fixes and verifies 8 security findings.

### Security

- **Bearer token auth on HTTP endpoints** (AUTH-01/02) — all HTTP endpoints
  now require `Authorization: Bearer <token>` header. Token is generated
  on daemon startup and stored in `.dfmt/port` alongside the port number.
  Unauthenticated requests receive 401.
- **PATH prepend world-writable rejection** (CMDI-002) — `ValidatePathPrepend`
  now returns an error (not warning) when path_prepend entries are
  world-writable or non-existent. Daemon startup fails fast on invalid config.
- **Azure IMDS IP blocked** (SSRF-001) — `168.63.129.16` added to `isBlockedIP()`.
- **GCP metadata hostname blocklist expanded** (SSRF-002) — `metadata.goog.internal`
  and `metadata.goog.com` added alongside existing `metadata.google.internal`.
- **IPv6 AWS IMDS blocked** (SSRF-003) — `fd00:ec2::254` added to `isBlockedIP()`.
- **SSRF block logging** (SSRF-006) — blocked fetch attempts are now logged
  via `logging.Warnf` for operator visibility.
- **Dashboard CSP hardened** (XSS-01) — removed `unsafe-inline` from
  Content-Security-Policy header; replaced inline `style="display:none"`
  with class-based visibility toggling.

### Verified Protected (no action needed)

- CMDI-001/010 (shell chaining) — `hasShellChainOperators` + `splitByShellOperators`
- CMDI-003/004/009/01/02 — env var injection, LookPath, heredoc, here-string
- SSRF-005/007 — redirect metadata check, URL scheme enforcement
- RACE-01/02/03 — logger mutex, registry snapshot, FSWatcher recover

## [0.2.5] — 2026-05-03

Feature release: dashboard multi-project switching.

### Features

- **Dashboard multi-project dropdown** — the dashboard now shows all
  running daemon projects in a dropdown and lets you switch between them
  to view per-project stats. Two new HTTP endpoints support this:
  `/api/all-daemons` (returns unfiltered daemon registry) and
  `/api/proxy` (forwards requests to other daemons via HTTP, enabling
  the browser to talk to any daemon through the local relay without
  hitting same-origin restrictions).

### Internal

- `internal/transport/http.go` — added `handleAPIAllDaemons` and
  `handleAPIProxy` handlers
- `internal/transport/dashboard.go` — `loadStatsForProject()` and
  `projectSelect` change listener wired; `loadDaemons` now fetches
  `/api/all-daemons`

## [0.2.3] — 2026-05-02

Patch release: BatchExec stub now returns `ErrBatchExecNotImplemented`
instead of silently succeeding, preventing silent failures when batch
operations are called before the feature is implemented.

### Security

- **BatchExec stub returns error** — `BatchExec` in
  `internal/sandbox/permissions.go` now returns
  `ErrBatchExecNotImplemented` instead of `nil, nil`. Tests updated.
- **Write TOCTOU closed** — `safefs.WriteFile` now uses `O_NOFOLLOW`
  (Unix) / `FILE_FLAG_OPEN_REPARSE_POINT` (Windows) so a symlink at
  the leaf position is refused at open time, closing the residual
  TOCTOU window.
- **Panic recovery in long-running goroutines** — `consumeFSWatch`
  (all platforms), `journal.Append` scanner, and `daemon.idleMonitor`
  are now wrapped with `defer recover()` to prevent a single panic
  from terminating the daemon.
- **Doctor log-close errors now reported** — `runDoctor` surfaces
  file-close errors instead of silently suppressing them, fixing
  diagnosis on stale daemon paths.
- **stdlib CVEs patched** (GO-2026-4866/4870/4946/4947) — HTTP
  body lifecycle corrected in `Fetch`/`Exec`, `errors.Unwrap`
  chains added to JSON-RPC error responses, `RWMutex` write-skew
  fixed in handler stats cache, logging wrapper re-aligned.
- **Redaction dedup bypass closed** — `SetRedactor` now clears
  `dedupCache` / `sentCache` / `sentOrder` so a cached `content_id`
  never returns pre-redaction content under a changed redaction config.
- **LookPath cache staleness closed** — `Runtimes.Reload()` clears the
  binary-path cache and re-probes after a permitted exec may have
  mutated `PATH`.
- **Windows backslash normalization fixed** — `permissions.go` now
  replaces `\` with `/` for non-exec rules before regex matching.
- **Context leak in `tail --follow` closed** — cancel function properly
  captured and deferred in stream follow mode.
- **Redaction coverage expanded** — Azure storage account key pattern
  (`AccountKey=<86-char-base64>`) and GCP `client_email` JSON field
  matcher added to redaction patterns.

## [0.2.2] — 2026-05-01

Patch release. Config knob consolidation (ADR-0015 v0.4), metrics
instrumentation (ADR-0016/0017/0018), and operator override file
wiring (ADR-0014) land in this build. No wire-format changes.

### Added

- **`/metrics` Prometheus endpoint** — `GET /metrics` on the
  transport HTTP server emits in-tree Prometheus text format with
  gauges for index size, dedup-cache size, journal bytes, and
  tracked tool counts (ADR-0016).
- **Per-tool latency histograms** — `tool_call_duration_ms` per
  tool name, bucketed. `dfmt_stats` surfaces running totals
  (ADR-0018).
- **Per-tool call counters + dedup-hit counter** — `tool_calls_total`
  labelled by tool and outcome (success/allow/deny/error),
  `dedup_hits_total` for the content-stash dedup layer
  (ADR-0016 follow-up).
- **Journal `Size()` interface + `dfmt_journal_bytes` gauge** —
  `core.Journal` now exposes `Size() int64`; the daemon reports
  rotated-journal bytes to the `/metrics` endpoint (ADR-0017).

### Changed

- **Config knob wiring (ADR-0015 v0.4)** — the following
  previously-Reserved fields are now functional runtime gates:
  `transport.socket.enabled`, `logging.level`, `logging.format`,
  `retrieval.default_budget`, `retrieval.default_format`,
  `lifecycle.shutdown_timeout`, `index.bm25_k1`, `index.bm25_b`.
- **`.dfmt/redact.yaml` override wired** — operator-defined
  redact patterns are loaded and applied at daemon start
  (ADR-0014).
- **`.dfmt/permissions.yaml` override wired** — operator-defined
  exec allow rules are loaded and merged at daemon start,
  superseding defaults (ADR-0014).

### Fixed

- **Linux reserved-device rejection on Windows** —
  `safefs` now checks for Windows reserved device names
  (`CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`, case-insensitive)
  before `os.MkdirAll`, closing a path-confusion vector on
  cross-platform write paths.
- **glob regex precompilation** — `Rule` now compiles its glob
  pattern once at construction instead of on every match call,
  eliminating repeated regex compilation in hot sandbox paths.

### Internal

- **Linux race detector in CI** — `scripts/coverage-gate.sh`
  runs `go test -race` on Linux as a non-blocking report; any
  race reports are surfaced for developer follow-up.
- **Fuzz regression suite expanded** — BM25 search, HTML
  conversion, and glob matching now carry fuzz test coverage
  (Faz 4).
- **golangci-lint v2.4.0 → v2.11.4** — toolchain bump; closes
  130 lint findings across the tree.

## [0.2.1] — 2026-04-29

Patch release. The v0.2.0 binaries shipped before a Linux-only
security regression and a CI-toolchain mismatch were diagnosed
under WSL; v0.2.1 republishes the same feature set with both
closed. No wire-format or behavior changes for end users on
Windows or macOS — Linux operators should upgrade.

### Security

- **F-03 closure on Linux** —
  `internal/sandbox/permissions.go::globMatch` previously called
  `filepath.ToSlash` to normalise path separators before matching
  deny rules. `ToSlash` is a no-op on Unix because `\` is a valid
  filename byte there, so a Windows-shaped path like
  `C:\proj\.env` would slip past a `**/.env*` deny rule when the
  daemon ran on Linux/WSL — re-opening the same gap F-03 had
  closed for Windows hosts. Switched to a cross-platform
  `strings.ReplaceAll(text, "\\", "/")` so both axes are
  normalised regardless of host OS. Regression test:
  `internal/sandbox/sandbox_test.go::TestGlobMatch_NormalizesPathSeparatorsForAllPathOps`.

### Fixed

- **`internal/content/store_ttl_test.go::TestStore_PruneExpiredCountsDropped`** —
  live sets used `Created = now-1h` with `TTL = 1h`, putting their
  expiration at exactly `now`. On a fast Linux runner
  `PruneExpired` would observe `now+ε > expiry` and reap them too
  (dropped=5 instead of 3). Bumped the live-set TTL to 24 h.
- **`internal/sandbox/sandbox_test.go::TestSandboxEditReadOnly`** —
  on POSIX, `rename(2)` checks the parent directory's mode, not
  the target file's, so a `0o444` file inside a `0o755` parent is
  still atomically replaceable by its owner. The test now also
  locks the parent directory to `0o555` (and restores it in a
  `defer` so `t.TempDir` cleanup can remove it). Windows behavior
  is unchanged.
- **CI: golangci-lint v2.4.0 → v2.11.4** — v2.4.0 was built with
  go1.25 and panicked inside `go/types.(*Checker).initFiles` with
  `"file requires newer Go version go1.26 (application built with
  go1.25)"` once `setup-go@v5` started installing the go1.26
  toolchain. v2.11.4 is the first v2.x release built with go1.26.1
  and runs clean against this tree. The bump also turned on
  staticcheck QF1012; the 12 `WriteString(fmt.Sprintf(…))` sites in
  `cmd/dfmt-bench/tokensaving.go` were converted to
  `fmt.Fprintf(&builder, …)` (byte-identical bench output).

### Internal

- `internal/transport/http.go` — hoisted `runtime.GOOS == "windows"`
  literal into a `goosWindows` const (goconst threshold once Linux
  platform-only files compile in).
- `.golangci.yml` — added `internal/capture/fswatch*` exclusion
  for goconst (event-type literals `"create"` / `"modify"` /
  `"delete"` mirror inotify / `ReadDirectoryChangesW` operations
  and reading them inline is what one expects).

## [0.2.0] — 2026-04-29

First public release after the original v0.1.x prototype. Headline
work was a documentation/code consistency pass + a token-savings
hardening of the sandbox return path.

### Added

- **8-stage NormalizeOutput pipeline** for sandbox tool output —
  binary refusal, ANSI strip, CR-overwrite collapse, RLE,
  stack-trace path collapsing, git-diff `index` line drop,
  JSON/NDJSON/YAML noise compaction, Markdown frontmatter strip,
  HTML→markdown via the in-tree tokenizer (ADR-0008, ADR-0010).
- **Token-aware tier gating** (`ApproxTokens(s) =
  ascii_bytes/4 + non_ascii_runes`) replaces the byte-cinsinden
  tier check so CJK and Turkish bodies hit the same agent-cost
  thresholds as English (ADR-0012).
- **Cross-call wire dedup** — content stash keys on
  `sha256(kind, source, body)` for 30 s, and the MCP layer now
  tracks `content_id`s already emitted in this session and
  substitutes `(unchanged; same content_id)` on repeats
  (ADR-0009, ADR-0011).
- **`dfmt_search` excerpts** — each hit carries an opt-in
  `excerpt` field (≤ 80 bytes, rune-aligned) so agents get enough
  signal in a single round-trip to decide whether to follow up
  with `dfmt_recall`.
- **Journal event signing + verify-on-read** —
  `Event.ComputeSig()` runs on every `Append`, and
  `journal.Stream` / `scanLastID` re-verify with `Validate()` on
  every read. Tampered or mismatched lines are warn-and-skipped;
  legacy events (Sig == "") replay cleanly.
- **Expanded default exec allow-list** — `yarn`, `bun`, `npx`,
  `pnpx`, `bunx`, `deno`, `tsc`, `tsx`, `ts-node`, `vitest`,
  `jest`, `eslint`, `prettier`, `vite`, `next`, `webpack`,
  `make` (each pair: bare + `*` form). Removes the zero-config
  friction of having to drop a `permissions.yaml` override on
  every modern JS/TS project.
- **`.claude/settings.json` merge-safe auto-init** — the
  project-local Claude settings file is now structurally merged
  on every auto-init: pre-existing user keys
  (`mcp.callTimeoutMs`, `defaultMode`, `outputStyle`, custom
  permission entries) are preserved verbatim. Refuses to write
  under `$HOME/.claude/`.
- **Allow-rule trailing-space contract (V-20)** — exec allow
  rules ship as pairs (`<cmd>` + `<cmd> *`); the trailing
  space + `*` is the explicit end-of-token boundary so an entry
  like `git` no longer accidentally matches `git-shell` or
  `git-receive-pack`.
- **`docs/ROADMAP.md`**, **`SECURITY.md`**, **`CHANGELOG.md`**
  added; ARCHITECTURE.md synchronised with the post-V-20
  codebase.

### Changed

- **Three independent version strings consolidated** to a single
  source. `cmd/dfmt/version.go`'s `var version`,
  `internal/cli/Version`, and the literal `"0.1.0"` previously
  hard-coded into `transport/mcp.go::handleInitialize` are now
  all driven by `internal/version.Current`. Build sets the
  string via
  `-ldflags "-X github.com/ersinkoc/dfmt/internal/version.Current=$(VERSION)"`.
  `dfmt --version` and the MCP `serverInfo.version` returned by
  `initialize` now always agree.
- **CI: `golangci-lint` version pinned** in
  `.github/workflows/ci.yml` (was tracking `latest`). Lint rules
  no longer drift between PRs that don't change Go code.

### Fixed

- **V-9** — `journal.Stream` no longer silently drops malformed
  JSON lines; surfaces them as `journalWarnf` warnings with a
  snippet preview before skipping.
- **V-16 / V-17 / V-19** — transport API hygiene plus a Windows
  filesystem path case fix that affected project ID resolution
  on case-insensitive volumes.
- **V-18** — `index.gob`'s on-disk format documented as JSON
  payload (the `.gob` filename is retained for backwards
  compatibility with older daemons).
- **V-20** — exec allow-rule trailing-space contract above.

### Security

- **F-A-LOW-1** closure — operator-facing guidance for
  non-standard secret stores added to
  `internal/sandbox/permissions.go::DefaultPolicy()` doc
  comment.
- **gzip / file close on all paths** (commit 7fab730) — journal
  rotation now always closes the active file handle even on
  error paths; closes a handle leak that surfaced as
  `EBUSY`/`EIO` on Windows after long-running daemons.
- **Read-path event signature verification** (above under
  Added) — closes the in-place tampering blind spot called out
  in earlier audits.

### Known issues

These are documented stubs and unwired knobs that did not block
the v0.2.0 cut; they are tracked in `docs/ROADMAP.md` and slated
for v0.3.x:

- `storage.compress_rotated` config flag is wired through the
  option struct but the journal rotation path never invokes
  gzip; rotated `.jsonl.<ULID>.jsonl` segments stay plain JSONL.
- `index.heading_boost` config field is accepted and validated
  but not wired to any scoring path.
- `privacy.telemetry`, `privacy.remote_sync`,
  `privacy.allow_nonlocal_http` not wired — DFMT never
  phones home regardless.

[Unreleased]: https://github.com/ersinkoc/dfmt/compare/v0.7.2...HEAD
[0.7.2]: https://github.com/ersinkoc/dfmt/releases/tag/v0.7.2
[0.7.1]: https://github.com/ersinkoc/dfmt/releases/tag/v0.7.1
[0.7.0]: https://github.com/ersinkoc/dfmt/releases/tag/v0.7.0
[0.6.9]: https://github.com/ersinkoc/dfmt/releases/tag/v0.6.9
[0.2.8]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.8
[0.2.2]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.2
[0.2.1]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.1
[0.2.0]: https://github.com/ersinkoc/dfmt/releases/tag/v0.2.0
