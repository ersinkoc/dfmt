package sandbox

import (
	"net/http"
	"strings"
	"testing"
)

// TestFilterResponseHeaders: Set-Cookie reached the agent verbatim while
// the body was redacted, and multi-KB CSP headers rode along on every call.
func TestFilterResponseHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/html")
	h.Set("Location", "/next")
	h.Set("X-Ratelimit-Remaining", "42")
	h.Set("Set-Cookie", "session=s3cr3t")
	h.Set("Content-Security-Policy", strings.Repeat("default-src 'self'; ", 200))
	h.Set("Link", strings.Repeat("<https://example.com/p>; rel=next, ", 100))

	got := filterResponseHeaders(h)
	for _, keep := range []string{"Content-Type", "Location", "X-Ratelimit-Remaining", "Link"} {
		if _, ok := got[keep]; !ok {
			t.Errorf("useful header %s dropped", keep)
		}
	}
	for _, drop := range []string{"Set-Cookie", "Content-Security-Policy"} {
		if _, ok := got[drop]; ok {
			t.Errorf("header %s reached the agent", drop)
		}
	}
	if n := len(got["Link"]); n > maxResponseHeaderBytes {
		t.Errorf("Link is %d bytes, want <= %d", n, maxResponseHeaderBytes)
	}
}
