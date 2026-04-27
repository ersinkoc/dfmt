package transport

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"unicode/utf8"
)

// FuzzCodecRoundTrip pins the Codec's framing contract under arbitrary
// payloads. Pre-finding-#1 the writer emitted "{...}\n\n" because
// json.Encoder.Encode appended its own newline AND the codec wrote a second
// one — pipelined readers then surfaced the empty frame as "unexpected end
// of JSON input". This fuzz target generates random method strings, params,
// and ID values, writes N requests, reads them back, and asserts that
// every read deserialises into a request with the same method.
//
// Failure modes the fuzzer is designed to surface:
//   - any double-newline framing regression (catches the original bug)
//   - panics inside readCappedLine on weird byte sequences
//   - silent truncation when the writer fails partway
//
// Run with: go test ./internal/transport/ -run=^$ -fuzz=FuzzCodecRoundTrip
func FuzzCodecRoundTrip(f *testing.F) {
	f.Add("test", `{}`, int64(1))
	f.Add("dfmt.exec", `{"code":"ls"}`, int64(42))
	f.Add("", `null`, int64(0))
	f.Add("ping", `[]`, int64(-1))

	f.Fuzz(func(t *testing.T, method, paramsJSON string, id int64) {
		// JSON encoding lossy-converts invalid UTF-8 byte sequences in
		// strings to the replacement rune U+FFFD per RFC 8259 §8.1. The
		// codec round-trip therefore can't preserve a method name with
		// non-UTF-8 bytes — that's a JSON property, not a framing bug.
		// Skip such inputs so we keep testing framing/escaping rather
		// than re-discovering the same UTF-8 contract on every run.
		if !utf8.ValidString(method) {
			t.Skip()
		}

		// Skip params that don't parse as JSON — fuzzing the framing here,
		// not json.Unmarshal robustness.
		var rawParams json.RawMessage
		if paramsJSON != "" {
			if err := json.Unmarshal([]byte(paramsJSON), new(any)); err != nil {
				t.Skip()
			}
			rawParams = json.RawMessage(paramsJSON)
		}

		r, w := net.Pipe()
		defer r.Close()
		defer w.Close()

		writer := NewCodec(w)
		reader := NewCodec(r)

		const n = 3
		writeErrCh := make(chan error, 1)
		go func() {
			for i := 0; i < n; i++ {
				err := writer.WriteRequest(&Request{
					Method: method,
					Params: rawParams,
					ID:     id + int64(i),
				})
				if err != nil {
					writeErrCh <- err
					return
				}
			}
			writeErrCh <- nil
		}()

		for i := 0; i < n; i++ {
			got, err := reader.ReadRequest()
			if err != nil {
				t.Fatalf("ReadRequest %d: %v (method=%q params=%q id=%d)", i, err, method, paramsJSON, id)
			}
			if got.Method != method {
				t.Fatalf("ReadRequest %d: method = %q, want %q", i, got.Method, method)
			}
		}

		if err := <-writeErrCh; err != nil {
			t.Fatalf("writer goroutine: %v", err)
		}
	})
}

// FuzzReadCappedLine exercises the per-message cap and overflow-drain logic
// in readCappedLine. The function must:
//   - never panic on arbitrary byte sequences
//   - always either return a line ≤ MaxJSONRPCLineBytes or surface an
//     error, never a silently truncated line
//   - leave the underlying reader at a fresh message boundary after
//     returning
//
// The third invariant is critical: if a fuzz input causes readCappedLine to
// stop mid-line, the next call would see a malformed leftover and corrupt
// every subsequent message.
func FuzzReadCappedLine(f *testing.F) {
	f.Add([]byte("hello\nworld\n"))
	f.Add([]byte(""))
	f.Add([]byte("\n\n\n"))
	f.Add(bytes.Repeat([]byte("x"), MaxJSONRPCLineBytes+10))
	f.Add([]byte{0xff, 0xfe, '\n'})

	f.Fuzz(func(t *testing.T, data []byte) {
		c := NewCodec(struct {
			*bytes.Reader
			discardWriter
		}{Reader: bytes.NewReader(data)})
		// Drain the input; each call must not panic. We bound iterations so
		// a bug that returns 0 bytes + nil error doesn't infinite-loop the
		// fuzzer.
		for i := 0; i < 1000; i++ {
			line, err := c.readCappedLine()
			if err != nil {
				return
			}
			if len(line) > MaxJSONRPCLineBytes {
				t.Fatalf("readCappedLine returned %d bytes; cap is %d", len(line), MaxJSONRPCLineBytes)
			}
		}
	})
}

// discardWriter is a no-op io.Writer used so we can construct a Codec
// against a read-only fuzz input. The writer side is exercised by
// FuzzCodecRoundTrip.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
