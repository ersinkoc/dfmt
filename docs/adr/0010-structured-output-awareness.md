# ADR-0010: Structured-Output Awareness in NormalizeOutput

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-28 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | ADR-0008 (HTML parser) |

## Context

`NormalizeOutput` (`internal/sandbox/intent.go`) currently runs three text-shape transforms on every captured tool body:

1. `stripANSI` — drops CSI/OSC escape sequences.
2. `collapseCarriageReturns` — keeps only the final state of CR-rewritten lines.
3. `runLengthEncode` — squashes ≥ 4 consecutive identical lines.

These earn their keep on shell-spam shapes (progress bars, spinners, retry loops). They do nothing for **JSON-shaped output** — the kind produced by `gh api`, `kubectl get -o json`, `aws … --output json`, `docker inspect`, `terraform show -json`. In those outputs, the bytes the agent actually cares about (kind, name, status, error messages) are buried under timestamps, ETags, IDs, and self-referential URLs that DFMT then ships across the wire as opaque text.

Sample (`gh api repos/foo/bar/issues`, single issue, ~2.4 KB raw): `created_at`, `updated_at`, `node_id`, `id`, `url`, `html_url`, `events_url`, `repository_url`, `labels_url`, `comments_url`, `_links`. After dropping those: ~600 bytes. ~75% reduction on a single object; an issue list multiplies that.

ADR-0008 already established the pattern of **format-aware compaction** (HTML → plain text via a bundled parser). Structured JSON is the next obvious shape, and `encoding/json` is already in the runtime.

## Decision

`NormalizeOutput` gains a fourth transform, `CompactStructured`, run after RLE:

```go
s = stripANSI(s)
s = collapseCarriageReturns(s)
s = runLengthEncode(s)
s = CompactStructured(s)
```

`CompactStructured`:

- Trims leading whitespace, checks for `{` or `[` as the first non-space byte. Otherwise returns the input unchanged.
- Calls `json.Valid` on the trimmed bytes. Invalid JSON (e.g. NDJSON, partial output, log lines that happen to contain a brace) returns the input unchanged.
- Decodes via `json.Unmarshal` into `any`, walks the result recursively, drops keys whose name is in a small **noise field set**, then re-marshals with `json.Marshal` (compact form, no extra whitespace).
- The noise field set is a `map[string]struct{}` declared as a `var` in `internal/sandbox/structured.go`:
  - `created_at`, `updated_at`
  - `etag`
  - `_links`
  - `node_id`
  - `url`, `html_url`
  - other `*_url` fields ending in standard REST suffixes (`events_url`, `labels_url`, `comments_url`, `repository_url`)

The `*_url` family is matched by suffix-checking in the walk (any key ending in `_url` is dropped) so we don't have to enumerate every CRUD-style endpoint Github surfaces.

`id` is **not** in the default drop list. Numeric IDs are sometimes the only stable handle for an object; dropping them silently would change the agent's ability to reason about object identity. Users who want them gone can opt-in via a future `DFMT_STRUCTURED_DROP_ID` env knob — out of scope here.

The walk is recursive and shape-preserving:

- Object → object minus dropped keys; nested objects/arrays recurse.
- Array → array of recursed elements; preserves order.
- Scalars → unchanged.

Output size cap: if the *output* of compaction is larger than the input (pathological case, e.g. JSON containing nothing but our drop-list), we return the original. Compaction must never regress wire bytes.

## Trade-offs

**Information loss is real.** An agent that needs to know exactly when an issue was created, or wants to follow `_links.html`, will not get those bytes. Mitigation:

- The drop list is intentionally small and biased toward fields that are reconstructible (`html_url` ≡ `https://github.com/{owner}/{repo}/issues/{number}`) or rarely useful for the agent's reasoning task.
- `Return: "raw"` reaches the caller before `NormalizeOutput` runs at all (the raw mode bypasses the entire filter pipeline). Agents that need full structure opt in.

**Cost.** A single `json.Valid` + `json.Unmarshal` + walk + `json.Marshal` per tool call. For a 64 KB payload this is ~1-2 ms on a 2024-era laptop. Worth it given the wire savings; the cap on `MaxRawBytes` (256 KB) bounds the worst case.

**Why not a JSON-aware *parser* (in the ADR-0008 sense, custom-built)?** `encoding/json` is stdlib, already imported, well-tested, and does all the heavy lifting. The walk we add on top is ~30 LOC. A custom parser would only earn its keep if we needed sub-millisecond decode times or partial-document compaction; we don't.

**False positives — non-JSON output that starts with `{` or `[`.** Pretty-printed Python dict reprs, Lisp output, certain `tree`(1) emoji modes. `json.Valid` weeds these out. The cost of the check is the parse pass, but the alternative — eyeballing for a more rigorous detector — is the same parse pass dressed up.

**NDJSON (newline-delimited JSON, e.g. `kubectl get pods -o json | jq -c '.items[]'`).** First-line check fails because the document has multiple roots. NDJSON falls through to no-op. A future ADR could add per-line detection; deferred.

**Drop list as code, not config.** Per project policy on adding new dependencies, a YAML- or JSON-driven drop list is overkill. The list will rarely change; when it does, the change ships with a code review. Operators who need a different list can always pre-process bodies in their own wrapper.

## Consequences

- New file `internal/sandbox/structured.go` (~80 LOC including comments) and `internal/sandbox/structured_test.go`.
- `NormalizeOutput` gains one line.
- `cmd/dfmt-bench/tokensaving.go` gets a new scenario — `gh api`-shape issue list — so the bench surfaces the savings in a stable way.
- No new third-party deps. `encoding/json` is already used by `internal/transport` and the journal.
- `MaxRawBytes` cap continues to apply; bodies above it are truncated before this transform.

## Verification

- `internal/sandbox/structured_test.go`: GitHub issue sample → noise fields gone, `title`/`number`/`state` retained; partial-JSON → unchanged; NDJSON → unchanged; non-JSON → unchanged; pathological JSON-of-only-drop-list-keys → unchanged (cap regression guard).
- `make test` and `go test -race ./internal/sandbox/...` stay green.
- `go run ./cmd/dfmt-bench tokensaving` shows a new "gh api" line and the TOTAL Modern column drops further.

## Alternatives considered

1. **Path-aware drop list (e.g. `events.created_at` keeps; `issues.created_at` drops).** Too brittle. The benefit is small; the failure modes are obscure.
2. **Schema-driven compaction (e.g. detect `kind: issue` and apply an issue-specific projection).** Over-engineered. The flat drop list captures most of the wins.
3. **Run `CompactStructured` only when intent is provided.** Tempting (an empty intent suggests the agent didn't know what it wanted), but the bench shows the dominant `gh api`/`kubectl` cases call without intent. Always-on wins more.
