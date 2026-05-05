package sandbox

import (
	"testing"
)

// TestMergePoliciesHardDenyInvariant is a no-op test since the
// hard-deny list (hardDenyExecBaseCommands) is now empty.
// Renamed from Fuzz* to Test* so Go treats it as a unit test, not a fuzz test.
// Fuzz tests must call F.Fuzz or F.Fail; this stub would fail under fuzzing.
func TestMergePoliciesHardDenyInvariant(t *testing.T) {
	// No hard-deny exec base commands exist; no invariant to verify.
}
