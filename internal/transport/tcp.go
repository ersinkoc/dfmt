package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// TCPServer listens on a TCP port and handles JSON-RPC requests.
type TCPServer struct {
	addr     string
	portFile string
	listener net.Listener
	handlers *Handlers
	mu       sync.Mutex
	running  bool
}

// NewTCPServer creates a new TCP server.
func NewTCPServer(addr string, handlers *Handlers) *TCPServer {
	return &TCPServer{
		addr:     addr,
		handlers: handlers,
	}
}

// Start starts the TCP server.
func (s *TCPServer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen: %w", err)
	}

	// Get actual port and write port file if configured
	addr := ln.Addr().(*net.TCPAddr)
	actualPort := addr.Port

	s.listener = ln
	s.running = true
	s.mu.Unlock()

	// Write port file if configured
	if s.portFile != "" {
		if err := s.writePortFile(s.portFile, actualPort); err != nil {
			ln.Close()
			return fmt.Errorf("write port file: %w", err)
		}
	}

	go s.serve(ctx)
	return nil
}

func (s *TCPServer) serve(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		go s.handleConn(conn)
	}
}

func (s *TCPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	codec := NewCodec(conn)

	for {
		req, err := codec.ReadRequest()
		if err != nil {
			return
		}

		resp, err := s.dispatch(context.Background(), req)
		if err != nil {
			codec.WriteError(req.ID, -32603, err.Error(), nil)
			continue
		}

		codec.WriteResponse(&Response{
			Result: resp,
			ID:     req.ID,
		})
	}
}

func (s *TCPServer) dispatch(ctx context.Context, req *Request) (any, error) {
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

	default:
		return nil, fmt.Errorf("unknown method: %s", req.Method)
	}
}

// SetPortFile sets the path to write the chosen port.
func (s *TCPServer) SetPortFile(path string) {
	s.portFile = path
}

// Port returns the actual port the server is listening on.
func (s *TCPServer) Port() int {
	if s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return 0
}

// Stop stops the TCP server.
func (s *TCPServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false

	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *TCPServer) writePortFile(path string, port int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(port)), 0644)
}
