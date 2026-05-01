# sc-path-traversal — Path / Directory Traversal

## Scope

Caller-supplied paths flow into `dfmt_read`, `dfmt_write`, `dfmt_edit`,
`dfmt_glob`, `dfmt_grep`. This scan re-examines the full surface against
the methodology in `C:\Users\ersin\.claude\skills\security-check\skills\sc-path-traversal\SKILL.md`,
covering all six focus areas: path traversal patterns, symlink attacks,
glob pattern injection, write-to-read escalation, journal/path injection,
and absolute path validation bypass.

## Surfaces examined

| File | Focus |
|------|-------|
| `internal/sandbox/permissions.go` | Read (1239), Write (2035), Edit (1942), Glob (1611), Grep (1729) |
| `internal/safefs/safefs.go` | CheckNoSymlinks, EnsureResolvedUnder, WriteFile, WriteFileAtomic |
| `internal/core/journal.go` | journalSegments, streamFile, Rotate, scanLastID |
| `internal/core/index_persist.go` | writeRawAtomic, writeJSONAtomic, PersistIndex |
| `internal/content/store.go` | validateID, persistChunkSetToDisk, LoadChunkSet |
| `internal/transport/handlers.go` | All MCP tool handlers |

## Findings

### PATH-01 — Verified Fixed: Glob symlink leaf escape
- **Severity:** Info
- **CWE:** CWE-22 (Path Traversal), CWE-59 (Symlink Following)
- **File:** `internal/sandbox/permissions.go:1685`
- **Description:** Prior audit (PATH-01) identified that Glob's intent-content
  path could read through a symlink leaf pointing outside `wd`. The fix is
  confirmed at line 1685:

  ```go
  if _, sym := safefs.EnsureResolvedUnder(fullPath, absWd); sym != nil {
      continue
  }
  ```

  `EnsureResolvedUnder` calls `filepath.EvalSymlinks` on both the target and
  `wd`, then re-checks `Rel` on the resolved values. A symlink like
  `notes_link -> /etc/passwd` inside `wd` is resolved to `/etc/passwd`, the
  `rel` from `absWd` starts with `..`, and the entry is dropped. The
  previous test `TestSandboxGlobRefusesSymlinkLeafEscape` (sandbox_test.go:1712)
  explicitly covers this.
- **Remediation:** N/A — already fixed. Maintain test coverage.
- **Confidence:** High.

---

### PATH-02 — Verified Fixed: Grep symlink leaf escape
- **Severity:** Info
- **CWE:** CWE-22, CWE-59
- **File:** `internal/sandbox/permissions.go:1837`
- **Description:** Prior audit (PATH-02) identified that Grep's per-file
  `os.ReadFile` could follow a symlink leaf outside `wd`. The fix is
  confirmed at line 1837:

  ```go
  if _, sym := safefs.EnsureResolvedUnder(path, absWd); sym != nil {
      return nil
  }
  ```

  Same pattern as Glob: `EvalSymlinks` on the resolved path, `Rel` re-check
  against `absWd`, silent skip if the target escapes. `TestSandboxGrepRefusesSymlinkLeafEscape`
  (sandbox_test.go:1745) explicitly covers this scenario.
- **Remediation:** N/A — already fixed. Maintain test coverage.
- **Confidence:** High.

---

### PATH-03 — Verified Fixed: Grep req.Path symlink root bypass
- **Severity:** Info
- **CWE:** CWE-22, CWE-59
- **File:** `internal/sandbox/permissions.go:1769–1783`
- **Description:** Prior audit (PATH-03) flagged that a symlinked `req.Path`
  could theoretically set the walk root outside `wd`. The code path was
  analyzed in detail:

  1. `p = filepath.Clean(req.Path)` → absolutized → `filepath.Rel(absWd, p)` checked.
     A symlink target like `/etc` would produce `rel = "../../../etc"`, which
     starts with `..`, so the walk is refused before it starts.
  2. Additionally, the per-file `safefs.EnsureResolvedUnder(path, absWd)` at line 1837
     acts as defense-in-depth: even if a crafted symlink somehow widened the
     scope, any individual file outside `absWd` is silently skipped.

  The theoretical concern does not translate to an exploitable path given the
  layered checks. No remediation needed.
- **Remediation:** N/A — already defended. The existing per-file symlink check
  at line 1837 provides the safety net even if the walk root were somehow
  widened.
- **Confidence:** High.

---

### PATH-04 — Verified Safe: Read implements full symlink resolution + re-containment check
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/sandbox/permissions.go:1268–1281`
- **Description:** `Read` performs the most thorough path validation of all tools:

  ```go
  // Lines 1272–1281
  if resolved, rerr := filepath.EvalSymlinks(absPath); rerr == nil {
      resolvedWd, werr := filepath.EvalSymlinks(absWd)
      if werr != nil {
          resolvedWd = absWd
      }
      relResolved, err := filepath.Rel(resolvedWd, resolved)
      if err != nil || relResolved == ".." ||
          strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
          return ReadResp{}, fmt.Errorf(
              "path outside working directory after symlink resolution: %s", req.Path)
      }
  }
  ```

  Both the path and the wd are resolved before `Rel` is checked. A symlink
  leaf pointing at any file outside `wd` is correctly refused. The only
  residual is the documented TOCTOU window between `EvalSymlinks` and `os.Open`
  — documented in `internal/safefs/safefs.go` comments and out of scope for
  the current threat model.
- **Confidence:** High.

---

### PATH-05 — Verified Safe: Write/Edit use CheckNoSymlinks + WriteFileAtomic
- **Severity:** Info
- **CWE:** CWE-22, CWE-59
- **File:** `internal/sandbox/permissions.go:1974` (Edit), `internal/sandbox/permissions.go:2070` (Write)
- **Description:** Both Write and Edit call `safefs.CheckNoSymlinks(absWd, cleanPath)`
  before any write operation. `CheckNoSymlinks` (safefs.go:107) Lstat-walks each
  path component starting from `baseDir` and returns `ErrSymlinkInPath` if any
  segment is a symlink. This closes the missing-leaf gap: the previous
  `EvalSymlinks`-only gate would skip the check entirely when the target file
  did not exist, allowing an attacker to plant `wd/leak -> /etc/cron.d/x` and
  write through it.

  Both tools then write via `safefs.WriteFileAtomic` (safefs.go:227), which:
  - Creates a temp file in the same directory as the target.
  - Writes, fsyncs, and closes the temp file.
  - Calls `os.Rename(tmpPath, path)` which atomically replaces the symlink
    as a directory entry rather than following it. The F-R-LOW-1 TOCTOU window
    is closed.

  File mode is preserved on re-write (line 2021 stat, line 2096 stat).
  Newly created files default to 0o600.
- **Confidence:** High.

---

### PATH-06 — Verified Safe: Glob file-list filter uses os.Stat (follows links) but policy is lexical-only
- **Severity:** Low
- **CWE:** CWE-22
- **File:** `internal/sandbox/permissions.go:1655`
- **Description:** The file-list build in Glob (lines 1654–1667) uses `os.Stat(m)`
  which follows symlinks. This is intentional: `Stat` is used only to filter out
  directories and check existence, not to read content. The per-file content
  read (intent filtering, lines 1674–1702) uses `safefs.EnsureResolvedUnder`
  before `os.ReadFile`. A symlink in the file-list that points to a directory
  is silently dropped (not followed into); a symlink that points to a file
  passes the `PolicyCheck` and `EnsureResolvedUnder` checks. No path traversal
  risk.
- **Confidence:** High.

---

### PATH-07 — Verified Safe: Grep uses filepath.WalkDir with lexical containment pre-filter
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/sandbox/permissions.go:1792–1838`
- **Description:** Grep uses `filepath.WalkDir(searchRoot, ...)` which does NOT
  follow symlinks into directories by default (walks the tree, not the link
  target). Symlinked file entries are enumerated but their type is detected
  via `d.Type()` (which uses Lstat, not stat). The walk has two containment
  layers:
  1. `filepath.Rel(absWd, path)` check at line 1808 — purely lexical, rejects
     paths whose text starts with `..`.
  2. `safefs.EnsureResolvedUnder(path, absWd)` at line 1837 — resolves symlinks
     and re-checks.

  Combined with the `PolicyCheck("read", path)` at line 1828, the attack
  surface for directory traversal is fully closed.
- **Confidence:** High.

---

### PATH-08 — Verified Safe: Null byte rejection in Read/Write/Edit
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/sandbox/permissions.go:1245–1247` (Read),
  `:2053–2056` (Write), `:1960–1963` (Edit)
- **Description:** All three tools explicitly reject paths containing `\0`:

  ```go
  if strings.IndexByte(cleanPath, 0) >= 0 {
      return /* error */ "path contains null byte"
  }
  ```

  Defense-in-depth. Go's `os.Open` rejects them on Windows, but Unix file
  APIs accept them as valid filename characters in some edge cases. The
  explicit check closes the gap.
- **Confidence:** High.

---

### PATH-09 — Verified Safe: Windows path normalization in deny rules
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/sandbox/permissions.go:482–485`
- **Description:** `globMatch` normalizes `\` → `/` for all path-style ops
  (`read`, `write`, `edit`, `fetch`) before regex compilation:

  ```go
  if op != "exec" {
      text = strings.ReplaceAll(text, `\`, "/")
      pattern = strings.ReplaceAll(pattern, `\`, "/")
  }
  ```

  F-03 closure: pre-fix, normalization was done only for `read`, so a Windows
  agent could write through deny rules like `**/.env*` because the pattern's
  backslash wasn't normalized. Confirmed by `TestGlobMatch_NormalizesPathSeparatorsForAllPathOps`
  (sandbox_test.go:1597).
- **Confidence:** High.

---

### PATH-10 — Verified Safe: Journal rotation uses ULID in filename
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/core/journal.go:497–498`
- **Description:** Rotation builds the new filename as:

  ```go
  newPath := fmt.Sprintf("%s.%s.jsonl", j.path, j.hiCursor)
  ```

  `hiCursor` is the ULID of the last appended event, generated by
  `core.NewULID(time.Now())` — server-side, not caller-supplied. An agent
  cannot control the rotated filename through any journal entry field.
  No path traversal risk in rotation.
- **Confidence:** High.

---

### PATH-11 — Verified Safe: Index persist uses JSON, not gob
- **Severity:** Info
- **CWE:** CWE-20 (Improper Input Validation)
- **File:** `internal/core/index_persist.go:93–98`
- **Description:** `PersistIndex` calls `writeJSONAtomic` which uses
  `json.Marshal` (line 93–98). The index is NOT serialized with `encoding/gob`.
  The project uses JSON for all cross-version persisted structures. The
  skill methodology flags gob as a deserialization risk; this does not apply.
- **Confidence:** High.

---

### PATH-12 — Verified Safe: Content store validates chunk-set IDs
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/content/store.go:31–40`
- **Description:** `validateID` restricts chunk/chunk-set IDs to
  `^[A-Za-z0-9_-]{1,128}$` via a compiled regexp (line 31). All callers
  (`stashContent`, `PutChunkSet`, `LoadChunkSet`) validate before any
  filesystem operation. Path components cannot escape the store directory.
- **Confidence:** High.

---

### PATH-13 — Verified Safe: Journal path has Windows reserved-name check
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/core/journal.go:134`
- **Description:** `OpenJournal` calls `safefs.CheckNoReservedNames(path)`
  before creating directories. This blocks Windows reserved device names
  (`NUL`, `CON`, `PRN`, `AUX`, `COM0-9`, `LPT0-9`) at the journal path level,
  preventing the NTFS Services-for-UNIX remap that silently creates a `NUL`
  directory when `NUL:` appears in a path component.
- **Confidence:** High.

---

### PATH-14 — Verified Safe: Grep req.Path validation includes existence check + Rel re-check
- **Severity:** Info
- **CWE:** CWE-22
- **File:** `internal/sandbox/permissions.go:1769–1783`
- **Description:** When `req.Path` is provided (non-empty), it is:
  1. Cleaned: `filepath.Clean(req.Path)`.
  2. Absolutized if relative: `filepath.Join(absWd, p)`.
  3. `Rel`-checked: must not start with `..`.
  4. `os.Stat(p)` verified — the path must exist.

  If `p` is a symlink to an out-of-bounds directory, `os.Stat` follows it
  and the target is what gets walked. But `filepath.WalkDir(searchRoot, ...)`
  is given `p` as the root; if the target is outside `absWd`, the subsequent
  per-file `filepath.Rel(absWd, path)` checks will reject everything. The walk
  is effectively empty — no exfil. The `safefs.EnsureResolvedUnder` at line 1837
  is the final backstop. No remediation needed.
- **Confidence:** High.

---

## Summary

All six focus areas were examined:

| Focus | Result |
|-------|--------|
| Path traversal (`../` patterns) | **No finding.** Read/Write/Edit/Glob/Grep all perform lexical `Rel` containment checks and re-check after symlink resolution. |
| Symlink attacks | **No finding.** Glob and Grep both use `safefs.EnsureResolvedUnder` before reading file content. Write and Edit use `safefs.CheckNoSymlinks` + `WriteFileAtomic`. Read uses `EvalSymlinks` + re-check. Tests `TestSandboxGlobRefusesSymlinkLeafEscape` and `TestSandboxGrepRefusesSymlinkLeafEscape` confirm coverage. |
| Glob pattern injection | **No finding.** Glob patterns are cleaned, made absolute, `Rel`-checked against `wd`, and then filtered through policy + symlink checks. |
| Write-to-read escalation | **No finding.** Write/Edit go through `CheckNoSymlinks` which refuses to operate on any path containing a symlink component. A written file cannot become a read vector for an OOB path. |
| Journal/path injection | **No finding.** Rotated filenames use server-generated ULIDs, not caller-supplied values. Journal path has Windows reserved-name check. |
| Absolute path validation bypass | **No finding.** Read explicitly rejects absolute paths when `wd` is not set. All tools consistently use `filepath.Clean` + `filepath.Abs` + `Rel` containment. |

**No new vulnerabilities found.** All findings from the prior audit (PATH-01 through PATH-03) have been confirmed fixed. The implementation demonstrates defense-in-depth: multiple independent checks (lexical `Rel`, symlink resolution + re-containment, policy evaluation) stack to eliminate single-point-of-failure risk.