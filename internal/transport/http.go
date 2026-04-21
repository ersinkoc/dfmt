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

// HTTPServer is an HTTP server for the transport layer.
type HTTPServer struct {
	bind     string
	portFile string
	handlers *Handlers
	server   *http.Server
	mu       sync.Mutex
	running  bool
}

// NewHTTPServer creates a new HTTP server.
func NewHTTPServer(bind string, handlers *Handlers) *HTTPServer {
	return &HTTPServer{
		bind:     bind,
		handlers: handlers,
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

	s.server = &http.Server{
		Addr:         s.bind,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Listen
	ln, err := net.Listen("tcp", s.bind)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen: %w", err)
	}

	// Check actual port
	addr := ln.Addr().(*net.TCPAddr)
	actualPort := addr.Port

	s.mu.Unlock()

	// Write port file if configured
	if s.portFile != "" {
		if err := s.writePortFile(s.portFile, actualPort); err != nil {
			ln.Close()
			return fmt.Errorf("write port file: %w", err)
		}
	}

	// Start serving
	go func() {
		<-ctx.Done()
		s.server.Shutdown(context.Background())
	}()

	go s.server.Serve(ln)

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	return nil
}

// Stop stops the HTTP server.
func (s *HTTPServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.server.Shutdown(ctx)
	s.running = false
	return err
}

func (s *HTTPServer) handle(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		resp := Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32700, Message: "Parse error"},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	var resp interface{}
	switch req.Method {
	case "dfmt.remember":
		resp = s.handleRemember(ctx, req)
	case "dfmt.search":
		resp = s.handleSearch(ctx, req)
	case "dfmt.recall":
		resp = s.handleRecall(ctx, req)
	default:
		resp = Response{
			JSONRPC: "2.0",
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
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: resp}
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
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: resp}
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
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32603, Message: err.Error()},
		}
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: resp}
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