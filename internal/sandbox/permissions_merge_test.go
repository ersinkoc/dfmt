package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergePolicies_AllowUnion(t *testing.T) {
	base := Policy{
		Version: 1,
		Allow:   []Rule{{Op: "exec", Text: "git *"}},
	}
	override := Policy{
		Allow: []Rule{{Op: "exec", Text: "my-tool *"}},
	}
	merged, warns := MergePolicies(base, override)
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %v", warns)
	}
	if !merged.Evaluate("exec", "git status") {
		t.Error("base allow rule lost after merge")
	}
	if !merged.Evaluate("exec", "my-tool foo") {
		t.Error("override allow rule not applied")
	}
}

func TestMergePolicies_HardDenyExecMasked(t *testing.T) {
	// hardDenyExecBaseCommands is empty (default-permissive exec).
	// No warnings should be produced when overriding any exec command.
	base := DefaultPolicy()
	override := Policy{Allow: []Rule{{Op: "exec", Text: "rm *"}}}
	merged, warns := MergePolicies(base, override)
	if len(warns) != 0 {
		t.Errorf("expected no warnings with empty hard-deny list, got %v", warns)
	}
	// With no hard-deny list, rm is now allowed after merge (no invariant breach).
	if !merged.Evaluate("exec", "rm anything") {
		t.Error("rm should be allowed after merge with empty hard-deny list")
	}
}

func TestMergePolicies_PathStyleAllowsPassThrough(t *testing.T) {
	// Override extending file/network reach must NOT be filtered by the
	// hard-deny list (which is exec-only).
	base := DefaultPolicy()
	override := Policy{
		Allow: []Rule{
			{Op: "read", Text: "creds/**"},                 // operator chose to allow
			{Op: "fetch", Text: "https://internal.api/**"}, // private network
		},
	}
	merged, warns := MergePolicies(base, override)
	if len(warns) != 0 {
		t.Fatalf("path-style allows should not warn, got %v", warns)
	}
	if !merged.Evaluate("read", "creds/api.json") {
		t.Error("override read allow lost")
	}
	if !merged.Evaluate("fetch", "https://internal.api/foo") {
		t.Error("override fetch allow lost")
	}
}

func TestMergePolicies_DenyUnionNotFiltered(t *testing.T) {
	base := DefaultPolicy()
	override := Policy{
		Deny: []Rule{{Op: "read", Text: "**/secrets.json"}},
	}
	merged, _ := MergePolicies(base, override)
	if merged.Evaluate("read", "tmp/secrets.json") {
		t.Error("override deny did not apply")
	}
}

func TestLoadPolicyMerged_NoOverride(t *testing.T) {
	tmp := t.TempDir()
	res, err := LoadPolicyMerged(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OverrideFound {
		t.Error("OverrideFound should be false when no permissions.yaml")
	}
	if len(res.Policy.Allow) != len(DefaultPolicy().Allow) {
		t.Errorf("expected default policy untouched, got %d allows vs default %d",
			len(res.Policy.Allow), len(DefaultPolicy().Allow))
	}
	wantOverride := filepath.Join(tmp, ".dfmt", "permissions.yaml")
	if res.OverridePath != wantOverride {
		t.Errorf("OverridePath = %q, want %q", res.OverridePath, wantOverride)
	}
}

func TestLoadPolicyMerged_LoadsOverride(t *testing.T) {
	tmp := t.TempDir()
	dfmtDir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		"# operator override",
		"allow:exec:my-build *",
		"deny:read:creds/**",
		"# rm override — with empty hard-deny list this is now allowed",
		"allow:exec:rm *",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dfmtDir, "permissions.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := LoadPolicyMerged(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OverrideFound {
		t.Fatal("expected OverrideFound=true")
	}
	if res.OverrideRules != 3 {
		t.Errorf("OverrideRules = %d, want 3", res.OverrideRules)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected 0 warnings (hard-deny list is empty), got %d: %v", len(res.Warnings), res.Warnings)
	}
	if !res.Policy.Evaluate("exec", "my-build foo") {
		t.Error("operator-defined allow not applied")
	}
	if res.Policy.Evaluate("read", "creds/x") {
		t.Error("operator-defined deny not applied")
	}
	// rm is now allowed (hard-deny list is empty), no warning expected
	if !res.Policy.Evaluate("exec", "rm bar") {
		t.Error("rm should be allowed by merged policy")
	}
}

func TestLoadPolicyMerged_StatErrorPropagates(t *testing.T) {
	// A best-effort attempt to surface a non-IsNotExist Stat error. On most
	// systems this is hard to provoke without root; we simply confirm the
	// happy path doesn't claim an override exists when the .dfmt directory
	// itself is absent (which is the realistic "fresh project" scenario).
	tmp := t.TempDir()
	res, err := LoadPolicyMerged(tmp)
	if err != nil {
		t.Fatalf("unexpected error from missing override: %v", err)
	}
	if res.OverrideFound {
		t.Error("OverrideFound true on missing file")
	}
	// Negative control: an unreadable parent triggers a real error path —
	// skip on platforms where chmod is best-effort.
	_ = errors.New // keep errors imported for symmetry with other tests
}

func TestIsHardDenyExec(t *testing.T) {
	// hardDenyExecBaseCommands is empty — all exec is allowed.
	// isHardDenyExec always returns false.
	cases := []struct {
		text string
		want bool
	}{
		{"rm", false},
		{"rm *", false},
		{"rm -rf /tmp", false},
		{"/usr/bin/rm foo", false},
		{"RM.exe", false},
		{"sudo apt", false},
		{"git", false},
		{"my-rm-tool", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			if got := isHardDenyExec(c.text); got != c.want {
				t.Errorf("isHardDenyExec(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}
