# sc-rce — Remote Code Execution (non-CMDI, non-deserialization)

## Scope

This scanner targets **dynamic code evaluation** (`eval`, `Function`,
ScriptEngine, plugin loaders, dynamic `require`/`import` of
attacker-controlled paths) — distinct from OS command injection
(sc-cmdi) and untrusted deserialization (sc-deserialization). In Go,
the relevant sinks are `plugin.Open`, code generation + exec, AST
interpreters (`yaegi`), and `text/template` / `html/template` with
attacker-controlled templates.

## Surfaces examined

- Searched the tree for `plugin.`, `text/template`, `html/template`,
  `go/ast`, `yaegi`, `Eval`, `LoadPlugin`. No matches in the runtime
  tree.
- `internal/sandbox/permissions.go::Exec` — covered by sc-cmdi.
- `internal/sandbox/permissions.go::writeTempFile` — for non-bash
  langs the agent's `req.Code` is interpreted by node / python / etc.
  This is exec, gated by the same allow-list policy that gates shell
  exec.
- `internal/sandbox/htmlmd.go`, `htmltok.go` — HTML→markdown
  conversion. Pure tokenizer; no template / no eval surface.
- `internal/sandbox/structured.go` — JSON/YAML noise stripping. Uses
  `json.Unmarshal` and `yaml.Unmarshal` into `any`. Walked under
  sc-deserialization.

## Findings

### RCE-01 — Verified safe: no dynamic code evaluation primitives in tree
- **Severity:** Info
- **File:** repo-wide.
- **Description:** No `text/template.Parse`, no `html/template.Parse`,
  no `plugin.Open`, no AST-walk-then-execute, no embedded JS engine,
  no Lua VM. The only "user code runs" path is `dfmt_exec`, which is
  the documented sandbox surface and is governed by an allow/deny
  policy (sc-cmdi).
- **Confidence:** High.

### RCE-02 — `dfmt_exec` interpreter polyglot is policy-gated
- **Severity:** Info
- **CWE:** N/A (intended behavior).
- **File:** `internal/sandbox/permissions.go:1796` (`execImpl`),
  `:1906` (`writeTempFile`).
- **Description:** Per project design `dfmt_exec` IS an RCE primitive
  in the literal sense — the agent says `lang=python, code=…` and the
  daemon runs that python source. The defense is the same allow-list
  used for shell exec: every interpreter in `Probe`'s list (`bash, sh,
  node, python, python3, go, ruby, perl, php, R, elixir`) must be in
  the policy's allow rules to be reachable. Default policy currently
  allows `node`, `python`, `go`. The agent is "trusted to run the
  code" insofar as it's already allowed to invoke that interpreter
  shell-side. There is no path where an UNTRUSTED party (e.g. a
  fetched HTML doc, a malicious file in the project) flows directly
  into `req.Code` without crossing the agent's reasoning.
- **Caveat (prompt injection):** The agent IS exposed to prompt
  injection from `dfmt_fetch` and `dfmt_read` content. A malicious
  fetched page can instruct the agent to run code via `dfmt_exec`.
  The mitigation is the policy gate plus the deny rules (`sudo`, `rm
  -rf /`, recursive `dfmt`, `curl|sh`). This is the intended threat
  model.
- **Confidence:** High.

### RCE-03 — Verified safe: env-injection via `req.Env` is allow-listed-by-exclusion
- **Severity:** Info
- **File:** `internal/sandbox/permissions.go:2022` (`buildEnv`),
  `:2046` (`sandboxBlockedEnvPrefixes`).
- **Description:** Without `isSandboxEnvBlocked`, an agent setting
  `req.Env[LD_PRELOAD]=/tmp/evil.so` would inject a shared library
  into every allow-listed binary, escalating any benign exec into
  RCE. The block list covers `LD_*`, `DYLD_*`, `GIT_*`, `NODE_*`,
  `NPM_CONFIG_*`, `PYTHON*`, `RUBY*`, `BUNDLE_*`, `GEM_*`, `PERL5*`,
  `LUA_*`, `PHP*`, `COMPOSER_*`, `JAVA_*`, plus fixed names
  (`PATH`, `IFS`, `BASH_ENV`, `ENV`, `PS4`, `PROMPT_COMMAND`,
  `_JAVA_OPTIONS`). The audit trail for F-G-LOW-2 is in the inline
  comment.
- **Note:** `env` is built from `os.Environ()` filtered to a small set
  on Windows / Unix; `DFMT_EXEC_*` from the daemon's environment is
  passed through verbatim (the operator owns those). The agent's
  `extra` map is then applied with the block-list filter. The
  block-list is **case-folded to upper** before comparison — the
  comment notes that the legacy `_JAVA_OPTIONS` variant is in the
  fixed-name set rather than via prefix.
- **Confidence:** High.

### RCE-04 — Possible gap: PathPrepend list trusted by daemon, but if config is attacker-controllable…
- **Severity:** Low
- **CWE:** CWE-426 (Untrusted Search Path).
- **File:** `internal/sandbox/permissions.go:479` (`WithPathPrepend`),
  `:1951` (`prependPATH`).
- **Description:** The daemon's `cfg.Exec.PathPrepend` is prepended
  to PATH for every Exec subprocess. Entries are deduped but not
  validated to be trusted-owned directories. If an agent could
  influence this config (e.g. by writing
  `.dfmt/config.yaml` — but that path is in the write deny list), an
  attacker who controlled a directory there could plant a fake
  `git`/`node`/`python` binary. The deny list (`write
  .dfmt/**`) currently keeps the agent off the config file.
- **Attack scenario:** Operator misconfigures the deny list (removes
  the `.dfmt/**` write deny) → agent writes
  `.dfmt/config.yaml` with `path_prepend: ["/tmp/agent-bin"]` → agent
  drops `/tmp/agent-bin/git` → next `dfmt_exec git status` runs
  attacker code.
- **Remediation:** Document that `cfg.Exec.PathPrepend` must contain
  only operator-trusted paths and refuse to apply it if any entry is
  agent-writeable (e.g. world-writable, /tmp, or under wd). Also
  require the deny rule on `.dfmt/**` to be present
  before honoring a non-empty PathPrepend — the daemon could log a
  warning otherwise.
- **Confidence:** Low (depends on operator misconfiguration).

## Summary

No dynamic-eval RCE primitives in the runtime tree. The only "execute
arbitrary code" path is `dfmt_exec` itself, which is the project's
designed boundary. Env-variable injection via `req.Env` is correctly
blocked. One operator-misconfiguration sharp edge noted around
`PathPrepend` (RCE-04).
