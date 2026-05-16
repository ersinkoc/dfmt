// Package osutil holds host-OS classification helpers. It centralizes
// the GOOS string-literal checks that previously lived as a per-package
// `goosWindows = "windows"` constant in six different packages
// (client, daemon, project, sandbox, setup, transport). Having one
// canonical home means a future port to a new GOOS only touches this
// file.
//
// The package is deliberately minimal — no third-party deps, only the
// stdlib's runtime package. New helpers should follow the same shape:
// short, branchless, behavior-equivalent to a `runtime.GOOS ==
// "<name>"` check at the call site.
package osutil

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Windows is the canonical GOOS string for Microsoft Windows. Use this
// constant rather than the literal "windows" so call sites grep cleanly
// and a hypothetical future rename (e.g., GOOS="windows-arm64-uwp") can
// be applied here once.
const Windows = "windows"

// IsWindows reports whether the running binary's GOOS is Windows.
// Equivalent to `runtime.GOOS == "windows"` but more readable at call
// sites and grep-friendly when auditing platform-specific code paths.
func IsWindows() bool { return runtime.GOOS == Windows }

// SamePath compares two filesystem paths for equality using the host
// OS's case-sensitivity rules. Windows NTFS/ReFS is case-insensitive
// (paths "C:\\Foo" and "c:\\foo" address the same inode); POSIX
// filesystems are case-sensitive by default.
//
// Inputs are compared as-given — no filepath.Clean, no symlink
// resolution. Callers that need normalization should clean their
// arguments before calling (or use SameCleanPath).
//
// Closes the duplicate-implementation cluster: pre-osutil, four
// near-identical functions lived in cli/dispatch.go (samePathCLI),
// setup/setup.go (samePath), transport/http.go (pathsEqualForRuntime),
// and cli/doctor.go (pathsEqual — that one also Cleaned).
func SamePath(a, b string) bool {
	if a == b {
		return true
	}
	if IsWindows() {
		return strings.EqualFold(a, b)
	}
	return false
}

// SameCleanPath is SamePath with filepath.Clean applied to both
// arguments first. Useful when comparing paths that may differ only in
// trailing slashes, "./" components, or other lexical noise — e.g.,
// matching an MCP server's `command` field against a doctor-resolved
// binary path.
func SameCleanPath(a, b string) bool {
	return SamePath(filepath.Clean(a), filepath.Clean(b))
}
