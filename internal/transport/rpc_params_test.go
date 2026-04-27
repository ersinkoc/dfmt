package transport

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestDecodeParams_EmptyBodyZeroValues — JSON-RPC 2.0 §4.1 says params is
// optional. An empty body must leave dst at its zero value with no error,
// so methods like Stats/Recall (which take an optional filter struct) keep
// working when the caller sends `"params": null` or omits the field.
func TestDecodeParams_EmptyBodyZeroValues(t *testing.T) {
	cases := []struct {
		name string
		data json.RawMessage
	}{
		{"nil", nil},
		{"empty slice", json.RawMessage([]byte{})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p RememberParams
			if err := decodeParams(tc.data, &p); err != nil {
				t.Fatalf("decodeParams(%s): %v", tc.name, err)
			}
			if p.Type != "" || p.Source != "" {
				t.Errorf("expected zero value, got %+v", p)
			}
		})
	}
}

// TestDecodeParams_ValidStruct — the happy path: well-formed params with
// known fields decode into the typed struct. Pinned because regressions
// here would silently zero out caller fields.
func TestDecodeParams_ValidStruct(t *testing.T) {
	data := json.RawMessage(`{"type":"note","source":"test"}`)
	var p RememberParams
	if err := decodeParams(data, &p); err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if p.Type != "note" || p.Source != "test" {
		t.Errorf("got %+v, want type=note source=test", p)
	}
}

// TestDecodeParams_UnknownFieldRejected — DisallowUnknownFields catches
// caller typos. `{"limt":10}` for `{"limit":10}` used to silently leave
// Limit=0; now the agent gets -32602 with the typo'd field name in the
// error message.
func TestDecodeParams_UnknownFieldRejected(t *testing.T) {
	data := json.RawMessage(`{"type":"note","unknownField":"oops"}`)
	var p RememberParams
	err := decodeParams(data, &p)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !IsParamsError(err) {
		t.Errorf("expected *ParamsError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "unknownField") {
		t.Errorf("error should name the offending field; got: %v", err)
	}
}

// TestDecodeParams_TrailingTokensRejected — json.Decoder accepts the first
// JSON value and stops; `{"a":1}garbage` would decode silently. dec.More()
// after Decode catches this so a malformed multi-value envelope can't slip
// through and produce surprising downstream behavior.
func TestDecodeParams_TrailingTokensRejected(t *testing.T) {
	data := json.RawMessage(`{"type":"note"}{"type":"two"}`)
	var p RememberParams
	err := decodeParams(data, &p)
	if err == nil {
		t.Fatal("expected error for trailing tokens, got nil")
	}
	if !IsParamsError(err) {
		t.Errorf("expected *ParamsError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error should mention trailing tokens; got: %v", err)
	}
}

// TestDecodeParams_InvalidJSONWrapped — straight syntax error from
// json.Decoder must still be wrapped in ParamsError so the connection
// loop maps it to -32602. Without the wrap, this would land on -32603
// (Internal error) — the very bug B-2 closes.
func TestDecodeParams_InvalidJSONWrapped(t *testing.T) {
	data := json.RawMessage(`{not json`)
	var p RememberParams
	err := decodeParams(data, &p)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !IsParamsError(err) {
		t.Errorf("expected *ParamsError, got %T: %v", err, err)
	}
}

// TestIsParamsError_NilAndForeignError — IsParamsError must not be fooled
// by nil or by an unrelated error type, otherwise the connection loop
// would misclassify Internal errors as Invalid params and vice versa.
func TestIsParamsError_NilAndForeignError(t *testing.T) {
	if IsParamsError(nil) {
		t.Error("IsParamsError(nil) = true, want false")
	}
	if IsParamsError(errors.New("some other error")) {
		t.Error("IsParamsError(plain error) = true, want false")
	}
}
