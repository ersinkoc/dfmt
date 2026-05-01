# SSRF Security Assessment — DFMT Fetch Tool

**Target:** `dfmt_fetch` (sandbox HTTP client at `internal/sandbox/permissions.go`)
**Date:** 2026-05-01
**Assessor:** sc-ssrf skill
**Reference:** SKILL.md (sc-ssrf v1.0.0)

---

## Summary

DFMT's fetch implementation has a layered SSRF defense in depth: policy-level URL blocklists, IP-based blocklisting with DNS rebinding protection, redirect re-checking, timeout enforcement, header injection protection, and response size capping. These are well-implemented and defend against the majority of SSRF attack classes.

However, several gaps exist that could allow a determined attacker to access internal cloud metadata endpoints or exfiltrate data via DNS.

**Overall Risk:** Medium
**Key Risk:** Azure IMDS (168.63.129.16) not blocked; GCP metadata.goog variants not in policy; IP encoding bypasses the policy regex layer.

---

## Findings

### SSRF-001 — Azure IMDS Endpoint Not Blocked (CVSS 6.5/Medium)

**File:** `internal/sandbox/permissions.go:1436` (`isBlockedIP`)
**Severity:** Medium | **Confidence:** 95

**Description:**

The `isBlockedIP` function blocks `169.254.169.254` (AWS/GCP metadata) and RFC1918 private ranges but does **not** block `168.63.129.16`, which is the Azure Instance Metadata Service (IMDS) endpoint. Azure VMs use this IP for metadata retrieval, and it is a well-known SSRF target in cloud environments.

AWS `169.254.169.254` and GCP `metadata.google.internal` are blocked at two layers:
- Policy-level: `internal/sandbox/permissions.go:295-300`
- IP-level: `internal/sandbox/permissions.go:1448`

Azure's `168.63.129.16` is only blocked as part of RFC1918 (`IsPrivate()`), which only covers `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`. The Azure IMDS IP is not in any private range and would pass the IP check.

**Impact:** An attacker who tricks an agent into fetching `http://168.63.129.16/latest/meta-data/instance-id` could retrieve Azure VM identity information.

**Remediation:**
```go
// Azure IMDS
if ip.Equal(net.IPv4(168, 63, 129, 16)) {
    return true
}
```

**References:** CWE-918, Azure IMDS docs

---

### SSRF-002 — GCP metadata.goog Variants Not in Policy Blocklist (CVSS 4.3/Medium)

**File:** `internal/sandbox/permissions.go:295-300` (policy), `internal/sandbox/permissions.go:1404` (code)
**Severity:** Medium | **Confidence:** 90

**Description:**

The policy-level blocklist covers:
- `http://169.254.169.254/*` / `https://169.254.169.254/*`
- `http://metadata.google.internal/*` / `https://metadata.google.internal/*`
- `http://metadata.goog/*` / `https://metadata.goog/*`

However, `metadata.goog.internal` and `metadata.goog.com` (GCP's actual metadata hostname) are not covered at the policy level. The code-level check at line 1404 only handles `metadata.google.internal` and `metadata.goog`, not the `.internal` variant or any other GCP metadata domains.

An attacker could use `http://metadata.goog.internal/` or an IP-based bypass if DNS resolves to the metadata IP.

**Impact:** Access to GCP service account tokens via the metadata server.

**Remediation:** Add to both policy deny list and the `assertFetchURLAllowed` hostname check:
```go
if lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog" || lowerHost == "metadata.goog.internal" {
```

**References:** GCP metadata server documentation

---

### SSRF-003 — IP Octal/Hex/Dword Encoding Bypasses Policy Regex (CVSS 5.5/Medium)

**File:** `internal/sandbox/permissions.go:1360-1372` (`normalizeFetchURLForPolicy`)
**Severity:** Medium | **Confidence:** 75

**Description:**

The `normalizeFetchURLForPolicy` function lowercases the scheme and host before passing to the policy regex engine:

```go
func normalizeFetchURLForPolicy(rawURL string) string {
    u, err := url.Parse(rawURL)
    if err != nil {
        return rawURL
    }
    if u.Scheme != "" {
        u.Scheme = strings.ToLower(u.Scheme)
    }
    if u.Host != "" {
        u.Host = strings.ToLower(u.Host)
    }
    return u.String()
}
```

This is only a string normalization. It does **not** canonicalize IP address representations. An attacker can use:

- **Octal:** `http://0177.0.0.1/` → parses as `127.0.0.1` (in some contexts)
- **Hex dword:** `http://0x7f000001/` → could bypass regex-based blocklist
- **IPv6 mapped:** `http://[::ffff:127.0.0.1]/`

The policy blocklist patterns like `http://169.254.169.254/*` match literal hostname strings, not parsed IP literals. If `url.Parse` produces a `Host` field containing the encoded form that does not equal `169.254.169.254`, the policy regex would not match.

The code-level IP check (`isBlockedIP` + `net.ParseIP`) would catch `0x7f000001` because Go's `net.ParseIP` handles hex and decimal forms. However, the policy layer would miss it.

**Impact:** An attacker with a custom DNS server could encode `127.0.0.1` as `2130706433` or `0x7f000001` to bypass policy-level glob matching while still passing the IP check. However, the code-level `assertFetchURLAllowed` does call `net.ParseIP` before DNS lookup, so the IP bypass alone is caught.

The combined policy + code approach is defense-in-depth. The risk is a corner case where the policy layer gives a false sense of protection.

**Remediation:** Consider adding an explicit IP canonicalization step:
```go
func canonicalizeIPHost(host string) string {
    if ip := net.ParseIP(host); ip != nil {
        return ip.String()
    }
    return host
}
```

**References:** CWE-918, OWASP SSRF prevention Cheat Sheet

---

### SSRF-004 — No AWS IPv6 Metadata Address Block (CVSS 3.7/Low)

**File:** `internal/sandbox/permissions.go:1447-1450`
**Severity:** Low | **Confidence:** 85

**Description:**

`isBlockedIP` blocks `169.254.169.254` (IPv4 AWS metadata), but AWS also exposes metadata at the IPv6 link-local address `fd00:ec2::254`. This address is not blocked by any current check:

- `IsLoopback()`: No
- `IsPrivate()`: No (ULA, but not RFC1918)
- `IsLinkLocalUnicast()`: `fd00:ec2::254` is ULA, not link-local
- Hard-coded block: Only `169.254.169.254` (IPv4)

**Impact:** On IPv6-enabled instances, an attacker could reach `http://[fd00:ec2::254]/latest/meta-data/` to retrieve instance metadata.

**Remediation:**
```go
// AWS IPv6 metadata (link-local ULA, but used by AWS)
if ip.Equal(net.ParseIP("fd00:ec2::254")) {
    return true
}
```

---

### SSRF-005 — Redirect URL SSRF Re-Check Does Not Include Cloud Metadata Hostname Check (CVSS 4.3/Medium)

**File:** `internal/sandbox/permissions.go:1561-1566`
**Severity:** Medium | **Confidence:** 80

**Description:**

The `CheckRedirect` callback re-validates redirect targets via `assertFetchURLAllowed`:

```go
CheckRedirect: func(r *http.Request, via []*http.Request) error {
    if len(via) >= 10 {
        return errors.New("stopped after 10 redirects")
    }
    return assertFetchURLAllowed(r.URL.String())
},
```

However, `assertFetchURLAllowed` (lines 1377-1431) checks cloud metadata **hostnames** only via string comparison on `lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog"`. The policy-level blocklist for metadata hostnames (`http://metadata.google.internal/*`, etc.) is **not** applied to redirect URLs.

If a safe public URL redirect chain leads to `http://metadata.google.internal/...`, the hostname check would catch it (since `lowerHost == "metadata.google.internal"` is present). But if the hostname is encoded differently (e.g., `METADATA.GOOGLE.INTERNAL`), the code-level check would handle it because `assertFetchURLAllowed` lowercases before comparing.

The policy-level cloud metadata denials (`permissions.go:297-298`) only apply to the **initial** URL policy check at line 1471, not to redirect targets.

**Impact:** If a legitimate site implements a redirect to a cloud metadata hostname, the policy layer would not catch it (only the code-level check would).

**Remediation:** The code-level `assertFetchURLAllowed` should be sufficient for redirect targets. However, to be consistent with the policy-layer defense, consider applying `PolicyCheck("fetch", normalizeFetchURLForPolicy(r.URL.String()))` in the redirect callback as well.

---

### SSRF-006 — No Out-of-Band SSRF Detection / DNS Rebinding Monitoring (CVSS 2.1/Low)

**File:** `internal/sandbox/permissions.go:1419` (LookupIP), `internal/sandbox/permissions.go:1537` (DialContext LookupIP)
**Severity:** Low | **Confidence:** 60

**Description:**

The implementation correctly defends against **DNS rebinding attacks** via the custom `DialContext` that resolves IPs and validates them before connecting (lines 1522-1549). This is well-implemented.

However, there is no monitoring or alerting when a blocked address is encountered. If an attacker probes internal addresses, there is no log event, no metric, and no operator notification. The `ErrBlockedHost` error is returned to the caller, but no security event is recorded in the journal.

For a blind SSRF scenario where the attacker cannot see the response (internal service returns no data), the agent may just see an error and retry. The operator has no visibility into the SSRF probe.

**Impact:** Low — the attack is still blocked. But without alerting, operator cannot detect sustained SSRF probing attempts.

**Remediation:** Consider emitting a security audit event when `ErrBlockedHost` is returned:
```go
h.logEvent(ctx, "security.ssrf_blocked", req.URL, map[string]any{
    "host": host,
    "reason": err.Error(),
})
```

---

### SSRF-007 — URL Scheme Allowlist Enforced Only Via Policy Regex (CVSS 3.1/Low)

**File:** `internal/sandbox/permissions.go:1382-1387` (`assertFetchURLAllowed`)
**Severity:** Low | **Confidence:** 90

**Description:**

`assertFetchURLAllowed` only allows `http` and `https`:

```go
switch strings.ToLower(u.Scheme) {
case "http", "https":
    // ok
default:
    return fmt.Errorf("%w: unsupported scheme %q", ErrBlockedHost, u.Scheme)
}
```

This is correct and blocks `file://`, `gopher://`, `dict://`, etc. at the code level. The policy-level blocklist also explicitly denies `file://*`.

However, the **default policy** (`DefaultPolicy()`, line 300) only has `file://*` in the deny list. There is no explicit allowlist for fetch schemes. An operator override that removes the `file://*` deny rule could inadvertently allow `gopher://` or other schemes.

**Impact:** Low — the code-level check would still block non-HTTP(S) schemes. But the security boundary should be explicit.

**Remediation:** Add an explicit scheme restriction in `assertFetchURLAllowed` (already done). Consider requiring a minimum policy rule: `deny:fetch:file://*` cannot be removed by operator override.

---

## Positive Security Controls (Not Vulnerabilities)

The following are correctly implemented and should be maintained:

| Control | Location | Notes |
|---|---|---|
| Cloud metadata IP block (AWS) | `permissions.go:1448` | Blocks `169.254.169.254` |
| Cloud metadata hostname block (GCP) | `permissions.go:1404` | `metadata.google.internal`, `metadata.goog` |
| RFC1918 private IP block | `permissions.go:1444` | `IsPrivate()` covers 10/8, 172.16/12, 192.168/16 |
| Loopback IP block | `permissions.go:1440` | `IsLoopback()` |
| Link-local / multicast block | `permissions.go:1440-1441` | `IsLinkLocalUnicast()`, `IsMulticast()`, etc. |
| DNS rebinding protection | `permissions.go:1522-1549` | Custom `DialContext` resolves and validates IPs before dial |
| Redirect re-checking | `permissions.go:1561-1566` | `CheckRedirect` calls `assertFetchURLAllowed` on every hop |
| Redirect hop limit | `permissions.go:1562-1563` | Max 10 redirects |
| Timeout enforcement | `permissions.go:1508-1513` | Default 30s, per-request override, guards against zero |
| Header CR/LF injection protection | `permissions.go:1501-1505` | Rejects headers with `\r`, `\n`, or `:` in key |
| IPv6 zone-id rejection | `permissions.go:1398-1399` | Blocks `[fe80::1%eth0]` |
| Response body size cap | `permissions.go:1574-1575` | `MaxFetchBodyBytes` = 8 MiB |
| TLS configuration | `permissions.go:1551-1554` | Uses Go's default TLS handshake settings |
| Policy normalization | `permissions.go:1360-1372` | Lowercases scheme/host for case-insensitive regex matching |

---

## CVSS Scores Summary

| ID | Title | CVSS v3.1 Vector | Score |
|---|---|---|---|
| SSRF-001 | Azure IMDS Endpoint Not Blocked | AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:L/A:L | 6.5 (Medium) |
| SSRF-002 | GCP metadata.goog Variants Not Blocked | AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:L/A:N | 4.3 (Medium) |
| SSRF-003 | IP Encoding Bypasses Policy Regex | AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:N/A:N | 5.5 (Medium) |
| SSRF-004 | AWS IPv6 Metadata Address Not Blocked | AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N | 3.7 (Low) |
| SSRF-005 | Redirect SSRF Re-check Gap | AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:L/A:N | 4.3 (Medium) |
| SSRF-006 | No SSRF Probe Alerting | AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N | 2.1 (Low) |
| SSRF-007 | URL Scheme Only Policy-Protected | AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:N/A:N | 3.1 (Low) |

---

## Remediation Priority

1. **SSRF-001** — Add Azure IMDS IP `168.63.129.16` to `isBlockedIP`
2. **SSRF-002** — Extend cloud metadata hostname checks to cover all GCP variants
3. **SSRF-004** — Add AWS IPv6 metadata address `fd00:ec2::254` to `isBlockedIP`
4. **SSRF-003** — Consider IP canonicalization for policy regex layer (informational, low priority)
5. **SSRF-006** — Add security event logging when `ErrBlockedHost` is triggered