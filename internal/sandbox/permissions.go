package sandbox

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ersinkoc/dfmt/internal/osutil"
)

const langBash = "bash"

// ErrPolicyDenied is the sentinel wrapped by every "operation denied by
// policy" error this package returns. Callers (and tests) should inspect via
// errors.Is(err, sandbox.ErrPolicyDenied) instead of substring-matching the
// formatted message — the human-readable suffix (op, text, hint) is not part
// of the contract and is free to change without breaking inspectors.
var ErrPolicyDenied = errors.New("operation denied by policy")

// ErrPathContainsNullByte is returned by Read/Edit/Write when the
// caller-supplied path contains a NUL (\x00) byte. Pre-extraction the
// same errors.New literal appeared at three sites with no caller able
// to inspect it as anything other than a string. Null-byte rejection
// is a load-bearing path-sanitization step (POSIX strings terminate
// at NUL, so a valid Go-string path with an embedded NUL can be
// truncated mid-call by the kernel — different effective path than
// the policy check evaluated).
var ErrPathContainsNullByte = errors.New("path contains null byte")

// pathHint returns a canonical path string for error messages.
// On Windows: converts to forward slashes and lowercases drive letter.
// On Unix: returns path as-is.
func pathHint(path string) string {
	if osutil.IsWindows() {
		path = strings.ReplaceAll(path, `\`, "/")
		if len(path) >= 2 && path[1] == ':' {
			return strings.ToLower(string(path[0])) + path[1:]
		}
	}
	return path
}

// For exec operations, * matches anything (shell-style).
// For read/write/edit/fetch operations, * doesn't match / (path-style).
type SandboxImpl struct {
	runtimes    *Runtimes
	policy      Policy
	wd          string   // Working directory
	pathPrepend []string // Absolute dirs prepended to PATH for every exec; populated from cfg.Exec.PathPrepend
}

// WithPathPrepend returns the sandbox after attaching the given list of
// absolute directories. They are prepended to PATH (in order, dedup'd
// against the inherited PATH) for every Exec call. Closes the recurring
// "exit 127" symptom when the daemon was auto-started from a shell that
// did not have the user's language toolchains on PATH.
func (s *SandboxImpl) WithPathPrepend(dirs []string) *SandboxImpl {
	if len(dirs) == 0 {
		s.pathPrepend = nil
		return s
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" {
			out = append(out, d)
		}
	}
	s.pathPrepend = out
	return s
}

// ValidatePathPrepend inspects each absolute directory in dirs for
// configuration smells the operator should know about (V-11). It does NOT
// mutate state and does not refuse the daemon — it returns one human-
// readable warning per problem so the caller (typically Daemon.Start) can
// surface them via logging.Warnf. Empty input returns nil. Each entry is
// checked in turn; the failure modes flagged are:
//
//   - Path does not exist (likely a typo; the entry is silently a no-op
//     until it appears, but operators usually want to know).
//   - Path exists but is world-writable (any local user can plant a
//     binary that the sandbox will then resolve before system tools).
//
// Per-uid writability is not checked: it would require platform-specific
// uid/gid plumbing for marginal benefit on a single-user workstation.
func ValidatePathPrepend(dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}
	var errs []error
	for _, d := range dirs {
		if d == "" {
			continue
		}
		fi, err := os.Stat(d)
		if err != nil {
			if os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("path_prepend entry %q does not exist", d))
				continue
			}
			errs = append(errs, fmt.Errorf("path_prepend entry %q stat failed: %w", d, err))
			continue
		}
		if !fi.IsDir() {
			errs = append(errs, fmt.Errorf("path_prepend entry %q is not a directory", d))
			continue
		}
		// World-writable detection. Windows ACLs don't map cleanly to the
		// Unix mode bits, so the check is Unix-only by Mode() Perm() bits;
		// on Windows fi.Mode().Perm() returns the synthesized bits which
		// don't reflect ACLs, so this is a no-op there (operators on
		// Windows must rely on file-system ACL hygiene separately).
		if fi.Mode().Perm()&0o002 != 0 {
			errs = append(errs, fmt.Errorf("path_prepend entry %q is world-writable (mode=%o); a local attacker can plant binaries that the sandbox will resolve before system tools", d, fi.Mode().Perm()))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// NewSandbox creates a new sandbox instance.
func NewSandbox(wd string) *SandboxImpl {
	return &SandboxImpl{
		runtimes: NewRuntimes(),
		policy:   DefaultPolicy(),
		wd:       wd,
	}
}

// NewSandboxWithPolicy creates a sandbox with a custom policy.
func NewSandboxWithPolicy(wd string, policy Policy) *SandboxImpl {
	return &SandboxImpl{
		runtimes: NewRuntimes(),
		policy:   policy,
		wd:       wd,
	}
}

// NewSandboxWithPolicyAndRuntimes creates a sandbox with custom policy and runtimes.
func NewSandboxWithPolicyAndRuntimes(wd string, policy Policy, runtimes *Runtimes) *SandboxImpl {
	return &SandboxImpl{
		runtimes: runtimes,
		policy:   policy,
		wd:       wd,
	}
}

// SetWorkingDir sets the working directory for exec operations.
func (s *SandboxImpl) SetWorkingDir(wd string) {
	s.wd = wd
}

// PolicyCheck checks if an operation is allowed by the policy.
func (s *SandboxImpl) PolicyCheck(op, text string) error {
	if !s.policy.Evaluate(op, text) {
		return fmt.Errorf("%w: %s %s\n%s", ErrPolicyDenied, op, text, policyDenyHint(op))
	}
	return nil
}

// policyDenyHint returns a one-line, actionable suffix appended to every
// "operation denied by policy" error. Without it the user sees only what was
// blocked, never how to unblock it — and the canonical override location
// (.dfmt/permissions.yaml, loaded by LoadPolicy) is undiscoverable.
//
// We deliberately do NOT split allow-miss vs. deny-hit here: PolicyCheck's
// caller can't tell which branch tripped, and for the user the remediation
// is the same — open .dfmt/permissions.yaml. A few op-specific notes (cloud
// metadata for fetch, secret paths for read/write/edit) are added so users
// don't try to "fix" things that are deliberately walled off.
func policyDenyHint(op string) string {
	switch op {
	case "exec":
		return "  hint: all exec commands are allowed by default.\n" +
			"        Use .dfmt/permissions.yaml to restrict commands if needed."
	case "read":
		return "  hint: all read operations are allowed by default.\n" +
			"        Use .dfmt/permissions.yaml to restrict paths if needed."
	case "write", "edit":
		return "  hint: all write/edit operations are allowed by default.\n" +
			"        Use .dfmt/permissions.yaml to restrict paths if needed."
	case "fetch":
		return "  hint: .dfmt/permissions.yaml to review fetch rules — blocked by SSRF\n" +
			"        defenses (cloud-metadata IPs, loopback, RFC1918, link-local,\n" +
			"        file://) or an explicit deny rule. SSRF IP classes cannot be allow-listed."
	default:
		return "  hint: see .dfmt/permissions.yaml to inspect or override the policy."
	}
}

// Exec implements the Sandbox interface.

// BatchExec implements the Sandbox interface.
// Glob implements the Sandbox interface.
// execImpl performs the actual execution.
