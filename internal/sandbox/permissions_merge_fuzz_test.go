package sandbox

import (
	"strings"
	"testing"
)

// FuzzMergePoliciesHardDenyInvariant is the security-critical fuzz for
// ADR-0014's merge semantics. The invariant: for every input that an
// operator might write into .dfmt/permissions.yaml as `allow:exec:<X>`,
// if the base command of X (after path/case/.exe stripping) is on the
// hard-deny list, the merged policy MUST NOT permit the bare base
// command. A regression here re-opens the "permissive override
// re-enables rm/sudo/dd" hole.
//
// The fuzzer only generates allow rules; deny union has no filter and is
// covered by the unit tests in permissions_merge_test.go.
func FuzzMergePoliciesHardDenyInvariant(f *testing.F) {
	seeds := []string{
		"rm",
		"rm *",
		"rm -rf /tmp",
		"/usr/local/bin/rm",
		"RM.exe foo",
		"sudo apt update",
		"DEL bar",
		"shutdown -h now",
		"dd if=/dev/zero of=/dev/sda",
		"mkfs.ext4 /dev/sda",
		"my-rm-tool",  // not a real hard-deny base
		"git status",  // not a real hard-deny base
		"",
		"   ",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	base := DefaultPolicy()
	f.Fuzz(func(t *testing.T, ruleText string) {
		override := Policy{Allow: []Rule{{Op: "exec", Text: ruleText}}}
		merged, _ := MergePolicies(base, override)

		// If the rule's base command is on the hard-deny list, the
		// merged policy must reject the bare base command. The unit
		// test covers a fixed set of well-formed inputs; this
		// fuzz exercises the parser on garbage.
		if !isHardDenyExec(ruleText) {
			return
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
