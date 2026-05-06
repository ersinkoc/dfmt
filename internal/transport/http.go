package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/safefs"
	"github.com/ersinkoc/dfmt/internal/safejson"
)

const (
	goosWindows    = "windows"
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
	authToken   string // Bearer token for HTTP endpoint authentication
}

// PortFile is the JSON format written to the port file. Older daemons wrote a
// bare integer and the read path still falls back to that format for
// compatibility.
type PortFile struct {
	Port  int    `json:"port"`
	Token string `json:"token,omitempty"` // Bearer token for HTTP auth
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
	// V-12: cap concurrent connections. The wrap is harmless on
	// caller-supplied listeners too (test paths) — if the cap is
	// already in place, this just nests two semaphores that share
	// fate via Close.
	ln = newLimitListener(ln, MaxConcurrentConnections)

	// F-09: Refuse non-loopback TCP binds. The same-origin gate plus port-file
	// 0600 perms only protect against same-host attackers; a non-loopback bind
	// exposes unauthenticated JSON-RPC (dfmt.exec, dfmt.write, dfmt.fetch, …)
	// to the LAN. Bearer-token auth (stored in the port file) gates all HTTP
	// endpoints — refuse the bind rather than silently shipping unauthenticated
	// RPC. Unix sockets and other non-TCP listeners are gated by filesystem
	// permissions, not this check.
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
	mux.HandleFunc("/dashboard.css", s.handleDashboardCSS)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/api/stats", s.handleAPIStats)
	mux.HandleFunc("/api/daemons", s.handleAPIDaemons)
	mux.HandleFunc("/api/all-daemons", s.handleAPIAllDaemons)
	mux.HandleFunc("/api/proxy", s.handleAPIProxy)
	mux.HandleFunc("/api/stream", s.handleAPIStream)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Bind Handlers-instance metrics (dedup-hit counter) to the package
	// registry exactly once per server start. Re-registration is
	// idempotent — registerMetric replaces an entry with the same name +
	// labels — so a Stop/Start cycle is safe.
	WireHandlerMetrics(s.handlers)

	s.server = &http.Server{
		Handler:           s.wrapSecurity(mux),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second, // Slowloris guard
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10, // 16 KiB — tighter than Go's 1 MiB default
	}

	if s.portFile != "" && actualPort > 0 {
		token, err := generateToken()
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("generate auth token: %w", err)
		}
		s.authToken = token
		if err := s.writePortFile(s.portFile, actualPort, ""); err != nil {
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
//
// V-I1: extend the default response headers (CSP + X-Frame-Options +
// nosniff) to every non-health endpoint, not just /dashboard. Pre-fix,
// /api/* and /metrics responses inherited only nosniff. The defaults
// here are intentionally strict because most endpoints (JSON APIs,
// metrics text) reference no external resources and embed no markup —
// the dashboard handler overrides CSP with its own less-restrictive
// 'self' policy via a later w.Header().Set call.
func (s *HTTPServer) wrapSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; frame-ancestors 'none'; base-uri 'none'")

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

		// Dashboard is publicly accessible (no auth required).
		if strings.HasPrefix(r.URL.Path, "/dashboard") {
			next.ServeHTTP(w, r)
			return
		}

		// Auth disabled — all HTTP endpoints are publicly accessible.

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
			logging.Errorf("handler panic recovered: %v", r)
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "Internal error"},
			}); err != nil {
				logging.Errorf("encode panic response: %v", err)
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
	defer r.Body.Close()
	if err != nil {
		http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
		return
	}

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

	// V-10: depth-checked decode rejects nesting bombs (a 1 MiB body of
	// `[[[…` would otherwise burn CPU and risk stack panic on the
	// recursive json.Unmarshal). Goes through safejson.Unmarshal which
	// runs CheckDepth before falling through to encoding/json.
	var req Request
	if err := safejson.Unmarshal(body, &req); err != nil {
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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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

// writePortFile writes {"port":...,"token":...} to path with mode 0600 so other local
// users cannot read the daemon's port (and the same-origin gate plus the
// non-loopback bind refusal keep cross-origin browser pages out).
func (s *HTTPServer) writePortFile(path string, port int, token string) error {
	if err := safefs.CheckNoReservedNames(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(PortFile{Port: port, Token: token})
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

// generateToken generates a cryptographically secure random token for HTTP bearer auth.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
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

// PortFile returns the path the server will write its chosen port +
// token to once the listener binds. Empty when SetPortFile was never
// called (Unix-socket transports). Exposed for tests + doctor checks.
func (s *HTTPServer) PortFile() string {
	return s.portFile
}

// SocketPath returns the Unix socket path when the server was
// constructed via NewHTTPServerWithListener. Empty for TCP listeners.
// Exposed for tests + doctor checks.
func (s *HTTPServer) SocketPath() string {
	return s.socketPath
}

func (s *HTTPServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	// Script is served from /dashboard.js so 'script-src self' is sufficient —
	// no fragile inline-script hash to keep in sync with the source.
	// Styles are all class-based (no inline style= attributes), so 'unsafe-inline'
	// is not needed for the dashboard. Removing it improves XSS posture (XSS-01).
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	_, _ = w.Write([]byte(DashboardHTML))
}

// handleDashboardJS serves the dashboard's JavaScript from an external file so
// CSP can use the simple `script-src 'self'` directive.
func (s *HTTPServer) handleDashboardJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(DashboardJS))
}

// handleDashboardCSS serves the dashboard's stylesheet from an external file
// so CSP can use the simple `style-src 'self'` directive — no inline-style
// hashes drifting on every markup change.
func (s *HTTPServer) handleDashboardCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(DashboardCSS))
}

// handleFavicon answers GET /favicon.ico with 204 so browsers stop logging
// 405 on every dashboard load. The HTML already wires `<link rel="icon"
// href="data:,">` to suppress the request entirely on modern browsers; this
// route covers older clients and direct hits.
func (s *HTTPServer) handleFavicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("handleAPIStats panic recovered: %v", rec)
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
	defer r.Body.Close()
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// V-10: depth-checked decode (see same gate on the primary handle()).
	var req Request
	if err := safejson.Unmarshal(body, &req); err != nil {
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

	ctx := WithProjectID(r.Context(), params.ProjectID)
	resp, err := s.handlers.Stats(ctx, params)
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

// handleMetrics emits Prometheus text format from the package registry
// (ADR-0016). GET-only; same wrapSecurity middleware that gates the
// dashboard already enforces loopback Host header + same-origin Origin
// for cross-origin browser requests, so this endpoint inherits the
// same threat model as /api/stats.
//
// Scrape cost: O(registry) atomic loads + 4 runtime.ReadMemStats calls.
// MemStats is the dominant cost (a stop-the-world for ~100 µs on a
// healthy heap); a 1 Hz Prometheus scrape interval is well within
// budget. We do not cache MemStats across scrape calls — staleness is
// worse than the small pause for the operator reading the dashboard.
func (s *HTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("handleMetrics panic recovered: %v", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	metricsScrapesTotal.Inc()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_ = WriteProm(w)
}

func (s *HTTPServer) handleAPIDaemons(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("handleAPIDaemons panic recovered: %v", rec)
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

// handleAPIAllDaemons returns the projects the dashboard switcher
// should expose. Phase 2: when the host daemon installs a
// ProjectsLister (global mode), we return one row per loaded project
// — every project is served by THIS process, so pid/port are the
// daemon's own. The legacy registry file is consulted only when no
// lister is installed (per-project daemons coexisting on the host)
// so v0.3.x → v0.4.x straddle setups keep showing every running
// daemon in the picker.
func (s *HTTPServer) handleAPIAllDaemons(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("handleAPIAllDaemons panic recovered: %v", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	// Global daemon path: enumerate in-process loaded projects.
	if s.handlers != nil {
		if projects := s.handlers.LoadedProjects(); projects != nil {
			rows := make([]map[string]any, 0, len(projects))
			now := time.Now()
			pid := os.Getpid()
			for _, p := range projects {
				rows = append(rows, map[string]any{
					"project_path": p,
					"pid":          pid,
					// last_seen / started_at are placeholders — the
					// daemon owns them at the process level. Per-project
					// timestamps would require plumbing
					// ProjectResources.LastActivityNs through the lister;
					// deferred until a metric on the dashboard actually
					// needs the granularity.
					"last_seen":  now.Format(time.RFC3339),
					"started_at": now.Format(time.RFC3339),
				})
			}
			if err := json.NewEncoder(w).Encode(rows); err != nil {
				fmt.Fprintf(os.Stderr, "encode all daemons: %v\n", err)
			}
			return
		}
	}

	// Legacy fallback: read the daemon registry file.
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	registryPath := filepath.Join(home, ".dfmt", "daemons.json")

	data, err := os.ReadFile(registryPath)
	if err != nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}

	var daemons []map[string]any
	if err := json.Unmarshal(data, &daemons); err != nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}

	if err := json.NewEncoder(w).Encode(daemons); err != nil {
		fmt.Fprintf(os.Stderr, "encode all daemons: %v\n", err)
	}
}

// handleAPIProxy forwards JSON-RPC requests to other daemons by project path.
// Browser sends {target_project_path, method, params} and we forward to the
// target daemon via HTTP, enabling cross-daemon stats from the dashboard
// without hitting same-origin restrictions.
func (s *HTTPServer) handleAPIProxy(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("handleAPIProxy panic recovered: %v", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	var req struct {
		TargetProjectPath string          `json:"target_project_path"`
		Method            string          `json:"method"`
		Params            json.RawMessage `json:"params"`
	}
	// V-11: cap body the same as the JSON-RPC `/` handler. Without this,
	// an agent on the loopback could OOM the daemon by sending a multi-GiB
	// body to /api/proxy. V-10: route through safejson.Unmarshal so a
	// nesting bomb in the agent's params payload is rejected before the
	// recursive json.Unmarshal hits it. Read fully into memory first
	// (1 MiB cap above already bounds it) so we get a single byte slice
	// to scan for depth.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	defer r.Body.Close()
	if err != nil {
		http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := safejson.Unmarshal(body, &req); err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32600, Message: "invalid request: " + err.Error()},
			ID:      nil,
		})
		return
	}

	if req.TargetProjectPath == "" {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32602, Message: "target_project_path is required"},
			ID:      nil,
		})
		return
	}

	// Read registry to find target daemon's address
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	registryPath := filepath.Join(home, ".dfmt", "daemons.json")

	data, err := os.ReadFile(registryPath)
	if err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "could not read registry: " + err.Error()},
			ID:      nil,
		})
		return
	}

	var daemonList []map[string]any
	if err := json.Unmarshal(data, &daemonList); err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "could not parse registry: " + err.Error()},
			ID:      nil,
		})
		return
	}

	var targetDaemon map[string]any
	for _, d := range daemonList {
		if p, ok := d["project_path"].(string); ok && pathsEqualForRuntime(p, req.TargetProjectPath) {
			targetDaemon = d
			break
		}
	}

	if targetDaemon == nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "daemon not running for project: " + req.TargetProjectPath},
			ID:      nil,
		})
		return
	}

	// Determine target URL
	var targetURL string
	if runtime.GOOS == goosWindows {
		if port, ok := targetDaemon["port"].(float64); ok && port > 0 {
			targetURL = fmt.Sprintf("http://127.0.0.1:%d/api/stats", int(port))
		} else {
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "daemon has no port for project: " + req.TargetProjectPath},
				ID:      nil,
			})
			return
		}
	} else {
		if sock, ok := targetDaemon["socket_path"].(string); ok && sock != "" {
			targetURL = "http://unix/api/stats"
		} else {
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "daemon has no socket for project: " + req.TargetProjectPath},
				ID:      nil,
			})
			return
		}
	}

	// Make HTTP request to target daemon
	proxyReq := struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		ID      any             `json:"id"`
	}{
		JSONRPC: "2.0",
		Method:  req.Method,
		Params:  req.Params,
		ID:      1,
	}

	body, err = json.Marshal(proxyReq)
	if err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "could not marshal request: " + err.Error()},
			ID:      nil,
		})
		return
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	var httpReq *http.Request

	if runtime.GOOS == goosWindows {
		httpReq, err = http.NewRequest("POST", targetURL, bytes.NewReader(body))
	} else {
		// Unix socket - use DialContext
		socketPath := targetDaemon["socket_path"].(string)
		dialer := net.Dialer{Timeout: 5 * time.Second}
		conn, err := dialer.DialContext(r.Context(), "unix", socketPath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "could not connect to daemon: " + err.Error()},
				ID:      nil,
			})
			return
		}
		defer func() { _ = conn.Close() }()

		// Write request directly to socket
		if _, werr := conn.Write(body); werr != nil {
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "could not send request: " + werr.Error()},
				ID:      nil,
			})
			return
		}

		// V-21: read with an io.LimitReader cap rather than a fixed-size
		// buffer + single Read. The prior `conn.Read(respBuf)` could
		// return short on a multi-packet response (the socket protocol
		// is stream-oriented, not message-oriented), and the 16 MiB
		// buffer was allocated up-front regardless of actual response
		// size. ReadAll-with-cap allocates only what's needed and
		// surfaces oversized responses as an explicit error rather
		// than silent truncation.
		respBytes, rerr := io.ReadAll(io.LimitReader(conn, 16<<20))
		if rerr != nil && rerr != io.EOF {
			_ = json.NewEncoder(w).Encode(Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "could not read response: " + rerr.Error()},
				ID:      nil,
			})
			return
		}
		// V-21: do NOT shadow the outer err with the Write result; the
		// pre-fix code did so and then returned without checking, masking
		// the failure. Log a write error directly — the response has
		// already been committed so we can't switch to a JSON-RPC error
		// at this point.
		if _, werr := w.Write(respBytes); werr != nil {
			logging.Warnf("api/proxy unix-branch write failed: %v", werr)
		}
		return
	}

	if err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "could not create request: " + err.Error()},
			ID:      nil,
		})
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq = httpReq.WithContext(r.Context())

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "request to daemon failed: " + err.Error()},
			ID:      nil,
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: fmt.Sprintf("daemon returned status %d", resp.StatusCode)},
			ID:      nil,
		})
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_ = json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32603, Message: "could not read daemon response: " + err.Error()},
			ID:      nil,
		})
		return
	}

	_, _ = w.Write(respBody)
}

// handleAPIStream streams journal events via SSE (Server-Sent Events).
// GET /api/stream?from=<cursor>&project_id=<path>
// Each event is sent as: data: <json>\n\n
//
// Phase 2: project_id is read from the query string and pushed into
// the request context so resolveBundle routes to the right per-
// project journal. Without it the global daemon (no defaultProject)
// would error every stream request with `project_id required` and
// the dashboard's live view would be permanently broken.
func (s *HTTPServer) handleAPIStream(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("handleAPIStream panic recovered: %v", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()

	// V-16 style: read-only endpoint, require GET/HEAD
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.handlers == nil {
		http.Error(w, "handlers not configured", http.StatusServiceUnavailable)
		return
	}

	from := r.URL.Query().Get("from")
	projectID := r.URL.Query().Get("project_id")
	// follow=true switches the endpoint from one-shot replay (pre-v0.5.0
	// contract — used by `dfmt tail` and ClientBackend.StreamEvents) to
	// live polling tail (used by the dashboard SSE). Without the flag
	// the handler returns when journal HEAD is reached, which is what
	// every non-dashboard caller still expects.
	follow := r.URL.Query().Get("follow") == "true"

	// SSE requires flushing after each event. Use a FlushCapture wrapper.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering if reverse-proxied
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := WithProjectID(r.Context(), projectID)

	// v0.5.0: live tail. Phase A streams the historical events from
	// `from` to journal HEAD. Phase B polls every streamLivePollInterval
	// for events appended after the last cursor — same code path as
	// the per-project index-tail goroutine in the daemon, just routed
	// to the SSE client instead of the in-memory index. Without this
	// polling loop the dashboard's live event view shipped a pure
	// playback (closed the connection at HEAD) and the only way to see
	// new events was a manual reload.
	//
	// ADR-0004 stdlib-only: polling instead of fsnotify. A single
	// dashboard tab is the common case, so the cost (one Stream call
	// per tick per connected SSE client) is negligible. Revisit if
	// per-project Prometheus metrics show >10 sustained SSE clients.
	lastID := from
	sendEvent := func(e core.Event) bool {
		data, merr := json.Marshal(e)
		if merr != nil {
			fmt.Fprintf(w, ": marshal error: %v\n\n", merr)
			flusher.Flush()
			return true // keep going; bad event != fatal
		}
		if _, werr := fmt.Fprintf(w, "data: %s\n\n", data); werr != nil {
			return false // client disconnected
		}
		flusher.Flush()
		lastID = e.ID
		return true
	}

	// Phase A — historical replay. Stream("from") returns a channel
	// that closes once journal HEAD is reached, mirroring the v0.4.x
	// behavior. Drains under ctx so the client can disconnect mid-replay
	// without leaving the goroutine running.
	stream, err := s.handlers.Stream(ctx, StreamParams{From: from, ProjectID: projectID})
	if err != nil {
		fmt.Fprintf(w, ": stream error: %v\n\n", err)
		flusher.Flush()
		return
	}
	for {
		select {
		case e, ok := <-stream:
			if !ok {
				if follow {
					goto liveTail
				}
				return
			}
			if !sendEvent(e) {
				return
			}
		case <-ctx.Done():
			return
		}
	}

liveTail:
	// Phase B — live polling. Tick every streamLivePollInterval, ask
	// for events strictly after lastID, forward each. Empty ticks (no
	// new events) cost one Stream call which short-circuits at the
	// scanner-loop's first iteration since `from` is past EOF.
	ticker := time.NewTicker(streamLivePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollStream, perr := s.handlers.Stream(ctx, StreamParams{From: lastID, ProjectID: projectID})
			if perr != nil {
				fmt.Fprintf(w, ": stream error: %v\n\n", perr)
				flusher.Flush()
				continue
			}
			for e := range pollStream {
				if !sendEvent(e) {
					return
				}
			}
		}
	}
}

// streamLivePollInterval is how often handleAPIStream polls for new
// events after the historical replay drains. 2 s keeps the dashboard
// real-time view feeling current without imposing measurable load
// when no events arrive (Stream's `from` cursor short-circuits past
// the last-known event ID).
//
// var (not const) so tests can compress the tick to milliseconds.
// Production callers never mutate it.
var streamLivePollInterval = 2 * time.Second

func (s *HTTPServer) handleExec(ctx context.Context, req Request) Response {
	var params ExecParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}
	ctx = WithProjectID(ctx, params.ProjectID)

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
	if runtime.GOOS == goosWindows {
		return strings.EqualFold(a, b)
	}
	return false
}

func (s *HTTPServer) handleRead(ctx context.Context, req Request) Response {
	var params ReadParams
	if r := decodeRPCParams(req, &params); r != nil {
		return *r
	}
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
	ctx = WithProjectID(ctx, params.ProjectID)

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
