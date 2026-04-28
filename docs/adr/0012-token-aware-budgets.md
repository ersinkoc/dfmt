# ADR-0012: Token-Aware Policy Budgets

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-04-28 |
| Deciders | Ersin Koç |
| Supersedes | — |
| Related | ADR-0006 (sandbox), ADR-0010 (structured-output) |

## Context

Until now every policy decision in the sandbox gated on **byte counts**:

- `InlineThreshold = 4 KB` — bodies under this ship inline; over it go through summary+matches.
- `MediumThreshold = 64 KB` — bigger match/vocab caps kick in here.
- `TailBytes = 2 KB` — fallback tail snippet when no match landed.

Agents reading those responses pay a **token** cost, not a byte cost, and the byte→token ratio depends sharply on the content shape:

| Content | Bytes/token (Claude) | A 4 KB body costs… |
|---|---:|---:|
| English ASCII prose | ~4 | ~1024 tokens |
| Code (heavy punctuation) | ~3 | ~1365 tokens |
| Mixed UTF-8 (Turkish, German, Japanese romaji) | ~3 | ~1365 tokens |
| CJK (Chinese, Japanese kanji, Korean Hangul) | ~2 | ~2048 tokens |

So a 4 KB CJK body costs the agent **~2× the tokens** of a 4 KB English body, but the byte gate treated them identically. CJK callers were silently over-budget; English callers got slightly more headroom than expected. The intended outcome of this ADR: the budget reflects what the agent actually pays.

## Decision

**Migrate the inline / medium / tail policy gates from byte counts to approximated token counts.** I/O hard caps (`MaxFetchBodyBytes` 8 MiB, `maxRPCResponseBytes` 16 MiB, `MaxRawBytes` 256 KB stdout truncation) **stay byte-based** — they protect against network bloat / runaway daemon output, where bytes are the right unit.

Concretely:

1. **`internal/sandbox/tokens.go`** (new, ~50 LOC) exposes `ApproxTokens(s string) int`. Heuristic:

   ```
   ApproxTokens(s) = ascii_bytes(s)/4 + non_ascii_rune_count(s)
   ```

   Calibration anchor points (hand-checked against the real Claude tokenizer):

   | Input | Real tokens | ApproxTokens |
   |---|---:|---:|
   | "hello world" | 2-3 | 2 |
   | "the quick brown fox" | 4-5 | 4 |
   | "你好世界" | ~4 | 4 |
   | "merhaba dünya" | 4-5 | 4 |

   All within ~1.0–1.3× of the real count. Sufficient for tier decisions where the threshold has 2× headroom on either side.

2. **New token threshold constants** in `internal/sandbox/sandbox.go`:

   ```
   InlineTokenThreshold  = 1024   // ≈4 KB English; ≈2-3 KB CJK
   MediumTokenThreshold  = 16384  // ≈64 KB English
   TailTokens            = 512    // tail snippet budget
   ```

   Legacy byte constants (`InlineThreshold` / `MediumThreshold` / `MaxRawBytes`) **retained** — `MaxRawBytes` is still the active byte cap on the Windows PowerShell path; the others are kept as documentation anchors and for any test that pins the historical boundary.

3. **`ApplyReturnPolicy` in `internal/sandbox/intent.go`** computes `ApproxTokens(content)` once per call and uses the token thresholds for tier selection.

4. **`tailTokenSnippet`** is a new helper that walks back through runes accumulating tokens until the budget is met, then defers to the existing `tailLines` for the UTF-8-aware cut + marker. Direction-aware (short-circuits when the whole body fits the budget).

5. **`truncate(line, 120)` in `MatchContent`** stays byte-based. Match lines are short and the byte cap is fine; converting to tokens here would not measurably change behavior.

6. **The 7 byte-pinned tests** in `intent_policy_test.go` / `signals_test.go` use bodies of ~25 KB (well above any boundary in either unit), so they keep passing without changes. The Windows PowerShell truncation test (`permissions_test.go`) intentionally stays byte-based.

## Trade-offs

**Approximation accuracy.** The heuristic is ±25% against the real Claude tokenizer across diverse content. For a policy decision that has ≥2× headroom on either side of the threshold, this is fine: the worst case is that a body slightly under InlineTokenThreshold gets summarized when it could have been inlined, or vice versa. The 75% wire-byte saving from inline→summary swamps any uncertainty in the boundary itself.

**Why not ship a real BPE tokenizer?** Two paths, both rejected:
- **Bundle the vocabulary.** The Claude / OpenAI BPE vocabularies are ~1 MB each. ADR-0004 (stdlib-first) flatly forbids that kind of binary blob in the runtime tree, and the wire savings from a more accurate tokenizer would not approach the disk + memory cost.
- **CGO into `tiktoken-rs` / `tokenizers`.** Would introduce the project's first CGO dependency. ADR-0004 also rules this out, and Go's stdlib doesn't ship any BPE tokenizer.

A future ADR can revisit if (a) a pure-Go BPE library matures, (b) accuracy at the threshold becomes load-bearing for some new feature, or (c) a deliberate vendor blob is acceptable for a specific deployment.

**Why not adjust `MaxRawBytes` / `MaxFetchBodyBytes` to tokens?** Those caps protect *system invariants* (network bloat, runaway output, JSON-RPC body bound) that are naturally byte-quantified. A 1 MB binary body has zero tokens but should still be capped; converting to tokens would obscure the purpose. Keeping them byte-based is the right call.

**Backward compatibility.** Byte constants stay exported. Code outside the sandbox that referenced them (none does today, but third-party plugins might) keeps compiling. The behavior shift is internal to `ApplyReturnPolicy`, which has no external callers beyond the sandbox itself.

**Whitespace over-counts.** The heuristic counts a long run of spaces as ASCII bytes / 4 = ~25 tokens per 100 spaces. Real BPE tokenizers usually merge whitespace into adjacent tokens. The over-count is irrelevant in practice (whitespace bodies don't approach the threshold) and the inverse — under-counting — would be the dangerous failure mode for inline-vs-summary decisions.

## Consequences

- `internal/sandbox/tokens.go` (new, ~50 LOC) + `internal/sandbox/tokens_test.go` (new, 7 tests).
- `internal/sandbox/sandbox.go` gains 3 token threshold constants.
- `internal/sandbox/intent.go` migrates 3 sites: `if len(content) <= InlineThreshold` → token check, `if len(content) > MediumThreshold` → token check, `tailLines(content, TailBytes)` → `tailTokenSnippet(content, TailTokens)`.
- Bench output (`dfmt-bench tokensaving`) optionally adds a Tokens column showing `ApproxTokens(modern)` so operators can reason in token budgets directly.
- No test outside `internal/sandbox` is affected — the policy change is internal to the sandbox return path.
- `MaxRawBytes`, `MaxFetchBodyBytes`, and the Windows PowerShell truncation cap stay byte-based.

## Verification

- `internal/sandbox/tokens_test.go` covers ASCII, Turkish, CJK, empty, whitespace, JSON shapes, and monotonicity.
- Existing `intent_policy_test.go` / `signals_test.go` tests pass without modification — bodies are well above any boundary in either unit.
- `go test -count=1 ./...` and `go vet ./...` stay green.
- `dfmt-bench tokensaving` shows similar Modern bytes for English-dominated scenarios; CJK content (no current bench scenario) would shift toward summarization at smaller byte counts — the desired behavior.
- Manual smoke: feed a 3 KB Chinese-language fixture to `dfmt_read`; verify `auto` policy returns summary+matches (3 KB Chinese ≈ 1500 tokens > InlineTokenThreshold) instead of inlining.

## Alternatives considered

1. **Keep bytes, document the CJK over-budget as an accepted limitation.** Lowest-effort, but means the ADR-0006 budget contract diverges from what callers actually pay. Rejected.
2. **Add a `Return: "raw"`-style env knob `DFMT_TOKEN_AWARE`.** Hides the decision behind a flag; doubled testing surface; ADR-0010's similar flag (`DFMT_STRUCTURED_DROP_ID`) is for an info-loss case, this one isn't. Rejected.
3. **Plumb agent-supplied token counts through `dfmt_remember`** and gate on those. Already partly possible, but the policy decision happens before the agent processes the response; we need an estimate at filter time, not after. Rejected.
4. **Target a different ratio (bytes/3 instead of bytes/4).** Would over-budget English, slightly under-budget code. The English baseline of bytes/4 is the well-documented reference; deviating from it without a calibration corpus would be guessing.
