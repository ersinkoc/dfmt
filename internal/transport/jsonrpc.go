package transport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Request represents a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id,omitempty"`
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  any         `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      any         `json:"id,omitempty"`
}

// RPCError represents a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Codec handles JSON-RPC 2.0 encoding/decoding over an io.ReadWriter.
type Codec struct {
	rw     io.ReadWriter
	enc   *json.Encoder
	mu    sync.Mutex
}

// NewCodec creates a new JSON-RPC codec.
func NewCodec(rw io.ReadWriter) *Codec {
	return &Codec{
		rw:  rw,
		enc: json.NewEncoder(rw),
	}
}

// ReadRequest reads and decodes a JSON-RPC request.
func (c *Codec) ReadRequest() (*Request, error) {
	// Read a line (JSON-RPC uses line-delimited JSON)
	reader := bufio.NewReader(c.rw)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("read request: %w", err)
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}

	if req.JSONRPC != "2.0" {
		return nil, fmt.Errorf("unsupported JSON-RPC version: %s", req.JSONRPC)
	}

	return &req, nil
}

// ReadResponse reads and decodes a JSON-RPC response.
func (c *Codec) ReadResponse() (*Response, error) {
	reader := bufio.NewReader(c.rw)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// WriteRequest encodes and writes a JSON-RPC request.
func (c *Codec) WriteRequest(req *Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	req.JSONRPC = "2.0"
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	// Write newline to match ReadRequest's ReadBytes('\n')
	c.rw.Write([]byte("\n"))
	return nil
}

// WriteResponse encodes and writes a JSON-RPC response.
func (c *Codec) WriteResponse(resp *Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp.JSONRPC = "2.0"
	if err := c.enc.Encode(resp); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	// Write newline to match ReadResponse's ReadBytes('\n')
	c.rw.Write([]byte("\n"))
	return nil
}

// WriteError writes a JSON-RPC error response.
func (c *Codec) WriteError(id any, code int, message string, data any) error {
	return c.WriteResponse(&Response{
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
		ID: id,
	})
}
