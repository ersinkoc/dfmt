package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	aliasRemember  = "remember"
	aliasSearch    = "search"
	aliasRecall    = "recall"
	aliasStats     = "stats"
	aliasExec      = "exec"
	aliasRead      = "read"
	aliasFetch     = "fetch"
)

// HTTPServer is an HTTP server for the transport layer.
type HTTPServer struct {
	bind       string
	portFile   string
	socketPath string // For Unix socket cleanup
	listener   net.Listener
	handlers   *Handlers
	server     *http.Server
	mu         sync.Mutex
	running    bool
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
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}

	// Create HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/dashboard", s.handleDashboard)
	mux.HandleFunc("/api/stats", s.handleAPIStats)
	mux.HandleFunc("/api/daemons", s.handleAPIDaemons)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleHealth)

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	var ln net.Listener
	var err error
	var actualPort int

	if s.listener != nil {
		// Use custom listener (e.g., Unix socket)
		ln = s.listener
	} else {
		// Create TCP listener
		ln, err = net.Listen("tcp", s.bind)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("listen: %w", err)
		}
		addr := ln.Addr().(*net.TCPAddr)
		actualPort = addr.Port
	}

	s.mu.Unlock()

	// Write port file if configured and we have a TCP port
	if s.portFile != "" && actualPort > 0 {
		if err := s.writePortFile(s.portFile, actualPort); err != nil {
			ln.Close()
			return fmt.Errorf("write port file: %w", err)
		}
	}

	// Start serving
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Log and exit - don't let panic kill the server
				fmt.Printf("http server panic: %v\n", r)
			}
		}()
		<-ctx.Done()
		s.server.Shutdown(context.Background())
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("http serve panic: %v\n", r)
			}
		}()
		s.server.Serve(ln)
	}()

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	return nil
}

// Stop stops the HTTP server.
func (s *HTTPServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		// Server was never started; still release a pre-bound listener so
		// callers can rebind the same address/path (e.g. in tests that
		// create a daemon, never call Start, then re-create).
		if s.listener != nil {
			_ = s.listener.Close()
			s.listener = nil
		}
		if s.socketPath != "" {
			os.Remove(s.socketPath)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := s.server.Shutdown(shutdownCtx)
	s.running = false

	// Clean up Unix socket file if applicable
	if s.socketPath != "" {
		os.Remove(s.socketPath)
	}

	return err
}

func (s *HTTPServer) handle(w http.ResponseWriter, r *http.Request) {
	// Recover from panics in request handling
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("handler panic recovered: %v\n", r)
			w.Header().Set("Content-Type", "application/json")
			resp := Response{
				JSONRPC: jsonRPCVersion,
				Error:   &RPCError{Code: -32603, Message: "Internal error"},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}()

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
		resp := Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32700, Message: "Parse error"},
		}
		json.NewEncoder(w).Encode(resp)
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
	default:
		resp = Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *HTTPServer) handleRemember(ctx context.Context, req Request) Response {
	var params RememberParams
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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

func (s *HTTPServer) writePortFile(path string, port int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(port)), 0644)
}

// SetPortFile sets the path to write the chosen port.
func (s *HTTPServer) SetPortFile(path string) {
	s.portFile = path
}

func (s *HTTPServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
	w.Write([]byte(DashboardHTML))
}

func (s *HTTPServer) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		resp := Response{
			JSONRPC: jsonRPCVersion,
			Error:   &RPCError{Code: -32700, Message: "Parse error"},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	var params StatsParams
	if req.Params != nil {
		json.Unmarshal(req.Params, &params)
	}

	resp, err := s.handlers.Stats(r.Context(), params)
	if err != nil {
		json.NewEncoder(w).Encode(Response{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		})
		return
	}

	json.NewEncoder(w).Encode(Response{
		JSONRPC: jsonRPCVersion,
		ID:      req.ID,
		Result:  resp,
	})
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *HTTPServer) handleAPIDaemons(w http.ResponseWriter, r *http.Request) {
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
		json.NewEncoder(w).Encode([]any{})
		return
	}

	var daemons []map[string]any
	if err := json.Unmarshal(data, &daemons); err != nil {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]any{})
		return
	}

	json.NewEncoder(w).Encode(daemons)
}

func (s *HTTPServer) handleExec(ctx context.Context, req Request) Response {
	var params ExecParams
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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
	if req.Params != nil {
		data, _ := json.Marshal(req.Params)
		json.Unmarshal(data, &params)
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
