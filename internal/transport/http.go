package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

const (
	jsonRPCVersion = "2.0"
	// methodXxx are the JSON-RPC method names accepted on the HTTP and socket
	// transports. They use dot namespacing for historical reasons and remain
	// stable for back-compat — any existing client posting `dfmt.exec` keeps
	// working.
	methodRemember = "dfmt.remember"
	methodSearch   = "dfmt.search"
	methodRecall   = "dfmt.recall"
	methodStats    = "dfmt.stats"
	methodExec     = "dfmt.exec"
	methodRead     = "dfmt.read"
	methodFetch    = "dfmt.fetch"
	methodGlob     = "dfmt.glob"
	methodGrep     = "dfmt.grep"
	methodEdit     = "dfmt.edit"
	methodWrite    = "dfmt.write"
	aliasRemember  = "remember"
	aliasSearch    = "search"
	aliasRecall    = "recall"
	aliasStats     = "stats"
	aliasExec      = "exec"
	aliasRead      = "read"
	aliasFetch     = "fetch"
	aliasGlob      = "glob"
	aliasGrep      = "grep"
	aliasEdit      = "edit"
	aliasWrite     = "write"
	// mcpToolXxx are the names exposed over the MCP stdio transport. The MCP
	// spec restricts tool names to ^[a-zA-Z][a-zA-Z0-9_-]*$ — dots are
	// rejected by Claude Code's MCP client, which silently drops every tool
	// from tools/list if dot-named, leaving the server "connected" with no
	// callable tools.
	mcpToolRemember = "dfmt_remember"
	mcpToolSearch   = "dfmt_search"
	mcpToolRecall   = "dfmt_recall"
	mcpToolStats    = "dfmt_stats"
	mcpToolExec     = "dfmt_exec"
	mcpToolRead     = "dfmt_read"
	mcpToolFetch    = "dfmt_fetch"
	mcpToolGlob     = "dfmt_glob"
	mcpToolGrep     = "dfmt_grep"
	mcpToolEdit     = "dfmt_edit"
	mcpToolWrite    = "dfmt_write"
)

// HTTPServer is an HTTP server for the transport layer.
type HTTPServer struct {
	bind        string
	portFile    string
	socketPath  string // For Unix socket cleanup
	listener    net.Listener
	ownListener bool // true if Start() created the listener (so Stop may close it)
	handlers    *Handlers
	server      *http.Server
	mu          sync.Mutex
	running     bool
	// doneCh is closed by Stop so the shutdown-watcher goroutine exits even
	// when the Start ctx is never canceled (common: daemon passes a fresh
	// stopCtx to Stop). Without this the watcher goroutine leaks for every
	// Start/Stop cycle.
	doneCh chan struct{}

	projectPath string // Used to filter /api/daemons to only this daemon
}

// PortFile is the JSON format written to the port file. Older daemons wrote a
// bare integer and the read path still falls back to that format for
// compatibility.
type PortFile struct {
	Port int `json:"port"`
}

// NewHTTPServer creates a new HTTP server with TCP listener.
func NewHTTPServer(bind string, handlers *Handlers) *HTTPServer {
	return &HTTPServer{
		bind:     bind,
		handlers: handlers,
	}
}

// NewHTTPServerWithListener creates a new HTTP server with a custom listener.
// For Unix sockets, also provide socketPath for proper cleanup on stop.
func NewHTTPServerWithListener(listener net.Listener, handlers *Handlers, socketPath string) *HTTPServer {
	return &HTTPServer{
		listener:   listener,
		handlers:   handlers,
		socketPath: socketPath,
	}
}

// Start starts the HTTP server.
func (s *HTTPServer) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return errors.New("server already running")
	}

	// Pick a listener first so actualPort is known before we write the port file.
	var ln net.Listener
	var actualPort int
	var ownListener bool
	if s.listener != nil {
		ln = s.listener
	} else {
		l, err := net.Listen("tcp", s.bind)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		ln = l
		ownListener = true
		if addr, ok := l.Addr().(*net.TCPAddr); ok {
			actualPort = addr.Port
		}
	}

	// F-09: Refuse non-loopback TCP binds. The same-origin gate plus port-file
	// 0600 perms only protect against same-host attackers; a non-loopback bind
	// exposes unauthenticated JSON-RPC (dfmt.exec, dfmt.write, dfmt.fetch, …)
	// to the LAN. Bearer-token auth is not implemented (the dead authToken
	// plumbing was ripped out under F-22). Until auth is wired, refuse the
	// bind rather than silently shipping unauthenticated RPC. Unix sockets
	// and other non-TCP listeners are gated by filesystem permissions, not
	// this check.
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		if !addr.IP.IsLoopback() {
			if ownListener {
				_ = ln.Close()
			}
			return fmt.Errorf("non-loopback HTTP bind refused: listener bound to %s — bearer-token auth not implemented (F-09)", addr.IP.String())
		}
	}

	// Create HTTP handler with same-origin + security headers middleware.
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/dashboard", s.handleDashboard)
	mux.HandleFunc("/dashboard.js", s.handleDashboardJS)
	mux.HandleFunc("/api/stats", s.handleAPIStats)
	mux.HandleFunc("/api/daemons", s.handleAPIDaemons)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleHealth)

	s.server = &http.Server{
		Handler:           s.wrapSecurity(mux),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second, // Slowloris guard
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10, // 16 KiB — tighter than Go's 1 MiB default
	}

	if s.portFile != "" && actualPort > 0 {
		if err := s.writePortFile(s.portFile, actualPort); err != nil {
			_ = ln.Close()
			return fmt.Errorf("write port file: %w", err)
		}
	}

	s.listener = ln
	s.ownListener = ownListener
	s.running = true
	s.doneCh = make(chan struct{})

	// Shutdown watcher exits on either the Start ctx being canceled or Stop
	// closing doneCh, whichever comes first.
	doneCh := s.doneCh
	server := s.server
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("http server panic: %v\n", r)
			}
		}()
		select {
		case <-ctx.Done():
		case <-doneCh:
		}
		_ = server.Shutdown(context.Background())
	}()

	// Serve loop.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("http serve panic: %v\n", r)
			}
		}()
		_ = s.server.Serve(ln)
	}()

	return nil
}

// wrapSecurity rejects cross-origin browser requests to non-dashboard
// endpoints (the dashboard HTML is same-origin so legitimate XHRs never
// carry a foreign Origin), validates the Host header against the listener
// (closes F-17: defends against DNS-rebinding where the browser would
// send `Host: attacker.com` after the rebind), and sets minimal security
// response headers. Health endpoints bypass the origin/host checks so
// readiness probes work.
func (s *HTTPServer) wrapSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		if !s.isAllowedHost(r.Host) {
			http.Error(w, "host header rejected", http.StatusForbidden)
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			if !s.isAllowedOrigin(origin) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (s *HTTPServer) isAllowedOrigin(origin string) bool {
	// Only accept same-origin dashboard requests. Compare host:port against
	// the listener.
	//
	// V-17: the Unix-socket / non-TCP branch returns false intentionally
	// (fail-closed). A Unix-socket listener has no `host:port` we could
	// reconstruct an Origin string from; a browser tab navigated to
	// `http://something/dashboard` could only be reaching a Unix socket
	// through an external proxy, which is not part of the supported
	// surface. Any future contributor "fixing" this to return true would
	// reopen a cross-origin gap — keep the explicit reject.
	if s.listener == nil {
		return false
	}
	var want string
	if addr, ok := s.listener.Addr().(*net.TCPAddr); ok {
		want = fmt.Sprintf("http://%s", addr.String())
	} else {
		return false
	}
	return origin == want
}

// isAllowedHost validates the request's Host header against the listener.
// On TCP, the Host must be the literal listener address (e.g.
// `127.0.0.1:54321`) or `localhost:<port>` — both are safe ways to dial a
// loopback daemon, anything else (including arbitrary attacker domains
// post-DNS-rebind) is rejected. On Unix-socket transports the Host header
// is whatever the HTTP client put there; the connection itself is gated
// by filesystem permissions so we accept any value.
func (s *HTTPServer) isAllowedHost(host string) bool {
	if s.listener == nil {
		return false
	}
	addr, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		// Non-TCP listener (Unix socket): no DNS rebinding vector.
		return true
	}
	want := addr.String()
	if host == want {
		return true
	}
	// Allow `localhost:<port>` as a parallel form, since browsers and
	// curl reach 127.0.0.1 either way.
	if host == fmt.Sprintf("localhost:%d", addr.Port) {
		return true
	}
	// Allow `[::1]:<port>` for clients that prefer IPv6.
	if host == fmt.Sprintf("[::1]:%d", addr.Port) {
		return true
	}
	return false
}

// Stop stops the HTTP server.
// Only touches the Unix socket path / port file if the server actually owns
// them — this prevents a Stop() call that never successfully started from
// deleting another daemon's socket.
func (s *HTTPServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		if s.listener != nil && s.ownListener {
			_ = s.listener.Close()
			s.listener = nil
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Wake the shutdown watcher so it doesn't leak after we return.
	if s.doneCh != nil {
		select {
		case <-s.doneCh:
		default:
			close(s.doneCh)
		}
	}

	err := s.server.Shutdown(shutdownCtx)
	s.running = false

	// Only remove the socket if we were started with this socketPath.
	// Removing here after a successful Shutdown is safe because no new
	// daemon can bind the path before the Shutdown completes.
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
	// Clean up the port file we wrote.
	if s.portFile != "" {
		_ = os.Remove(s.portFile)
	}

	return err
}

func (s *HTTPServer) handle(w http.ResponseWriter, r *http.Request) {
	// Recover from panics in request handling. Encode error is logged but the
	// connection will be dropped anyway, so we don't retry.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "handler panic recovered: %v\n", r)
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "Internal error"},
			}); err != nil {
				fmt.Fprintf(os.Stderr, "encode panic response: %v\n", err)
			}
		}
	}()

	if s.handlers == nil {
		http.Error(w, "handlers not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Limit request body to 1MB to prevent OOM
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
		return
	}
	defer r.Body.Close()

	// Per-request session ID (ADR-0011). HTTP is stateless — there's no
	// "connection" the way socket has — so callers that want wire-dedup
	// across multiple HTTP calls must opt in by sending the same
	// X-DFMT-Session header on each. Absent header → fresh ULID per
	// request → no dedupe (the second call's session is unique). This
	// matches the loopback CLI use case (each invocation is its own
	// session) without forcing the dashboard or ad-hoc curl to think
	// about session identity.
	sessionID := r.Header.Get("X-DFMT-Session")
	if sessionID == "" {
		sessionID = string(core.NewULID(time.Now()))
	}
	ctx := WithSessionID(r.Context(), sessionID)

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		// JSON-RPC 2.0 §5.1: on parse error the response ID MUST be null.
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			ID:      nil,
			Error:   &RPCError{Code: -32700, Message: "Parse error"},
		})
		return
	}

	var resp any
	switch req.Method {
	case methodRemember, aliasRemember:
		resp = s.handleRemember(ctx, req)
	case methodSearch, aliasSearch:
		resp = s.handleSearch(ctx, req)
	case methodRecall, aliasRecall:
		resp = s.handleRecall(ctx, req)
	case methodStats, aliasStats:
		resp = s.handleStats(ctx, req)
	case methodExec, aliasExec:
		resp = s.handleExec(ctx, req)
	case methodRead, aliasRead:
		resp = s.handleRead(ctx, req)
	case methodFetch, aliasFetch:
		resp = s.handleFetch(ctx, req)
	case methodGlob, aliasGlob:
		resp = s.handleGlob(ctx, req)
	case methodGrep, aliasGrep:
		resp = s.handleGrep(ctx, req)
	case methodEdit, aliasEdit:
		resp = s.handleEdit(ctx, req)
	case methodWrite, aliasWrite:
		resp = s.handleWrite(ctx, req)
	default:
		resp = Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
	}
}

// decodeRPCParams unmarshals req.Params (a json.RawMessage already produced by
// the outer request decoder) directly into dst. Returns a non-nil Response
// with JSON-RPC error code -32602 ("Invalid params") on decode failure. Empty
// or absent params is not an error — callers receive a zero-value struct.
//
// Replaces the prior `data, _ := json.Marshal(req.Params); json.Unmarshal(data,
// &params)` round-trip whose discarded errors silently produced zero-value
// params on malformed input. See V-16 in security-report/.
func decodeRPCParams(req Request, dst any) *Response {
	if len(req.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		return &Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32602, Message: "Invalid params: " + err.Error()},
		}
	}
	return nil
}

func (s *HTTPServer) handleRemember(ctx context.Context, req Request) Response {
	var params RememberParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Remember(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleSearch(ctx context.Context, req Request) Response {
	var params SearchParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Search(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleRecall(ctx context.Context, req Request) Response {
	var params RecallParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Recall(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleStats(ctx context.Context, req Request) Response {
	var params StatsParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Stats(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

// writePortFile writes {"port":...} to path with mode 0600 so other local
// users cannot read the daemon's port (and the same-origin gate plus the
// non-loopback bind refusal keep cross-origin browser pages out).
func (s *HTTPServer) writePortFile(path string, port int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(PortFile{Port: port})
	if err != nil {
		return err
	}
	// Write atomically via tmp + rename so a concurrent client never reads a
	// truncated or empty port file. Without this, the old code's
	// os.WriteFile (truncate-then-write) had a window where Connect() could
	// observe a 0-byte file and bail with "empty port file" before the bytes
	// landed — manifesting as "daemon not responding" on slow Windows hosts.
	tmp, err := os.CreateTemp(dir, ".port.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return werr
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return cerr
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// On Windows, os.Rename fails if the target exists. Remove then rename.
	_ = os.Remove(path)
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// SetPortFile sets the path to write the chosen port.
func (s *HTTPServer) SetPortFile(path string) {
	s.portFile = path
}

// SetProjectPath sets the project path used to filter /api/daemons responses.
func (s *HTTPServer) SetProjectPath(path string) {
	s.projectPath = path
}

// Bind returns the configured listen address (e.g. "127.0.0.1:0",
// "127.0.0.1:8765"). Empty when the server was constructed with a
// pre-built listener via NewHTTPServerWithListener — that path does
// not pass through net.Listen here. Exposed for tests; callers should
// not depend on the format.
func (s *HTTPServer) Bind() string {
	return s.bind
}

func (s *HTTPServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	// Script is served from /dashboard.js so 'script-src self' is sufficient —
	// no fragile inline-script hash to keep in sync with the source.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	_, _ = w.Write([]byte(DashboardHTML))
}

// handleDashboardJS serves the dashboard's JavaScript from an external file so
// CSP can use the simple `script-src 'self'` directive.
func (s *HTTPServer) handleDashboardJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(DashboardJS))
}

func (s *HTTPServer) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "handleAPIStats panic recovered: %v\n", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	if s.handlers == nil {
		http.Error(w, "handlers not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			ID:      nil,
			Error:   &RPCError{Code: -32700, Message: "Parse error"},
		})
		return
	}

	var params StatsParams
	if len(req.Params) != 0 {
		if perr := json.Unmarshal(req.Params, &params); perr != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				ID:      req.ID,
				Error:   &RPCError{Code: -32602, Message: "Invalid params: " + perr.Error()},
			})
			return
		}
	}

	resp, err := s.handlers.Stats(r.Context(), params)
	if err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		})
		return
	}

	if err := json.NewEncoder(w).Encode(Response{
		JSONRPC: jsonRPCVersion,
		ID:      req.ID,
		Result:  resp,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "encode stats response: %v\n", err)
	}
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *HTTPServer) handleAPIDaemons(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "handleAPIDaemons panic recovered: %v\n", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	// V-16: /api/daemons is a read-only listing endpoint; reject any
	// non-GET / non-HEAD method so the API surface stays predictable
	// (matches handleAPIStats's POST-only contract).
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	// Read registry file directly (avoid circular import)
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	registryPath := filepath.Join(home, ".dfmt", "daemons.json")

	data, err := os.ReadFile(registryPath)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}

	var daemons []map[string]any
	if err := json.Unmarshal(data, &daemons); err != nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}

	// Filter to only this daemon's project. Prevents disclosing the existence
	// of other projects' daemons to whoever can reach this loopback port (V-4).
	//
	// F-16: fail CLOSED, not open. If projectPath was never set (test harness,
	// future caller that forgets SetProjectPath, integrator subclass), return
	// an empty list rather than the full host-wide registry. The cost of a
	// false-empty response is tiny (the dashboard shows "no daemons");
	// the cost of a fail-open is leaking every other project on the box to
	// any same-host reader.
	if s.projectPath == "" {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	filtered := make([]map[string]any, 0, 1)
	for _, d := range daemons {
		if path, ok := d["project_path"].(string); ok && pathsEqualForRuntime(path, s.projectPath) {
			filtered = append(filtered, d)
			break
		}
	}
	daemons = filtered

	if err := json.NewEncoder(w).Encode(daemons); err != nil {
		fmt.Fprintf(os.Stderr, "encode daemons: %v\n", err)
	}
}

func (s *HTTPServer) handleExec(ctx context.Context, req Request) Response {
	var params ExecParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Exec(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

// pathsEqualForRuntime compares two filesystem paths using the host OS's
// case-sensitivity rules. NTFS / ReFS treat paths case-insensitively
// (V-19); ext4 / APFS-default / etc. are case-sensitive. The /api/daemons
// filter uses this so a registry entry written by the daemon at
// `C:\Users\Foo\Proj` still matches a request whose s.projectPath was
// resolved as `c:\users\foo\proj`.
func pathsEqualForRuntime(a, b string) bool {
	if a == b {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return false
}

func (s *HTTPServer) handleRead(ctx context.Context, req Request) Response {
	var params ReadParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Read(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleFetch(ctx context.Context, req Request) Response {
	var params FetchParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Fetch(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleGlob(ctx context.Context, req Request) Response {
	var params GlobParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Glob(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleGrep(ctx context.Context, req Request) Response {
	var params GrepParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Grep(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleEdit(ctx context.Context, req Request) Response {
	var params EditParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Edit(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}

func (s *HTTPServer) handleWrite(ctx context.Context, req Request) Response {
	var params WriteParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}

	resp, err := s.handlers.Write(ctx, params)
	if err != nil {
		return Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: jsonRPCVersion, ID: req.ID, Result: resp}
}
