package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/osutil"
)

func (s *SandboxImpl) Exec(ctx context.Context, req ExecReq) (ExecResp, error) {
	// Default to auto-detected shell when caller omits the language.
	// On Windows: detects Git Bash > PowerShell > CMD
	// On Unix: defaults to bash
	if req.Lang == "" {
		req.Lang = DetectShell()
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

var ErrBatchExecNotImplemented = errors.New("batch exec not implemented")

// Fetch implements the Sandbox interface.
func (s *SandboxImpl) BatchExec(ctx context.Context, items []any) ([]any, error) {
	return nil, ErrBatchExecNotImplemented
}

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

	if osutil.IsWindows() {
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
