package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

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
	return globMatch(r.Text, text)
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
			{Op: "exec", Text: "git *"},
			{Op: "exec", Text: "npm *"},
			{Op: "exec", Text: "pnpm *"},
			{Op: "exec", Text: "pytest *"},
			{Op: "exec", Text: "cargo *"},
			{Op: "exec", Text: "go *"},
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
		if action == "allow" {
			policy.Allow = append(policy.Allow, rule)
		} else if action == "deny" {
			policy.Deny = append(policy.Deny, rule)
		}
	}

	return policy, nil
}

// globMatch does simple glob matching (* matches any number of chars).
func globMatch(pattern, text string) bool {
	// Convert glob pattern to regex
	regex := globToRegex(pattern)
	return regexMatch(regex, text)
}

func globToRegex(pattern string) string {
	var result strings.Builder
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			// ** matches anything including path separators
			result.WriteString(".*")
			i += 2
		} else if i+1 < len(pattern) && pattern[i] == '/' && pattern[i+1] == '*' {
			// /* at end or followed by end: matches just "/" (root only)
			// /* followed by more: matches "/<something>" path component after /
			if i+2 >= len(pattern) || (i+2 < len(pattern) && pattern[i+2] == '$') {
				// /* at end - match root only
				result.WriteString("/")
				i += 2
			} else {
				// /* followed by more content - match a path component after /
				result.WriteString("/[^/]+")
				i += 2
			}
		} else if pattern[i] == '*' {
			result.WriteString("[^/]*")
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

func regexMatch(pattern, text string) bool {
	// Use Go's regexp for proper matching
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
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
		wd:      wd,
	}
}

// NewSandboxWithPolicy creates a sandbox with a custom policy.
func NewSandboxWithPolicy(wd string, policy Policy) *SandboxImpl {
	return &SandboxImpl{
		runtimes: NewRuntimes(),
		policy:   policy,
		wd:      wd,
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

// Exec implements the Sandbox interface.
func (s *SandboxImpl) Exec(ctx context.Context, req ExecReq) (ExecResp, error) {
	// Policy check
	cmd := req.Code
	if req.Lang != "" && req.Lang != "sh" && req.Lang != "bash" {
		cmd = req.Lang + " " + cmd
	}
	if err := s.PolicyCheck("exec", cmd); err != nil {
		return ExecResp{}, err
	}

	// Get runtime
	rt, ok := s.runtimes.Get(req.Lang)
	if !ok || !rt.Available {
		return ExecResp{}, fmt.Errorf("runtime not available: %s", req.Lang)
	}

	// Execute
	return s.execImpl(ctx, req, rt)
}

// Read implements the Sandbox interface.
func (s *SandboxImpl) Read(ctx context.Context, req ReadReq) (ReadResp, error) {
	// Policy check
	if err := s.PolicyCheck("read", req.Path); err != nil {
		return ReadResp{}, err
	}

	// Basic read - full implementation would handle chunking
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return ReadResp{}, err
	}

	content := string(data)
	if req.Offset > 0 && int(req.Offset) < len(content) {
		content = content[req.Offset:]
	}
	if req.Limit > 0 && int(req.Limit) < len(content) {
		content = content[:req.Limit]
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

	// Stub - full implementation would do HTTP request
	return FetchResp{
		Status:  200,
		Body:    "",
		Summary: "fetch not yet implemented",
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

	if rt.Lang == "bash" || rt.Lang == "sh" {
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

	output := string(out)
	if len(output) > MaxRawBytes {
		output = output[:MaxRawBytes]
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
	defer tmpfile.Close()

	if _, err := tmpfile.WriteString(code); err != nil {
		return "", err
	}

	return tmpfile.Name(), nil
}

// buildEnv builds the environment for a subprocess.
func buildEnv(extra map[string]string) []string {
	// Start with a minimal environment
	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin",
		"LANG=en_US.UTF-8",
		"TERM=xterm",
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