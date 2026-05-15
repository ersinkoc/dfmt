package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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
	if op == "exec" {
		// Lowercase text so deny rules written in conventional lowercase form
		// (e.g. "deny:exec:git *") match uppercase invocations like "GIT" or "Git".
		// This is consistent with normalizeFetchURLForPolicy which lowercases
		// scheme/host before matching fetch deny rules.
		text = strings.ToLower(text)
	} else {
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
// **Default-permissive by design.** Read, write, edit, and exec are
// allow-all; the only built-in deny rules are the SSRF safety net
// (cloud-metadata literals + `file://`). The threat model is a single-
// user, single-trust-principal local daemon — the agent runs with the
// same authority as the operator's shell, so a default that withheld
// `.env` or `.git/` would be surface-area theater (the agent's shell
// already reads them) without preventing the actual exfil paths
// (network egress, content output). See `project_sandbox_default_
// permissive` decision note.
//
// Two layers do the actual work:
//
//   - **redact.yaml** (loaded by `internal/redact`) scrubs sensitive
//     values from tool output before the bytes reach the agent or land
//     in the journal. Add custom shapes there, not here. As of V-01,
//     redaction is also re-applied on `dfmt_recall` render so updates
//     to redact.yaml apply retroactively.
//
//   - **.dfmt/permissions.yaml** lets operators add project-scoped
//     `deny:` rules that ride on top of this policy via `MergePolicies`.
//     Examples for operators with secret stores outside the redactor's
//     default shapes:
//
//     deny:read:creds/**
//     deny:read:**/private_keys/**
//     deny:write:creds/**
//     deny:edit:creds/**
//
// V-05 closure: this docstring previously claimed `.env*`, `.dfmt/**`,
// `.git/**`, and similar were in the default deny list. They were not,
// and never had been in the default-permissive era — the docstring lied
// about coverage that an operator would reasonably trust. Aligned with
// the actual `Deny:` slice below.
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
