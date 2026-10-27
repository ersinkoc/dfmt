package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Per-call project routing (GitHub issue #2).
//
// The daemon has always honored a project_id on every tool call, but the
// schemas never advertised it, so a host that builds its function-calling
// interface from tools/list could not send one. That matters for hosts with
// a single global MCP config and no per-workspace variables (Windsurf): the
// `dfmt mcp` they spawn starts outside any project and could serve nothing.
//
// Two rules keep this from widening what an agent can reach:
//
//   - The parameter is advertised only on an UNBOUND session (started with
//     no project). A session bound to a project costs nothing extra in
//     tools/list, and its agent has no reason to name another project.
//   - Every explicit project_id is validated before it reaches the daemon,
//     whose loader would otherwise create .dfmt/ in any directory named and
//     make it the sandbox root — so a prompt-injected `project_id: "/"`
//     would have re-rooted exec/read/write at the filesystem root.

// projectIDParamDesc is the schema text for project_id on unbound sessions.
const projectIDParamDesc = "Absolute path of the project root (a directory containing .dfmt/ or .git/). " +
	"Required: this MCP session was started outside any project."

// SetUnbound marks a session started with no project: every tool call must
// then carry a project_id, and tools/list advertises it. Call once at
// startup, before any request, like SetProjectID.
func (m *MCPProtocol) SetUnbound() {
	m.unbound = true
}

// withProjectIDParam adds a required project_id to every tool schema.
func withProjectIDParam(tools []MCPTool) []MCPTool {
	for i := range tools {
		schema := tools[i].InputSchema
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			schema["properties"] = props
		}
		props["project_id"] = map[string]any{"type": "string", "description": projectIDParamDesc}
		required, _ := schema["required"].([]string)
		schema["required"] = append(append([]string(nil), required...), "project_id")
	}
	return tools
}

// validateCallProjectID checks a project_id taken from tool arguments. It
// must be an absolute path to an existing directory that is a project root
// (holds .dfmt/ or .git/, the same markers project discovery uses), and it
// must not be the filesystem root or the user's home directory.
func validateCallProjectID(id string) error {
	if !filepath.IsAbs(id) {
		// The daemon would resolve a relative path against ITS cwd, which
		// has nothing to do with the caller's.
		return fmt.Errorf("project_id must be an absolute path, got %q", id)
	}
	p := filepath.Clean(id)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if p == filepath.VolumeName(p)+string(filepath.Separator) {
		return fmt.Errorf("project_id %q is the filesystem root, not a project", id)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if realHome, herr := filepath.EvalSymlinks(home); herr == nil {
			home = realHome
		}
		if samePathFold(p, filepath.Clean(home)) {
			return fmt.Errorf("project_id %q is the home directory, not a project", id)
		}
	}
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("project_id %q is not an existing directory", id)
	}
	for _, marker := range []string{".dfmt", ".git"} {
		if _, err := os.Stat(filepath.Join(p, marker)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("project_id %q is not a project root (no .dfmt/ or .git/); run `dfmt init` there first", id)
}

// samePathFold compares paths with the platform's case rules.
func samePathFold(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
