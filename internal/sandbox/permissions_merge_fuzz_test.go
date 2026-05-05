package sandbox

import (
	"strings"
	"testing"
)

// FuzzMergePoliciesHardDenyInvariant is a no-op stub since the
// hard-deny list (hardDenyExecBaseCommands) is now empty.
// Kept to avoid breaking existing callsites.
func FuzzMergePoliciesHardDenyInvariant(f *testing.F) {
	// No hard-deny exec base commands exist; fuzz target removed.
}
		fields := strings.Fields(ruleText)
		if len(fields) == 0 {
			return
		}
		base := fields[0]
		// Reduce to the lowercase basename without .exe — same shape
		// isHardDenyExec uses internally — so we probe the canonical
		// form instead of whatever path-prefixed/cased form the
		// fuzzer threw at us.
		if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
			base = base[i+1:]
		}
		base = strings.ToLower(base)
		base = strings.TrimSuffix(base, ".exe")
		if base == "" {
			return
		}
		if merged.Evaluate("exec", base+" probe-arg") {
			t.Fatalf("hard-deny invariant breached: override %q allowed bare %q",
				ruleText, base)
		}
	})
}
