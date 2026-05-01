# sc-deserialization — Insecure Deserialization

**Scan date:** 2026-05-01
**Skill:** sc-deserialization v1.0.0
**DFMT root:** `D:\Codebox\PROJECTS\DFMT`

---

## Active Serialization Formats

| Format | File | Library | Risk |
|--------|------|---------|------|
| JSON | `index.gob` (payload), `index.cursor`, `journal.jsonl` | `encoding/json` | Safe |
| Line-format | `permissions.yaml` | Hand-rolled parser | Safe |
| YAML | `config.yaml`, `redact.yaml` | `gopkg.in/yaml.v3` | Low |

No `gob` encoder/decoder is invoked anywhere in the codebase. The `.gob`
extension on `index.gob` is a backwards-compat filename only.

---

## Finding: DESER-01 — Verified safe: `index.gob` is JSON, not gob

- **Severity:** Info
- **Confidence:** High
- **File:** `internal/core/index_persist.go:71` (`writeJSONAtomic`)

DFMT **never uses `encoding/gob`**. The comment on line 31 says "gob" in the
context of a saved search session (which is not yet implemented), but the
actual `PersistIndex` function calls `writeJSONAtomic`, which calls
`json.Marshal`. The `.gob` extension is a backwards-compat artifact from a
planned feature that never shipped.

```go
// PersistIndex saves the index and cursor to disk atomically.
func PersistIndex(index *Index, path string, hiULID string) error {
    if err := writeJSONAtomic(path, index); err != nil {  // JSON, not gob
        return err
    }
    ...
}
```

`LoadIndex` reads back via `json.Unmarshal`. Go's `encoding/json` is a
type-safe format with no arbitrary code execution surface.

**No gob deserialization attack surface.**

---

## Finding: DESER-02 — Verified safe: `yaml.v3` does not instantiate Go types

- **Severity:** Info
- **Confidence:** High
- **Files:** `internal/config/config.go`, `internal/redact/load.go`, `internal/sandbox/structured.go`

DFMT uses `gopkg.in/yaml.v3 v3.0.1` for `config.yaml` and `redact.yaml`
parsing. Unlike SnakeYAML in unsafe mode, `gopkg.in/yaml.v3` does not honor
`!!python/object` or other language-specific tags. It unmarshals YAML into
`map[string]any` / `[]any` / `string` / `float64` / `bool` / `nil` — no
instantiation of arbitrary Go types.

`structured.go` uses `yaml.NewDecoder(...).Decode(&v)` where `v` is
`map[string]any`, then walks the tree with recursive type switches. No
arbitrary struct instantiation.

---

## Finding: DESER-03 — Low: `yaml.v3` recursive alias expansion (DoS vector)

- **Severity:** Low
- **Confidence:** Medium
- **File:** `internal/sandbox/structured.go:132`

`yaml.v3` internally bounds alias expansion (billion laughs mitigation),
but `SetMaxAliasCount` is not called explicitly. A maliciously crafted YAML
with deeply nested aliases could cause exponential expansion before the
internal limit kicks in, producing a panic or OOM.

The code comment acknowledges this:
```
// yaml.v3 v3.0.1's internal alias-bomb fix (GO-2022-0956).
// yaml.v3 does not expose SetMaxAliasCount, so we cap upstream
```

**Impact:** DoS only. No RCE. `structured.go` processes agent-generated YAML
from MCP tool responses, not attacker-controlled external input.

**Remediation:** Track upstream for a `SetMaxAliasCount` equivalent; consider
a process-level timeout on `Decode`.

---

## Finding: DESER-04 — Verified safe: Journal JSONL is type-safe, events are server-generated

- **Severity:** Info
- **Confidence:** High
- **File:** `internal/core/journal.go:353` (`streamFile`)

Journal entries are JSONL. Each `Append` call generates a ULID, computes a
SHA-256 signature (`e.Sig = e.ComputeSig()`), and writes the event. During
replay, `streamFile` calls `json.Unmarshal(line, &e)` into a typed `Event`
struct, then calls `e.Validate()` which verifies the signature before the
event is used for indexing or recall.

The `Event.Data` field is `map[string]any`, meaning `json.Unmarshal`
produces `map[string]any` values — not arbitrary Go types. There is no
type confusion path because:

1. All event fields are typed (`ID string`, `TS time.Time`, `Type string`,
   `Source string`, etc.)
2. Events are server-generated with cryptographic signatures
3. `Validate()` rejects tampered entries

**No injection or type-confusion exploit path through journal replay.**

---

## Finding: DESER-05 — Verified safe: HTTP/MCP JSON-RPC params are typed

- **Severity:** Info
- **Confidence:** High
- **Files:** `internal/transport/http.go:409,469`, `internal/transport/jsonrpc.go`

HTTP handler at line 409:
```go
if err := json.Unmarshal(body, &req); err != nil { ... }
```

`req` is a typed `Request` struct with `Params json.RawMessage`. Each
method handler calls `decodeRPCParams(req, &params)` which unmarshals into
the specific typed param struct (`SearchParams`, `RecallParams`,
`RememberParams`, `ExecParams`, etc.). No interface-based type dispatch or
type-name-based polymorphism.

**No unsafe deserialization in the RPC transport layer.**

---

## Finding: DESER-06 — Verified safe: `permissions.yaml` is NOT parsed as YAML

- **Severity:** Info
- **Confidence:** High
- **File:** `internal/sandbox/permissions.go:254` (`LoadPolicy`)

Despite the `.yaml` extension, `LoadPolicy` uses a hand-rolled line parser:
```
allow:exec:git *
deny:exec:rm *
```

Each line is split and matched with `strings.Fields` / `strings.HasPrefix`.
No YAML decoder touches this file. The security properties (hard-deny
invariant on exec allows, 0o600 file mode) are enforced by the policy
merge logic, not by a YAML parser.

Comment in code: "Simple format — a future refactor might switch to
yaml.v3 and should set KnownFields(true) + MaxAliasCount if it does."

---

## Finding: DESER-07 — Verified safe: index file permissions are 0o600

- **Severity:** Info
- **Confidence:** High
- **File:** `internal/core/index_persist.go:120`

```go
if err := os.Chmod(tmpName, 0o600); err != nil {
    return err
}
```

Both `index.gob` and `index.cursor` are written with mode `0o600` via
`writeRawAtomic`. The directory `.dfmt/` itself is `0o700`. This is
confirmed across all write paths (journal, config, permissions, etc.).

---

## Finding: DESER-08 — Info: `yaml.v3` version is unmaintained (H-6 tracking)

- **Severity:** Info
- **Confidence:** High
- **File:** `go.mod` (`gopkg.in/yaml.v3 v3.0.1`, last release 2022-05-27)

`yaml.v3 v3.0.1` is no longer actively maintained upstream. If a CVE is
published for this version, the fix may come from a community fork
(`goccy/go-yaml`, `go-yaml/yaml`).

Current status: `yaml.v3` is only used for operator-supplied config files
(`config.yaml`, `redact.yaml`) — not attacker-controlled input. The DoS
vector (DESER-03) has internal mitigations. Track for alternative.

---

## Summary

| ID | Finding | Severity | Confidence |
|----|---------|----------|------------|
| DESER-01 | No gob deserialization; index is JSON | Info | High |
| DESER-02 | yaml.v3 does not instantiate Go types | Info | High |
| DESER-03 | yaml.v3 recursive alias expansion (DoS, internal limits) | Low | Medium |
| DESER-04 | Journal JSONL: server-generated, signed, validated | Info | High |
| DESER-05 | HTTP/MCP params are typed structs, no interface dispatch | Info | High |
| DESER-06 | permissions.yaml is hand-parsed, not YAML | Info | High |
| DESER-07 | Index files are mode 0o600 | Info | High |
| DESER-08 | yaml.v3 v3.0.1 is unmaintained upstream | Info | High |

**No exploitable deserialization vulnerabilities.** All deserialization
targets typed structs or well-defined data types. Events are
server-generated with cryptographic signatures (ULID + SHA-256). The only
realistic risk is the unmaintained `yaml.v3` library receiving a future CVE
— currently no known exploits. DESER-03 (DoS via alias expansion) is
mitigated by yaml.v3's internal bounds but would benefit from explicit
`SetMaxAliasCount` if the upstream exposes it.

**Previous report note:** The CLAUDE.md note claiming `index.gob` uses gob
serialization is stale — `index_persist.go` switched to JSON in 2025.