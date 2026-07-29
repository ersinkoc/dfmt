package transport

import (
	"strings"
	"testing"
)

// TestValidateRejectsMissingRequiredArgs is the regression test for the
// silent-empty-success bug: a tool call whose arguments object omits the
// required field used to decode to a zero value and RUN, returning
// {"exit":0,...} with no output — indistinguishable from a real command
// that printed nothing.
func TestValidateRejectsMissingRequiredArgs(t *testing.T) {
	tests := []struct {
		name      string
		params    validator
		wantField string
	}{
		{"exec without code", ExecParams{Intent: "x"}, "code"},
		{"read without path", ReadParams{Intent: "x"}, "path"},
		{"fetch without url", FetchParams{Intent: "x"}, "url"},
		{"glob without pattern", GlobParams{Intent: "x"}, "pattern"},
		{"grep without pattern", GrepParams{Intent: "x"}, "pattern"},
		{"edit without path", EditParams{OldString: "a"}, "path"},
		{"edit without old_string", EditParams{Path: "f.go"}, "old_string"},
		{"write without path", WriteParams{Content: "x"}, "path"},
		{"search without query", SearchParams{}, "query"},
		{"remember without type", RememberParams{Message: "x"}, "type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.params.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error naming %q", tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("Validate() = %q, want it to name the missing field %q",
					err.Error(), tc.wantField)
			}
			// Must map to -32602 (Invalid params), not -32603 (Internal
			// error): the caller can fix this, and the code is how they
			// learn that.
			if !IsParamsError(err) {
				t.Errorf("Validate() error is not a ParamsError; it would be "+
					"reported as an internal error instead of invalid params: %v", err)
			}
		})
	}
}

func TestValidateAcceptsCompleteArgs(t *testing.T) {
	tests := []struct {
		name   string
		params validator
	}{
		{"exec", ExecParams{Code: "go version"}},
		{"read", ReadParams{Path: "main.go"}},
		{"fetch", FetchParams{URL: "https://example.com"}},
		{"glob", GlobParams{Pattern: "**/*.go"}},
		{"grep", GrepParams{Pattern: "func"}},
		{"edit", EditParams{Path: "f.go", OldString: "a", NewString: "b"}},
		{"search", SearchParams{Query: "daemon"}},
		{"remember", RememberParams{Type: "decision"}},
		// Empty values that are legitimate requests, not mistakes: JSON
		// Schema "required" constrains key presence, not emptiness.
		{"edit with empty new_string is a deletion", EditParams{Path: "f.go", OldString: "a", NewString: ""}},
		{"write with empty content truncates", WriteParams{Path: "f.go", Content: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.params.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// The tools with no required fields must not implement validator — if they
// did, an empty-arguments call (the documented way to use them) would fail.
func TestNoRequiredFieldToolsAreNotValidated(t *testing.T) {
	if _, isValidator := any(StatsParams{}).(validator); isValidator {
		t.Error("StatsParams implements validator; dfmt_stats accepts empty arguments")
	}
	if _, isValidator := any(RecallParams{}).(validator); isValidator {
		t.Error("RecallParams implements validator; dfmt_recall accepts empty arguments")
	}
}
