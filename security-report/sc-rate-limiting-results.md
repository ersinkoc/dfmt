# sc-rate-limiting Results

## Target: DFMT daemon (HTTP, socket, MCP transports)

## Methodology

- Inspected per-handler concurrency limiters
  (`handlers.go:29-38,145-149,1148,1279,1404,1489,1587,1616`).
- Checked request-body and header caps (`http.go:170,369,630`,
  `jsonrpc.go:38`).
- Checked timeouts: `ReadTimeout`, `ReadHeaderTimeout`, `WriteTimeout`,
  `IdleTimeout` (`http.go:166-171`), socket idle/lifetime
  (`socket.go:17,25,56`).
- Inspected sandbox-side caps that protect against output-flooding DoS:
  `MaxRawBytes`, `MaxFetchBodyBytes`, `MaxSandboxReadBytes`, exec timeout.
- Checked for back-pressure on long-running exec output and goroutine
  leaks per long-lived connection.
- Reviewed `/api/stats` 5-second TTL cache (`handlers.go:86-87,892-899`)
  to ensure dashboard polling cannot DoS journal streaming.

## Findings

### sc-rate-limiting-01 — No per-source request-rate limit beyond concurrency caps

- **Severity:** Low
- **CWE:** CWE-770 (Allocation of Resources Without Limits or Throttling)
- **File:** `internal/transport/handlers.go:140-149`
- **Description:** Concurrency is bounded by semaphores
  (`execSem=4`, `fetchSem=8`, `readSem=8`, `writeSem=4`), but there is no
  *requests-per-second* cap. A loopback caller that issues quick non-
  blocking RPCs (e.g. `dfmt.stats`, `dfmt.search`) faster than the
  `statsTTL` (5s) and the BM25 lookup window can keep the daemon CPU-
  bound. The threat is moot in practice because every caller has already
  cleared the loopback boundary (i.e. is the operator) and because
  `dfmt.stats` is memoised, but the code does not actively protect
  against same-host-attacker scenarios.
- **Attack scenario:** A second local user on a multi-tenant dev box who
  has somehow gained read access to the port file (the file is 0o600,
  so this requires either UID match or a separate filesystem flaw)
  sustains 10k QPS of `dfmt.search` to pin a CPU core. Blast radius:
  one CPU; the daemon idle-timer still observes activity and stays up.
- **Evidence:** `acquireLimiter(ctx, h.readSem)` blocks at 8-deep but
  immediately returns to serve the next request. No token-bucket / leaky-
  bucket / per-IP rate limit anywhere.
- **Remediation:** If multi-user host hardening becomes in scope, add a
  per-process token-bucket on the dispatch entry (e.g. 100 RPS sustained,
  burst 200). For the current threat model (single operator on loopback),
  the existing concurrency caps + the 1 MiB body cap + the 5s `ReadHeader`
  cap are sufficient.
- **Confidence:** Medium (theoretical; not exercised by any known
  attacker path under the documented design).

### Verified-clean controls

| Control | Location | Status |
|---|---|---|
| Body 1 MiB cap | `http.go:369,630` | PASS |
| Header 16 KiB cap | `http.go:170` | PASS |
| Slowloris (5s `ReadHeaderTimeout`) | `http.go:167` | PASS |
| Read 30s, Write 30s, Idle 60s timeouts | `http.go:166,168,169` | PASS |
| Socket per-connection 60s read idle + 30 min hard cap | `socket.go:17,25,180-183,196-199` | PASS |
| Per-frame JSON-RPC line cap 1 MiB | `jsonrpc.go:38,77-79` | PASS |
| Exec concurrency cap 4 | `handlers.go:145,1148` | PASS |
| Fetch concurrency cap 8 | `handlers.go:146,1404` | PASS |
| Read/Glob/Grep concurrency cap 8 | `handlers.go:147,1279,1489,1517` | PASS |
| Write/Edit concurrency cap 4 | `handlers.go:148,1587,1616` | PASS |
| Subprocess output cap (`MaxRawBytes`) with stream-and-discard drain | `permissions.go:1834-1845` | PASS |
| HTTP fetch body cap (`MaxFetchBodyBytes` 8 MiB) | `permissions.go:1280` | PASS |
| Sandbox read in-memory cap (`MaxSandboxReadBytes` 4 MiB) | `permissions.go:958,1042-1046` | PASS |
| Default 30s fetch timeout when caller passes 0 | `permissions.go:1216-1219` | PASS |
| `/api/stats` 5s TTL cache (avoids journal re-stream on poll) | `handlers.go:86-87,892-899` | PASS |
| Recall per-tier FIFO bucket caps | `handlers.go:715-749` | PASS |
| Index BM25 / trigram bounded result limit (default 10) | `handlers.go:610-612` | PASS |
| Grep regex node / repeat-depth caps (ReDoS guard) | `permissions.go:29-32` | PASS |
| HTTP fetch redirect cap 10 | `permissions.go:1267-1270` | PASS |
| stash dedup cache cap (`dedupCap=64`) | `handlers.go:108,326-338` | PASS |
| Wire-dedup sent cache cap (`sentCap=256`) | `handlers.go:120,396-400` | PASS |
| Regex compile LRU cap (`regexLRUMaxEntries=512`) | `permissions.go:406,444-451` | PASS |
| Socket Stop drain bounded at 5s | `socket.go:57,355-363` | PASS |

### Confidence: High on the inventory; Medium on the residual rate-limit
gap (untested end-to-end against a malicious local user).
