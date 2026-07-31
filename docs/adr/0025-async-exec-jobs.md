# ADR-0025: Async Exec Jobs

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-07-31 |
| Deciders | Ersin Koç |
| Related | [ADR-0023](0023-real-deadlines-concurrent-proxy-and-rpc-auth.md), [ADR-0024](0024-retention-ceilings-and-line-oriented-reads.md) |

## Context

ADR-0024 raised `MaxExecTimeout` to 900 s and named what it did not solve:

> a job that outlives even this — a long migration, a soak test — needs an async job handle (submit, poll, fetch output), which is a new MCP tool, a job store, and a cancellation story. That is a feature with its own ADR, not a constant.

This is that ADR. The pressure is real and does not go away by choosing a larger number: a watch build never ends, a data migration runs for an hour, a soak test is measured in hours by definition. Every synchronous ceiling is simultaneously too small for that work and too large as a bound on a command that has hung — the same constant cannot do both jobs.

There is also a cost the ceiling hides. A synchronous call holds an RPC open, an exec slot, and — before ADR-0023's concurrent dispatch — the agent's entire MCP session. Work that takes an hour should not hold any of those.

## Decision

Async is **parameters on `dfmt_exec`**, not a new tool:

```
{code}                      run it, answer when it finishes
{code, async:true}          submit it, answer with a job_id now
{job_id}                    status, or the finished result
{job_id, cancel:true}       stop it
```

A separate `dfmt_job` tool was the obvious alternative and is worse on both axes that matter here. On cost: `tools/list` ships on every session start and is byte-budgeted, and a second tool object pays for its own name, description, and schema envelope before it describes anything. On coherence: "run a command" is one capability whose result the caller may want now or later — splitting it would mean an agent has to know two tools to use one capability.

### What the job store is, and is not

**Process-local, not persisted.** A daemon restart loses the table. Persisting the *record* while the subprocess died with its parent would be a handle that resolves to nothing — worse than an honest `unknown job_id`, which is what a caller now gets.

**Bounded, and it refuses rather than evicts.** 32 outstanding jobs, finished ones retained 30 minutes. Past the cap a submission is refused: an agent holding 32 forgotten jobs has a bug, and silently dropping a *running* one to make room would hide it behind a result that never arrives.

**Its own semaphore.** Async work runs at concurrency 2, separate from the interactive `execSem` (4). Sharing would let two hour-long jobs occupy half the interactive capacity, so a `git status` would queue behind a migration. Two pools, two purposes.

**A detached context.** The worker cannot inherit the submitting request's context — that context is done milliseconds later, when the submission returns, which would cancel the job at birth. It gets a fresh one carrying the project and session identity, so redaction and wire-dedup resolve for the job exactly as they did for its submitter.

### Ceilings

Three now exist, and they are different questions:

| Constant | Value | Question it answers |
| --- | --- | --- |
| `sandbox.DefaultExecTimeout` | 60 s | how long an unspecified command may take |
| `sandbox.MaxExecTimeout` | 900 s | how long something may hold an RPC and an exec slot |
| `transport.MaxAsyncExecTimeout` | 2 h | how long a job that holds neither may run |
| `sandbox.HardMaxExecTimeout` | 2 h | the sandbox's own backstop under all of the above |

The last one is new, and it existed as a bug before it existed as a constant: `execImpl` clamped every call to `MaxExecTimeout`, so a two-hour job context was silently cut to fifteen minutes and the feature quietly did not do the one thing it exists for. Policy per call shape belongs to the transport; the sandbox enforces only the absolute bound.

### Cancellation

`cancel:true` cancels the job's context, which is precisely what a timeout does — so it goes through ADR-0023's tree-kill and stops the whole process group, not just the shell. Verified end to end: a canceled `sleep 120; echo … > file` left no file.

The cancel call waits up to 6 s for the worker to record its terminal state, so one call answers with `canceled` rather than `running`. That bound is not arbitrary — it covers the sandbox's own kill grace (`execWaitDelay`, 2 s) plus the normalize-and-journal tail. Against a real daemon a 2 s wait was consistently too short, and a cancel that reports "running" reads as a cancel that did nothing.

A canceled job reports no `error` text. "context canceled" tells the caller nothing its status does not already say, and reads like a defect in DFMT rather than the stop they asked for.

## Consequences

`tools/list` grows by ~420 bytes and the budget test's constant moves from 6 KiB to 6.75 KiB — the first deliberate raise. The rule it encodes is unchanged: raise for a new capability, never to absorb prose drift. Every other addition in this release (`replace_all`, search's `type` filter, the tag vocabulary, line-oriented read) was paid for by trimming elsewhere.

Daemon shutdown cancels running jobs (`Handlers.StopJobs`, wired into `Daemon.Stop`). A job surviving its daemon would be a subprocess nobody owns and nobody can poll.

The wire is backward compatible: a caller that never sets `async` or `job_id` sees exactly the old shape, and `status`/`job_id` are `omitempty`, so a synchronous response does not carry the async vocabulary at all.

Two things deliberately left for later, since neither blocks the use case:

- **No output streaming.** A poll returns the finished result, not a tail of a running job. Streaming means either an SSE surface per job or a cursor into a growing buffer; the work this feature targets is checked on completion, not watched.
- **No cross-daemon or cross-session job discovery.** Jobs belong to the daemon that ran them, and an agent that loses its `job_id` loses the handle. A `list jobs` operation would be the natural addition if that turns out to bite.
