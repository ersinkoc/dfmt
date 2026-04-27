package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// DFMT injects per-project routing rules into each agent's instruction file
// (CLAUDE.md, AGENTS.md, .cursorrules, etc.) so the agent prefers `dfmt_*`
// MCP tools over native ones. Blocks are delimited by markers so subsequent
// `dfmt init` runs upsert in place — no duplicates accumulating, easy to
// strip on uninstall.
//
// Marker style depends on the host file format:
//   - Markdown / HTML-tolerant files use HTML-comment markers so they
//     render invisibly in any preview.
//   - Plain-text rule files (.cursorrules) use shell-style `#` comments
//     because Cursor parses them line by line and would surface stray
//     HTML literally to the user.
const (
	// Markdown-style markers — CLAUDE.md, AGENTS.md, GEMINI.md,
	// .github/copilot-instructions.md.
	dfmtBlockBeginMD = "<!-- dfmt:v1 begin -->"
	dfmtBlockEndMD   = "<!-- dfmt:v1 end -->"

	// Plain-text comment markers — .cursorrules.
	dfmtBlockBeginCursor = "# dfmt:v1 begin"
	dfmtBlockEndCursor   = "# dfmt:v1 end"

	// dfmtBlockBegin / dfmtBlockEnd are aliases for the markdown-style
	// markers, kept for tests that landed before the multi-style refactor.
	dfmtBlockBegin = dfmtBlockBeginMD
	dfmtBlockEnd   = dfmtBlockEndMD
)

// markerStyle bundles a begin/end pair so call sites can pass a single
// value rather than juggling two strings.
type markerStyle struct {
	begin string
	end   string
}

// markersFor returns the marker style appropriate to the file at path.
// Selection is purely by basename; everything but `.cursorrules` gets
// markdown-style. Adding a new plain-text target later means a new
// case here, no other call-site changes.
func markersFor(path string) markerStyle {
	if filepath.Base(path) == ".cursorrules" {
		return markerStyle{begin: dfmtBlockBeginCursor, end: dfmtBlockEndCursor}
	}
	return markerStyle{begin: dfmtBlockBeginMD, end: dfmtBlockEndMD}
}

// markdownProjectBlockBody is the canonical DFMT block body for the
// markdown-style instruction files used by Claude Code (CLAUDE.md),
// Gemini CLI (GEMINI.md), and VS Code Copilot
// (.github/copilot-instructions.md). Tool names use backticks so prompt
// renderers display them as code rather than English nouns; the
// concatenation tax buys a non-trivial bump in agent compliance.
var markdownProjectBlockBody = "## Context Discipline\n" +
	"\n" +
	"This project uses DFMT to keep tool output from flooding the context\n" +
	"window and to preserve session state across compactions. When working\n" +
	"in this project, follow these rules.\n" +
	"\n" +
	"### Tool preferences\n" +
	"\n" +
	"Prefer DFMT's MCP tools over native ones:\n" +
	"\n" +
	"| Native     | DFMT replacement | `intent` required? |\n" +
	"|------------|------------------|--------------------|\n" +
	"| `Bash`     | `dfmt_exec`      | yes                |\n" +
	"| `Read`     | `dfmt_read`      | yes                |\n" +
	"| `WebFetch` | `dfmt_fetch`     | yes                |\n" +
	"| `Glob`     | `dfmt_glob`      | yes                |\n" +
	"| `Grep`     | `dfmt_grep`      | yes                |\n" +
	"| `Edit`     | `dfmt_edit`      | n/a                |\n" +
	"| `Write`    | `dfmt_write`     | n/a                |\n" +
	"\n" +
	"Every `dfmt_*` call MUST pass an `intent` parameter — a short phrase\n" +
	"describing what you need from the output (e.g. \"failing tests\",\n" +
	"\"error message\", \"imports\"). Without `intent` the tool returns raw\n" +
	"bytes and the token savings are lost.\n" +
	"\n" +
	"On DFMT failure, report it to the user. Do NOT silently fall back to\n" +
	"the native tool — that defeats the routing.\n" +
	"\n" +
	"### Session memory\n" +
	"\n" +
	"DFMT tracks tool calls automatically. After substantive decisions or\n" +
	"findings, call `dfmt_remember` with descriptive tags (`decision`,\n" +
	"`finding`, `summary`) so future sessions can recall the context after\n" +
	"compaction.\n" +
	"\n" +
	"### When native tools are acceptable\n" +
	"\n" +
	"Native `Bash` and `Read` are acceptable for outputs you know are small\n" +
	"(< 2 KB) and will not be referenced again. For everything else, DFMT\n" +
	"tools are preferred.\n"

// agentsMdProjectBlockBody is the body for AGENTS.md, shared by Codex,
// OpenCode, and Zed. Phrasing is more emphatic than markdown-style
// because Codex CLI has no hook support — the instruction file is the
// only enforcement layer, so compliance language matters.
var agentsMdProjectBlockBody = "# Context Discipline — REQUIRED\n" +
	"\n" +
	"This project uses DFMT to keep large tool outputs from exhausting the\n" +
	"context window. **Read this section at the start of every conversation\n" +
	"in this project.**\n" +
	"\n" +
	"## Rule 1 — Prefer DFMT tools over native tools\n" +
	"\n" +
	"Always use DFMT's MCP tools when an output might exceed 2 KB:\n" +
	"\n" +
	"| Native     | DFMT replacement |\n" +
	"|------------|------------------|\n" +
	"| `Bash`     | `dfmt_exec`      |\n" +
	"| `Read`     | `dfmt_read`      |\n" +
	"| `WebFetch` | `dfmt_fetch`     |\n" +
	"| `Glob`     | `dfmt_glob`      |\n" +
	"| `Grep`     | `dfmt_grep`      |\n" +
	"| `Edit`     | `dfmt_edit`      |\n" +
	"| `Write`    | `dfmt_write`     |\n" +
	"\n" +
	"Include an `intent` argument on every call, describing what you need\n" +
	"from the output. The `intent` lets DFMT return the relevant portion of\n" +
	"a large output without flooding the context.\n" +
	"\n" +
	"## Rule 2 — Record user decisions\n" +
	"\n" +
	"When the user states a preference or correction (\"use X instead of Y\",\n" +
	"\"do not modify Z\"), call `dfmt_remember` with a `decision` tag so the\n" +
	"choice survives context compaction.\n" +
	"\n" +
	"## Why these rules matter\n" +
	"\n" +
	"Some agents do not provide hooks to enforce these rules automatically.\n" +
	"**Compliance is your responsibility as the agent.** A single raw shell\n" +
	"output above 8 KB can push earlier context out of the window, erasing\n" +
	"the conversation's history. Following the rules above preserves it.\n"

// cursorrulesProjectBlockBody is the body for `.cursorrules`. Plain text,
// each line prefixed with `# ` so it survives Cursor's line-based parser
// without rendering markdown syntax to the user. Short on purpose — the
// fuller guide lives in AGENTS.md, which Cursor users can also keep.
var cursorrulesProjectBlockBody = "# Context Discipline\n" +
	"#\n" +
	"# This project uses DFMT. Prefer the dfmt_* MCP tools over native\n" +
	"# Bash, Read, WebFetch, Glob, Grep. Always include an `intent`\n" +
	"# argument describing what you need from the output.\n" +
	"#\n" +
	"# Call dfmt_remember to record user decisions so they survive context\n" +
	"# compaction. On any dfmt_* failure, report it — do not fall back to\n" +
	"# the native tool silently.\n" +
	"#\n" +
	"# See full rules in AGENTS.md.\n"

// ProjectInstructionPath returns the absolute path to the project-level
// instruction file for the given agent ID, rooted at projectDir. Returns
// ("", false) for agents that have no canonical project-level instruction
// file (Windsurf, Continue) or unknown IDs. Several agents map to the
// same file (Codex/OpenCode/Zed all use AGENTS.md); callers should
// de-duplicate paths before calling Upsert.
func ProjectInstructionPath(projectDir, agentID string) (string, bool) {
	switch agentID {
	case AgentClaudeCode:
		return filepath.Join(projectDir, "CLAUDE.md"), true
	case AgentGemini:
		return filepath.Join(projectDir, "GEMINI.md"), true
	case AgentVSCode:
		return filepath.Join(projectDir, ".github", "copilot-instructions.md"), true
	case AgentCodex, AgentOpenCode, AgentZed:
		return filepath.Join(projectDir, "AGENTS.md"), true
	case AgentCursor:
		return filepath.Join(projectDir, ".cursorrules"), true
	default:
		return "", false
	}
}

// projectBlockBodyFor returns the inner body of the DFMT block for the
// given agent. Bodies are keyed by *target file family* rather than by
// individual agent so siblings sharing a path (Codex/OpenCode/Zed all
// land in AGENTS.md) write the same content regardless of detection
// order. Returns "" for agents without a registered body.
func projectBlockBodyFor(agentID string) string {
	switch agentID {
	case AgentClaudeCode, AgentGemini, AgentVSCode:
		return markdownProjectBlockBody
	case AgentCodex, AgentOpenCode, AgentZed:
		return agentsMdProjectBlockBody
	case AgentCursor:
		return cursorrulesProjectBlockBody
	default:
		return ""
	}
}

// UpsertDFMTBlock atomically writes a DFMT-marked block into the
// instruction file at filePath. Behaviour:
//
//   - File does not exist: create it with a single marker-delimited block.
//   - File exists, marker absent: append the block (separated by a blank
//     line if not already trailing one).
//   - File exists, marker present: replace the content between the first
//     begin marker and its matching end marker. Surrounding content
//     (the user's own notes) is left untouched.
//
// Marker style is selected from the file's basename (markdown vs
// .cursorrules) so callers don't have to think about it.
//
// The body argument is the inner content; it must NOT contain the begin
// or end markers for this file's marker style. Trailing newlines in body
// are normalised so the final block always ends with "\n<end>\n".
//
// Writes go through safefs.WriteFileAtomic — symlink-safe, and a
// concurrent reader sees either the old or new file, never a partial.
//
// Closes the gap where `dfmt init` registered the MCP server but never
// told the agent to *use* it, so agents kept calling native tools.
func UpsertDFMTBlock(filePath, body string) error {
	if body == "" {
		return errors.New("dfmt block body must not be empty")
	}

	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", filePath, err)
	}
	m := markersFor(abs)

	if bytes.Contains([]byte(body), []byte(m.begin)) ||
		bytes.Contains([]byte(body), []byte(m.end)) {
		return errors.New("dfmt block body must not contain the begin/end markers")
	}

	// Trim trailing newlines so we control the final layout, then frame
	// the block. Framed result: <begin>\n<body>\n<end>\n.
	bodyTrimmed := body
	for len(bodyTrimmed) > 0 && bodyTrimmed[len(bodyTrimmed)-1] == '\n' {
		bodyTrimmed = bodyTrimmed[:len(bodyTrimmed)-1]
	}
	framed := m.begin + "\n" + bodyTrimmed + "\n" + m.end + "\n"

	var existing []byte
	if data, rerr := os.ReadFile(abs); rerr == nil {
		existing = data
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", abs, rerr)
	}

	var out []byte
	switch {
	case len(existing) == 0:
		out = []byte(framed)

	case bytes.Contains(existing, []byte(m.begin)):
		begin := bytes.Index(existing, []byte(m.begin))
		endRel := bytes.Index(existing[begin:], []byte(m.end))
		if endRel < 0 {
			return fmt.Errorf("malformed %s: %q present without matching %q", abs, m.begin, m.end)
		}
		end := begin + endRel + len(m.end)
		// Eat one trailing newline after the end marker if present so we
		// don't leave a blank line orphan after replacement (the framed
		// block already supplies its own trailing "\n").
		if end < len(existing) && existing[end] == '\n' {
			end++
		}
		out = make([]byte, 0, len(existing)-(end-begin)+len(framed))
		out = append(out, existing[:begin]...)
		out = append(out, []byte(framed)...)
		out = append(out, existing[end:]...)

	default:
		// Append. Ensure exactly one blank line precedes the block.
		sep := "\n\n"
		switch {
		case bytes.HasSuffix(existing, []byte("\n\n")):
			sep = ""
		case bytes.HasSuffix(existing, []byte("\n")):
			sep = "\n"
		}
		out = make([]byte, 0, len(existing)+len(sep)+len(framed))
		out = append(out, existing...)
		out = append(out, []byte(sep)...)
		out = append(out, []byte(framed)...)
	}

	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return safefs.WriteFileAtomic(dir, abs, out, 0o644)
}

// StripDFMTBlock removes the DFMT-marked block from filePath, leaving the
// rest of the file untouched. Used by `dfmt setup --uninstall` to reverse
// the injection. No-op on missing file or absent markers. If the file is
// empty after stripping (DFMT was the sole resident), the file itself is
// removed so we don't leave a 0-byte CLAUDE.md / AGENTS.md the user
// didn't ask for.
func StripDFMTBlock(filePath string) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", filePath, err)
	}
	m := markersFor(abs)

	existing, rerr := os.ReadFile(abs)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", abs, rerr)
	}
	begin := bytes.Index(existing, []byte(m.begin))
	if begin < 0 {
		return nil
	}
	endRel := bytes.Index(existing[begin:], []byte(m.end))
	if endRel < 0 {
		return fmt.Errorf("malformed %s: %q present without matching %q", abs, m.begin, m.end)
	}
	end := begin + endRel + len(m.end)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	out := make([]byte, 0, len(existing)-(end-begin))
	out = append(out, existing[:begin]...)
	tail := existing[end:]
	// Collapse "\n\n" boundary that would otherwise be left behind so
	// repeated install/uninstall cycles don't accumulate blank lines.
	if len(out) > 0 && bytes.HasSuffix(out, []byte("\n")) && bytes.HasPrefix(tail, []byte("\n")) {
		tail = tail[1:]
	}
	out = append(out, tail...)

	if len(out) == 0 {
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", abs, err)
		}
		return nil
	}
	return safefs.WriteFileAtomic(filepath.Dir(abs), abs, out, 0o644)
}

// UpsertProjectInstructions writes/updates the DFMT block in the project
// instruction file for the given agent. Returns the resolved path that
// was written, or "" if the agent has no canonical instruction file or
// no registered body (silent no-op — callers iterating over Detect()
// don't need to filter). On error, the path is still returned so callers
// can surface it in diagnostics.
func UpsertProjectInstructions(projectDir, agentID string) (string, error) {
	p, ok := ProjectInstructionPath(projectDir, agentID)
	if !ok {
		return "", nil
	}
	body := projectBlockBodyFor(agentID)
	if body == "" {
		return "", nil
	}
	return p, UpsertDFMTBlock(p, body)
}
