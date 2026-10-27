package sandbox

import (
	"net"
	"net/http"
	"net/url"
	"testing"
)

// SBX-13: transition and reserved prefixes the original predicate chain
// missed. 64:ff9b::7f00:1 is 127.0.0.1 through a NAT64 gateway.
func TestIsBlockedIPTransitionAndReserved(t *testing.T) {
	for _, s := range []string{
		"64:ff9b::7f00:1", "64:ff9b:1::a9fe:a9fe", "2002:7f00:1::1", "2001:0:4136:e378::1",
		"192.0.0.192", "198.18.0.1", "198.19.255.254", "240.0.0.1", "255.255.255.255",
	} {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s not blocked", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "2606:4700:4700::1111", "198.20.0.1"} {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("public address %s blocked", s)
		}
	}
}

// SBX-12: a deny rule must hold on redirect hops, not only on the URL the
// agent passed in.
func TestFetchRedirectHonorsDenyRule(t *testing.T) {
	policy := DefaultPolicy()
	policy.Deny = append(policy.Deny, Rule{Op: "fetch", Text: "https://denied.example.org/**"})
	sb := NewSandboxWithPolicy(t.TempDir(), policy)

	hop := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}
	if err := sb.checkFetchRedirect(hop("https://denied.example.org/secret"), nil); err == nil {
		t.Error("redirect to a host named by a deny rule was allowed")
	}
	if err := sb.checkFetchRedirect(hop("https://1.1.1.1/next"), nil); err != nil {
		t.Errorf("redirect to an allowed public URL refused: %v", err)
	}
}
