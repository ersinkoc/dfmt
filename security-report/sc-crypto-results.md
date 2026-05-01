# sc-crypto — Cryptography Misuse Scan Results

Scope: hash/cipher choice, IV/nonce handling, PRNG source, constant-time compare, TLS configuration, error handling for `crypto/rand`.

## Summary

DFMT uses cryptography sparingly and stays inside stdlib. All hashing is `crypto/sha256`. There is **no** use of `crypto/md5`, `crypto/sha1`, `crypto/des`, `crypto/rc4`, `math/rand`, ECB-mode cipher constructions, or hardcoded IVs anywhere in the runtime tree. No HTTPS-disabling `InsecureSkipVerify` was found. The single non-trivial randomness source — ULID generation in `internal/core/ulid.go` — uses `crypto/rand` and has a documented fallback path that downgrades to a pid+counter+nanotime mix on `crypto/rand.Read` failure (logged via `logging.Warnf`). For the ULID use case (event identity + monotonic ordering) the fallback is acceptable; ULIDs are not used as auth tokens.

## Findings

### CRYPTO-01 — Event signature `==` compare is not constant-time
- **Severity:** Low (Info)
- **CWE:** CWE-208 (Observable Timing Discrepancy)
- **File:** `internal/core/event.go:176`
- **Description:** `Event.Validate` compares the stored 16-hex-char signature against a freshly computed `ComputeSig()` using Go string `==`. Go's string compare is not constant-time. In an authentication context this would be a timing oracle for forging signatures.
- **Impact:** Negligible in DFMT's threat model. The signature is an integrity tag for events stored in `.dfmt/journal.jsonl` (mode 0600, owner-only). It is not used as an authentication token; an attacker who could time the comparison would already need read+write access to the journal, at which point they can rewrite events directly. Listed as Info for completeness.
- **Evidence:**
  ```go
  // internal/core/event.go:172-177
  func (e *Event) Validate() bool {
      if e.Sig == "" {
          return true
      }
      return e.Sig == e.ComputeSig()
  }
  ```
- **Remediation:** Replace with `subtle.ConstantTimeCompare([]byte(e.Sig), []byte(e.ComputeSig())) == 1` if Validate ever moves into a network-exposed code path.
- **Confidence:** High (correct call shape, low impact).

### CRYPTO-02 — `dedupCache` and `sentCache` keys hashed with SHA-256 over `kind\x00source\x00body` — unauthenticated, but not security-relevant
- **Severity:** Info
- **CWE:** N/A
- **File:** `internal/transport/handlers.go:282-290`
- **Description:** `stashDedupKey` and `sentCacheKey` use plain SHA-256 (no HMAC). This is fine — the keys are local cache lookups, not signatures or MACs. Flagged only to note the choice was deliberate (plain hash, not a MAC) and is correct for this use case.
- **Impact:** None.
- **Evidence:** `internal/transport/handlers.go:282-290`.
- **Remediation:** No change.
- **Confidence:** High.

### CRYPTO-03 — ULID fallback on `crypto/rand` failure produces predictable randomness
- **Severity:** Low
- **CWE:** CWE-330 (Insufficient Randomness in Fallback)
- **File:** `internal/core/ulid.go:47-57`
- **Description:** When `rand.Read(lastRandom[:])` fails (rare on real systems but possible under entropy starvation, seccomp confinement, or a future Linux kernel that returns EINTR), the code falls back to a deterministic `pid << 32 | counter ^ nanotime` mix. Comment is explicit: "ULIDs minted through the fallback are still unique-per-process (the counter guarantees it) but lose unpredictability."
- **Impact:** ULIDs are used as event IDs (`Event.ID`) and content-store chunk-set IDs. They are not auth tokens — an agent or attacker who could predict a future ULID gains nothing because lookup by ID in `Index.Excerpt`, `content.Store.GetChunkSet`, etc. all require the daemon to have already emitted that ID to that session. Risk surfaces only if a future feature treats a ULID as a capability token.
- **Evidence:**
  ```go
  // internal/core/ulid.go:47-57
  if _, err := rand.Read(lastRandom[:]); err != nil {
      logging.Warnf("crypto/rand.Read failed for ULID: %v (using fallback seed)", err)
      ulidFallbackCtr++
      mix := uint64(os.Getpid())<<32 | ulidFallbackCtr
      binary.BigEndian.PutUint64(lastRandom[:8], mix^uint64(ts.UnixNano()))
      binary.BigEndian.PutUint16(lastRandom[8:], uint16(ulidFallbackCtr))
  }
  ```
- **Remediation:** If a future feature uses ULIDs as auth tokens, return an error from `NewULID` instead of falling back. Today the warning + degrade is the right trade-off for an event-ID minter that must not crash the daemon.
- **Confidence:** High (correct trade-off for the current threat model, flagged as a future-facing risk).

### CRYPTO-04 — TLS verification not disabled anywhere; redirect-aware fetch transport is correctly configured
- **Severity:** Info (positive finding)
- **File:** `internal/sandbox/permissions.go:1220-1272`
- **Description:** The fetch transport correctly resolves DNS once via `net.DefaultResolver.LookupIP`, validates every returned IP with `isBlockedIP` (RFC1918, loopback, cloud metadata), and dials the resolved literal IP — closing the DNS-rebinding window between SSRF check and connect. `CheckRedirect` re-runs the block-check on each redirect. No `InsecureSkipVerify`, no overridden `TLSClientConfig`. This is a clean implementation.
- **Confidence:** High.
