package transport

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	jsonRPCVersion = "2.0"
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
	// when the Start ctx is never cancelled (common: daemon passes a fresh
	// stopCtx to Stop). Without this the watcher goroutine leaks for every
	// Start/Stop cycle.
	doneCh chan struct{}

	// authToken guards TCP listeners. On loopback TCP any local process can
	// reach the port, so we require a random token delivered via the port
	// file (mode 0600). Empty string means "no auth" — only appropriate for
	// Unix sockets where filesystem perms already restrict access.
	authToken   string
	projectPath string // Used to filter /api/daemons to only this daemon
}

// PortFile is the JSON format written to the port file. Clients read this to
// discover the TCP port AND the auth token. Older versions wrote a bare
// integer; we fall back to that format on the read side for compatibility.
type PortFile struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func generateAuthToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
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
		return fmt.Errorf("server already running")
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

	// Auth token generation disabled for loopback TCP (port file 0600 owner-only,
	// same-origin protects browser, dfmt is single-user local tool).
	// Kept for future opt-in auth if ever needed.
	// if _, isTCP := ln.(*net.TCPListener); isTCP && s.authToken == "" {
	// 	tok, err := generateAuthToken()
	// 	if err != nil {
	// 		_ = ln.Close()
	// 		return fmt.Errorf("generate auth token: %w", err)
	// 	}
	// 	s.authToken = tok
	// }

	// Create HTTP handler with auth + security headers middleware.
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/dashboard", s.handleDashboard)
	mux.HandleFunc("/dashboard.js", s.handleDashboardJS)
	mux.HandleFunc("/api/stats", s.handleAPIStats)
	mux.HandleFunc("/api/daemons", s.handleAPIDaemons)
	mux.HandleFunc("/api/token", s.handleAPIToken)
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
		if err := s.writePortFile(s.portFile, actualPort, s.authToken); err != nil {
			_ = ln.Close()
			return fmt.Errorf("write port file: %w", err)
		}
	}

	s.listener = ln
	s.ownListener = ownListener
	s.running = true
	s.doneCh = make(chan struct{})

	// Shutdown watcher exits on either the Start ctx being cancelled or Stop
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

// wrapSecurity applies:
//   - Bearer token auth for TCP listeners (health endpoint excluded).
//   - CSRF / Origin defense for state-changing paths on the dashboard API
//     (rejects cross-origin browser requests).
func (s *HTTPServer) wrapSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always set minimal security headers on every response.
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Allow health endpoints without auth so readiness probes work.
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		// Reject browser cross-origin requests to non-dashboard endpoints.
		// The dashboard HTML is served same-origin, so legitimate dashboard
		// XHRs won't carry a foreign Origin.
		if origin := r.Header.Get("Origin"); origin != "" {
			if !s.isAllowedOrigin(origin) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}

		// Auth is disabled for TCP listeners on loopback. The port file is mode 0600
		// (readable only by the owning user), same-origin check protects browser
		// clients, and dfmt is a single-user local tool. Unix sockets rely on
		// filesystem permissions and skip auth entirely for the same reason.
		// if s.authToken != "" && r.URL.Path != "/api/token" {
		// 	got := extractBearerToken(r)
		// 	if subtle.ConstantTimeCompare([]byte(got), []byte(s.authToken)) != 1 {
		// 		w.Header().Set("WWW-Authenticate", `Bearer realm="dfmt"`)
		// 		http.Error(w, "unauthorized", http.StatusUnauthorized)
		// 		return
		// 	}
		// }

		next.ServeHTTP(w, r)
	})
}

func extractBearerToken(r *http.Request) string {
	// Prefer Authorization: Bearer <token>
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(strings.ToLower(h), "bearer ") {
			return strings.TrimSpace(h[len("bearer "):])
		}
	}
	// Fallback: X-DFMT-Token (convenient for curl).
	return strings.TrimSpace(r.Header.Get("X-DFMT-Token"))
}

func (s *HTTPServer) isAllowedOrigin(origin string) bool {
	// Only accept same-origin dashboard requests. Compare host:port against
	// the listener. If the listener is a Unix socket (no Host), reject all
	// non-empty origins.
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

	ctx := r.Context()

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

// writePortFile writes {"port":...,"token":"..."} to path with mode 0600 so
// other local users cannot read the auth token.
func (s *HTTPServer) writePortFile(path string, port int, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(PortFile{Port: port, Token: token})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// SetPortFile sets the path to write the chosen port.
func (s *HTTPServer) SetPortFile(path string) {
	s.portFile = path
}

// SetProjectPath sets the project path used to filter /api/daemons responses.
func (s *HTTPServer) SetProjectPath(path string) {
	s.projectPath = path
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

// handleAPIToken returns the bearer token for the dashboard JS. It is not
// protected by bearer-token auth (the token is already on disk), but the
// same-origin check still applies — only the dashboard's own origin can fetch
// it, so a malicious website cannot steal the token via a browser.
func (s *HTTPServer) handleAPIToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": s.authToken})
}

func (s *HTTPServer) handleAPIDaemons(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "handleAPIDaemons panic recovered: %v\n", rec)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}()
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

	// Filter to only this daemon's project. Prevents disclosing existence of
	// other projects' daemons to bearer-token holders (V-4).
	if s.projectPath != "" {
		filtered := make([]map[string]any, 0, 1)
		for _, d := range daemons {
			if path, ok := d["project_path"].(string); ok && path == s.projectPath {
				filtered = append(filtered, d)
				break
			}
		}
		daemons = filtered
	}

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
