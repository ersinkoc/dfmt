package transport

import "fmt"

// Required-argument validation for MCP tool calls.
//
// Why this exists: every tool schema in mcp_schemas.go declares a
// "required" list, but nothing enforced it. decodeRequiredParams only
// rejects a wholly ABSENT arguments object; an arguments object that is
// present but missing the required field decoded happily into a zero value
// and the tool ran on it. For dfmt_exec that meant an empty command string
// handed to the shell, which exits 0 in ~25 ms having produced nothing — so
// the caller received:
//
//	{"exit":0,"duration_ms":25,"timed_out":false}
//
// A successful-looking result. Indistinguishable from "your command ran and
// printed nothing", which is a perfectly ordinary outcome for a real
// command. The failure mode is worst for the most likely mistake — an agent
// guessing the argument name (`command` instead of `code`, `file` instead of
// `path`) gets silence instead of a correction, and no amount of retrying
// the same wrong name reveals the problem.
//
// Unknown-field rejection (decodeParams' DisallowUnknownFields) would catch
// the misnamed-argument case too, but it is off by default and turning it on
// would reject extra fields from otherwise-correct clients. Validating the
// fields the schema already declares required is the version that cannot
// break a correct caller: it only rejects calls that could never have done
// what the caller intended.
//
// Keep this list in sync with the "required" arrays in mcp_schemas.go, with
// one deliberate divergence: JSON Schema "required" means the KEY must be
// present, not that its value must be non-empty. Two schema-required fields
// have meaningful empty values — edit's `new_string` (an empty replacement
// is a deletion) and write's `content` (truncating a file to zero bytes) —
// so they are NOT validated here. Rejecting those would remove capability
// rather than catch a mistake.

// validator is implemented by param types with required fields. dispatchTool
// checks for it after decoding; types without required fields (StatsParams,
// RecallParams) simply do not implement it.
type validator interface {
	Validate() error
}

// missingParam builds the -32602 message. It names the tool and the field so
// the caller can fix the call without consulting the schema — the whole
// point is that a wrong guess must be self-correcting.
func missingParam(tool, field string) error {
	return &ParamsError{Err: fmt.Errorf(
		"%s: missing required argument %q", tool, field)}
}

// Validate reports a missing `code`. Empty-string counts as missing: an
// empty command is never a meaningful request, and treating it as one is
// exactly the silent-success bug this guards.
func (p ExecParams) Validate() error {
	if p.Code == "" {
		return missingParam("dfmt_exec", "code")
	}
	return nil
}

func (p ReadParams) Validate() error {
	if p.Path == "" {
		return missingParam("dfmt_read", "path")
	}
	return nil
}

func (p FetchParams) Validate() error {
	if p.URL == "" {
		return missingParam("dfmt_fetch", "url")
	}
	return nil
}

func (p GlobParams) Validate() error {
	if p.Pattern == "" {
		return missingParam("dfmt_glob", "pattern")
	}
	return nil
}

func (p GrepParams) Validate() error {
	if p.Pattern == "" {
		return missingParam("dfmt_grep", "pattern")
	}
	return nil
}

// Validate requires path and old_string. new_string is deliberately NOT
// required — an empty new_string is a valid deletion.
func (p EditParams) Validate() error {
	if p.Path == "" {
		return missingParam("dfmt_edit", "path")
	}
	if p.OldString == "" {
		return missingParam("dfmt_edit", "old_string")
	}
	return nil
}

// Validate requires only path. An empty content is a legitimate request:
// truncating a file to zero bytes is a real operation, and rejecting it
// would remove capability rather than catch a mistake.
func (p WriteParams) Validate() error {
	if p.Path == "" {
		return missingParam("dfmt_write", "path")
	}
	return nil
}

func (p SearchParams) Validate() error {
	if p.Query == "" {
		return missingParam("dfmt_search", "query")
	}
	return nil
}

// Validate requires `type` — the event type — matching the schema. Not
// `message`: the schema leaves it optional because a journal event can
// carry its payload in `data` instead.
func (p RememberParams) Validate() error {
	if p.Type == "" {
		return missingParam("dfmt_remember", "type")
	}
	return nil
}
