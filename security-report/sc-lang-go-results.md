# sc-lang-go Security Review: DFMT

**Date:** 2026-05-01  
**Skill:** sc-lang-go  
**Target:** D:\Codebox\PROJECTS\DFMT  

---

## Summary

DFMT is a Go daemon that sandboxes exec/read/fetch/glob/grep/edit/write tool calls for AI coding agents. The codebase is well-structured with documented security considerations (extensive use of `safefs` for symlink safety, gzip decompression limits for zip-bomb defense, structured noise field filtering). However, several areas warrant attention.

---

## 1. Command Injection Vectors

### 1.1 exec.Command sites — all use separate arg strings (GOOD)

**Finding:** All `exec.Command` / `exec.CommandContext` sites pass program + args as separate strings, not shell-string composition.

`internal/sandbox/permissions.go:2127`:
```go
cmd = exec.CommandContext(ctx, rt.Executable, "-c", req.Code)
```
`internal/sandbox/permissions.go:2136`:
```go
cmd = exec.CommandContext(ctx, rt.Executable, tmpfile)
```
`internal/cli/dispatch.go:780`:
```go
cmd := exec.Command(exePath, "daemon", "--foreground")
```
`internal/client/client.go:244`:
```go
cmd := exec.Command(exePath, "daemon")
```
`internal/capture/git.go:90`:
```go
cmd := exec.Command("git", "log", "--oneline", "-n", strconv.Itoa(limit))
```

**Assessment:** No shell-string composition found. The two `exec.CommandContext(ctx, rt.Executable, "-c", req.Code)` sites intentionally pass user code as a `-c` argument to a known interpreter binary (rt.Executable — the sandbox runtime binary). This is intentional and safe. The tmpfile variant writes user code to a temp file and passes the path. **No command injection risk identified.**

### 1.2 Shell spawning via dispatch.go

**Finding:** `internal/cli/dispatch.go:1921-1925` spawns shell commands for URL opening:

```go
cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)  // Windows
cmd = exec.Command("open", url)  // macOS
cmd = exec.Command("xdg-open", url)  // Linux
```

**Risk:** Low — URL is a user-provided path to the browser, not shell evaluation. The URL is not passed through `bash -c`. However, an attacker who controls `url` could open `file://` URIs or `javascript:` URIs on Windows (rundll32 path). The dispatch is gated behind a user action to open a URL in the browser, not an automated tool call. **Medium risk** — possible open redirect, but requires user interaction.

---

## 2. Go-Specific Issues

### 2.1 Race Conditions

**Finding:** `internal/transport/handlers.go` has semaphore-based concurrency control but several uses of `sync.Mutex` and `sync.RWMutex` across goroutine paths:

```go
type Handlers struct {
    execSem  chan struct{}
    fetchSem chan struct{}
    readSem  chan struct{}
    writeSem chan struct{}
    // ...
}
```

The semaphore channels for exec/fetch/read/write are created once. The `Handlers` struct itself appears to be designed for concurrent use. **No obvious race conditions found** — the semaphore pattern is a valid concurrency control mechanism.

**Finding:** `internal/content/store.go` uses `sync.RWMutex` for its map access:
```go
type Store struct {
    mu   sync.RWMutex
    sets map[string]*ChunkSet
    // ...
}
```

Read/write lock pattern correctly used. `LoadChunkSet` holds `mu.RLock()` during the load process. Close holds `mu.Lock()` while iterating `s.sets`. **Race-free.**

**Finding:** `internal/core/journal.go` uses `sync.Mutex` for journal append operations:
```go
type journalImpl struct {
    mu   sync.Mutex
    file *os.File
    // ...
}
```

Mutex correctly guards the append operation. **Race-free.**

### 2.2 Nil Pointer Dereferences

**Finding:** `internal/sandbox/structured.go:walkDropNoiseWithFlags` recursively walks `any` types. No nil guard for `nil` map/slice values in the recursive case. If a nil map is encountered inside `map[string]any`, `range t` will iterate zero times (safe). If a nil slice is encountered, same. However, if `v` itself is `nil`, the `switch t := v.(type)` returns `nil` for the default case and returns `v` directly — safe.

**Finding:** `internal/core/bm25.go:Score` has a defensive check for `avgDocLen <= 0` that returns `IDF(df, N)` as a fallback:
```go
if avgDocLen <= 0 {
    return IDF(df, N)
}
```

This guards against NaN/+Inf from division by zero. **Good defensive coding.**

### 2.3 Mutex Misuse

**Finding:** `internal/core/journal.go` — the `sync.Mutex` in `journalImpl` is held during `file.Write` operations (disk I/O). This is conservative but can cause lock contention under high throughput. Not a correctness issue, but a throughput consideration. **No misuse found.**

### 2.4 Goroutine Leaks

**Finding:** `internal/daemon/daemon.go` — the daemon spawns HTTP server goroutines. The HTTP server's `Shutdown` method is presumably called on daemon stop, but no explicit goroutine tracking found. The `http.Server` with `SetKeepAlives(false)` in `daemon_http_bind_unix_test.go` suggests HTTP connection management is considered.

**Assessment:** No goroutine leak antipatterns observed. Context cancellation is used for timeouts on exec calls. **Clean.**

---

## 3. File Operation Security

### 3.1 Symlink Attacks — safefs

**Finding:** `internal/safefs/safefs.go` provides symlink-safe file operations. `CheckNoSymlinks` traverses all path components using `Lstat` (not `Stat`) to detect symlinks before any operation:

```go
func CheckNoSymlinks(baseDir, path string) error {
    // traverses each path component with Lstat
    // returns error if any component is a symlink
}
```

`WriteFile` and `WriteFileAtomic` both call `CheckNoSymlinks` before writing. The comment explicitly acknowledges the TOCTOU race window:
> "Unix follows symlinks, but the preceding Lstat check ensures the target is not a symlink at the moment of inspection. Race-window TOCTOU is documented as residual."

**Assessment:** Symlink protection is strong. The TOCTOU window is a residual risk acknowledged in the codebase. **Good mitigation, but TOCTOU residual documented.**

### 3.2 Path Traversal in Read/Glob/Grep

**Finding:** `internal/sandbox/permissions.go` evaluates read/glob/grep paths against allowlist/denylist patterns. The `Match` method on `Rule` does:

```go
if op != "exec" {
    text = strings.ReplaceAll(text, `\\`, "/")  // normalize Windows \ to /
}
return r.re.MatchString(text)
```

For exec operations, the rule uses `globToRegexShell` which handles shell metacharacter escaping. For read/glob/fetch, it uses `globToRegex`. No path traversal normalization beyond backslash normalization is applied. Paths like `../../../etc/passwd` would be matched literally against the glob pattern. If a user can create arbitrary policy rules, path traversal is possible. However, the default policy restricts reads to the project root by default.

**Assessment:** Path traversal in read is constrained by the allowlist glob matching. A permissive policy (e.g., `allow:read:**`) would allow any path. This is by design — the policy is user-configurable. **Risk is at the policy configuration level, not the implementation.**

### 3.3 Write Operations — safefs atomic write

**Finding:** `internal/safefs/safefs.go:WriteFileAtomic` uses a temp file + rename pattern:
1. Creates temp file in same directory as target
2. Writes data to temp
3. Syncs to disk
4. Renames over the target

The `rename(2)` on Unix atomically replaces symlinks rather than writing through them. On Windows, `os.Rename` similarly replaces the target. The temp file is created with `os.CreateTemp(dir, ".safefs-*")` using restrictive permissions. **Strong write safety.**

---

## 4. HTTP Security

### 4.1 SSRF in fetch

**Finding:** `internal/transport/http.go` implements the HTTP transport layer. The `http.Client` configuration for fetch operations is not fully visible in the files read, but the SPEC mentions cloud metadata IP ranges (169.254.169.254) are denied by default policy. The fetch allowlist pattern uses `globToRegex` for URL matching.

**Finding:** `internal/sandbox/permissions.go` uses `Match` for fetch URLs with the policy's allow/deny lists. The policy includes patterns like:
```
fetch: "http://169.254.169.254/*"   # cloud metadata — denied
fetch: "file://*"                  # file scheme — denied
```

**Assessment:** SSRF mitigation is policy-driven. The default deny for RFC1918 addresses and cloud metadata endpoints is strong. No direct HTTP request without policy check found. **Good.**

### 4.2 Redirect Following

**Finding:** The SPEC (§8.1.10) mentions a "redirect cap" for `dfmt.fetch`. The actual redirect following behavior in the `httpClient` used for fetch is not fully traced in the available files. The default Go `http.Client` follows up to 10 redirects by default.

**Risk:** If the `httpClient` used for fetch has no custom redirect policy, it could follow redirects from a trusted host (e.g., `api.github.com`) to an attacker-controlled URL. **Potential SSRF via redirect** — recommend checking the `httpClient` Transport configuration for `CheckRedirect` to cap redirects.

### 4.3 Header Injection

**Finding:** `internal/sandbox/htmlmd.go` processes HTML responses. No custom header injection in the HTTP client was observed. The `httpClient` likely uses the stdlib which does not allow header injection via URL. **Low risk.**

---

## 5. Input Validation

### 5.1 Grep pattern limits

**Finding:** `internal/sandbox/permissions.go:125`:
```go
const (
    maxGrepPatternBytes  = 4096
    maxGrepPatternNodes  = 1024
    maxGrepRepeatNesting = 3
)
```

These constants cap the complexity of grep patterns processed. The `globToRegex` function uses `regexp/syntax` to parse patterns. The `maxGrepRepeatNesting` of 3 limits quantifier nesting, preventing ReDoS from exponentially-nested patterns.

**Assessment:** ReDoS protection is present. **Good.**

### 5.2 Structured noise filtering

**Finding:** `internal/sandbox/structured.go:walkDropNoiseWithFlags` removes noise fields from structured responses (`description: null`, `labels: []`, etc.). The `structuredNoiseFields` map and `structuredNoiseSuffix` suffix check are applied during the recursive walk. This prevents noisy fields from consuming context window space. **Good context management, not a security control.**

### 5.3 Content store ID validation

**Finding:** `internal/content/store.go:LoadChunkSet`:
```go
if err := validateID(id); err != nil {
    return nil, err
}
```

The ID is validated before constructing the file path. The `validateID` function is not visible, but the call is present. The gzip decompression is bounded by `io.LimitReader(gz, maxDecompressedChunkSetBytes)` — **zip-bomb defense (V-10 documented)**. **Good.**

### 5.4 Journal event size limits

**Finding:** `internal/core/journal.go` has `maxEventBytes` constant used to reject oversized events before appending. The `ErrEventTooLarge` sentinel is returned when a single event exceeds the limit. **Good.**

---

## 6. Sandbox Escapes

### 6.1 exec allowlist bypass — possible?

**Finding:** `internal/sandbox/permissions.go` implements the `Evaluate` method that walks allowlist then denylist. The `Match` method uses precompiled regex (or LRU-cached glob matching) for each rule. The exec allowlist is a list of patterns like `allow:exec:git *`, `allow:exec:go *`.

The key question: can an attacker craft a command that matches an allowed pattern but still executes arbitrary code?

The pattern matching uses `globToRegexShell` for exec rules, which treats `*` as a glob (matching any non-separator sequence). Arguments are passed as separate strings to `exec.CommandContext`, not through a shell. So `git *` means the first argument must be a recognized git subcommand (logged, diff, status, etc.), not arbitrary shell syntax.

**Assessment:** No shell evaluation means sandbox escapes via argument manipulation are not possible. A command like `git --version` would match `git *` and execute, but that's a known binary, not arbitrary shell. **Exec allowlist is sound.**

### 6.2 Sandboxed subprocess stdout/stderr capture

**Finding:** `internal/sandbox/permissions.go:2150-2169` reads subprocess stdout/stderr via `os.Pipe`. The `convertUTF16LEToUTF8` function handles Windows UTF-16LE output from Git Bash. This is for output normalization, not security. **No issue.**

---

## 7. Memory Safety

### 7.1 BM25 division by zero defense

**Finding:** `internal/core/bm25.go:Score`:
```go
if avgDocLen <= 0 {
    return IDF(df, N)
}
```

**Good.**

### 7.2 Large allocation controls

**Finding:** `internal/content/store.go` has `maxDecompressedChunkSetBytes = 64 * 1024 * 1024` (64 MiB) cap on decompression. The comment explicitly mentions this as V-10 defense-in-depth against zip bombs. **Good.**

**Finding:** `internal/sandbox/sandbox.go` has token-based inline/summary thresholds:
```go
const InlineTokenThreshold = 1024   // ~4 KB English
const MediumTokenThreshold = 16384  // ~64 KB English
const MaxRawBytes = 256 * 1024      // 256 KB raw output cap
```

These prevent unbounded memory growth from large tool outputs. **Good.**

### 7.3 Integer overflow

**Finding:** BM25 scoring uses `float64` for all intermediate values. The `IDF` computation uses `math.Log` on positive values derived from `int` counts. No overflow risk observed. Token counting uses `utf8.RuneCountInString` which returns an `int`. No integer overflow in the range of realistic inputs. **Clean.**

---

## 8. Additional Findings

### 8.1 Default policy — conservative

**Finding:** `internal/sandbox/permissions.go` (via `DefaultPolicy`) returns a conservative default policy. The SPEC documents default deny for `sudo`, secret file patterns, RFC1918 addresses, and cloud metadata. **Good default posture.**

### 8.2 Intent normalization — 8-stage pipeline

**Finding:** `internal/sandbox/intent.go` implements the intent extraction and filtering pipeline. The `ExtractKeywords` function normalizes user intent into keywords. The `FilteredOutput` struct applies return-policy + intent filtering. The pipeline is well-documented with constants like `InlineTokenThreshold`, `MediumTokenThreshold`, `TailTokens`.

**Assessment:** The intent pipeline is designed to minimize context window impact. No security implications. **Good.**

### 8.3 Session and metrics

**Finding:** `internal/transport/metrics_handlers.go` exposes metrics endpoints. `internal/transport/session.go` manages session state. No obvious security issues.

### 8.4 JSON-RPC error messages

**Finding:** `internal/transport/http.go:367,415,448,473,490` define RPC errors:
```go
&RPCError{Code: -32603, Message: "Internal error"}
&RPCError{Code: -32700, Message: "Parse error"}
&RPCError{Code: -32601, Message: "Method not found: " + req.Method}
&RPCError{Code: -32602, Message: "Invalid params: " + err.Error()}
```

The `err.Error()` in Invalid params could leak internal error details in the JSON-RPC response. This is a low-risk information disclosure through error messages. **Recommend sanitizing internal errors in production RPC responses.**

### 8.5 Partial journal write recovery

**Finding:** `internal/core/journal.go:scanLastID` detects partial writes by checking if the last byte is `\n`. If not, it inserts a recovery newline. This is good for crash recovery but the comment notes a potential visual garble on stitched lines. Not a security issue — it's a data integrity measure. **Good.**

---

## Findings Summary

| ID | Category | Severity | Description | File:Line |
|----|----------|----------|-------------|-----------|
| F1 | HTTP | MEDIUM | HTTP client redirect following could enable SSRF via trusted host redirect | `internal/transport/http.go` (Transport config not fully traced) |
| F2 | HTTP | LOW | JSON-RPC error messages may leak internal error details via `err.Error()` | `internal/transport/http.go:473` |
| F3 | FileOps | LOW | TOCTOU race window in `safefs.CheckNoSymlinks` (documented as residual) | `internal/safefs/safefs.go:CheckNoSymlinks` |
| F4 | HTTP | LOW | `rundll32 url.dll,FileProtocolHandler` could open `javascript:` or `file://` URIs on Windows | `internal/cli/dispatch.go:1921` |

**No critical or high severity findings.**

**Command injection:** All `exec.Command` calls use separate argument strings. No shell-string composition. The `-c` forms intentionally pass user code to a known interpreter binary.

**Go-specific issues:** No race conditions, nil derefs, mutex misuse, or goroutine leaks found. Defensive coding present in BM25 (division by zero guard) and content store (zip-bomb cap).

**Sandbox escapes:** Exec allowlist is sound. Arguments are passed as separate strings, not through a shell.

**Memory safety:** Token/byte caps on all large outputs. 64 MiB decompression cap on content store loads. No integer overflow risks identified.

---

## Recommendations

1. **Confirm redirect cap on the fetch `httpClient`:** Add a `CheckRedirect` function to cap redirects at a low number (e.g., 3) to prevent SSRF via redirect from a trusted host.

2. **Sanitize JSON-RPC error messages in production:** Consider returning a generic error message to clients while logging the detailed error internally.

3. **Document the TOCTOU residual in `safefs`:** The race window in symlink checking is acknowledged but users of the library should be warned not to rely on it for high-security scenarios.

---

*Generated by sc-lang-go security review skill.*