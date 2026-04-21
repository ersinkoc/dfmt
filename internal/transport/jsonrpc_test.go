package transport

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
)

// badJSONMarshaler is a type that always fails to marshal
type badJSONMarshaler struct {
	err error
}

func (b badJSONMarshaler) MarshalJSON() ([]byte, error) {
	return nil, b.err
}

var mockEncodeErr = errors.New("mock encode error")

func TestReadRequest_EOF(t *testing.T) {
	// Create a pipe
	conn, peer := net.Pipe()
	defer conn.Close()

	// Close the writing side (peer) to simulate EOF
	peer.Close()

	// Now conn should receive EOF when reading
	codec := NewCodec(conn)

	req, err := codec.ReadRequest()
	if req != nil {
		t.Errorf("expected nil request, got %v", req)
	}
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestReadResponse_EOF(t *testing.T) {
	// Create a pipe
	conn, peer := net.Pipe()
	defer conn.Close()

	// Close the writing side (peer) to simulate EOF
	peer.Close()

	// Now conn should receive EOF when reading
	codec := NewCodec(conn)

	resp, err := codec.ReadResponse()
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestWriteRequest_EncodeError(t *testing.T) {
	// Create a pipe where we control the writer
	r, w := net.Pipe()
	defer r.Close()

	codec := NewCodec(w)

	// Try to write a request with a type that fails marshaling
	req := &Request{
		Method: "test",
		Params: json.RawMessage(`{}`),
		ID:     &badJSONMarshaler{err: mockEncodeErr},
	}

	err := codec.WriteRequest(req)
	if err == nil {
		t.Errorf("expected encode error, got nil")
	}
}

func TestWriteResponse_EncodeError(t *testing.T) {
	// Create a pipe where we control the writer
	r, w := net.Pipe()
	defer r.Close()

	codec := NewCodec(w)

	// Try to write a response with a type that fails marshaling
	resp := &Response{
		Result: &badJSONMarshaler{err: mockEncodeErr},
		ID:     1,
	}

	err := codec.WriteResponse(resp)
	if err == nil {
		t.Errorf("expected encode error, got nil")
	}
}