package transport

import (
	"bufio"
	"encoding/json"
	"errors"
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
	JSONRPC string    `json:"jsonrpc"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
	ID      any       `json:"id,omitempty"`
}

// RPCError represents a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MaxJSONRPCLineBytes caps a single framed JSON-RPC message. A misbehaving
// peer that never sends a newline could otherwise grow the read buffer
// without bound and OOM the daemon.
const MaxJSONRPCLineBytes = 1 << 20 // 1 MiB

// Codec handles JSON-RPC 2.0 encoding/decoding over an io.ReadWriter.
// The bufio.Reader is owned by the codec and reused across calls so that
// bytes buffered past a line boundary (pipelined requests) are not dropped
// on the next ReadRequest/ReadResponse.
type Codec struct {
	rw  io.ReadWriter
	r   *bufio.Reader
	enc *json.Encoder
	mu  sync.Mutex
}

// NewCodec creates a new JSON-RPC codec.
func NewCodec(rw io.ReadWriter) *Codec {
	return &Codec{
		rw:  rw,
		r:   bufio.NewReader(rw),
		enc: json.NewEncoder(rw),
	}
}

// readCappedLine reads up to MaxJSONRPCLineBytes, ending at '\n' or cap.
// Returns an error if cap is reached before a newline.
func (c *Codec) readCappedLine() ([]byte, error) {
	buf := make([]byte, 0, 512)
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				// Partial line at EOF is an error — we only accept fully
				// framed messages.
				return nil, errors.New("unterminated JSON-RPC line at EOF")
			}
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		if len(buf) >= MaxJSONRPCLineBytes {
			return nil, fmt.Errorf("JSON-RPC line exceeds %d bytes", MaxJSONRPCLineBytes)
		}
		buf = append(buf, b)
	}
}

// ReadRequest reads and decodes a JSON-RPC request.
func (c *Codec) ReadRequest() (*Request, error) {
	line, err := c.readCappedLine()
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

	if req.JSONRPC != jsonRPCVersion {
		return nil, fmt.Errorf("unsupported JSON-RPC version: %s", req.JSONRPC)
	}

	return &req, nil
}

// ReadResponse reads and decodes a JSON-RPC response.
func (c *Codec) ReadResponse() (*Response, error) {
	line, err := c.readCappedLine()
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
//
// json.Encoder.Encode appends a trailing newline after every value, which is
// the frame separator readCappedLine expects. Writing an extra "\n" here used
// to produce {…}\n\n on the wire — readCappedLine then surfaced the bare
// second \n as an empty frame, json.Unmarshal failed with "unexpected end of
// JSON input", and handleConn closed the connection. Single-shot HTTP never
// hit it (one request per conn); only the socket transport's pipelined loop
// would, and there was no regression test for that path. See
// TestCodecRoundTrip_MultiMessage.
func (c *Codec) WriteRequest(req *Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	req.JSONRPC = jsonRPCVersion
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	return nil
}

// WriteResponse encodes and writes a JSON-RPC response. See WriteRequest for
// why we rely on json.Encoder's own trailing newline.
func (c *Codec) WriteResponse(resp *Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp.JSONRPC = jsonRPCVersion
	if err := c.enc.Encode(resp); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
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
