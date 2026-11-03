# ADR-0034: One Installed dfmt, At Most One Daemon

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-03 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

ADR-0019/0021/0022 made the daemon a host-wide singleton and made a *newer
client* replace a *stale daemon*. They do nothing about the other ways a host
ends up running or holding more than one dfmt:

- The daemon and every `dfmt mcp` proxy outlive the install. After an update
  they keep running the replaced code (on Linux the kernel reports the path as
  `… (deleted)`; on Windows the file is renamed `<exe>.old` and stays locked).
- `install.ps1` deliberately wrote a *second* copy to `~/.dfmt/dfmt.exe`.
  Nothing reads it (`ResolveDFMTCommand` prefers `os.Executable`), and
  `dfmt update` replaces only one file, so the copy goes stale at once.
- `install.sh`'s source fallback leaves `$GOBIN/dfmt` beside the installed
  copy; older install directories stay on PATH and shadow the new binary.
- `install.ps1` never stopped the running daemon, so `Move-Item` hit a locked
  `dfmt.exe` and reported "could not replace locked file".
- `dfmt doctor` could see path disagreement but said "Nothing is deleted;
  the operator decides", and had no view of processes or of copies that no
  agent config points at.

## Decision

A new command, **`dfmt cleanup [--yes]`**, backed by `internal/instances`,
leaves exactly one installed binary and at most one daemon.

1. **Identity is the image, not the name.** A process or file is dfmt only if
   its bytes carry the module path `github.com/ersinkoc/dfmt` (Go build info
   and symbol table). `dfmt` is also the D formatter's binary name; matching on
   the name would kill or delete someone else's program. The scan reads the
   file without executing it, so a corrupt binary left by a failed update
   still qualifies. On Linux a running image is read through `/proc/<pid>/exe`,
   which works after its path was replaced.
2. **Keep = the binary that runs `cleanup`.** Everything else is judged
   against it.
3. **Processes.** Stale ones — another path, or its file replaced or deleted —
   are stopped (SIGTERM, 12 s grace, then kill; Windows kills at once, since
   a detached daemon has no console to receive a control event). Of several
   daemons on the kept binary only the one named by `daemon.json` survives; if
   none is named nothing is killed, because that daemon may merely have lost
   its rendezvous files, which it repairs itself. `dfmt mcp` proxies on the
   kept binary are the agents' sessions and are left alone.
4. **Never kill what is running us.** The ancestor set comes from the real
   process tree, not from dfmt processes only (`dfmt ← bash ← daemon` would
   otherwise break at the shell). A stale ancestor is reported instead.
5. **Files.** Every verified dfmt binary other than the kept one in a PATH
   directory, a known per-user install directory (`~/.local/bin`, `~/go/bin`,
   `$GOBIN`, `$GOPATH/bin`, `~/.dfmt`, `%LOCALAPPDATA%\Programs\dfmt`, …), or
   beside the kept binary (`*.old`, `.dfmt-update-*`) is removed; a locked file
   is renamed to `.old`. Files inside a Go checkout (a `go.mod` within four
   parents) are never touched: `dist/dfmt` belongs to the developer.
6. **Safe by default.** Without `--yes` it prints the plan and changes nothing.

Wiring: `dfmt update` runs the plan after the swap; `install.sh` and
`install.ps1` run `dfmt cleanup --yes` after placing the binary (a pinned old
release without the command is a warning, not an error); `install.ps1` stops
the running `dfmt.exe` (matched by full path) before replacing it and no
longer writes `~/.dfmt/dfmt.exe`; `dfmt doctor` reports stale processes and
stray copies and points at `dfmt cleanup`.

## Consequences

- An install or update ends the old daemon and the old agents' `dfmt mcp`
  proxies. Agents must restart their MCP server, which the installers already
  tell the user to do; the proxy self-heals only against a vanished *daemon*,
  not a vanished *proxy*.
- Removing copies is destructive, so verification is by content and the
  default is a dry run. A user who keeps a deliberate second dfmt outside a Go
  checkout must expect `cleanup --yes` to remove it.
- Process listing uses `/proc` on Linux, `ps` on macOS/BSD and PowerShell
  `Win32_Process` on Windows. The macOS and Windows paths are covered by
  parser tests and cross-compile checks, not exercised on those hosts here.
- A home directory containing `go.mod` would hide copies beneath it from
  removal; that errs toward not deleting.
