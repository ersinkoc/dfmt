package redact

import (
	"testing"
)

// FuzzRedact hardens the redactor against arbitrary inputs. The redactor
// runs before journal writes and on every recall snapshot — a panic or
// pathological regex backtrack here takes down the daemon. The seed corpus
// includes real provider markers (AWS, GitHub, JWT) and adversarial inputs
// (very long strings, control chars, partial markers).
//
// Invariants:
//   - never panics
//   - output length is bounded relative to input (no exponential blow-up)
//   - idempotent: Redact(Redact(s)) == Redact(s)
//
// Run with: go test ./internal/redact/ -run=^$ -fuzz=FuzzRedact -fuzztime=30s
func FuzzRedact(f *testing.F) {
	f.Add("hello world")
	f.Add("AKIAIOSFODNN7EXAMPLE")
	f.Add("ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	f.Add("eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature")
	f.Add("password=hunter2")
	f.Add("")
	// Adversarial: many short tokens
	f.Add("a a a a a a a a a a a a a a a a a a a")
	// Adversarial: long single token
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	r := NewRedactor()

	f.Fuzz(func(t *testing.T, s string) {
		// Bound input length so the fuzzer doesn't waste time on
		// pathologically large strings — the redactor's wall-clock is the
		// concern, not how it handles 10MB strings.
		if len(s) > 8192 {
			t.Skip()
		}

		out1 := r.Redact(s)

		// Linear-bounded output. Replacements are typically the same length
		// as or shorter than the matched secret; the redactor occasionally
		// expands (e.g., wraps in markers), but a 4× cap is generous.
		if len(out1) > 4*len(s)+128 {
			t.Fatalf("output expansion: in=%d out=%d", len(s), len(out1))
		}

		// Idempotence: running Redact on already-redacted output should be
		// a fixed point. If not, an attacker could feed redacted output
		// back through a chain and observe behavioural divergence.
		out2 := r.Redact(out1)
		if out1 != out2 {
			t.Fatalf("not idempotent:\n in:  %q\n one: %q\n two: %q", s, out1, out2)
		}
	})
}
