package transport

import (
	"net"
	"sync"
	"testing"
	"time"
)

// TestLimitListenerCapsConcurrent pins V-12: the wrapper must cap the
// number of simultaneously-live connections, blocking Accept once the
// cap is reached and unblocking when an existing conn closes.
func TestLimitListenerCapsConcurrent(t *testing.T) {
	const cap = 3

	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer inner.Close()

	ll := newLimitListener(inner, cap)
	defer ll.Close()

	addr := inner.Addr().String()

	// Open `cap` connections and hold them. Each must succeed and
	// immediately produce an accepted server-side conn.
	clients := make([]net.Conn, 0, cap)
	servers := make([]net.Conn, 0, cap)
	defer func() {
		for _, c := range clients {
			if c != nil {
				_ = c.Close()
			}
		}
		for _, c := range servers {
			if c != nil {
				_ = c.Close()
			}
		}
	}()

	for i := 0; i < cap; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
		s, err := ll.Accept()
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		servers = append(servers, s)
	}

	// One more client tries to connect; the kernel will accept it at the
	// TCP level but Accept on our wrapper must NOT yet return because the
	// semaphore is full. Probe with a goroutine + timeout.
	probe, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("probe dial: %v", err)
	}
	defer probe.Close()

	gotAccept := make(chan net.Conn, 1)
	go func() {
		c, err := ll.Accept()
		if err == nil {
			gotAccept <- c
		} else {
			close(gotAccept)
		}
	}()

	select {
	case c := <-gotAccept:
		if c != nil {
			c.Close()
		}
		t.Fatal("Accept returned while at cap; expected block")
	case <-time.After(100 * time.Millisecond):
		// Expected: Accept is blocked on the semaphore.
	}

	// Close one held server-side conn. The semaphore slot frees and the
	// blocked Accept must unblock with the probe connection.
	_ = servers[0].Close()
	servers[0] = nil

	select {
	case c := <-gotAccept:
		if c == nil {
			t.Fatal("Accept errored after slot freed")
		}
		_ = c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not unblock after a slot freed")
	}
}

// TestLimitListenerMaxZeroIsPassthrough confirms the cap=0 / cap<0 escape
// hatch: callers don't have to special-case "no cap" — the wrapper just
// returns the inner listener unchanged.
func TestLimitListenerMaxZeroIsPassthrough(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer inner.Close()
	if got := newLimitListener(inner, 0); got != inner {
		t.Errorf("max=0 should return inner unchanged; got wrapper")
	}
	if got := newLimitListener(inner, -1); got != inner {
		t.Errorf("max=-1 should return inner unchanged; got wrapper")
	}
}

// TestLimitListenerCloseUnblocksAcquire pins the second half of the
// graceful-shutdown contract: Close on the wrapped listener must wake
// any goroutine blocked in acquire() so it doesn't leak after the
// daemon stops.
func TestLimitListenerCloseUnblocksAcquire(t *testing.T) {
	const cap = 1

	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ll := newLimitListener(inner, cap)

	// Saturate the cap with one held conn.
	addr := inner.Addr().String()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	s, err := ll.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer s.Close()

	// Now block another Accept on the semaphore.
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = ll.Accept() // expected to error after Close
		close(done)
	}()

	// Give the goroutine a moment to land in acquire().
	time.Sleep(50 * time.Millisecond)

	// Close the wrapper; the blocked Accept must unblock.
	if err := ll.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock acquire()")
	}
	wg.Wait()
}
