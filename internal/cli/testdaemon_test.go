package cli

import (
	"context"
	"testing"
	"time"
)

// runForegroundDaemonBriefly runs `dfmt daemon -foreground` the way the
// older tests did — in a goroutine, since it blocks — but waits for the
// daemon to come up and STOPS it afterwards. Those tests used to walk away,
// leaving a global daemon alive in the shared test process for the rest of
// the run; once the daemon repaired its own state files every 5 s, that
// stray daemon rewrote ~/.dfmt under unrelated tests and made them flaky
// (TestRunStopWithRunningDaemon, TestRunDashboard_UnixReportsBrowserUnreachable).
func runForegroundDaemonBriefly(t *testing.T) {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- Dispatch([]string{"daemon", "-foreground"}) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		testDaemonsMu.Lock()
		n := len(testDaemons)
		testDaemonsMu.Unlock()
		select {
		case code := <-done:
			t.Logf("daemon -foreground returned %d", code)
			stopTestDaemons()
			return
		default:
		}
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopTestDaemons()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("foreground daemon did not return after Stop")
	}
}

// stopTestDaemons stops every daemon trackTestDaemon recorded.
func stopTestDaemons() {
	testDaemonsMu.Lock()
	ds := testDaemons
	testDaemons = nil
	testDaemonsMu.Unlock()
	for _, d := range ds {
		ctx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
		_ = d.Stop(ctx)
		cancel()
	}
}
