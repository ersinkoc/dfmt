package cli

import (
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
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

// TestGlobalSuccessor: after the stopped daemon exits, its pid/port/lock
// files may already belong to a successor spawned during the shutdown.
// stopGlobalDaemon deleted them unconditionally, orphaning that daemon
// (observed: alive, listening, no port file, lock unlinked).
func TestGlobalSuccessor(t *testing.T) {
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())
	const stopped = 999999 // not a live PID
	if got := globalSuccessor(stopped); got != 0 {
		t.Fatalf("no pid file: successor = %d, want 0", got)
	}
	live := os.Getpid() // a process that is certainly running
	if err := os.WriteFile(project.GlobalPIDPath(), []byte(strconv.Itoa(live)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := globalSuccessor(stopped); got != live {
		t.Errorf("successor = %d, want %d", got, live)
	}
	if got := globalSuccessor(live); got != 0 {
		t.Errorf("the stopped daemon's own pid file counted as a successor (%d)", got)
	}
}
