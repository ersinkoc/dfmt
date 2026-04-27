package transport

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestSocketServerStopWaitsForInflightConn pins finding #6: Stop() must
// wait for in-flight handleConn goroutines to exit before returning.
// Pre-fix Stop returned as soon as the listener closed, racing the
// daemon's journal/index teardown against handlers still using those
// resources.
//
// Strategy: Stop's drain only completes when connWG reaches zero. We hold
// connWG up by one (no real conn — pure phantom counter), call Stop on a
// goroutine, observe that it does NOT return immediately, then release
// the phantom and observe that Stop now returns. This isolates the drain
// behaviour from listener/Read interactions, which on Unix do not unblock
// each other (closing a listener does not interrupt existing reads).
func TestSocketServerStopWaitsForInflightConn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets unavailable on Windows")
	}

	prevTimeout := stopDrainTimeout
	stopDrainTimeout = 2 * time.Second // generous; we'll release the phantom well before this
	defer func() { stopDrainTimeout = prevTimeout }()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "drain-wait.sock")
	server := NewSocketServer(sockPath, &Handlers{})

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Phantom in-flight conn — drain blocks until Done() fires.
	server.connWG.Add(1)

	stopDone := make(chan struct{})
	go func() {
		_ = server.Stop(context.Background())
		close(stopDone)
	}()

	// Stop should NOT have returned yet — it's blocked in connWG.Wait().
	select {
	case <-stopDone:
		t.Fatal("Stop returned before phantom conn drained; WaitGroup not honoured")
	case <-time.After(100 * time.Millisecond):
	}

	// Release the phantom; Stop should now drain promptly.
	server.connWG.Done()

	select {
	case <-stopDone:
	case <-time.After(stopDrainTimeout + 500*time.Millisecond):
		t.Fatal("Stop did not return after phantom released; drain mechanism broken")
	}
}

// TestSocketServerStopDrainTimeoutFires forces the drain timeout path: a
// handler that intentionally blocks must NOT prevent Stop from returning
// within stopDrainTimeout + epsilon. We set stopDrainTimeout very short
// for this test to avoid sleeping the full default 5s.
func TestSocketServerStopDrainTimeoutFires(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets unavailable on Windows")
	}

	prevTimeout := stopDrainTimeout
	stopDrainTimeout = 100 * time.Millisecond
	defer func() { stopDrainTimeout = prevTimeout }()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "drain-timeout.sock")
	server := NewSocketServer(sockPath, &Handlers{})

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Manually inflate connWG so the drain has nothing to drain TO. This
	// simulates a stuck handler. The OS goroutine that increments
	// genuinely runs; we just keep one phantom counter pinned.
	server.connWG.Add(1)
	defer server.connWG.Done() // release after Stop returns

	stopDone := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = server.Stop(context.Background())
		stopDone <- time.Since(start)
	}()

	select {
	case d := <-stopDone:
		if d < stopDrainTimeout {
			t.Fatalf("Stop returned in %s; expected at least %s (drain wait)", d, stopDrainTimeout)
		}
		// Sanity: shouldn't have waited orders of magnitude past timeout.
		if d > stopDrainTimeout+500*time.Millisecond {
			t.Fatalf("Stop took %s; expected ~%s + small epsilon", d, stopDrainTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after 2s; drain timeout is broken")
	}
}

// TestSocketConnLifetimeCapFires pins finding #12: a connection that hits
// socketConnMaxLifetime must be force-closed by the AfterFunc timer so
// the handler goroutine exits even when the peer never disconnects.
//
// We override socketConnMaxLifetime to a small value, dial, hold the
// connection idle, and assert the conn is closed by the server within
// the cap window.
func TestSocketConnLifetimeCapFires(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets unavailable on Windows")
	}

	prevLifetime := socketConnMaxLifetime
	prevIdle := socketReadIdleTimeout
	socketConnMaxLifetime = 200 * time.Millisecond
	// Idle timeout longer than lifetime so we test the lifetime path,
	// not the idle path.
	socketReadIdleTimeout = 30 * time.Second
	defer func() {
		socketConnMaxLifetime = prevLifetime
		socketReadIdleTimeout = prevIdle
	}()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "lifetime.sock")
	server := NewSocketServer(sockPath, &Handlers{})

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Stop(context.Background())

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Read from the conn — when the server's AfterFunc fires it calls
	// conn.Close(), which causes our Read to return EOF. If we get EOF
	// within the cap window + a small grace, the lifetime cap worked.
	var closed atomic.Bool
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		_, _ = conn.Read(buf) // returns when server closes
		closed.Store(true)
		close(done)
	}()

	select {
	case <-done:
		if !closed.Load() {
			t.Fatal("Read returned but closed flag not set")
		}
	case <-time.After(socketConnMaxLifetime + time.Second):
		t.Fatalf("conn not closed within %s; lifetime cap did not fire", socketConnMaxLifetime+time.Second)
	}
}
