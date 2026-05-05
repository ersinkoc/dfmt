package sandbox

import (
	"context"
	"strings"
	"testing"
)

// TestPolicyDenyHint_AllOps pins that every operation kind PolicyCheck can
// see produces a non-empty, actionable hint. Without this, a future op
// (added but forgotten in the switch) would silently fall through to a
// less-useful default message and the user would lose the remediation
// pointer they came to depend on.
func TestPolicyDenyHint_AllOps(t *testing.T) {
	cases := []struct {
		op          string
		mustContain string
	}{
		{"exec", ".dfmt/permissions.yaml"},
		{"read", ".dfmt/permissions.yaml"},
		{"write", ".dfmt/permissions.yaml"},
		{"edit", ".dfmt/permissions.yaml"},
		{"fetch", "SSRF"},
		{"unknown-op", ".dfmt/permissions.yaml"},
	}
	for _, c := range cases {
		got := policyDenyHint(c.op)
		if got == "" {
			t.Errorf("policyDenyHint(%q) returned empty", c.op)
			continue
		}
		if !strings.Contains(got, c.mustContain) {
			t.Errorf("policyDenyHint(%q) = %q, want to contain %q",
				c.op, got, c.mustContain)
		}
		// Must lead with the "  hint:" indent so it visually attaches to
		// the parent error line.
		if !strings.HasPrefix(strings.TrimLeft(got, " "), "hint:") {
			t.Errorf("policyDenyHint(%q) = %q, want to start with 'hint:' after indent", c.op, got)
		}
	}
}

// TestPolicyCheck_HintIsAppended exercises the wire-up: the hint produced
// by policyDenyHint must actually end up in the error returned by
// PolicyCheck. Catches a future refactor that drops the trailing %s%s.
// Under the default-permissive policy exec is allowed; we test fetch
// denial (cloud metadata IP) since SSRF blocks are always enforced.
func TestPolicyCheck_HintIsAppended(t *testing.T) {
	sb := NewSandbox("/tmp")
	err := sb.PolicyCheck("fetch", "http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("PolicyCheck(fetch cloud-metadata) returned nil, want denied")
	}
	msg := err.Error()
	// Existing tests assert this prefix — must remain present.
	if !strings.Contains(msg, "operation denied by policy") {
		t.Errorf("error = %q, want to contain 'operation denied by policy'", msg)
	}
	// The hint must come along.
	if !strings.Contains(msg, ".dfmt/permissions.yaml") {
		t.Errorf("error = %q, want to contain hint pointing to .dfmt/permissions.yaml", msg)
	}
}

// TestExec_ChainDenialIncludesHint covers the three Exec-chain branches
// (base-cmd allow-miss, full-cmd deny-hit, individual-part allow-miss).
// The hint suffix must be reachable from each.
func TestExec_ChainDenialIncludesHint(t *testing.T) {
	sb := NewSandbox("/tmp")
	ctx := context.Background()

	cases := []struct {
		name string
		code string
	}{
		// Fetch to cloud metadata IP is always denied (SSRF rule).
		{"fetch SSRF denial", "curl http://169.254.169.254/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := sb.Exec(ctx, ExecReq{Code: c.code, Lang: "sh"})
			if err == nil {
				t.Fatalf("Exec(%q) returned nil error, want denial", c.code)
			}
			msg := err.Error()
			if !strings.Contains(msg, "operation denied by policy") {
				t.Errorf("error = %q, want 'operation denied by policy'", msg)
			}
			if !strings.Contains(msg, ".dfmt/permissions.yaml") {
				t.Errorf("error = %q, want hint mentioning .dfmt/permissions.yaml", msg)
			}
		})
	}
}
