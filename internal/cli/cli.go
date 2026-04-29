package cli

import "github.com/ersinkoc/dfmt/internal/version"

// Version is the CLI version. Mirrors internal/version.Current — the
// single build-time-injected source of truth. Kept as a thin re-export
// so existing call sites (and external tooling) that import
// internal/cli/Version keep compiling.
var Version = version.Current
