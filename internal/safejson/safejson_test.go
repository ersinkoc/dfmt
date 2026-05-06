package safejson

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckDepthShallow(t *testing.T) {
	cases := []string{
		`{}`,
		`[]`,
		`{"a":1}`,
		`{"a":[1,2,3]}`,
		`[{"a":[1,2]},{"b":3}]`,
		`null`,
		`""`,
		`"with {{{ braces in string"`, // not counted toward depth
	}
	for _, in := range cases {
		if err := CheckDepth([]byte(in)); err != nil {
			t.Errorf("CheckDepth(%q) unexpected error: %v", in, err)
		}
	}
}

// TestCheckDepthRejectsDeep confirms the depth-bomb shape from the V-10
// audit finding is refused. Picking 100 levels well over the default bound
// of 64 — the exact constant boundary is exercised by TestCheckDepthBoundary.
func TestCheckDepthRejectsDeep(t *testing.T) {
	deep := strings.Repeat("[", 100) + strings.Repeat("]", 100)
	err := CheckDepth([]byte(deep))
	if err == nil {
		t.Fatal("CheckDepth on 100-deep array returned nil; want ErrMaxDepthExceeded")
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Errorf("err = %v; want errors.Is(ErrMaxDepthExceeded)", err)
	}
}

// TestCheckDepthBoundary asserts that exactly MaxJSONDepth levels are
// accepted and exactly MaxJSONDepth+1 is refused. Pin both edges so a
// future tweak to the constant can't accidentally drift the boundary.
func TestCheckDepthBoundary(t *testing.T) {
	atLimit := strings.Repeat("[", MaxJSONDepth) + strings.Repeat("]", MaxJSONDepth)
	if err := CheckDepth([]byte(atLimit)); err != nil {
		t.Errorf("CheckDepth at MaxJSONDepth (%d) errored: %v", MaxJSONDepth, err)
	}

	overLimit := strings.Repeat("[", MaxJSONDepth+1) + strings.Repeat("]", MaxJSONDepth+1)
	if err := CheckDepth([]byte(overLimit)); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Errorf("CheckDepth at MaxJSONDepth+1 err = %v; want ErrMaxDepthExceeded", err)
	}
}

// TestCheckDepthIgnoresStringBraces confirms the V-10 false-positive guard:
// JSON-encoded strings containing brace characters must not be counted
// toward the nesting depth. Without this, a payload like `"[[[[[[…"`
// (a single string with bracket characters inside) would falsely trip
// the limit. Use a string with thousands of brackets to make the
// assertion unambiguous.
func TestCheckDepthIgnoresStringBraces(t *testing.T) {
	bracketsInString := `"` + strings.Repeat("[", 1000) + `"`
	if err := CheckDepth([]byte(bracketsInString)); err != nil {
		t.Errorf("CheckDepth on string-with-brackets errored: %v", err)
	}
}

// TestCheckDepthHonorsEscapes pins escape handling: `\"` inside a string
// must not terminate the string early. A document like `{"k":"\"[",…}`
// must read the `[` as part of the string, not as a depth opener.
func TestCheckDepthHonorsEscapes(t *testing.T) {
	in := `{"k":"\"[\""}` // value is the string `"["`
	if err := CheckDepth([]byte(in)); err != nil {
		t.Errorf("CheckDepth on escaped quote errored: %v", err)
	}
}

func TestUnmarshalForwardsErrors(t *testing.T) {
	var v map[string]any
	// Malformed JSON with shallow depth — should fall through to
	// encoding/json's parse error, not return ErrMaxDepthExceeded.
	err := Unmarshal([]byte(`{"a":}`), &v)
	if err == nil {
		t.Fatal("Unmarshal accepted malformed JSON")
	}
	if errors.Is(err, ErrMaxDepthExceeded) {
		t.Errorf("malformed-JSON error must not be ErrMaxDepthExceeded; got %v", err)
	}
}

func TestUnmarshalRejectsDeepBeforeStdlib(t *testing.T) {
	deep := strings.Repeat("[", 200) + strings.Repeat("]", 200)
	var v any
	err := Unmarshal([]byte(deep), &v)
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Errorf("err = %v; want ErrMaxDepthExceeded (depth gate must run before json.Unmarshal)", err)
	}
}
