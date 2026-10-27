package cli

import (
	"runtime"
	"testing"
	"time"
)

// TestGracefulStopWaitCoversDaemonGrace: the stop path waited 5 s while the
// daemon may take its 10 s shutdown grace, so a busy daemon was SIGKILLed
// mid-Stop on every upgrade (observed: "escalating to forced kill" on Linux).
func TestGracefulStopWaitCoversDaemonGrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot deliver the graceful signal; the short wait is deliberate")
	}
	if got := gracefulStopWait(); got < 10*time.Second {
		t.Errorf("gracefulStopWait = %s, shorter than the daemon's default shutdown grace", got)
	}
}
