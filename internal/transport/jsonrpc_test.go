package transport

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

// badJSONMarshaler is a type that always fails to marshal
type badJSONMarshaler struct {
	err error
}

func (b badJSONMarshaler) MarshalJSON() ([]byte, error) {
	return nil, b.err
}

var errMockEncode = errors.New("mock encode error")

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
		ID:     &badJSONMarshaler{err: errMockEncode},
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
		Result: &badJSONMarshaler{err: errMockEncode},
		ID:     1,
	}

	err := codec.WriteResponse(resp)
	if err == nil {
		t.Errorf("expected encode error, got nil")
	}
}

func TestReadRequest_MarshalError(t *testing.T) {
	// Create a pipe
	r, w := net.Pipe()
	defer r.Close()

	codec := NewCodec(r)

	// Write invalid JSON (valid JSON but wrong type for Request unmarshal)
	go func() {
		w.Write([]byte("invalid json here\n"))
		w.Close()
	}()

	req, err := codec.ReadRequest()
	if req != nil {
		t.Errorf("expected nil request, got %v", req)
	}
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestReadResponse_MarshalError(t *testing.T) {
	// Create a pipe
	r, w := net.Pipe()
	defer r.Close()

	codec := NewCodec(r)

	// Write something that unmarshals but isn't a proper Response
	go func() {
		w.Write([]byte("not a response object\n"))
		w.Close()
	}()

	resp, err := codec.ReadResponse()
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestReadRequest_UnsupportedVersion(t *testing.T) {
	// Create a pipe
	r, w := net.Pipe()
	defer r.Close()

	codec := NewCodec(r)

	// Write a request with unsupported JSON-RPC version
	go func() {
		w.Write([]byte(`{"jsonrpc":"1.0","method":"test","id":1}` + "\n"))
		w.Close()
	}()

	req, err := codec.ReadRequest()
	if req != nil {
		t.Errorf("expected nil request, got %v", req)
	}
	if err == nil {
		t.Error("expected error for unsupported version, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected unsupported version error, got %v", err)
	}
}

// TestCodecRoundTrip_MultiMessage pins the framing contract: writing N
// requests through one codec and reading N back through another must round-
// trip cleanly. Pre-fix the writer emitted "{...}\n\n" because json.Encoder
// already appends a newline and the codec added a second one — the second
// ReadRequest then surfaced the empty frame as "unexpected end of JSON
// input" and the socket loop dropped the connection.
func TestCodecRoundTrip_MultiMessage(t *testing.T) {
	r, w := net.Pipe()
	defer r.Close()
	defer w.Close()

	writer := NewCodec(w)
	reader := NewCodec(r)

	const n = 3
	writes := make([]*Request, n)
	for i := 0; i < n; i++ {
		writes[i] = &Request{
			Method: "test",
			Params: json.RawMessage(`{}`),
			ID:     i + 1,
		}
	}

	writeErrCh := make(chan error, 1)
	go func() {
		for _, req := range writes {
			if err := writer.WriteRequest(req); err != nil {
				writeErrCh <- err
				return
			}
		}
		writeErrCh <- nil
	}()

	for i := 0; i < n; i++ {
		got, err := reader.ReadRequest()
		if err != nil {
			t.Fatalf("ReadRequest %d: %v", i, err)
		}
		if got.Method != "test" {
			t.Fatalf("ReadRequest %d: method = %q, want %q", i, got.Method, "test")
		}
		// JSON numbers decode as float64 into `any`.
		gotID, ok := got.ID.(float64)
		if !ok {
			t.Fatalf("ReadRequest %d: id type = %T, want float64", i, got.ID)
		}
		if int(gotID) != i+1 {
			t.Fatalf("ReadRequest %d: id = %v, want %d", i, gotID, i+1)
		}
	}

	if err := <-writeErrCh; err != nil {
		t.Fatalf("writer goroutine: %v", err)
	}
}

// TestCodecRoundTrip_ResponseMultiMessage is the symmetric test for
// WriteResponse / ReadResponse.
func TestCodecRoundTrip_ResponseMultiMessage(t *testing.T) {
	r, w := net.Pipe()
	defer r.Close()
	defer w.Close()

	writer := NewCodec(w)
	reader := NewCodec(r)

	const n = 3
	writeErrCh := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			if err := writer.WriteResponse(&Response{
				Result: map[string]any{"i": i},
				ID:     i + 1,
			}); err != nil {
				writeErrCh <- err
				return
			}
		}
		writeErrCh <- nil
	}()

	for i := 0; i < n; i++ {
		got, err := reader.ReadResponse()
		if err != nil {
			t.Fatalf("ReadResponse %d: %v", i, err)
		}
		gotID, ok := got.ID.(float64)
		if !ok {
			t.Fatalf("ReadResponse %d: id type = %T, want float64", i, got.ID)
		}
		if int(gotID) != i+1 {
			t.Fatalf("ReadResponse %d: id = %v, want %d", i, gotID, i+1)
		}
	}

	if err := <-writeErrCh; err != nil {
		t.Fatalf("writer goroutine: %v", err)
	}
}
