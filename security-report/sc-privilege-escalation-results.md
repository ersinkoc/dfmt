# sc-privilege-escalation Results

## Target: DFMT sandbox + filesystem write paths

## Methodology

- Reviewed every place the daemon executes a subprocess (`permissions.go`
  `execImpl`, `prependPATH`, `buildEnv`).
- Reviewed every filesystem write site for symlink-traversal,
  TOCTOU, and mode-widening (sandbox `Edit`/`Write`,
  `internal/safefs/safefs.go`).
- Searched for any explicit drop / no-drop of process privileges.
- Inspected the journal- / index- / port-file write paths for hardcoded
  modes vs. process umask.
- Checked the `dfmt` deny rule: recursive invocation of dfmt by an agent
  would otherwise bypass the parent policy (`permissions.go:206-208`).

## Findings

### sc-privilege-escalation-01 — Daemon runs as the user; `dfmt setup`/`init` may inherit elevated rights when launched via `sudo`

- **Severity:** Low
- **CWE:** CWE-250 (Execution with Unnecessary Privileges)
- **File:** N/A — runtime concern, not a single source line
- **Description:** DFMT does not call `setuid`/`setgid` and does not warn
  the user if launched via `sudo`. An operator who runs `sudo dfmt
  daemon` (e.g. by mistake or on a multi-tenant host) ends up with a
  daemon running as root that any subsequent `dfmt mcp` connection (also
  running as root) will inherit. Sandbox tool output is then written by
  root, with predictable file-mode + ownership consequences for the
  user's project tree.
- **Attack scenario:** Less an attack than an own-foot scenario. An
  agent that does `dfmt_exec mkdir build` while the daemon runs as
  root creates a root-owned `build/` that the user later cannot delete.
  Not an external escalation path.
- **Remediation:** Print a one-line warning on daemon start if
  `os.Geteuid() == 0` and recommend running as the project owner. No
  code change is required for security correctness; this is operator
  ergonomics.
- **Confidence:** Low (theoretical; the documented installation path
  never invokes `sudo`).

### Verified-clean controls

| Control | Location | Status |
|---|---|---|
| Subprocess env strips loader-injection vars (LD_*, DYLD_*) | `permissions.go:2045-2099` | PASS — closes F-G-LOW-2 |
| Subprocess env strips PATH/IFS/BASH_ENV/PROMPT_COMMAND/PS4 overrides | `permissions.go:2079-2098` | PASS |
| Subprocess env strips per-runtime startup hooks (NODE_*, PYTHON*, RUBY*, JAVA_*, NPM_CONFIG_*, GIT_*, etc.) | `permissions.go:2046-2078` | PASS |
| Subprocess inherits a curated env (no `os.Environ()` passthrough) | `permissions.go:1981-2030` | PASS |
| `_JAVA_OPTIONS` and `JAVA_TOOL_OPTIONS` blocked (no javaagent injection) | `permissions.go:2073-2077,2094-2098` | PASS |
| Recursive `dfmt`/`dfmt *` exec denied to prevent outer-policy bypass | `permissions.go:206-208` | PASS |
| `sudo *`, `rm -rf /*`, `curl * | sh`, `wget * | sh`, `shutdown *`, `reboot *`, `mkfs *`, `dd if=*` all on default deny list | `permissions.go:198-205` | PASS |
| Symlink-safe write helper (`safefs.WriteFileAtomic`) used in sandbox Edit/Write | `permissions.go:1710,1785`, `safefs/safefs.go:116-156` | PASS — closes F-04/F-07/F-08/F-25 cluster |
| Path-traversal check in Read/Edit/Write (Rel against absWd, EvalSymlinks recheck) | `permissions.go:980-1003,1651-1662,1744-1758` | PASS |
| Null-byte rejection in Read/Edit/Write paths | `permissions.go:967,1647,1740` | PASS |
| `.git/**` and `.dfmt/**` write/edit-blocked so agent cannot rewrite repo or its own audit trail | `permissions.go:230-240` | PASS |
| `.env*`, `**/secrets/**`, `**/id_*` read AND write/edit blocked | `permissions.go:209-227` | PASS |
| File-mode preservation on overwrite (no widening) | `permissions.go:1706-1709,1781-1784` | PASS |
| Newly-created intermediate dirs use 0o700 | `permissions.go:1768` | PASS |
| Port file directory 0o700, port file 0o600 | `http.go:534,560` | PASS |
| Unix-socket directory 0o700, socket 0o700 | `socket.go:77,97` | PASS |
| Subprocess output capped (`MaxRawBytes`), pipe drained so child can exit cleanly | `permissions.go:1828-1845` | PASS |
| Exec timeout default 30s, hard cap `MaxExecTimeout` | `permissions.go:1800-1810` | PASS |

### Privilege-escalation surface summary

The sandbox cannot grant *more* than the host process has. Every elevation
vector found in the audit (env-injected loader, `_JAVA_OPTIONS`, recursive
dfmt, symlinked `id_rsa` write target) is closed. The remaining residual
is operator-side: launching the daemon with elevated privileges
propagates them to every subprocess. That is consistent with the
documented threat model.

### Confidence: High
