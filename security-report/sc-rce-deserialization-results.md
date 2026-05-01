# sc-rce / sc-deserialization — Code Execution & Untrusted Data

## RCE surface

The intended RCE surface is `dfmt_exec` itself — agents pass code, the
sandbox runs it. This is the product, not a vulnerability. The relevant
question is whether RCE can happen **outside** that intended path.

### Channels examined

1. **JSON-RPC method dispatch** — `handle` in `http.go`, `dispatch` in
   `socket.go`. Both use a fixed `switch req.Method` over a hard-coded
   set of method names. Unknown methods return -32601 ("Method not
   found"). No reflection, no method lookup by string.

2. **JSON-RPC params decoding** — `decodeRPCParams` (HTTP) and
   `decodeParams` (socket) call `json.Unmarshal(req.Params, &dst)` where
   `dst` is a per-method struct. JSON unmarshal of plain Go structs is
   safe by construction — no `UnmarshalJSON` hooks invoke code paths,
   no struct fields are `interface{}` that could resolve to executable
   data. Closes V-16 (the prior round-trip Marshal+Unmarshal that
   discarded errors).

3. **MCP tools dispatch** — `internal/transport/mcp.go` is a thin
   adapter mapping MCP tool calls onto the same handler interface. Tool
   names are validated against a fixed set (`dfmt_*`). Same safety
   profile as JSON-RPC.

4. **YAML config** — `gopkg.in/yaml.v3@v3.0.1` parses
   `.dfmt/config.yaml` into a typed struct. v3.0.1 is the patched
   release for the prior CVE; YAML decoding does not invoke code.

5. **Embedded shell hooks** — Hook scripts are read out of `embed.FS`,
   their content is **fixed at compile time**. The `readHookFile` path
   takes a hard-coded name (`"git-post-commit.sh"`, etc.); user input
   never names a hook file.

6. **`writeTempFile`** — used by `Exec` for non-shell langs. The temp
   file is created via `os.CreateTemp` with a static prefix, the user's
   code is written to it, and the file path is passed as argv. This is
   the intended exec path; sandboxing is the policy layer.

## Deserialization (CWE-502)

**Status:** safe.

- **No `encoding/gob` on untrusted input.** `gob` only appears in
  `internal/core/index_test.go`. Index persistence uses custom JSON.
- **JSON unmarshal target** is always a typed struct, not
  `map[string]any` for security-sensitive paths.
- **Index loading** (`LoadIndexWithCursor`) reads from
  `.dfmt/index.gob` which is owned by the same user as the daemon
  (mode 0o600). On corruption / version mismatch / cursor error, the
  index rebuilds from journal — no panics propagate.

## Findings

No RCE or deserialization issues identified.

### Notes

- The journal file is also owned by the user and never parsed from
  network input. A malicious user with shell access on the same
  machine could craft a `.dfmt/journal.jsonl` to influence subsequent
  recall outputs, but that user already has full code execution.
- The `index.gob` filename is historical; the on-disk format is
  custom JSON (per `MarshalJSON`/`UnmarshalJSON` documented in
  CLAUDE.md). The `.gob` extension is misleading but cosmetic.

## Confidence

High.
