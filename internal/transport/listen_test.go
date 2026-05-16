package transport

import (
	"errors"
	"net"
	"testing"
)

// TestEphemeralBind_HappyPath: a "host:port" with non-empty host
// becomes "host:0" — kernel-picks-port semantics, host preserved.
func TestEphemeralBind_HappyPath(t *testing.T) {
	got := ephemeralBind("127.0.0.1:8765")
	if got != "127.0.0.1:0" {
		t.Errorf("ipv4: want 127.0.0.1:0, got %q", got)
	}
	got = ephemeralBind("[::1]:8765")
	if got != "[::1]:0" {
		t.Errorf("ipv6: want [::1]:0, got %q", got)
	}
	got = ephemeralBind("localhost:9090")
	if got != "localhost:0" {
		t.Errorf("hostname: want localhost:0, got %q", got)
	}
}

// TestEphemeralBind_MalformedFallsBackToLoopback: a string that doesn't
// parse as host:port (or whose host portion is empty) returns the
// hardcoded 127.0.0.1:0 — the daemon stays bindable even with a typo'd
// config rather than refusing to start.
func TestEphemeralBind_MalformedFallsBackToLoopback(t *testing.T) {
	cases := []string{
		"",            // empty
		"not-a-bind",  // no colon
		":8765",       // empty host part
		"host:port:x", // too many colons
	}
	for _, in := range cases {
		got := ephemeralBind(in)
		if got != "127.0.0.1:0" {
			t.Errorf("ephemeralBind(%q): want 127.0.0.1:0, got %q", in, got)
		}
	}
}

// TestMetricKind_String covers the formatted name for each registered
// metric kind plus the catch-all default. The pretty name is what the
// /metrics endpoint emits as TYPE lines, so a typo would break
// Prometheus scrapers.
func TestMetricKind_String(t *testing.T) {
	cases := []struct {
		k    metricKind
		want string
	}{
		{metricCounter, "counter"},
		{metricGauge, "gauge"},
		{metricHistogram, "histogram"},
		{metricKind(999), "untyped"}, // unknown values fall through
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("metricKind(%d).String(): want %q, got %q", c.k, c.want, got)
		}
	}
}

// TestIsPortUnavailable_Classification is the Windows-specific
// errno classification probe. Verifies the four errno values that
// should trigger an ephemeral-port retry are reported as unavailable
// and a generic error is not.
func TestIsPortUnavailable_NonMatchingError(t *testing.T) {
	// A plain error never matches any of the bind-fallback errnos.
	if isPortUnavailable(errors.New("some other error")) {
		t.Error("plain error misclassified as port-unavailable")
	}
	if isPortUnavailable(nil) {
		t.Error("nil misclassified as port-unavailable")
	}
}

// TestIsPortUnavailable_RealBindCollision exercises isPortUnavailable
// via an actual bind collision: two listeners on the same port produce
// the OS's "address in use" errno, which is exactly what the fallback
// path needs to detect. Skipped if the OS refuses the first bind for
// any reason (e.g., locked-down CI).
func TestIsPortUnavailable_RealBindCollision(t *testing.T) {
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind ephemeral loopback: %v", err)
	}
	defer l1.Close()
	addr := l1.Addr().String()

	_, err = net.Listen("tcp", addr)
	if err == nil {
		t.Fatal("expected bind collision, got nil")
	}
	if !isPortUnavailable(err) {
		t.Errorf("bind-collision error not classified as port-unavailable: %v", err)
	}
}
