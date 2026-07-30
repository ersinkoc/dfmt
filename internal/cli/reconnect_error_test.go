package cli

import (
	"strings"
	"testing"
)

// The unreachable-daemon error is what an agent sees when the proxy cannot
// reach or start a daemon. It has to name a next step, because the agent
// has no other signal — a bare "not reachable" leaves the user staring at a
// dead toolset with nothing to try.
func TestErrDaemonUnreachableIsActionable(t *testing.T) {
	msg := errDaemonUnreachable.Error()
	if msg == "" {
		t.Fatal("error message is empty")
	}
	if !strings.Contains(msg, "dfmt doctor") {
		t.Errorf("message = %q, want it to point at `dfmt doctor`", msg)
	}
}

// dialBackend must not panic or spawn anything when no daemon exists; it
// returns a backend whose first call fails, and renew() is what decides
// whether a daemon should be started.
func TestDialBackendWithNoDaemon(t *testing.T) {
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())
	t.Setenv("DFMT_DISABLE_AUTOSTART", "1")

	// Must return without hanging or panicking. Either outcome (a backend
	// that will fail on use, or nil) is acceptable — the contract is only
	// that dialing is side-effect free.
	_ = dialBackend(t.TempDir())
}
