package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// SocketServer listens on a Unix socket and handles JSON-RPC requests.
type SocketServer struct {
	path     string
	listener net.Listener
	handlers *Handlers
	mu       sync.Mutex
	running  bool
}

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
		return fmt.Errorf("server already running")
	}

	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("create socket dir: %w", err)
	}

	// Remove existing socket
	os.Remove(s.path)

	// Listen
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen: %w", err)
	}

	// Set permissions
	os.Chmod(s.path, 0700)

	s.listener = ln
	s.running = true
	s.mu.Unlock()

	// Accept connections
	go s.serve(ctx)

	return nil
}

// serve handles incoming connections.
func (s *SocketServer) serve(ctx context.Context) {
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

// handleConn handles a single connection.
func (s *SocketServer) handleConn(conn net.Conn) {
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

// dispatch dispatches a request to the appropriate handler method.
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

	default:
		return nil, fmt.Errorf("unknown method: %s", req.Method)
	}
}

// decodeParams decodes JSON-RPC params.
func decodeParams(data json.RawMessage, v any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// Stop stops the socket server.
func (s *SocketServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false

	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return err
		}
	}

	os.Remove(s.path)
	return nil
}
