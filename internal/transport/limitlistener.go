package transport

import (
	"errors"
	"net"
	"sync"
)

// MaxConcurrentConnections is the per-listener concurrent-connection cap.
// Closes V-12: without it, an attacker on the loopback can park 10k HTTP
// or socket connections, each holding a 1 MiB body buffer (≈10 GiB) plus
// goroutine + per-conn idle-timer state — a same-host DoS that the
// sandbox-level semaphores (exec=4, fetch=8, read=8, write=4) do not
// gate because they only fire after Accept and after a full request is
// parsed.
//
// 128 is well above any realistic single-user scenario (a couple of
// editor instances + dashboard tab + CLI invocations rarely exceeds a
// dozen) but small enough to bound resource usage on a mass-Accept
// attack. The value is a constant rather than a config knob because
// V-12 is a security floor; operators with legitimate fan-out needs
// can raise it via a code change.
const MaxConcurrentConnections = 128

// errListenerClosed is returned by acquire when the listener has been
// closed mid-accept. It mirrors the shape of the net.errClosing error
// the standard library returns from a closed listener so callers see
// the expected "use of closed network connection" termination semantics.
var errListenerClosed = errors.New("limit listener closed")

// limitListener wraps a net.Listener with a fixed-capacity semaphore.
// Each accepted connection holds one slot; the slot is released on
// Conn.Close. When the cap is reached, Accept blocks until a slot
// frees, the listener is closed, or the underlying listener returns
// an error.
type limitListener struct {
	net.Listener
	sem    chan struct{}
	closed chan struct{}
	once   sync.Once
}

// newLimitListener wraps inner with a concurrent-connection cap of max.
// max <= 0 disables the cap (returns inner unchanged) so callers don't
// have to special-case "unlimited" environments.
func newLimitListener(inner net.Listener, max int) net.Listener {
	if max <= 0 {
		return inner
	}
	return &limitListener{
		Listener: inner,
		sem:      make(chan struct{}, max),
		closed:   make(chan struct{}),
	}
}

func (l *limitListener) acquire() error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-l.closed:
		return errListenerClosed
	}
}

func (l *limitListener) release() {
	<-l.sem
}

// Accept blocks until a slot is available, then accepts a connection.
// The returned conn releases its slot exactly once on Close.
//
// Acquire-then-accept (rather than accept-then-acquire) so Accept's
// blocking behavior degrades gracefully: under load, new TCP/Unix
// connection attempts queue at the kernel level and the listener
// goroutine doesn't spin or burn descriptors above the cap. The
// kernel's listen-backlog provides the upstream backpressure.
func (l *limitListener) Accept() (net.Conn, error) {
	if err := l.acquire(); err != nil {
		return nil, err
	}
	c, err := l.Listener.Accept()
	if err != nil {
		l.release()
		return nil, err
	}
	return &limitConn{Conn: c, release: l.release}, nil
}

// Close closes the listener and signals any goroutines blocked in
// acquire() to exit.
func (l *limitListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

// limitConn wraps a net.Conn so its Close releases the parent
// listener's semaphore slot exactly once. Multiple Close calls on the
// same conn (an explicit Close + a deferred Close, or a wrapping
// http.Server that closes during shutdown) must not double-decrement.
type limitConn struct {
	net.Conn
	release  func()
	released sync.Once
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.released.Do(c.release)
	return err
}
