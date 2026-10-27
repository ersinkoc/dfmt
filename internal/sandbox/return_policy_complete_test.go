package sandbox

import (
	"strings"
	"testing"
)

// TestApplyReturnPolicyComplete: wire dedup trusts Complete to mean the
// agent got the whole content. The big tier's tail snippet is a Body too,
// and must not count.
func TestApplyReturnPolicyComplete(t *testing.T) {
	small := "ok\n"
	big := strings.Repeat("filler line without the keyword\n", 4000)
	cases := []struct {
		name, content, mode string
		want                bool
	}{
		{"inline", small, "auto", true},
		{"raw", big, "raw", true},
		{"tail snippet", big, "auto", false},
		{"summary", small, "summary", false},
		{"search", small, "search", false},
	}
	for _, tc := range cases {
		out := ApplyReturnPolicy(tc.content, "nomatchterm", tc.mode)
		if out.Complete != tc.want {
			t.Errorf("%s: Complete = %v, want %v (body %d bytes)", tc.name, out.Complete, tc.want, len(out.Body))
		}
	}
}
