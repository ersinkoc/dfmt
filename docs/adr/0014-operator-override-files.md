# ADR-0014: Operator Override Files (permissions.yaml + redact.yaml)

- **Status:** Accepted
- **Date:** 2026-04-30
- **Supersedes:** —
- **Superseded by:** —
- **Related:** ADR-0006 (Sandbox Scope), ADR-0007 (Content Store ≠ Journal)

## Context

DFMT ships two policy surfaces an operator might want to extend per
project:

1. **Sandbox permissions** (`internal/sandbox/permissions.go`).
   `DefaultPolicy()` is a curated whitelist of dev tools (git, npm, go,
   …) plus a deny list for sensitive paths (`.env*`, `**/secrets/**`,
   `**/.git/**` for write/edit, cloud metadata for fetch). Operators
   with site-specific tooling or extra secret directories need a way
   to add allow / deny rules without forking the daemon.

2. **Redaction patterns** (`internal/redact/redact.go`). The
   `commonPatterns` list catches provider keys (Anthropic / OpenAI /
   GitHub / AWS / …), generic auth headers, JWTs, PEM blocks, and
   common database URL credentials. Operators with bespoke credential
   shapes (homegrown auth tokens, per-tenant API keys with custom
   prefixes) need a way to register additional patterns.

The plumbing was half-built: `sandbox.LoadPolicy(path)` parsed the
intended override format, and `redact.AddPattern(name, regex, repl)`
accepted entries at runtime. But **no production call site** read the
override files. `sandbox.NewSandbox(wd)` always returned
`DefaultPolicy()`; the daemon's redactor was always the bare
`commonPatterns`. Documentation in ARCHITECTURE.md, the redact godoc,
and CLAUDE.md described `.dfmt/permissions.yaml` and
`.dfmt/redact.yaml` as authoritative — a doc-vs-code lie that the
2026-04-28 audit flagged as a v0.3 sprint item.

This ADR records the wiring decision plus the override semantics that
the wiring implies.

## Decision

### File locations

- `<projectPath>/.dfmt/permissions.yaml`
- `<projectPath>/.dfmt/redact.yaml`

Both optional; missing files are silently treated as "no override".

### Permissions format

Line-based, as already shipped by `LoadPolicy`. One rule per non-blank,
non-comment line:

```
allow:exec:<base-cmd> *
allow:read:<glob>
deny:write:<glob>
deny:fetch:<scheme://host/glob>
```

Trailing ` *` on exec allows is mandatory (V-20 rule contract). Comment
character is `#`.

### Redact format

YAML, in contrast to permissions:

```yaml
patterns:
  - name: company-api-key
    pattern: 'CO-[A-Z0-9]{20}'
    replacement: '[CO-KEY]'   # optional
```

The two formats diverge because regex bodies routinely contain `:`
(URL schemes, port markers, time formats), which the line parser
splits on. Reusing the line format for redact would force operators
to escape every regex `:`. The cost of a second format is one
yaml.v3 unmarshal call already covered by the existing
gopkg.in/yaml.v3 dependency.

### Permissions merge semantics

`MergePolicies(default, override)`:

1. **Allow**: union, with one filter — override allow rules whose
   exec base command is on the **hard-deny list** are silently
   dropped and emitted as a warning. The hard-deny list is exec-only
   (it does not affect `allow:read:` / `allow:write:` /
   `allow:fetch:`):

   ```
   rm rmdir del erase
   sudo doas su
   shutdown reboot halt poweroff
   format diskpart mkfs fdisk
   mount umount dd
   dfmt   # recursion guard
   ```

2. **Deny**: union, no filter. Operators can always tighten.

Hard-deny invariant rationale: `DefaultPolicy()`'s exec whitelist
withholds these commands today, but a single permissive override line
(`allow:exec:rm *`) would re-enable them with no obvious flag at
review time. Treating the hard-deny set as a load-time mask is a
defense-in-depth layer that survives override mistakes and policy
copy-paste from less-trusted sources. An operator who legitimately
needs `rm` inside a sandboxed exec should run that tool without the
sandbox, not weaken the daemon's defaults.

### Redact merge semantics

LoadProjectRedactor seeds a fresh `Redactor` from `commonPatterns`
and adds each override entry via `AddPattern`. There is no removal
mechanism — operators **extend** the default set, they don't reduce
it. (Disabling a default pattern would degrade the journal's
defense-in-depth without surfacing in any visible config; future
need can land as a separate ADR.)

Per-entry resilience: a missing required field or an invalid regex
on one entry yields a warning and skips that entry. The whole load
fails only on YAML parse errors / read errors at the file level.
This trades a noisy warning for graceful operation on partially-bad
input.

### Visibility

`dfmt doctor` adds two rows above the daemon-liveness checks:

```
✓ Redact override (.dfmt/redact.yaml) — loaded 3 pattern(s)
✓ Permissions override (.dfmt/permissions.yaml) — loaded 5 rule(s); 1 hard-deny mask(s): override allow:exec:rm * ignored (hard-deny base command)
```

A warning surfaces both in `dfmt doctor` output and in `logging.Warnf`
at daemon startup. Operators inspect both — `doctor` for ad-hoc
verification, daemon log for monitoring / dashboard ingest.

## Alternatives Considered

### Single file, both surfaces

`.dfmt/policy.yaml` with `permissions:` and `redact:` sections.
Rejected: each surface has different format requirements (line vs
YAML, hard-deny invariant only on one side) and operators tend to
edit one without the other. Keeping them in separate files makes
each one's failure modes localized.

### YAML for permissions too

Migrating `LoadPolicy` to YAML would unify formats. Rejected at this
step: the line format is shipped, parsed, and battle-tested; switching
shape now creates a migration burden for operators with existing
permissions.yaml files (we'd need a v1 → v2 detector). Future ADR can
revisit if the line format constrains a needed feature.

### Hard-deny via separate file

A `.dfmt/hard-deny.yaml` or build-time-baked deny list separate from
`DefaultPolicy()`. Rejected: hard-deny is an invariant of the merge
operation, not a third tier of policy. Keeping it in `permissions.go`
keeps the rule next to the merge logic — a single file to read for
"why was my override silently dropped".

### Strict mode (fail on hard-deny breach)

Refuse to start the daemon when an override attempts hard-deny base
commands. Rejected: an over-eager strict mode locks operators out on
typos. Warning + drop + visible doctor row catches the case without
the recovery cost.

## Consequences

### Positive

- The doc-vs-code lie closes: `.dfmt/permissions.yaml` and
  `.dfmt/redact.yaml` now match what ARCHITECTURE.md, CLAUDE.md, and
  the godoc have been promising for two release cycles.
- Hard-deny invariant tightens the security stance: an operator can
  broaden file/network reach but cannot re-enable destructive
  commands the default policy intentionally withholds.
- `dfmt doctor` + daemon-startup logs provide two independent
  paths to verify what state the daemon will see, closing the most
  common "I edited the file but nothing changed" support loop.
- Per-entry redact resilience means a fat-fingered regex doesn't
  brick the whole pattern set.

### Negative

- Two formats (line for permissions, YAML for redact) mean two
  parser code paths to maintain. Mitigated by the operator-facing
  format guidance in `dfmt init`'s generated stubs (follow-up).
- Hard-deny list is hard-coded in Go. Adding to it requires a new
  release. We accept this — the list is small, evolves rarely, and
  a project-config knob would defeat the invariant's purpose.
- The redact override is additive only (no way to disable a default
  pattern). An operator who needs to suppress, say, `bearer_token`
  for a debugging session has no path short of forking. We accept
  this as a v0.3 limitation; an explicit `disable: [pattern_name]`
  list can be added without a breaking change if demand surfaces.

## Implementation Notes

- `sandbox.LoadPolicyMerged(projectPath)` returns a
  `PolicyLoadResult` with the composed policy plus
  OverrideFound / OverrideRules / Warnings fields. Daemon and
  CLI-fallback paths consume the same struct.
- `redact.LoadProjectRedactor(projectPath)` returns the seeded
  Redactor + a `LoadResult` with the same shape. Daemon installs
  the result on both the Daemon struct and the Handlers redactor
  via `SetRedactor`.
- Both functions tolerate `projectPath == ""` and return
  default-only state.
- Test coverage: hard-deny invariant tested across the full set
  (rm / sudo / shutdown / dd / RM.exe / DEL / absolute-path forms);
  per-entry redact failure modes tested for bad regex,
  missing-field, malformed YAML.

## Migration

None. Operators with no override file see no behavior change. Operators
with an existing `permissions.yaml` (line format) keep working — that
format is what's been shipping. Override files added after this commit
take effect on the next daemon start.
