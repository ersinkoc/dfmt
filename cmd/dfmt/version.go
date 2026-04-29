package main

// The version string moved to internal/version.Current as part of the
// v0.2.0 consolidation work — see that package's doc comment. The
// build-time ldflag changed from
//
//	-X main.version=$VERSION
//
// to
//
//	-X github.com/ersinkoc/dfmt/internal/version.Current=$VERSION
//
// This file is intentionally empty; the prior `var version = "dev"`
// would now race the canonical value and read stale on `dfmt --version`.
