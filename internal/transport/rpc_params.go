package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ParamsError wraps a JSON-RPC params decode failure so the connection loop
// can map it to JSON-RPC error code -32602 ("Invalid params"). The bare
// json.Unmarshal error used to fall through to -32603 ("Internal error"),
// which is wrong per RFC and made client-side typo diagnosis harder
// ("internal error" tells the caller nothing).
type ParamsError struct{ Err error }

func (e *ParamsError) Error() string { return e.Err.Error() }
func (e *ParamsError) Unwrap() error { return e.Err }

// IsParamsError reports whether err (or any error it wraps) is a ParamsError.
// Connection loops use this to choose between -32602 and -32603.
func IsParamsError(err error) bool {
	var pe *ParamsError
	return errors.As(err, &pe)
}

// decodeParams decodes JSON-RPC params into dst with strict semantics:
//
//   - Empty / nil body is accepted and leaves dst at its zero value. RFC
//     8259 doesn't forbid an empty params field, and JSON-RPC 2.0 §4.1
//     says params is optional, so refusing it would break legitimate
//     callers (Stats, Recall with no filters, …).
//
//   - Unknown fields are rejected. A client sending {"limt":10} for
//     {"limit":10} previously got back a successful empty result instead
//     of an error — silent typos. DisallowUnknownFields surfaces these
//     as -32602 so the agent learns about the typo immediately.
//
//   - Trailing tokens after the JSON value are rejected. json.Unmarshal
//     allows {"a":1}garbage to decode silently; the strict path catches
//     that as a malformed envelope.
//
// On any decode failure, the returned error is *ParamsError so callers
// can map it to -32602.
func decodeParams(data json.RawMessage, dst any) error {
	if len(data) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &ParamsError{Err: fmt.Errorf("decode params: %w", err)}
	}
	if dec.More() {
		return &ParamsError{Err: fmt.Errorf("decode params: trailing tokens after JSON value")}
	}
	return nil
}
