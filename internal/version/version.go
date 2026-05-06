// Package version exposes the single source of truth for the DFMT
// release identity.
//
// Until v0.2.0 the version string lived in three independent places —
// cmd/dfmt/version.go (build-time ldflags target), internal/cli.Version
// (a constant that nothing in production read), and a literal "0.1.0"
// hardcoded into internal/transport/mcp.go::handleInitialize. The three
// drifted whenever a release was cut; an inspector reading the MCP
// serverInfo.version against the binary's --version output saw two
// different answers.
//
// This package consolidates the source: every consumer reads
// version.Current; the build supplies the value via
//
//	go build -ldflags "-X github.com/ersinkoc/dfmt/internal/version.Current=v0.2.0"
//
// (or whatever tag is being cut). The default below is the
// last-known-released tag so a `go install` from main produces a
// non-"dev" build out of the box; the Makefile's release target
// overrides it with the actual VERSION variable.
package version

// Current is the build-time-injected DFMT release identity.
//
// Reads:
//   - cmd/dfmt/main.go        --version output
//   - internal/cli/cli.go     re-exports as cli.Version
//   - internal/core/core.go   re-exports as core.Version
//   - internal/transport/mcp.go::handleInitialize  serverInfo.version
//
// Override at build time with:
//
//	-ldflags "-X github.com/ersinkoc/dfmt/internal/version.Current=<value>"
//
// The default tracks the most-recently-released tag so that an
// untagged `go install` produces a sensible string.
var Current = "v0.6.2"
