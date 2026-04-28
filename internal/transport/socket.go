package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/logging"
)

var socketReadIdleTimeout = 60 * time.Second

// socketConnMaxLifetime is a hard ceiling on a single connection's total
// lifetime. socketReadIdleTimeout already kills idle peers, but a peer that
// drips one request just under the idle timeout keeps a goroutine pinned
// indefinitely. 30 minutes is well above any legitimate single-session
// workflow and well below "leaked goroutine for the daemon's whole uptime".
// Set to 0 to disable.
var socketConnMaxLifetime = 30 * time.Minute

// SocketServer listens on a Unix socket and handles JSON-RPC requests.
type SocketServer struct {
	path     string
	listener net.Listener
	handlers *Handlers
	mu       sync.Mutex
	running  bool

	// serverCtx is derived from the Start ctx; serverCancel fires on Stop so
	// every in-flight dispatch sees cancellation propagate. Pre-fix the
	// handler dispatch used context.Background(), which meant SIGTERM during
	// a long dfmt_exec waited for the handler's own timeout instead of the
	// daemon's shutdown ctx.
	serverCtx    context.Context
	serverCancel context.CancelFunc

	// connWG tracks every accepted-and-dispatched connection so Stop() can
	// drain them with a bounded wait. Pre-fix Stop returned as soon as the
	// listener closed, racing the daemon's journal.Close / index persist
	// against in-flight handlers that still held references to those
	// resources. The HTTP transport gets the same property for free via
	// http.Server.Shutdown; the socket transport had no equivalent.
	connWG sync.WaitGroup
}

// stopDrainTimeout caps how long Stop() waits for in-flight connections to
// finish. Past this, Stop returns and the OS unwinds the rest. 5s is
// generous for a handler that has already observed serverCtx cancellation —
// most calls return promptly; a long-running exec finishes whatever syscall
// it was on and unwinds.
var stopDrainTimeout = 5 * time.Second

// NewSocketServer creates a new socket server.
func NewSocketServer(socketPath string, handlers *Handlers) *SocketServer {
	return &SocketServer{
		path:     socketPath,
		handlers: handlers,
	}
}

// Start starts the socket server.
func (s *SocketServer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("server already running")
	}

	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("create socket dir: %w", err)
	}

	// Remove existing socket
	os.Remove(s.path)

	// Listen with a restrictive umask on Unix so the socket never exists with
	// broad default permissions before the chmod below runs.
	ln, err := listenUnixSocket(s.path)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen: %w", err)
	}

	// Set permissions. A failure here is unusual on the platforms where Unix
	// sockets exist, but if it happens we log so the operator can spot the
	// regression — silently allowing 0666 perms on the socket would let any
	// local user dial the daemon.
	if cerr := os.Chmod(s.path, 0o700); cerr != nil {
		logging.Warnf("chmod socket: %v", cerr)
	}

	s.listener = ln
	s.running = true
	s.serverCtx, s.serverCancel = context.WithCancel(ctx)
	s.mu.Unlock()

	// Accept connections. serve uses serverCtx so handleConn can derive
	// per-request contexts that observe Stop()'s cancel.
	go s.serve(s.serverCtx)

	return nil
}

// serve handles incoming connections. On Accept error we bail when the
// server has been stopped — otherwise we'd spin at 100% CPU on the
// "listener closed" error that follows Stop(). Previously the select's
// default branch just `continue`d, which only exited if the Start ctx was
// cancelled — but daemon.Stop uses a fresh ctx, so the goroutine would
// spin forever.
func (s *SocketServer) serve(ctx context.Context) {
	// Recover from any panic in the accept loop. handleConn has its own
	// recover; this guard catches anything in the loop body itself
	// (s.listener.Accept impl, future refactors). Without it, a panic kills
	// the daemon's serve goroutine with no diagnostic.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "socket serve panic recovered: %v\n", r)
		}
	}()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			running := s.running
			s.mu.Unlock()
			if !running {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		s.connWG.Add(1)
		go func(c net.Conn) {
			defer s.connWG.Done()
			s.handleConn(ctx, c)
		}(conn)
	}
}

// handleConn handles a single connection. A defer recover prevents a
// misbehaving request from tearing down the daemon; a nil-handlers guard
// makes the server usable in tests that build a SocketServer without a
// full Handlers.
//
// serverCtx is the server-lifetime context — Stop() cancels it so any
// in-flight dispatch unblocks promptly. Each request derives its own
// short-lived ctx so a finished dispatch frees its child context node
// rather than accreting one per request on the parent.
func (s *SocketServer) handleConn(serverCtx context.Context, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "socket handleConn panic recovered: %v\n", r)
		}
		_ = conn.Close()
	}()

	if s.handlers == nil {
		return
	}

	// Hard cap on total connection lifetime so a peer that drips one
	// request per (idle - ε) seconds cannot pin this goroutine forever.
	// SetReadDeadline below would clobber a SetDeadline-based cap on every
	// iteration, so use a one-shot timer that closes the conn — closing
	// unblocks ReadRequest and the loop exits cleanly.
	if socketConnMaxLifetime > 0 {
		t := time.AfterFunc(socketConnMaxLifetime, func() { _ = conn.Close() })
		defer t.Stop()
	}

	// Stamp every request from this connection with a per-connection
	// session ID (ADR-0011). Two distinct CLI invocations or two
	// concurrent dashboard polls now have independent wire-dedup buckets:
	// one caller's repeated reads still trigger "(unchanged)" for them,
	// but the other caller still sees full payload on its first read.
	// ULID gives time-sortable uniqueness without external deps.
	sessionID := string(core.NewULID(time.Now()))
	serverCtx = WithSessionID(serverCtx, sessionID)

	codec := NewCodec(conn)

	for {
		if socketReadIdleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(socketReadIdleTimeout))
		}
		req, err := codec.ReadRequest()
		if err != nil {
			return
		}

		reqCtx, cancel := context.WithCancel(serverCtx)
		resp, err := s.dispatch(reqCtx, req)
		cancel()
		if err != nil {
			// Decode-time failures (unknown method already handled below
			// via -32601 string match isn't reliable, so we only special-case
			// ParamsError → -32602; everything else is -32603). The previous
			// blanket -32603 made every malformed-params response look like
			// an internal server fault, hiding caller bugs (typo'd field
			// names, wrong types) under the wrong error class.
			code := -32603
			if IsParamsError(err) {
				code = -32602
			}
			if werr := codec.WriteError(req.ID, code, err.Error(), nil); werr != nil {
				return
			}
			continue
		}

		if werr := codec.WriteResponse(&Response{
			Result: resp,
			ID:     req.ID,
		}); werr != nil {
			return
		}
	}
}

// dispatch dispatches a request to the appropriate handler method.
//
// All non-nil error returns from this function are application-level
// failures (handler returned err, malformed params, unknown method) — they
// are recoverable: the caller writes a JSON-RPC -32603/-32601/-32602
// response and keeps the connection open. There is no transport-level
// error class; if a transport-level fault occurs (broken codec, conn
// closed mid-request) the error surfaces from ReadRequest in the caller's
// loop, not from here. handleConn relies on this contract — it never
// closes the connection on a dispatch error.
func (s *SocketServer) dispatch(ctx context.Context, req *Request) (any, error) {
	switch req.Method {
	case methodRemember, aliasRemember:
		var params RememberParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Remember(ctx, params)

	case methodSearch, aliasSearch:
		var params SearchParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Search(ctx, params)

	case methodRecall, aliasRecall:
		var params RecallParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Recall(ctx, params)

	case methodExec, aliasExec:
		var params ExecParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Exec(ctx, params)

	case methodRead, aliasRead:
		var params ReadParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Read(ctx, params)

	case methodFetch, aliasFetch:
		var params FetchParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Fetch(ctx, params)

	case methodGlob, aliasGlob:
		var params GlobParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Glob(ctx, params)

	case methodGrep, aliasGrep:
		var params GrepParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Grep(ctx, params)

	case methodEdit, aliasEdit:
		var params EditParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Edit(ctx, params)

	case methodWrite, aliasWrite:
		var params WriteParams
		if err := decodeParams(req.Params, &params); err != nil {
			return nil, err
		}
		return s.handlers.Write(ctx, params)

	default:
		return nil, fmt.Errorf("unknown method: %s", req.Method)
	}
}

// Stop stops the socket server.
func (s *SocketServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false

	// Cancel serverCtx before closing the listener so any in-flight dispatch
	// observes shutdown and unwinds.
	if s.serverCancel != nil {
		s.serverCancel()
		s.serverCancel = nil
	}

	listener := s.listener
	s.mu.Unlock()

	// Lock released for the drain so a handler that calls a method needing
	// s.mu (none today, but future-proof) cannot deadlock shutdown. Order:
	// Close listener → no new connections; cancel already fired → in-flight
	// dispatches see Done(); drain → handlers exit; remove socket file.
	var listenerErr error
	if listener != nil {
		listenerErr = listener.Close()
	}

	drained := make(chan struct{})
	go func() {
		s.connWG.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(stopDrainTimeout):
		// Bounded by stopDrainTimeout — if a misbehaving handler refuses
		// to unwind we still return so the daemon's shutdown sequence
		// (journal close, index persist) can finish. The handler
		// goroutine remains; the OS will reap it on process exit.
		logging.Warnf("socket Stop drain timed out after %s; one or more connections may still be unwinding",
			stopDrainTimeout)
	}

	os.Remove(s.path)
	return listenerErr
}
