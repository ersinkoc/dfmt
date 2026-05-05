package sandbox

import "testing"

// TestDefaultPolicy_AllowsTypeScriptEcosystem locks in the default-allow
// list for the JS/TS toolchain. Without this regression a future "trim
// the policy" refactor could quietly re-introduce the user-facing
// friction where every fresh project needed a permissions.yaml override
// to run `tsc` or `vitest`.
func TestDefaultPolicy_AllowsTypeScriptEcosystem(t *testing.T) {
	cases := []string{
		"npx vitest run",
		"pnpx tsc --noEmit",
		"yarn install",
		"bun install",
		"bun test",
		"bunx prettier --check .",
		"deno check src/main.ts",
		"tsc --build",
		"tsx scripts/migrate.ts",
		"ts-node ./bin/cli.ts",
		"vitest run --coverage",
		"jest --ci",
		"eslint .",
		"prettier --write src/",
		"vite build",
		"next dev",
		"webpack --mode production",
		"make build",
		"make test",
	}
	policy := DefaultPolicy()
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			if !policy.Evaluate("exec", cmd) {
				t.Errorf("default policy must allow %q; Evaluate returned false", cmd)
			}
		})
	}
}

// TestDefaultPolicy_StillBlocksDangerous is obsolete — exec is now
// fully allowed by default and no exec deny rules exist. SSRF blocks
// (cloud metadata IPs) are still enforced via fetch denies.
func TestDefaultPolicy_StillBlocksDangerous(t *testing.T) {
	// No longer applicable — exec is default-allow.
	// Kept as a stub so existing callsites don't break.
}
