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
// version.Current; release builds supply the value via
//
//	go build -ldflags "-X github.com/ersinkoc/dfmt/internal/version.Current=v0.2.0"
//
// (or whatever tag is being cut). Unstamped builds default to "dev" and then
// incorporate Go's VCS build info when available, so local builds no longer
// report a specific released tag they are not.
package version

import "runtime/debug"

const devVersion = "dev"

// Current is the DFMT release identity used by CLI output, MCP serverInfo, and
// daemon identity checks.
//
// Override release builds at build time with:
//
//	-ldflags "-X github.com/ersinkoc/dfmt/internal/version.Current=<value>"
//
// Unstamped builds start as "dev" and are expanded during init to include the
// VCS revision recorded by `debug.ReadBuildInfo`, e.g. `dev-0123456789ab` or
// `dev-0123456789ab-modified`.
var Current = devVersion

func init() {
	Current = resolveCurrent(Current, debug.ReadBuildInfo)
}

func resolveCurrent(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "" && stamped != devVersion {
		return stamped
	}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return devVersion
	}
	revision, modified := buildInfoVCS(info)
	if revision == "" {
		return devVersion
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	out := devVersion + "-" + revision
	if modified {
		out += "-modified"
	}
	return out
}

func buildInfoVCS(info *debug.BuildInfo) (revision string, modified bool) {
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return revision, modified
}
