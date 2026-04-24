package redact

import (
	"regexp"
	"strings"
)

// Redactor handles PII and sensitive data redaction.
type Redactor struct {
	patterns []*redactPattern
}

// redactPattern is a pattern for redacting sensitive data.
type redactPattern struct {
	name  string
	regex *regexp.Regexp
	repl  string
}

// envExportLineRegex matches `NAME=value` and `export NAME=value` lines.
// Captures: (1) optional "export " prefix, (2) NAME, (3) value.
// The replacement decision (redact vs keep) is made by IsSensitiveKey(NAME) in Redact().
var envExportLineRegex = regexp.MustCompile(`(?m)^(export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// Common patterns for sensitive data
var commonPatterns = []*redactPattern{
	// API keys and tokens
	{name: "github_token", regex: regexp.MustCompile(`(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,}`), repl: "[GITHUB_TOKEN]"},
	{name: "openai_key", regex: regexp.MustCompile(`sk-[A-Za-z0-9_]{48,}`), repl: "[OPENAI_KEY]"},
	{name: "aws_key", regex: regexp.MustCompile(`AKIA[A-Z0-9]{16}`), repl: "[AWS_KEY]"},
	// aws_secret: capture a 40-char base64-ish token that follows an AWS secret-key marker.
	// The prior pattern matched any 40-char token at end-of-line, producing too many false positives.
	{name: "aws_secret", regex: regexp.MustCompile(`(?i)(aws[_-]?secret[_-]?access[_-]?key|aws[_-]?secret)\s*[:=]\s*['"]?([A-Za-z0-9/+=]{40})['"]?`), repl: "$1: [AWS_SECRET]"},
	{name: "stripe_key", regex: regexp.MustCompile(`sk_live_[A-Za-z0-9]{24,}`), repl: "[STRIPE_KEY]"},
	{name: "stripe_token", regex: regexp.MustCompile(`tok_[A-Za-z0-9]{24,}`), repl: "[STRIPE_TOKEN]"},
	{name: "generic_secret", regex: regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|access[_-]?token|auth[_-]?token)\s*[:=]\s*['"]?([A-Za-z0-9_/+=.-]{20,})['"]?`), repl: "$1: [REDACTED]"},
	{name: "bearer_token", regex: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_.-]{20,}`), repl: "Bearer [REDACTED]"},
	{name: "basic_auth", regex: regexp.MustCompile(`(?i)basic\s+[A-Za-z0-9_.-]{10,}={0,2}`), repl: "Basic [REDACTED]"},

	// env_export is handled in Redact() via ReplaceAllStringFunc so the var
	// name can be matched against IsSensitiveKey() instead of a brittle regex.
	// A placeholder pattern is kept here only so persisted stats reference this name.
	{name: "env_export", regex: envExportLineRegex, repl: ""},

	// Private keys
	{name: "private_key", regex: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`), repl: "[PRIVATE KEY]"},

	// JWT tokens
	{name: "jwt", regex: regexp.MustCompile(`eyJ[A-Za-z0-9_=-]+\.eyJ[A-Za-z0-9_=-]+\.[A-Za-z0-9_/+=.-]*`), repl: "[JWT]"},

	// IP addresses (optionally - can be noisy)
	// {name: "ipv4", regex: regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`), repl: "[IP]"},
}

// NewRedactor creates a new Redactor with default patterns.
func NewRedactor() *Redactor {
	return &Redactor{
		patterns: commonPatterns,
	}
}

// NewRedactorWithCustom creates a redactor with custom patterns.
func NewRedactorWithCustom(patterns []*redactPattern) *Redactor {
	all := append([]*redactPattern(nil), commonPatterns...)
	all = append(all, patterns...)
	return &Redactor{patterns: all}
}

// Redact redacts sensitive data from the input string.
func (r *Redactor) Redact(s string) string {
	for _, p := range r.patterns {
		if p.name == "env_export" {
			s = redactEnvExport(s)
			continue
		}
		s = p.regex.ReplaceAllString(s, p.repl)
	}
	return s
}

// redactEnvExport replaces the value of every `[export] NAME=value` line whose
// NAME is classified as sensitive by IsSensitiveKey. Non-sensitive assignments
// (e.g. HOME=/tmp, DEBUG=true) are left untouched.
func redactEnvExport(s string) string {
	return envExportLineRegex.ReplaceAllStringFunc(s, func(line string) string {
		m := envExportLineRegex.FindStringSubmatch(line)
		if m == nil {
			return line
		}
		prefix, name := m[1], m[2]
		if !IsSensitiveKey(name) {
			return line
		}
		return prefix + name + "=[REDACTED]"
	})
}

// countEnvExportRedactions returns how many env_export lines would be redacted,
// used for RedactWithStats accounting.
func countEnvExportRedactions(s string) int {
	matches := envExportLineRegex.FindAllStringSubmatch(s, -1)
	count := 0
	for _, m := range matches {
		if len(m) >= 3 && IsSensitiveKey(m[2]) {
			count++
		}
	}
	return count
}

// RedactMap redacts sensitive data in a map of strings.
func (r *Redactor) RedactMap(m map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		result[k] = r.Redact(v)
	}
	return result
}

// RedactEvent redacts sensitive data from event data.
func (r *Redactor) RedactEvent(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}

	result := make(map[string]any)
	for k, v := range data {
		switch val := v.(type) {
		case string:
			result[k] = r.Redact(val)
		case map[string]any:
			result[k] = r.RedactEvent(val)
		case []string:
			redacted := make([]string, len(val))
			for i, s := range val {
				redacted[i] = r.Redact(s)
			}
			result[k] = redacted
		default:
			result[k] = val
		}
	}
	return result
}

// AddPattern adds a custom redaction pattern.
func (r *Redactor) AddPattern(name, pattern, repl string) error {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	r.patterns = append(r.patterns, &redactPattern{
		name:  name,
		regex: regex,
		repl:  repl,
	})
	return nil
}

// IsSensitiveKey returns true if the key name suggests sensitive data.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	sensitive := []string{
		"password", "passwd", "secret", "token", "api_key", "apikey",
		"access_token", "refresh_token", "auth", "credential", "private",
		"key", "session", "jwt", "bearer",
	}
	for _, s := range sensitive {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// Stats represents redaction statistics.
type Stats struct {
	RedactedCount int            `json:"redacted_count"`
	RedactedTypes map[string]int `json:"redacted_types"`
	OriginalSize  int            `json:"original_size"`
	RedactedSize  int            `json:"redacted_size"`
}

// RedactWithStats redacts and returns statistics.
func (r *Redactor) RedactWithStats(s string) (string, Stats) {
	stats := Stats{
		RedactedTypes: make(map[string]int),
		OriginalSize:  len(s),
	}

	result := s
	for _, p := range r.patterns {
		if p.name == "env_export" {
			if n := countEnvExportRedactions(result); n > 0 {
				stats.RedactedCount += n
				stats.RedactedTypes[p.name] = n
			}
			result = redactEnvExport(result)
			continue
		}
		matches := p.regex.FindAllStringIndex(result, -1)
		if len(matches) > 0 {
			stats.RedactedCount += len(matches)
			stats.RedactedTypes[p.name] = len(matches)
		}
		result = p.regex.ReplaceAllString(result, p.repl)
	}

	stats.RedactedSize = len(result)
	return result, stats
}
