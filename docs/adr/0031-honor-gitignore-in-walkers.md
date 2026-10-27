# ADR-0031: Honor .gitignore in the Grep/Glob Walkers

| Field | Value |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-27 |
| Deciders | Ersin Koç |
| Supersedes | — |

## Context

The recursive Grep walker and the `**` Glob walker prune directories by a
hardcoded basename list (`walkskip.go`): VCS internals, `dist`, `build`,
`node_modules`, tool caches. That list is a guess at "not source", and the
repository already states the answer in its `.gitignore` files. The guess
drifts both ways: a project's real build directory (`bin/`, `generated/`,
`*.min.js`) is searched and fills the 100-match budget, while a directory
that happens to be named `out` or `build` but holds source is hidden.
ROADMAP tracked this as needing an ADR because it needs an in-tree matcher
(ADR-0004: stdlib only; no `go-gitignore`).

## Decision

The walkers also honor `.gitignore`, using `internal/sandbox/gitignore.go`:

- **Sources**: `<project>/.git/info/exclude`, the project root's
  `.gitignore`, the `.gitignore` of every directory between the root and
  the search root, and each nested `.gitignore` as the walk enters its
  directory. Rules from a nested file apply only beneath it. The user's
  global `core.excludesFile` is not read (it is per-machine, not project
  knowledge, and would make results depend on who runs the search).
- **Semantics** (gitignore(5)): blank lines and `#` comments skipped, `!`
  negates, trailing `/` matches directories only, a `/` elsewhere anchors to
  the file's directory, otherwise any depth; `**` spans directories; `*`,
  `?`, `[...]` stay within a segment; the last matching rule wins. A matched
  directory is pruned (`SkipDir`), so — as in git — a file under an ignored
  directory cannot be re-included.
- **Union with the default list.** The basename list stays: it covers VCS
  internals, dfmt's own `.dfmt/`, and projects with no `.gitignore`.
- **Explicit requests win.** If the search root itself is ignored
  (`path: "generated"`), the walk ignores `.gitignore`, mirroring the
  existing rule that a search root is never pruned.
- **Reported, never silent.** The skip note counts them:
  `(skipped 3 ignored dirs, 12 gitignored, 2 binary)`.

## Consequences

- Results follow what the project considers source, without configuration.
- A search that should include ignored files (generated code, vendored
  sources) passes the directory as `path`.
- The basename list's guesses about `out`/`obj`/`target`/`build` remain;
  whether to drop them now that `.gitignore` is honored is left to a later
  change with evidence from real projects.
