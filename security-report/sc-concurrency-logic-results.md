# SC HUNT Results: Concurrency / Business Logic / Privilege

Scope: race conditions / TOCTOU (CWE-362, CWE-367), business logic flaws,
privilege escalation in setup (CWE-269), authorization between per-project
daemons (CWE-285), JSON-RPC mass assignment (CWE-915).

Methodology: native Read of every file in scope plus targeted Grep for
os.OpenFile, os.Symlink, filepath.EvalSymlinks, os.Lstat, net.Listen,
os.Chmod, os.WriteFile, os.Rename, O_APPEND, and shared-state mutations
across goroutines.

Findings are listed by severity. Each carries a CWE, file:line, root-cause
summary, exploit sketch, and a fix proposal.

---

## H-01 (HIGH) — Sandbox Write/Edit symlink check skipped on non-existent target -> arbitrary file write outside project

- CWE: CWE-59 (Link Following) / CWE-367 (TOCTOU)
- File: internal/sandbox/permissions.go:1432-1442 (Write),
  internal/sandbox/permissions.go:1352-1361 (Edit)
- Status: the comment on the Write check explicitly claims the parent
  directory is checked when the file doesn't exist yet — it isn't.

Code:

```go
// Resolve symlinks and re-check containment. A symlink inside the wd that
// points outside (e.g. ln -s /etc/passwd wd/leak) must be refused, even if
// the file doesn't exist yet — we check the parent directory's target.
if resolved, rerr := filepath.EvalSymlinks(cleanPath); rerr == nil {
    ...containment check...
}
// If EvalSymlinks errors (file doesn't exist), the entire check is
// skipped. The parent is never resolved.
```

Exploit (Linux, agent inside the sandbox):

1. Agent uses an allowed exec, e.g. dfmt_exec("ln -s /etc subdir"),
   creating a symlink <wd>/subdir -> /etc.
2. Agent calls dfmt_write({path:"subdir/cron.d/evil", content:"..."}).
3. cleanPath = <wd>/subdir/cron.d/evil. filepath.Rel(absWd, cleanPath)
   is subdir/cron.d/evil — passes the lexical containment check.
4. EvalSymlinks(cleanPath) returns ENOENT (cron.d/evil doesn't exist
   yet); the resolved-containment block is skipped.
5. os.MkdirAll(filepath.Dir(cleanPath), 0o700) traverses the symlink and
   creates /etc/cron.d/. os.WriteFile writes to /etc/cron.d/evil.
6. Privilege escalation if dfmt was run as root (uncommon) or any
   automation reads from project paths trusting them (common on dev hosts).

The same hole applies to Edit (line 1352) — but Edit additionally calls
os.ReadFile first, which would fail for a non-existent target, so Edit
is reachable only if the attacker can pre-populate the target. Write is
the cleaner exploit.

Fix: Walk the path component-by-component using os.Lstat from absWd
downward; refuse any segment that is a symlink whose target leaves
resolvedWd. Or, on Linux, open with O_NOFOLLOW after openat from a
pinned wd fd.

---

## H-02 (HIGH) — Daemon Unix socket bound without umask wrapper; brief world-permissive window before chmod

- CWE: CWE-362 (Race Condition), CWE-276 (Incorrect Default Permissions)
- File: internal/daemon/daemon.go:160-168

Code:

```go
socketPath := project.SocketPath(projectPath)
ln, err := net.Listen("unix", socketPath)            // no umask
if err != nil { return nil, ... }
os.Chmod(socketPath, 0700)                            // error discarded
```

The fix that exists for the legacy transport.SocketServer
(internal/transport/socket.go:53 calls listenUnixSocket which sets umask
0o077) was never applied to the daemon's own listener. The daemon path
is the actual listener in production — the legacy server is only used
by some tests. Result: the socket lives at the umask-default mode
(commonly 0o755 -> socket file is 0o755) for the few microseconds
between Listen and Chmod. A racing local user (e.g. on a multi-tenant
CI host or shared dev box) can connect(2) during the window and reach
the daemon's RPC endpoint with the dfmt user's privileges (Read, Write,
Edit, Exec — arbitrary code execution on the dfmt user's behalf).

The os.Chmod error is also silently dropped — a chmod failure (NFS,
exotic FS, container quirks) leaves the socket world-accessible
indefinitely with no warning.

Fix: Use transport.listenUnixSocket(socketPath) (which already exists
with the right umask) from daemon.New() instead of bare net.Listen, and
propagate the chmod error.

---

## H-03 (HIGH) — Daemon socket path collision via SHA-256 truncation to 64 bits + /tmp squat

- CWE: CWE-330 (Use of Insufficiently Random Values) / CWE-285 (Improper
  Authorization)
- File: internal/project/discover.go:67-75

Code:

```go
func SocketPath(projectPath string) string {
    full := filepath.Join(projectPath, ".dfmt", "daemon.sock")
    if len(full) <= 100 {
        return full
    }
    h := sha256.Sum256([]byte(projectPath))
    return filepath.Join(os.TempDir(), "dfmt-"+hex.EncodeToString(h[:8])+".sock")
}
```

For "long" project paths the socket falls back to
/tmp/dfmt-<16-hex>.sock — only 64 bits of entropy. With many projects on
the same host, collisions become probable; with adversarial projects
(an attacker creating projects whose paths hash to the prefix of a
target), grinding a 64-bit collision is feasible on commodity hardware.

Worse, /tmp is a shared directory: a malicious local user can pre-create
/tmp/dfmt-<predicted-16-hex>.sock as a symlink (or just listen on it) to
intercept the legitimate daemon's first dial. Once the agent connects,
all subsequent journal writes, recall snapshots, and sandbox results
flow through the attacker's process.

The lock file (.dfmt/lock) lives inside the project, so it does NOT
prevent another user's daemon from holding /tmp/dfmt-<hash>.sock.

Fix: (a) Use full 256-bit hash. (b) Place the fallback socket inside
the user's runtime dir ($XDG_RUNTIME_DIR, mode 0700) instead of /tmp.
(c) os.Remove the socket path before bind to clear any squat after
acquiring the project lock.

---

## H-04 (HIGH) — Setup BackupFile and PatchClaudeCodeUserJSON follow symlinks on backup write

- CWE: CWE-59 (Link Following), CWE-269 (Improper Privilege Management)
- Files:
  - internal/setup/setup.go:324-347 (BackupFile)
  - internal/setup/claude.go:159-169 (backup of ~/.claude.json)

Code:

```go
// setup.go
return os.WriteFile(backup, data, mode)         // follows symlinks

// claude.go
if werr := os.WriteFile(backupPath, raw, 0600); werr != nil {
    return fmt.Errorf("write backup %s: %w", backupPath, werr)
}
```

The write of the modified claude.json is atomic via tmp+rename
(line 176-198) — that part is fine; rename(2) on Linux replaces the
symlink, not its target. But the pristine backup (line 163) and every
agent-config backup (BackupFile) use os.WriteFile, which follows
symlinks (it opens with O_TRUNC|O_WRONLY|O_CREATE).

Exploit: A local attacker who can write to the user's home (e.g.
compromised package post-install script, malicious npm dep) plants

```
ln -s ~/.bashrc ~/.claude.json.dfmt.bak
```

Next time the user runs dfmt setup, the contents of the prior
~/.claude.json are written through the symlink and overwrite ~/.bashrc
— giving the attacker the ability to inject arbitrary bash that runs at
every login.

The same pattern applies to every *.dfmt.bak written via BackupFile
across all configured agents (Codex, Cursor, VS Code, Gemini, etc.).

The post-check at claude.go:162-167 (os.Stat(backupPath); os.IsNotExist)
is a TOCTOU window: it checks whether the backup exists, then writes —
the attacker can race a symlink-creation between the stat and the write.

Fix: Open the backup with
os.OpenFile(backup, O_WRONLY|O_CREATE|O_EXCL, mode), which fails if the
file already exists or is a symlink; if existing, os.Lstat and refuse if
it's a symlink. Even better: write to a temp file in the same dir and
rename, matching the strategy for the main config file.

---

## H-05 (HIGH) — Daemon daemon.pid write follows symlinks; PID-file pre-plant attack

- CWE: CWE-59 (Link Following)
- File: internal/daemon/daemon.go:246-250

Code:

```go
pidPath := filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
pidData := fmt.Sprintf("%d\n", os.Getpid())
if err := os.WriteFile(pidPath, []byte(pidData), 0o600); err != nil { ... }
```

Mirrors H-04. If an attacker can pre-create .dfmt/daemon.pid as a
symlink to a file the user has write access to (e.g. ~/.profile), the
daemon overwrites that file with the daemon PID on every startup.
Because .dfmt/ is typically inside the project (often a git repo where
several users may share write access on CI runners or umask 002 setups),
the attack surface is real.

Fix: os.OpenFile(pidPath, O_WRONLY|O_CREATE|O_EXCL|O_TRUNC, 0600) after
a Lstat rejection of symlinks; or use O_NOFOLLOW on Unix.

---

## H-06 (HIGH) — Sandbox glob/grep do NOT recheck symlinks after filepath.Glob resolution

- CWE: CWE-22 (Path Traversal)
- File: internal/sandbox/permissions.go:1095-1106 (Glob),
  internal/sandbox/permissions.go:1209-1244 (Grep)

Code (Glob):

```go
matches, err := filepath.Glob(pattern)         // pattern is in absWd
...
for _, m := range matches {
    fi, err := os.Stat(m)                      // follows symlinks
    if err != nil { continue }
    if !fi.IsDir() {
        relPath, _ := filepath.Rel(absWd, m)
        files = append(files, relPath)         // no symlink recheck
    }
}
```

filepath.Glob returns matches resolved as path strings — but if any
match is a symlink to outside absWd, the function happily lists it. A
subsequent Read of that path would catch the symlink (H-01 aside), but
the content read inline by Glob's intent matcher at line 1117-1122 does
NOT:

```go
data, err := os.ReadFile(fullPath)             // follows symlinks
```

So an agent can do:

```
dfmt_exec("ln -s /etc/passwd inproject_passwd")
dfmt_glob({pattern:"inproject_*", intent:"users root daemon"})
```

-> Glob reads /etc/passwd and returns matched lines (filtered through
intent). Same hole in Grep at line 1225 (os.ReadFile(f)).

Fix: After globbing, os.Lstat each match; reject any that are symlinks,
or re-resolve and recheck containment as Read does.

---

## M-07 (MEDIUM) — runInstallHooks does not handle .git as a file (worktree / submodule case) and follows .git/hooks symlinks

- CWE: CWE-59 / CWE-754 (Improper Check for Unusual or Exceptional
  Conditions)
- File: internal/cli/dispatch.go:1269-1301

Code:

```go
hooksDir := filepath.Join(proj, ".git", "hooks")
if err := os.MkdirAll(hooksDir, 0755); err != nil { ... }
...
dst := filepath.Join(hooksDir, hook)
if err := os.WriteFile(dst, []byte(content), 0755); err != nil { ... }
```

Two problems:

1. Worktree / submodule: in a git worktree, .git is a file whose
   contents are "gitdir: /path/to/real/dotgit". os.MkdirAll returns
   ENOTDIR and the install fails silently; the user thinks hooks were
   installed but git never sees them. (Logic bug; not exploitable.)

2. Symlink in .git/hooks: if .git/hooks is a symlink to outside the
   project (some users symlink hooks across repos), os.WriteFile follows
   it and writes three new executables (mode 0755) wherever the link
   points. If that destination is on the user's $PATH (~/.local/bin/,
   ~/bin/), an attacker who can pre-plant the symlink later runs the
   post-commit script content — which the dfmt binary itself defines,
   but which the attacker can swap by being able to control .git/hooks
   at all.

The recent commit 1d8a26b claimed to fix "settings hook cleanup" and
"write symlink gap" but only addressed the sandbox layer. The
install-hooks command was not updated.

Fix: os.Lstat(hooksDir); if it's a symlink, refuse with a clear error.
Parse .git as a gitfile when it is a regular file and use the resolved
gitdir. os.OpenFile(dst, O_WRONLY|O_CREATE|O_EXCL|O_TRUNC, 0755).

---

## M-08 (MEDIUM) — Concurrent dfmt mcp instances on the same project race on journal/index without daemon lock

- CWE: CWE-362 (Concurrent Execution Using Shared Resource with Improper
  Synchronization)
- File: internal/cli/dispatch.go:1912-2014 (runMCP)

runMCP is the stdio MCP server spawned directly by Claude Code et al.
It does NOT acquire the project's singleton lock. Two MCP processes on
the same project (two Claude Code windows, or Cursor + Claude Code)
both:

- OpenJournal the same file (O_RDWR|O_APPEND). POSIX guarantees each
  write(2) is atomic if it fits in PIPE_BUF (4 KiB on Linux), but
  serialized JSONL events can be longer. Two processes appending
  concurrently can interleave bytes and produce a corrupt line. The
  scanner's "skip malformed" path swallows it silently.
- LoadIndexWithCursor the same .gob. Both rebuild from journal, both
  write to index.gob.tmp-*, both rename. Last writer wins. Events
  ingested by the loser are journaled but not persisted to the cursor;
  on next restart the rebuild path re-reads them from journal, so this
  is mostly self-healing — but during shutdown the persist call uses
  journal.Checkpoint whose hiCursor may not include events appended by
  the other MCP process, so the persisted cursor lies about which
  events are indexed (the same scenario the daemon's skipPersist guard
  at daemon.go:378 was added to prevent — for daemon mode only).
- Both register in ~/.dfmt/daemons.json with the same project path.
  Registry.Register overwrites the entry; daemons.json is written via
  os.WriteFile (no temp+rename, no lock). Concurrent writes can produce
  a truncated JSON file -> all clients fail dial via /api/daemons
  filter at http.go:642.

Fix: runMCP should acquire the project lock (AcquireLock) and become
the authoritative process for that project; subsequent MCP launches
must either wait or proxy through the existing one.

---

## M-09 (MEDIUM) — ~/.dfmt/daemons.json written non-atomically; concurrent registration corrupts registry

- CWE: CWE-362
- File: internal/client/registry.go:84-105 (saveNoLock)

```go
_ = os.WriteFile(r.filePath, data, 0o600)
```

os.WriteFile opens with O_TRUNC|O_WRONLY|O_CREATE: it truncates first,
then writes. A second daemon registering at the same time may read the
truncated 0-byte file (returning empty registry), or two daemons can
both produce overlapping data on the disk.

Same write also follows symlinks (~/.dfmt/daemons.json -> /etc/sudoers
etc.) and is not protected by a file lock.

Fix: Write to daemons.json.tmp + os.Rename; acquire a flock on a sibling
.lock for the read-modify-write window.

---

## M-10 (MEDIUM) — Daemon socket bind in New() does not remove stale socket; crashed daemon causes self-DoS

- CWE: CWE-754 (Improper Check)
- File: internal/daemon/daemon.go:162

```go
ln, err := net.Listen("unix", socketPath)
```

If the previous daemon crashed (SIGKILL, OOM, power loss) the socket
file persists. net.Listen returns EADDRINUSE, New() errors out before
Start() runs, so the lock is never acquired and never released either.
The user sees "create socket listener: address already in use" until
they manually rm the socket.

transport.SocketServer.Start (line 49) does call os.Remove(s.path)
before Listen, but the daemon path bypasses that and uses
NewHTTPServerWithListener with a pre-bound listener.

The lock file SHOULD be the synchronization point — acquire lock first,
then if we got the lock, no live daemon owns the socket so it's safe to
unlink and rebind. Currently the order is reversed.

Fix: Move AcquireLock from Start() into New() BEFORE the socket bind,
then os.Remove(socketPath) (now safely held under the project lock)
before net.Listen.

---

## M-11 (MEDIUM) — JSON-RPC params accept arbitrary fields (no DisallowUnknownFields); allows source/actor spoofing in RememberParams

- CWE: CWE-915 (Improperly Controlled Modification of Dynamically-
  Determined Object Attributes)
- File: internal/transport/handlers.go:225-313 (RememberParams),
  internal/transport/socket.go:228-233 (decodeParams),
  internal/transport/http.go:383-395 (decodeRPCParams)

Code:

```go
type RememberParams struct {
    Type     string         `json:"type"`
    Priority string         `json:"priority"`
    Source   string         `json:"source"`     // agent-controlled
    Actor    string         `json:"actor,omitempty"`
    Data     map[string]any `json:"data,omitempty"`
    ...
}
```

Neither decodeParams nor decodeRPCParams calls
json.Decoder.DisallowUnknownFields. Agents can pass arbitrary fields
silently (no harm today since the struct only Unmarshals declared
fields), but more importantly:

The handler at handlers.go:283-294 builds the core.Event directly from
these params:

```go
e := core.Event{
    ...
    Source: core.Source(params.Source),     // free-form string
    Actor:  params.Actor,
    ...
}
```

An agent calling dfmt.remember over MCP can claim source: "githook",
priority: "p1", type git.commit — landing in the journal indistinguish-
able from real git hooks. The Recall snapshot is priority-sorted, so a
crafted P1 "decision" event will displace genuine higher-priority events
from the budget-bounded snapshot. This is prompt-injection
amplification: the agent can write into its own future memory at any
priority tier it chooses, and re-ingestion on the next session treats
those events as authoritative.

Fix: Restrict Source to "mcp" for handler-originated events; ignore the
client's value. Restrict Priority to a server-validated whitelist
(reject anything other than p1..p4, and reject p1 from MCP entirely —
let the classifier promote events).

---

## M-12 (MEDIUM) — Discover honors $DFMT_PROJECT without containment check

- CWE: CWE-20 (Improper Input Validation), CWE-426 (Untrusted Search
  Path)
- File: internal/project/discover.go:13-19

```go
if envPath := os.Getenv("DFMT_PROJECT"); envPath != "" {
    if _, err := os.Stat(envPath); err == nil {
        return filepath.Abs(envPath)
    }
}
```

Any path the user can stat is accepted as the project root. Setting
DFMT_PROJECT=/etc before launching dfmt mcp would make the daemon treat
/etc as a project — MkdirAll(/etc/.dfmt, 0700) would fail for non-root,
but for root it would create /etc/.dfmt/ and write journal/index there.
While this requires control of the agent's environment (which already
implies a compromise), it removes the cwd sanity check that catches some
accidents.

Fix: Require $DFMT_PROJECT to (a) be an absolute path and (b) contain
either .dfmt/ or .git/ (same predicate as the walk-up Discover does).

---

## M-13 (MEDIUM) — os.Chmod errors silently dropped on socket and pid file

- CWE: CWE-703 (Improper Check or Handling of Exceptional Conditions)
- File: internal/daemon/daemon.go:166, internal/setup/claude.go:191-194

```go
os.Chmod(socketPath, 0700)               // daemon
if err := os.Chmod(tmpPath, 0600); err != nil {
    _ = err                              // claude.go: TODO removed
}
```

Both ignore the error. On Windows, NFS, or exotic FS this can silently
leave 0666/0644 perms; on properly configured Linux the chmod should
succeed but defense-in-depth suggests surfacing the failure so operators
can detect it.

---

## L-14 (LOW) — Journal append size race with periodic sync

- CWE: CWE-362
- File: internal/core/journal.go:148-188

Append holds j.mu throughout, so the size check, write, and Sync are
atomic relative to other journal operations. However, the periodic-sync
goroutine (periodicSync, line 190-208) acquires j.mu to call
j.file.Sync. If a Sync is in progress when Stop is called, Stop waits
via syncDone channel; that path is correct.

But: Append checks j.maxBytes against f.Stat().Size() AFTER writing the
previous Append's bytes — meaning the journal can grow up to
maxBytes + maxEventBytes - 1 (1 MiB above the configured cap). Not
exploitable; documented for completeness.

---

## L-15 (LOW) — journalSegments glob pattern can match unrelated files; Sig is computed but never verified on read

- CWE: CWE-345 (Insufficient Verification of Data Authenticity)
- File: internal/core/journal.go:250-264, internal/core/journal.go:284-298

```go
pattern := filepath.Join(dir, base+".*.jsonl")
matches, err := filepath.Glob(pattern)
```

base+".*.jsonl" matches journal.jsonl.<anything>.jsonl. An attacker with
write access to .dfmt/ can drop journal.jsonl.MALICIOUS.jsonl files; the
Stream loop will read them as historical journal segments and surface
their content (after JSON parse) into search/recall. Inside .dfmt/
(which is 0o700 inside the project) this requires write access to the
project, which is roughly the agent's own permission level — but the
agent itself should not be able to forge journal events without going
through Append (which sets Sig).

The downstream streamFile uses json.Unmarshal and accepts events with
any signature — Sig is computed in Append but never *verified* on
read. So forged events bypass the integrity check entirely.

Fix: Verify e.Sig == e.ComputeSig() on the Stream path; tighten the
glob to anchored ULID format (^journal\.jsonl\.[0-9A-Z]{26}\.jsonl$).

---

## L-16 (LOW) — Idle exit window vs in-flight request

- CWE: CWE-362 (transient race)
- File: internal/daemon/daemon.go:519-545 (idle monitor)

The idle monitor reads lastActivityNs and, if older than threshold,
calls Stop. There is a small window where:

1. Idle monitor reads stale lastActivityNs.
2. A new RPC arrives, calls Touch(), then enters the handler.
3. Idle monitor calls Stop, which sets running=false and shuts down the
   listener.
4. Handler completes, but caller's connection is dropped mid-write.

Touch() updates lastActivityNs BEFORE the handler runs (it's wired via
SetActivityFn), but the monitor already loaded the old value. The race
window is bounded by the tick interval (1s-60s, which is large in
tests). A request that arrives in this window receives a connection-
reset error rather than a clean response. Not security critical but
it's a logical flaw worth fixing — re-check lastActivityNs again under
running.CompareAndSwap before calling Stop, so a Touch between the load
and CAS aborts the shutdown.

---

## L-17 (LOW) — handleAPIStats inline-decodes params; redundant with decodeRPCParams

- CWE: CWE-1188 (Insecure Default Initialization)
- File: internal/transport/http.go:564-585 vs decodeRPCParams at line 383

Stats endpoint inline-decodes params; behavior is consistent today but
the code path is redundant and a future change could let invalid params
silently produce a zero-value struct (the bug decodeRPCParams was added
to fix per the comment at line 380-382).

---

## Per-project daemon: cross-project authorization analysis (CWE-285)

- The HTTP /api/daemons endpoint at http.go:626-672 filters the registry
  to entries whose project_path == s.projectPath. Good — V-4 fix is
  correctly applied.
- The JSON-RPC endpoints (/, exec, read, write, etc.) do NOT identify
  the caller's project. They operate on the daemon's bound project. A
  client that connects to daemon A's socket cannot reach daemon B's
  data. This is correct because each daemon binds a per-project socket.
- HOWEVER: if H-03 (socket collision/squat) succeeds, an attacker can
  bind a socket whose path collides with another project's daemon and
  receive RPCs intended for the legitimate daemon. This is an IDOR-
  shaped vulnerability via path collision rather than parameter
  tampering.
- The HTTP same-origin check (isAllowedOrigin) compares Origin header
  to the listener's TCP addr; for Unix sockets it returns false for any
  non-empty Origin. This is a reasonable browser-facing defense.

---

## Top-level setup -> privilege summary

| Path                                         | Symlink-safe?    | Atomic? | Issue        |
|----------------------------------------------|------------------|---------|--------------|
| ~/.claude.json (main write)                  | yes (rename)     | yes     | OK           |
| ~/.claude.json.dfmt.bak (pristine backup)    | NO               | no      | H-04         |
| *.dfmt.bak via BackupFile                    | NO               | no      | H-04         |
| setup-manifest.json                          | follows links    | no      | minor risk   |
| .dfmt/daemon.pid                             | NO               | no      | H-05         |
| .dfmt/daemon.sock (bind)                     | umask race       | n/a     | H-02         |
| .dfmt/lock                                   | follows links    | n/a     | minor        |
| index.gob / index.cursor                     | yes (rename)     | yes     | OK           |
| journal.jsonl                                | follows links on first open | append-atomic via O_APPEND | OK |
| ~/.dfmt/daemons.json                         | NO               | NO      | M-09         |
| .git/hooks/<name> (install-hooks)            | NO               | no      | M-07         |

---

## Summary

- 6 HIGH, 7 MEDIUM (M-07, M-08, M-09, M-10, M-11, M-12, M-13), 4 LOW
  (L-14, L-15, L-16, L-17) = 17 findings.
- Common root cause across H-01, H-04, H-05, H-06, M-07, M-09:
  os.WriteFile and friends are used wherever a fresh file should be
  created, but O_NOFOLLOW/O_EXCL are never set, and os.Lstat checks are
  absent. Auditing every os.WriteFile / os.Create in the codebase and
  switching to symlink-aware variants would collectively close five of
  the six HIGH findings.
- The recent commit (1d8a26b) plugged the symlink check on Edit/Write
  for the existing-target case but left the non-existent-target case
  open — H-01 is the regression test that should accompany the actual
  fix.
- The project-singleton invariant (lock file) is correctly enforced for
  the daemon path but NOT for dfmt mcp (M-08) — agents that run MCP
  directly bypass the lock entirely.
- JSON-RPC RememberParams allow the client to spoof Source/Priority/
  Type fields (M-11), enabling self-injection of P1 events that
  displace genuine high-priority context in Recall snapshots — a
  prompt-injection amplification vector worth treating with care given
  DFMT's mandate to govern future agent context.
