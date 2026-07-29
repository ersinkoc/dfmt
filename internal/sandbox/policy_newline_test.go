package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Regression tests for newline handling in policy globs.
//
// Go's `.` does not match `\n`, so `**` compiled to `^.*$` and failed
// against ANY multi-line text. Under the documented default-permissive
// policy — allow:exec:**, and no exec deny rules at all — every multi-line
// command failed its ALLOW check and was then reported as "blocked by deny
// rule", naming a rule that does not exist. The accompanying hint said "all
// exec commands are allowed by default", so the two halves of the error
// contradicted each other.

func TestEvaluateAllowsMultiLineCommands(t *testing.T) {
	p := DefaultPolicy()
	cases := []string{
		"go version",
		"echo a; echo b",
		"echo a\necho b",
		"cat <<'EOF'\nline one\nline two\nEOF",
		"if true; then\n  echo yes\nfi",
	}
	for _, c := range cases {
		if !p.Evaluate("exec", c) {
			t.Errorf("Evaluate(exec, %q) = false under the default-permissive "+
				"policy, which has no exec deny rules at all", c)
		}
	}
}

// The default policy allows reads of any path. A path containing a newline
// is legal on Unix and must not produce a mystery denial.
func TestEvaluateAllowsPathsWithNewline(t *testing.T) {
	p := DefaultPolicy()
	if !p.Evaluate("read", "weird\nname.txt") {
		t.Error("Evaluate(read, path-with-newline) = false under allow:read:**")
	}
}

// EvaluateReason must name the rule kind actually responsible, because the
// remedies are opposites: remove a deny rule vs. add an allow rule.
func TestEvaluateReasonDistinguishesDenyFromNoAllowMatch(t *testing.T) {
	// Explicit deny: the built-in SSRF list.
	ok, reason := DefaultPolicy().EvaluateReason("fetch", "http://169.254.169.254/latest/meta-data/")
	if ok {
		t.Fatal("cloud-metadata fetch was allowed")
	}
	if reason != ReasonExplicitDeny {
		t.Errorf("reason = %v, want ReasonExplicitDeny", reason)
	}

	// No allow match: a policy with an allow list that doesn't cover the op.
	narrow := Policy{Version: 1, Allow: []Rule{{Op: "exec", Text: "git *"}}}
	narrow.CompileAll()
	ok, reason = narrow.EvaluateReason("exec", "curl example.com")
	if ok {
		t.Fatal("command outside the allow list was allowed")
	}
	if reason != ReasonNoAllowMatch {
		t.Errorf("reason = %v, want ReasonNoAllowMatch", reason)
	}

	// Allowed.
	if ok, reason := narrow.EvaluateReason("exec", "git status"); !ok || reason != ReasonAllowed {
		t.Errorf("EvaluateReason(git status) = (%v, %v), want (true, ReasonAllowed)", ok, reason)
	}
}

// Newline-transparency must not let a multi-line command smuggle a denied
// command past an explicit deny rule. `(?s)` makes deny globs match MORE
// text, never less — this pins that direction.
func TestNewlineDoesNotBypassExplicitDeny(t *testing.T) {
	p := Policy{
		Version: 1,
		Allow:   []Rule{{Op: "exec", Text: "**"}},
		Deny:    []Rule{{Op: "exec", Text: "*sudo*"}},
	}
	p.CompileAll()

	for _, c := range []string{
		"sudo whoami",
		"echo hi\nsudo whoami",
		"echo hi\nsudo whoami\necho bye",
	} {
		if p.Evaluate("exec", c) {
			t.Errorf("Evaluate(exec, %q) = true; a deny rule must still match "+
				"when the denied token sits on another line", c)
		}
	}
}

// End-to-end through Exec: a multi-line script must run, and the per-part
// chain checks must still reject a denied part.
func TestExecRunsMultiLineScript(t *testing.T) {
	sb := execTestSandbox(t)
	resp, err := sb.Exec(context.Background(), ExecReq{
		Code:    "echo first\necho second",
		Intent:  "two line script",
		Return:  "raw",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Exec on a multi-line script: %v", err)
	}
	if resp.Exit != 0 {
		t.Fatalf("Exit = %d, stderr = %q", resp.Exit, resp.Stderr)
	}
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(resp.RawStdout, want) {
			t.Errorf("RawStdout = %q, want it to contain %q", resp.RawStdout, want)
		}
	}
}
