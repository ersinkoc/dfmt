package sandbox

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
			{Op: "exec", Text: "dfmt"},
			{Op: "exec", Text: "dfmt *"},
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
			{Op: "read", Text: ".env*"},
			{Op: "read", Text: "**/secrets/**"},
			{Op: "read", Text: "**/id_rsa"},
			{Op: "read", Text: "**/id_*"},
			{Op: "fetch", Text: "http://169.254.169.254/*"},
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
			result.WriteByte(pattern[i])
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
			result.WriteByte(pattern[i])
			i++
		}
	}
	return "^" + result.String() + "$"
}

// regexCache caches compiled regex patterns for performance.
var regexCache sync.Map

func regexMatch(pattern, text string) bool {
	// Check cache first
	if cached, ok := regexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp).MatchString(text)
	}

	// Compile and cache
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	regexCache.Store(pattern, re)
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
func splitByShellOperators(cmd string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

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
			// Check for two-char operators
			if i+1 < len(cmd) {
				next := cmd[i+1]
				if c == '&' && next == '&' {
					parts = append(parts, current.String())
					current.Reset()
					i++
					continue
				}
				if c == '|' && next == '|' {
					parts = append(parts, current.String())
					current.Reset()
					i++
					continue
				}
			}
			if c == ';' || c == '|' || c == '>' || c == '<' {
				if current.Len() > 0 {
					parts = append(parts, current.String())
					current.Reset()
				}
				continue
			}
			if c == '\n' {
				if current.Len() > 0 {
					parts = append(parts, current.String())
					current.Reset()
				}
				continue
			}
			current.WriteByte(c)
		} else {
			current.WriteByte(c)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// Read implements the Sandbox interface.
func (s *SandboxImpl) Read(ctx context.Context, req ReadReq) (ReadResp, error) {
	// Clean the path to prevent directory traversal
	cleanPath := filepath.Clean(req.Path)

	// If working directory is set and path is relative, verify it's within wd
	if s.wd != "" && !filepath.IsAbs(cleanPath) {
		absWd, err := filepath.Abs(s.wd)
		if err == nil {
			absPath := filepath.Join(absWd, cleanPath)
			cleanAbsPath := filepath.Clean(absPath)
			// Verify the resolved path is still within working directory
			rel, err := filepath.Rel(absWd, cleanAbsPath)
			if err != nil || strings.HasPrefix(rel, "..") {
				return ReadResp{}, fmt.Errorf("path outside working directory: %s", req.Path)
			}
		}
	}

	// Policy check with the clean path
	if err := s.PolicyCheck("read", cleanPath); err != nil {
		return ReadResp{}, err
	}

	// Basic read - full implementation would handle chunking
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return ReadResp{}, err
	}

	content := string(data)

	// Validate Offset and Limit to prevent panic or out-of-bounds access
	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	offset := int(req.Offset)
	limit := int(req.Limit)
	if offset > 0 && offset < len(content) {
		content = content[offset:]
	}
	if limit > 0 && limit < len(content) {
		content = content[:limit]
	}

	// Intent-based filtering
	keywords := ExtractKeywords(req.Intent)
	if len(keywords) > 0 {
		matches := MatchContent(content, keywords, 10)
		if len(matches) > 0 {
			return ReadResp{
				Matches:   matches,
				Summary:   GenerateSummary(content, keywords),
				Size:      int64(len(data)),
				ReadBytes: int64(len(content)),
			}, nil
		}
		return ReadResp{
			Content:   content,
			Summary:   GenerateSummary(content, keywords),
			Size:      int64(len(data)),
			ReadBytes: int64(len(content)),
		}, nil
	}

	return ReadResp{
		Content:   content,
		Size:      int64(len(data)),
		ReadBytes: int64(len(content)),
	}, nil
}

// Fetch implements the Sandbox interface.
func (s *SandboxImpl) Fetch(ctx context.Context, req FetchReq) (FetchResp, error) {
	// Policy check
	if err := s.PolicyCheck("fetch", req.URL); err != nil {
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

	client := &http.Client{Timeout: req.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return FetchResp{}, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
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

// writeTempFile writes code to a temp file.
func writeTempFile(lang, code string) (string, error) {
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
	// Ensure cleanup on error - name needed for removal before Close
	tmpName := tmpfile.Name()
	defer func() {
		if err != nil {
			tmpfile.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmpfile.WriteString(code); err != nil {
		return "", err
	}
	// Sync before returning to ensure data is written
	tmpfile.Sync()
	tmpfile.Close()
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

	// Add extra env vars
	for k, v := range extra {
		env = append(env, k+"="+v)
	}

	return env
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
