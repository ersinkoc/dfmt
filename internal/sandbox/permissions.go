package sandbox

import (
	"context"
	"container/list"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const langBash = "bash"

// Policy represents a security policy for sandbox operations.
type Policy struct {
	Version int
	Allow   []Rule
	Deny    []Rule
}

// Rule is a single allow or deny rule.
type Rule struct {
	Op   string // "exec" | "read" | "fetch"
	Text string // Pattern to match
}

// Match checks if a rule matches the given operation and text.
func (r Rule) Match(op, text string) bool {
	if r.Op != op {
		return false
	}
	return globMatch(r.Text, text, op)
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
func DefaultPolicy() Policy {
	return Policy{
		Version: 1,
		Allow: []Rule{
			{Op: "exec", Text: "git"},
			{Op: "exec", Text: "git *"},
			{Op: "exec", Text: "npm"},
			{Op: "exec", Text: "npm *"},
			{Op: "exec", Text: "pnpm"},
			{Op: "exec", Text: "pnpm *"},
			{Op: "exec", Text: "pytest"},
			{Op: "exec", Text: "pytest *"},
			{Op: "exec", Text: "cargo"},
			{Op: "exec", Text: "cargo *"},
			{Op: "exec", Text: "go"},
			{Op: "exec", Text: "go *"},
			{Op: "exec", Text: "echo"},
			{Op: "exec", Text: "echo *"},
			{Op: "exec", Text: "ls"},
			{Op: "exec", Text: "ls *"},
			{Op: "exec", Text: "cat"},
			{Op: "exec", Text: "cat *"},
			{Op: "exec", Text: "find"},
			{Op: "exec", Text: "find *"},
			{Op: "exec", Text: "grep"},
			{Op: "exec", Text: "grep *"},
			{Op: "exec", Text: "dir"},
			{Op: "exec", Text: "dir *"},
			{Op: "exec", Text: "pwd"},
			{Op: "exec", Text: "whoami"},
			// Note: running dfmt recursively from inside a sandboxed exec is intentionally
			// denied — `dfmt exec ...` passes arbitrary shell code to the runtime and would
			// bypass the outer policy (e.g. `dfmt exec 'sudo rm -rf /'`). Agents must call
			// DFMT tools via MCP, not via a shell invocation.
			{Op: "exec", Text: "wc"},
			{Op: "exec", Text: "wc *"},
			{Op: "exec", Text: "tail"},
			{Op: "exec", Text: "tail *"},
			{Op: "exec", Text: "node"},
			{Op: "exec", Text: "node *"},
			{Op: "exec", Text: "python"},
			{Op: "exec", Text: "python *"},
			{Op: "read", Text: "**"},
			{Op: "fetch", Text: "https://*"},
			{Op: "fetch", Text: "http://*"},
		},
		Deny: []Rule{
			{Op: "exec", Text: "sudo *"},
			{Op: "exec", Text: "rm -rf /*"},
			{Op: "exec", Text: "curl * | sh"},
			{Op: "exec", Text: "wget * | sh"},
			{Op: "exec", Text: "shutdown *"},
			{Op: "exec", Text: "reboot *"},
			{Op: "exec", Text: "mkfs *"},
			{Op: "exec", Text: "dd if=*"},
			// Block recursive dfmt invocations that would bypass the outer policy.
			{Op: "exec", Text: "dfmt"},
			{Op: "exec", Text: "dfmt *"},
			{Op: "read", Text: ".env*"},
			{Op: "read", Text: "**/secrets/**"},
			{Op: "read", Text: "**/id_rsa"},
			{Op: "read", Text: "**/id_*"},
			// SSRF: block cloud metadata and file:// explicitly. Network-level
			// guards (loopback, RFC1918, link-local) are also applied in Fetch().
			{Op: "fetch", Text: "http://169.254.169.254/*"},
			{Op: "fetch", Text: "https://169.254.169.254/*"},
			{Op: "fetch", Text: "http://metadata.google.internal/*"},
			{Op: "fetch", Text: "https://metadata.google.internal/*"},
			{Op: "fetch", Text: "http://metadata.goog/*"},
			{Op: "fetch", Text: "file://*"},
		},
	}
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

	return policy, nil
}

// globMatch does simple glob matching (* matches any number of chars).
// For exec operations, * matches anything (shell-style).
// For read/fetch operations, * doesn't match / (path-style).
func globMatch(pattern, text string, op string) bool {
	// Normalize Windows path separators for path-based ops so rules written
	// with forward slashes (e.g. "**/id_rsa") still match `C:\Users\x\id_rsa`.
	if op == "read" {
		text = filepath.ToSlash(text)
		pattern = filepath.ToSlash(pattern)
	}
	// For exec operations, use shell-style globbing where * matches anything including /
	// For read/fetch, * doesn't match / (path segments)
	if op == "exec" {
		regex := globToRegexShell(pattern)
		return regexMatch(regex, text)
	}
	// Convert glob pattern to regex for read/fetch (path-based)
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
			result.WriteString("/.+")
			i += 2
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
			// Single * - check BEFORE /* to properly handle https://*
			// For URL patterns like https://*, * should match everything including /
			if i >= 3 && pattern[i-3] == ':' && pattern[i-2] == '/' && pattern[i-1] == '/' {
				result.WriteString(".*")
			} else {
				result.WriteString("[^/]*") // Path-style: * doesn't match /
			}
			i++
		} else if i+1 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' {
			// /* at end matches any non-empty path segment
			// But skip if this is part of ://* URL pattern (://* should use .*)
			if i >= 2 && pattern[i-2] == ':' && pattern[i-1] == '/' {
				// This is ://* URL pattern - skip /* and let * branch handle it
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
	runtimes *Runtimes
	policy   Policy
	wd       string // Working directory
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
		return fmt.Errorf("operation denied by policy: %s %s", op, text)
	}
	return nil
}

// extractBaseCommand extracts the base command (first word) from a shell command.
func extractBaseCommand(cmd string) string {
	// Remove leading/trailing whitespace
	cmd = strings.TrimSpace(cmd)

	// Handle quoted strings - find first unquoted space
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(cmd); i++ {
		if !inQuote && (cmd[i] == '"' || cmd[i] == '\'') {
			inQuote = true
			quoteChar = cmd[i]
		} else if inQuote && cmd[i] == quoteChar {
			inQuote = false
		} else if !inQuote && cmd[i] == ' ' {
			return cmd[:i]
		}
	}
	return cmd
}

// shellOperators returns true if cmd contains shell operators that chain commands.
func hasShellChainOperators(cmd string) bool {
	// Check for common shell operators that chain commands
	operators := []string{"&&", "||", ";", "|", ">", ">>", "<", "<<", "\n"}
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
	// Policy check - for shell commands, check each chained command
	cmd := req.Code
	isLangPrefix := req.Lang != "" && req.Lang != "sh" && req.Lang != langBash
	if isLangPrefix {
		cmd = req.Lang + " " + cmd
	}

	// For shell commands with operators, check the full command chain first
	// against deny rules to catch dangerous patterns like "; rm -rf /" or "| sh"
	if hasShellChainOperators(cmd) {
		// First check: does the base command match any allow rule?
		baseCmd := extractBaseCommand(cmd)
		// Skip env var assignments (e.g., "GOCACHE=xxx go test")
		if baseCmd != "" && !isEnvAssignment(baseCmd) && !s.policy.Evaluate("exec", baseCmd) {
			return ExecResp{}, fmt.Errorf("operation denied by policy: %s: base command '%s' not allowed", cmd, baseCmd)
		}
		// Second check: does the full command match any deny rule?
		if !s.policy.Evaluate("exec", cmd) {
			return ExecResp{}, fmt.Errorf("operation denied by policy: %s: %v", cmd, "blocked by deny rule")
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
				return ExecResp{}, fmt.Errorf("operation denied by policy: %s: part '%s' not allowed", cmd, part)
			}
		}
	} else if isLangPrefix {
		// For non-shell single commands, check the full command with lang prefix
		// e.g., "python script.py" against "python *"
		if err := s.PolicyCheck("exec", cmd); err != nil {
			return ExecResp{}, err
		}
	} else {
		// For shell single commands, check the full command
		if err := s.PolicyCheck("exec", cmd); err != nil {
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
			// $(...) command substitution: extract the inner part as its own segment.
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
					parts = append(parts, cmd[i+2:j])
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
					parts = append(parts, cmd[i+1:j])
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
			if c == ';' || c == '|' || c == '>' || c == '<' || c == '\n' {
				flush()
				continue
			}
			current.WriteByte(c)
		} else {
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
		return ReadResp{}, fmt.Errorf("absolute paths not allowed without working directory")
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

	// Intent-based filtering
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 {
		matches := MatchContent(content, keywords, 10)
		if len(matches) > 0 {
			return ReadResp{
				Matches:   matches,
				Summary:   GenerateSummary(content, keywords),
				Size:      totalSize,
				ReadBytes: int64(len(content)),
			}, nil
		}
		return ReadResp{
			Content:   content,
			Summary:   GenerateSummary(content, keywords),
			Size:      totalSize,
			ReadBytes: int64(len(content)),
		}, nil
	}

	return ReadResp{
		Content:   content,
		Size:      totalSize,
		ReadBytes: int64(len(content)),
	}, nil
}

// MaxFetchBodyBytes caps the size of an HTTP response body that Fetch will read.
const MaxFetchBodyBytes = 8 * 1024 * 1024 // 8 MiB

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
	// Well-known cloud metadata hostnames. Block these before DNS so a
	// resolver that maps them to public IPs cannot evade the IP filter.
	lowerHost := strings.ToLower(host)
	if lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog" {
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
	// Cloud metadata literal.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return true
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

// Fetch implements the Sandbox interface.
func (s *SandboxImpl) Fetch(ctx context.Context, req FetchReq) (FetchResp, error) {
	// Policy check
	if err := s.PolicyCheck("fetch", req.URL); err != nil {
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

	for k, v := range req.Headers {
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

	// Intent-based filtering
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 {
		matches := MatchContent(content, keywords, 10)
		if len(matches) > 0 {
			return FetchResp{
				Status:     resp.StatusCode,
				Headers:    headers,
				Matches:    matches,
				Summary:    GenerateSummary(content, keywords),
				Vocabulary: ExtractVocabulary(content),
			}, nil
		}
		return FetchResp{
			Status:     resp.StatusCode,
			Headers:    headers,
			Body:       content,
			Summary:    GenerateSummary(content, keywords),
			Vocabulary: ExtractVocabulary(content),
		}, nil
	}

	return FetchResp{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    content,
	}, nil
}

// BatchExec implements the Sandbox interface.
func (s *SandboxImpl) BatchExec(ctx context.Context, items []any) ([]any, error) {
	// Stub
	return nil, nil
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
	cmd.Env = buildEnv(req.Env)

	out, err := cmd.Output()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ExecResp{}, err
		}
	}

	// Windows Git Bash outputs UTF-16LE with null bytes; convert to UTF-8
	output := convertUTF16LEToUTF8(out)
	if len(output) > MaxRawBytes {
		output = output[:MaxRawBytes]
	}

	// Intent-based filtering
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 {
		matches := MatchContent(output, keywords, 10)
		if len(matches) > 0 {
			return ExecResp{
				Exit:       exitCode,
				Matches:    matches,
				Summary:    GenerateSummary(output, keywords),
				Vocabulary: ExtractVocabulary(output),
				DurationMs: int(time.Since(start).Milliseconds()),
			}, nil
		}
		return ExecResp{
			Exit:       exitCode,
			Stdout:     output,
			Summary:    GenerateSummary(output, keywords),
			Vocabulary: ExtractVocabulary(output),
			DurationMs: int(time.Since(start).Milliseconds()),
		}, nil
	}

	return ExecResp{
		Exit:       exitCode,
		Stdout:     output,
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

// buildEnv builds the environment for a subprocess.
func buildEnv(extra map[string]string) []string {
	var env []string

	if runtime.GOOS == "windows" {
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
var (
	sandboxBlockedEnvPrefixes = []string{
		"LD_",
		"DYLD_",
		"GIT_",
		"NODE_",
		"PYTHON",
		"RUBY",
		"PERL5",
	}
	sandboxBlockedEnvNames = map[string]struct{}{
		"PATH":            {},
		"IFS":             {},
		"BASH_ENV":        {},
		"ENV":             {},
		"PS4":             {},
		"PROMPT_COMMAND":  {},
		"HOME":            {},
		"USER":            {},
		"USERPROFILE":     {},
		"APPDATA":         {},
		"LOCALAPPDATA":    {},
		"PATHEXT":         {},
		"COMSPEC":         {},
		"SYSTEMROOT":      {},
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
func convertUTF16LEToUTF8(data []byte) string {
	// Check if it looks like UTF-16LE (null bytes alternating with ASCII)
	isUTF16 := len(data) >= 4
	if isUTF16 {
		nullCount := 0
		for i := 0; i < len(data) && i < 100; i += 2 {
			if data[i] == 0 {
				nullCount++
			}
		}
		// If more than 30% of even-position bytes are null, treat as UTF-16LE
		if nullCount > 15 {
			isUTF16 = true
		} else {
			isUTF16 = false
		}
	}

	if !isUTF16 {
		return string(data)
	}

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
