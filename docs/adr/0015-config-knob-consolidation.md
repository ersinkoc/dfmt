# ADR-0015: Config Knob Consolidation

- **Status:** Accepted
- **Date:** 2026-04-30 (amended 2026-05-02 — `lifecycle.shutdown_timeout` wired)
- **Supersedes:** —
- **Superseded by:** —
- **Related:** ADR-0014 (Operator Override Files)

## Context

`internal/config/config.go` defines a `Config` struct with ~30 leaf
fields, each with a YAML tag, default value, and (in many cases) a
range check in `Validate()`. The 2026-04-28 audit flagged that not
every field is read at runtime: roughly half are parsed and validated
but no production caller ever consumes the value. An operator who
edits `index.bm25_k1: 1.5` in `.dfmt/config.yaml` sees the file
accepted, the value preserved through `Load()`, and zero observable
behavior change — the daemon keeps using the package-level
`core.DefaultBM25K1 = 1.2` constant.

`yaml.v3`'s `KnownFields(true)` (already enabled at the decoder)
guarantees that misspelled top-level keys produce parse errors, so
operators can't accidentally write `idx.bm25_k1` and have it
silently ignored. But the larger silent-ignore is the gap between
"field exists in `Config`" and "field is read at runtime" — that's
what this ADR catalogs and decides.

## Decision

Each existing knob is classified into one of three buckets. The
classification is recorded both in source (per-field comment in
`config.go`) and here (canonical reference). No knob is **deleted**
in this ADR — the cost of a removed YAML key breaking an operator's
existing config is higher than the cost of a documented no-op.

### Wired (read at runtime, behavior change visible)

| Field | Caller |
|---|---|
| `storage.durability` | `daemon.go::New` (journal options) |
| `storage.max_batch_ms` | `daemon.go::New` (journal batch flush) |
| `storage.journal_max_bytes` | `daemon.go::New` (journal rotation) |
| `storage.compress_rotated` | `daemon.go::New` (journal rotation) |
| `capture.fs.enabled` | `daemon.go::New` (FSWatcher gate) |
| `capture.fs.ignore` | `capture.NewFSWatcher` |
| `capture.fs.debounce_ms` | `capture.NewFSWatcher` |
| `transport.http.enabled` | `daemon.go::New` (TCP fallback gate) |
| `transport.http.bind` | `transport.NewHTTPServer` |
| `lifecycle.idle_timeout` | daemon idle monitor |
| `exec.path_prepend` | `sandbox.WithPathPrepend` |

### Reserved (parsed + validated, NOT read at runtime; v0.4 wire-or-delete)

| Field | Why reserved | v0.4 plan |
|---|---|---|
| `index.bm25_k1` | `core.NewIndex()` has no constructor parameter; 50+ call sites | wire via `NewIndexWithParams` overload |
| `index.bm25_b` | same | same |
| `index.heading_boost` | same | same |
| `index.rebuild_interval` | no caller in any version | likely **delete** |
| `index.stopwords_path` | no caller in any version | wire to TokenizeFull via stopword-loader helper |
| `retrieval.default_budget` | `handlers.Recall` uses an internal const | wire as a default for unset request budget |
| `retrieval.default_format` | not read | wire alongside default_budget |
| `retrieval.throttle.first_tier_calls` | no throttle implementation | likely **delete** unless v0.4 ships throttle |
| `retrieval.throttle.second_tier_calls` | same | same |
| `retrieval.throttle.results_first_tier` | same | same |
| `retrieval.throttle.results_second_tier` | same | same |
| `capture.mcp.enabled` | MCP capture is always-on when MCP transport is up | likely **delete** |
| `capture.git.enabled` | git capture is gated by `dfmt install-hooks`, not this | likely **delete** |
| `capture.shell.enabled` | shell capture gated by `dfmt shell-init`, not this | likely **delete** |
| `capture.fs.watch` | FSWatcher reads everything under root with ignore filtering | likely **delete** |
| `transport.mcp.enabled` | MCP is the `dfmt mcp` entry point; can't be disabled by a daemon-side flag | likely **delete** |
| `transport.socket.enabled` | Unix socket built unconditionally on Linux/macOS | wire as a runtime gate |
| ~~`lifecycle.shutdown_timeout`~~ | ~~daemon uses hard-coded 10s grace~~ | **WIRED 2026-05-02** via `Daemon.ShutdownGrace()`; idle-monitor stop + dispatch.go SIGTERM handler both bracket `d.Stop` with `context.WithTimeout` |
| `privacy.telemetry` | no telemetry shipped | **delete** at v1.0 if telemetry stays off-by-design |
| `privacy.remote_sync` | no remote sync feature | **delete** at v1.0 |
| `privacy.allow_nonlocal_http` | enforced via `goosWindows`-aware bind logic, not this knob | review and either wire or delete |
| `logging.level` | `DFMT_LOG` env var is authoritative | wire as fallback when env unset |
| `logging.format` | same | same |

### Hardened invariant: env > yaml > default precedence (forward declaration)

When `logging.level` and `logging.format` are wired in v0.4, the
precedence order will be:

1. `DFMT_LOG=<level>` env (and any future `DFMT_LOG_FORMAT`)
2. `<projectRoot>/.dfmt/config.yaml` `logging:` block
3. `~/.local/share/dfmt/config.yaml` global `logging:` block
4. Hardcoded defaults (`info`, `text`)

Setting both env AND yaml emits a one-time warning at daemon startup
naming the file path, so an operator wondering "why is my YAML log
level ignored" sees the answer in the daemon log.

## Alternatives Considered

### Delete every Reserved knob now

Tempting — it would shrink the struct and force the issue. Rejected
because:

- A YAML key that worked yesterday and produces a parse error today
  breaks running deployments without notice. `KnownFields(true)`
  is strict.
- Several of the Reserved knobs (BM25 trio, retrieval defaults,
  shutdown timeout) have legitimate v0.4 wiring paths. Deleting
  them would mean re-introducing them later with the same names —
  pointless churn.

### Wire everything in this ADR

Equally tempting and equally wrong for v0.3 scope. The BM25 trio
alone requires a constructor change touching 50+ test sites; the
retrieval/throttle group needs a feature design (when does throttle
kick in? how does it interact with the existing budget gate?). v0.4
is the right horizon.

### Add a `KnownFields(false)` permissive mode

Loosen the YAML decoder so unknown keys produce a warning instead of
an error, giving operators a softer migration path. Rejected: the
strict-by-default decoder catches typos that would otherwise be
silently ignored, which is more valuable than the migration
ergonomic. If a future ADR removes a knob, an explicit migration
note in CHANGELOG covers the transition.

### Per-field "experimental" tag

A YAML-level `experimental:` flag operators could set to opt into
Reserved knobs. Rejected: complexity without payoff; the source
comment + ADR table convey the same status with zero runtime
machinery.

## Consequences

### Positive

- The doc-vs-code lie around `Config` shrinks: every field's runtime
  status is now in the source comment AND this ADR.
- v0.4 has a concrete punch list (the Reserved table above) that
  doesn't need re-discovery.
- New contributors reading `Config` can tell at a glance which
  knobs change behavior and which are documented no-ops.
- `dfmt doctor` could grow a "config knob status" check in v0.4
  that flags any explicitly-set Reserved knob — telling the
  operator "you set this but it has no effect; here's why."

### Negative

- The Reserved status itself is technical debt — every release that
  ships with documented no-ops invites legitimate complaints. The
  v0.4 wire-or-delete commitment in the table above is the
  countdown clock; if v0.4 ships without resolving most Reserved
  rows, this ADR was a stalling tactic and should be superseded.
- Operators who have already set Reserved knobs (e.g. an
  `index.bm25_k1: 1.5` in their config) won't notice anything is
  off until they read the comment or this ADR. Mitigation: v0.4's
  wire-up will retroactively make those settings effective with no
  config change required.

## Implementation Notes

- Per-field comments in `internal/config/config.go` use the literal
  string `Reserved (v0.4)` so a future grep can find every knob to
  revisit. The wired fields are also annotated with their caller
  line for audit traceability.
- `Validate()` is left untouched — it continues to range-check the
  Reserved knobs. Catching `bm25_k1: -1` at config-load time is
  still useful even when the value isn't read at runtime, because
  it prevents the load from "succeeding" with garbage and then
  surprising operators when v0.4 wires it.
- No test changes in this commit. Existing tests verify that
  default values survive YAML round-trip, which is the contract
  this ADR keeps.

## Migration

None. Operators with Reserved knobs in their config keep their
files unchanged; behavior was unchanged before this ADR and remains
unchanged after. v0.4 wire-ups will be additive (an existing value
becomes effective) — no schema migration required.
