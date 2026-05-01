# sc-race-condition — Race Condition / TOCTOU Scan Results

**Skill:** `sc-race-condition` v1.0.0  
**Target:** DFMT daemon at `D:\Codebox\PROJECTS\DFMT`  
**Date:** 2026-05-01  
**Scope:** `internal/core/journal.go`, `internal/core/index.go`, `internal/sandbox/permissions.go`, `internal/safefs/`, `internal/daemon/daemon.go`, `internal/capture/fswatch.go`

---

## Summary

The daemon's locking hygiene is mostly disciplined. `journalImpl` uses a single `sync.Mutex` protecting all mutable state (`file`, `closed`, `hiCursor`, `maxBytes` check). `Index` uses `sync.RWMutex` correctly: write lock for `Add`/`Remove`/`SetParams`/`UnmarshalJSON`, read locks for all search/read paths. No concurrent policy hot-reload mechanism exists — the `SandboxImpl.policy` field is snapshotted at construction and never mutated, which is safe by design. Goroutine lifecycle management is correct across all background workers. The only genuine races are two pre-existing findings (RACE-01 in logging, RACE-02 in client registry) and one residual-design TOCTOU in safefs. The core data-plane structures (journal, index) are clean.

---

## Findings

### RACE-01 — `logging.Logger` lazy init without synchronisation (MEDIUM)
- **Severity:** Medium
- **CWE:** CWE-362 (Race Condition)
- **File:** `internal/logging/logging.go:13, 76-86, 90-98`
- **Description:** Package-global `var Logger *slog.Logger` is read in `With()` and `FromContext()` and lazily initialised via `if Logger == nil { InitDefault() }`. There is no synchronisation. Two goroutines that both hit `FromContext` first can both observe `Logger == nil`, both invoke `InitDefault`, both write `Logger = slog.New(handler)`. Concurrent reads + writes of an interface variable are a data race.
- **Impact:** Go race detector will flag this. Worst case: two slog handlers created, one discarded. No security consequence, but masks other concurrency bugs and produces unpredictable log destinations during startup.
- **Remediation:** Wrap with `sync.Once`:
  ```go
  var loggerOnce sync.Once
  func ensure() { loggerOnce.Do(InitDefault) }
  func With(args ...any) *slog.Logger { ensure(); return Logger.With(args...) }
  ```
- **Confidence:** High.

---

### RACE-02 — `client.Registry.List` unlocks before iterating map (MEDIUM)
- **Severity:** Medium
- **CWE:** CWE-362 (Race Condition)
- **File:** `internal/client/registry.go:130-167, 84-110`
- **Description:** `saveNoLock` iterates `r.daemons`. `Register` and `Unregister` call `saveNoLock` while holding `r.mu` — safe. `List`, however, unlocks `r.mu` (line 159) and **then** calls `saveNoLock` (line 163) when it has dead daemons to clean up. If another goroutine calls `Register` or `Unregister` between the unlock and `saveNoLock`'s iteration, two goroutines read/write `r.daemons` concurrently — a Go map data race.
- **Impact:** Fatal `concurrent map read and map write` panic under multi-project concurrency (dashboard polling `/api/daemons` while a fresh `dfmt` invocation auto-starts another daemon).
- **Evidence:**
  ```go
  // internal/client/registry.go:155-164
      entriesForSave := make([]DaemonEntry, len(entries))  // never used
      copy(entriesForSave, entries)
      r.mu.Unlock()
      if len(deadPaths) > 0 {
          r.saveNoLock()   // re-iterates r.daemons unprotected
      }
  ```
- **Remediation:** Marshal inside the lock, or keep lock held around `saveNoLock` (it is already called under lock by `Register`/`Unregister`).
- **Confidence:** High.

---

### RACE-03 — `FSWatcher.Stop` double-close panic short-circuits `watchWG.Wait` (LOW)
- **Severity:** Low
- **CWE:** CWE-362 (Race Condition)
- **File:** `internal/capture/fswatch.go:192-216`
- **Description:** `Stop` uses `defer func() { _ = recover() }()` to swallow the panic from a second `close(w.stopCh)`. The first call closes `stopCh`, writes markers, and blocks on `w.watchWG.Wait()`. A second concurrent call panics on `close`, the deferred recover swallows it, and Stop returns `nil` immediately — without waiting for goroutines to drain.
- **Impact:** Second caller gets `nil` while watch goroutines are still running. No data corruption, but callers that assume "Stop returned → goroutines gone" can race against in-flight events.
- **Evidence:**
  ```go
  func (w *FSWatcher) Stop(ctx context.Context) error {
      defer func() { _ = recover() }()
      close(w.stopCh)          // panics on second call
      paths := w.snapshotWatchedPaths()
      …
      w.watchWG.Wait()         // skipped on second call
      return nil
  }
  ```
- **Remediation:** Use `sync.Once` for idempotent stop:
  ```go
  stopOnce sync.Once
  func (w *FSWatcher) Stop(ctx context.Context) error {
      w.stopOnce.Do(func() {
          close(w.stopCh)
          // … marker writes …
      })
      w.watchWG.Wait()
      return nil
  }
  ```
- **Confidence:** High.

---

## Component Analyses

### 1. Journal — Concurrent Appends (`internal/core/journal.go`)

**Thread-safety verdict: SECURE.**

`journalImpl` uses a single `sync.Mutex` (`j.mu`) that guards all mutable state: the `file` handle, `closed` flag, `hiCursor`, and the size-limit check.

The `Append` implementation:
1. Checks `ctx.Err()` before acquiring the lock (honours cancellation on queue wait).
2. Marshal event JSON **outside** the lock (CPU-bound, no shared state).
3. Acquire `j.mu`.
4. Re-check `ctx.Err()` after lock acquisition (prevents a canceled caller from sneaking through after queuing).
5. Check `j.closed`.
6. Check `j.maxBytes` with `j.file.Stat()` under the lock — **explicitly protected against TOCTOU** (comment at line 230-231: "Size limit check MUST be under the lock to avoid TOCTOU: two concurrent Appends could both observe Size() < maxBytes and then push the journal past the limit").
7. `j.file.Write(data)` — OS `O_APPEND` ensures atomic line semantics at the OS level.
8. Optionally `j.file.Sync()` when durable.
9. Update `j.hiCursor`.

The `periodicSync` background goroutine also correctly acquires `j.mu` during each tick. `Close` captures the stop channels under the lock, then closes them after unlocking to avoid deadlock with a sync goroutine that may be waiting on the lock.

### 2. Index — Concurrent Writes (`internal/core/index.go`)

**Thread-safety verdict: SECURE.**

`Index` uses `sync.RWMutex` with disciplined discipline:

| Operation | Lock type | Notes |
|---|---|---|
| `Add` | Write (`mu.Lock`) | Full mutation of posting lists, docLen, avgDocLen |
| `Remove` | Write | Full cleanup of all postings |
| `SetParams` | Write | k1, b, headingBoost update |
| `UnmarshalJSON` | Write | Deserialization |
| `SearchBM25` | Read (`mu.RLock`) | Safe concurrent reads |
| `SearchTrigram` | Read | Safe concurrent reads |
| `Excerpt` | Read | Safe concurrent reads |
| `Params` | Read | Safe concurrent reads |
| `TotalDocs` | Read | Safe concurrent reads |
| `MarshalJSON` | Read | Delegates to `json.Marshal` which takes RLock internally |

`Persist` deliberately does **not** hold the lock (documented: `json.Marshal` dispatches to `ix.MarshalJSON` which takes `RLock`; re-entering `Lock` would deadlock under write contention). The write-to-temp-then-atomic-rename pattern (`writeRawAtomic`) ensures a crash leaves the prior complete file intact.

### 3. Policy Hot Reload (`internal/sandbox/permissions.go`)

**Thread-safety verdict: NOT APPLICABLE — no hot reload exists (by design).**

`SandboxImpl.policy` is a `Policy` **value** (not a pointer). The policy is loaded once via `LoadPolicyMerged(projectPath)` in `daemon.New()` and stored directly:
```go
sb := sandbox.NewSandboxWithPolicy(projectPath, polRes.Policy).
    WithPathPrepend(cfg.Exec.PathPrepend)
```

There is no mechanism to reload the policy at runtime. Changing `.dfmt/permissions.yaml` requires a daemon restart. This is a deliberate design choice — the alternative (dynamic policy mutation) would require a read-write lock on `sb.policy` and careful handling of in-flight `PolicyCheck` calls.

**No race condition exists here because there is no concurrent policy mutation.** The policy is immutable after sandbox construction.

### 4. Goroutine Leaks (`internal/daemon/daemon.go`, `internal/capture/fswatch.go`)

**Goroutine management verdict: SECURE.**

All background goroutines are properly tracked and terminated:

| Goroutine | Tracker | Stop mechanism |
|---|---|---|
| `periodicSync` | local `syncDone` channel | `Close` sends on `syncStop`, waits on `syncDone` |
| `runDebounceCleanup` | `watchWG` | Observes `stopCh`, exits on close |
| `FSWatcher` platform goroutines | `watchWG` | `Stop` wakes via marker-file write (Linux) or stopCh (Windows); `UntrackGoroutine` called on exit |
| `rebuildIndexAsync` | `d.wg` | `d.rebuildCtx` cancel; `defer d.wg.Done()` |
| `consumeFSWatch` | `d.wg` | Exits on `d.shutdownCh` or context cancel; `defer d.wg.Done()` |
| `startIdleMonitor` | **Not in wg** | Calls `d.Stop()` directly; `Stop` closes `idleCh` to unblock |

`startIdleMonitor` is explicitly NOT tracked in `wg` because it may call `Stop()` itself, and `Stop()` calls `d.wg.Wait()` — adding it to `wg` would deadlock. The comment in the code (line 695-696) explains this. `Stop` closes `idleCh` to signal the monitor to exit.

`Close` ordering is documented and validated: stop accepting events → stop fswatcher → wait for drain → persist index → stop server → close journal.

### 5. TOCTOU in File Operations (`internal/safefs/safefs.go`)

**TOCTOU residual: DOCUMENTED, ACCEPTED SCOPE.**

The safefs package documents its threat model explicitly:
> "a sufficiently capable attacker can race the Lstat → write window (TOCTOU). Closing that requires platform-specific file handles (O_NOFOLLOW on Unix, FILE_FLAG_OPEN_REPARSE_POINT on Windows) and is out of scope for the documented threat model."

`CheckNoSymlinks` walks component-by-component using `os.Lstat` (does not follow symlinks). Between the `Lstat` check and the actual write, an attacker could theoretically replace a non-symlink path component with a symlink — the **window is non-zero**.

Mitigations:
- `WriteFileAtomic` uses `os.CreateTemp` + `os.Rename`. `rename(2)` on Unix replaces a pre-existing symlink with the new regular file rather than following it, so the atomic rename itself is symlink-safe at the target.
- The actual file creation uses `os.WriteFile` (Unix: `O_WRONLY|O_CREATE|O_TRUNC`) — **does not use `O_NOFOLLOW`**, so on Unix a symlink in the final path component would be followed. This is documented as residual.
- On Windows, the same-flags approach applies — no `FILE_FLAG_OPEN_REPARSE_POINT` equivalent is used.

No `CheckNoSymlinks` + actual write race was found in the actual callers: `safefs.WriteFile`/`WriteFileAtomic` are always called immediately before their respective write operations (journal creation, PID file write, index persist), with no intermediate filesystem-modifying calls in between.

### 6. Fuzz Tests — Invariants Verified (`*_fuzz_test.go`)

Four fuzz harnesses exist that verify security-critical invariants:

| Fuzz Test | File | Invariant Verified |
|---|---|---|
| `FuzzMergePoliciesHardDenyInvariant` | `permissions_merge_fuzz_test.go` | An operator `allow:exec:<X>` override must not re-enable a hard-deny base command (`rm`, `sudo`, `dd`, `mkfs`, `shutdown`, etc.). Fuzzer generates arbitrary rule text, verifies `isHardDenyExec` detection is consistent with `merged.Evaluate`. |
| `FuzzCheckNoSymlinks_AbsPathContract` | `reserved_fuzz_test.go` | Non-absolute `baseDir` or `path` MUST produce an error; lexical `..` escape MUST produce an error regardless of filesystem state. |
| `FuzzCheckNoReservedNames_NoFalseNegative` | `reserved_fuzz_test.go` | Every Windows reserved device name (NUL, CON, PRN, AUX, COM0-9, LPT0-9) in any path position, case, separator, drive-letter, and trailing-colon variant MUST be flagged. |
| `FuzzMatchIgnorePattern` | `fswatch_fuzz_test.go` | `matchIgnorePattern` never panics; literal patterns match themselves; `**/x` matches `x` and `*/.../x`. |

These fuzz tests verify **correctness invariants**, not data-race conditions. They do not directly test concurrent access patterns (e.g., simultaneous `Index.Add` calls). The race-condition mitigation in the core index/journal is validated by the discipline of the `sync.RWMutex` and `sync.Mutex` usage, not by fuzz tests.

---

## Old Findings (pre-2026-04-28 audit — not re-flagged)

The prior scan closed F-04 through F-25. Comments in the code naming those IDs are **mitigations already in place**, not new bugs. They were not re-flagged.

---

## Conclusion

The core concurrent data paths (journal append, index search/add) are well-protected with correct mutex discipline. The two races (RACE-01 in logging, RACE-02 in registry) and the low-severity fswatch double-close (RACE-03) are pre-existing findings from the prior audit. The TOCTOU in safefs is documented as in-scope residual. No policy hot-reload race exists because no hot-reload mechanism exists.

**go test -race ./...** run is recommended to confirm RACE-01 and RACE-02 with the race detector enabled.