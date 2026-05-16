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

import "runtime"

// Windows is the canonical GOOS string for Microsoft Windows. Use this
// constant rather than the literal "windows" so call sites grep cleanly
// and a hypothetical future rename (e.g., GOOS="windows-arm64-uwp") can
// be applied here once.
const Windows = "windows"

// IsWindows reports whether the running binary's GOOS is Windows.
// Equivalent to `runtime.GOOS == "windows"` but more readable at call
// sites and grep-friendly when auditing platform-specific code paths.
func IsWindows() bool { return runtime.GOOS == Windows }
