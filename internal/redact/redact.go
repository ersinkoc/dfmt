package redact

import (
	"regexp"
	"strings"
	"unicode"
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

// Common patterns for sensitive data.
//
// Ordering matters: more-specific prefixes run BEFORE broader ones so the
// labels stay accurate. Anthropic (sk-ant-…) runs before OpenAI (sk-…); the
// AWS prefix list runs before generic_secret. Each regex uses bounded
// character classes and no nested quantifiers to keep ReplaceAllString linear
// even on multi-MB inputs (sandbox stdout, journal lines).
var commonPatterns = []*redactPattern{
	// === API keys and tokens — provider-specific (most specific first) ===

	// Anthropic: sk-ant-api03-…, sk-ant-admin01-…. The body uses dashes,
	// so it must run BEFORE the OpenAI matcher (which is broadened below
	// to allow sk-proj-… style keys with dashes too).
	{name: "anthropic_key", regex: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{40,}`), repl: "[ANTHROPIC_KEY]"},

	// OpenAI legacy (sk-XXX) and project (sk-proj-XXX_YYY-ZZZ) keys. Body
	// is now [A-Za-z0-9_-]{40,} so multi-segment project keys match;
	// anthropic runs first to claim the sk-ant-* prefix.
	{name: "openai_key", regex: regexp.MustCompile(`sk-[A-Za-z0-9_-]{40,}`), repl: "[OPENAI_KEY]"},

	// GitHub classic PATs (ghp/gho/ghu/ghs/ghr) and modern fine-grained PATs
	// (github_pat_…, 82-char body). Both forms catalogued at:
	// https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-authentication-to-github
	{name: "github_token", regex: regexp.MustCompile(`(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,}`), repl: "[GITHUB_TOKEN]"},
	// Body length is variable in the wild (commonly 70–84 chars including
	// the version-prefix segment); {59,} is generous enough to catch every
	// version while still requiring far more entropy than any English word.
	{name: "github_fine_pat", regex: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{59,}`), repl: "[GITHUB_PAT]"},

	// AWS access key IDs across account/role/group/user/policy/instance-profile
	// prefixes. The previous regex only matched AKIA (long-term root/IAM user)
	// and missed ASIA (STS temporary), AGPA, AROA, AIDA, ANPA, AIPA, ANVA,
	// ABIA, ACCA. See AWS docs "IAM identifiers".
	{name: "aws_key", regex: regexp.MustCompile(`(AKIA|ASIA|AGPA|AROA|AIDA|ANPA|AIPA|ANVA|ABIA|ACCA)[A-Z0-9]{16}`), repl: "[AWS_KEY]"},
	// aws_secret: 40-char base64 token following an AWS-secret marker.
	//
	// F-13: The prior pattern required the marker pattern `aws_secret` (with
	// or without underscores/dashes) AND a single `:` or `=` AND optional
	// quotes — all on the same line. Real-world AWS credential dumps fail
	// that shape:
	//   - YAML / multi-line config:  `aws_secret_access_key:\n  XXX…40 chars`
	//   - JSON pretty-print:         `"SecretAccessKey": "XXX…40 chars"`
	//   - AWS CLI tabular output:    `secret_access_key  XXX…40 chars` (whitespace, no `:` or `=`)
	//   - `secretAccessKey` / `SecretAccessKey` camelCase without `aws_` prefix
	//
	// Fix: marker accepts the broader family (aws prefix optional, `access`
	// segment optional, separator-tolerant); marker→value gap allows up to
	// 80 chars of *any* non-base64 character (covers `:`, `=`, quotes,
	// whitespace, newlines, indentation, json `: "`); value still pinned
	// at exactly 40 chars of base64 with a non-base64 / end-of-string
	// boundary so longer secrets aren't truncated to a 40-char "match".
	{name: "aws_secret", regex: regexp.MustCompile(`(?i)((?:aws[_-]?)?secret(?:[_-]?access)?[_-]?key)([^A-Za-z0-9+/]{1,80})([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`), repl: "$1$2[AWS_SECRET]"},

	// Google API key (Maps, Cloud, etc.): AIza[35 chars].
	{name: "google_api_key", regex: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), repl: "[GOOGLE_API_KEY]"},

	// Slack: bot/user/admin/refresh tokens (xoxb-, xoxp-, xoxa-, xoxr-, xoxs-)
	// and app-level tokens (xapp-).
	{name: "slack_token", regex: regexp.MustCompile(`(xox[abprs]|xapp)-[A-Za-z0-9-]{10,}`), repl: "[SLACK_TOKEN]"},

	// Stripe: secret/test secret keys (sk_live_/sk_test_), tokens (tok_),
	// and restricted keys (rk_live_/rk_test_).
	{name: "stripe_key", regex: regexp.MustCompile(`sk_(live|test)_[A-Za-z0-9]{24,}`), repl: "[STRIPE_KEY]"},
	{name: "stripe_restricted", regex: regexp.MustCompile(`rk_(live|test)_[A-Za-z0-9]{24,}`), repl: "[STRIPE_RK]"},
	{name: "stripe_token", regex: regexp.MustCompile(`tok_[A-Za-z0-9]{24,}`), repl: "[STRIPE_TOKEN]"},

	// Discord bot tokens: <user-id-base64>.<timestamp>.<hmac>. Each segment
	// is base64url so [A-Za-z0-9_-] only.
	{name: "discord_token", regex: regexp.MustCompile(`M[TWN][A-Za-z0-9_-]{23,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{27,}`), repl: "[DISCORD_TOKEN]"},

	// Twilio API key SID (SK + 32 lowercase hex).
	{name: "twilio_key", regex: regexp.MustCompile(`SK[a-f0-9]{32}`), repl: "[TWILIO_KEY]"},

	// SendGrid: SG.<22>.<43> with literal dots.
	{name: "sendgrid_key", regex: regexp.MustCompile(`SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`), repl: "[SENDGRID_KEY]"},

	// Mailgun private API key: key-<32 hex>.
	{name: "mailgun_key", regex: regexp.MustCompile(`key-[a-f0-9]{32}`), repl: "[MAILGUN_KEY]"},

	// === Webhook URLs (carry implicit auth secrets) ===

	{name: "slack_webhook", regex: regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Z0-9]{8,}/B[A-Z0-9]{8,}/[A-Za-z0-9]{24,}`), repl: "[SLACK_WEBHOOK]"},
	{name: "discord_webhook", regex: regexp.MustCompile(`https://discord(?:app)?\.com/api/webhooks/[0-9]{17,}/[A-Za-z0-9_-]{60,}`), repl: "[DISCORD_WEBHOOK]"},

	// === Database connection strings with inline credentials ===
	// Captures user:password embedded in URI form. Replaces the credential
	// portion in-place while preserving scheme/host so the message remains
	// debuggable. Supported schemes: postgres(ql), mysql, mongodb(+srv),
	// redis(s), amqp(s). The TLS variants (rediss, amqps) share the same
	// userinfo shape and were called out as a coverage gap in the R14 diff
	// scan.
	{name: "db_url_creds", regex: regexp.MustCompile(`(?i)(postgres(?:ql)?|mysql|mongodb(?:\+srv)?|rediss?|amqps?)://[^:\s/]+:[^@\s]+@`), repl: "$1://[REDACTED]:[REDACTED]@"},

	// === Generic auth headers / inline assignments (broadest, run last) ===

	{name: "generic_secret", regex: regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|access[_-]?token|auth[_-]?token)\s*[:=]\s*['"]?([A-Za-z0-9_/+=.-]{20,})['"]?`), repl: "$1: [REDACTED]"},
	// F-12: char classes widened to cover the full base64 alphabet.
	// Prior `[A-Za-z0-9_.-]` matched only the URL-safe subset; RFC 6750
	// `b64token` allows `_.~+/` plus trailing `=` padding, and Basic auth
	// (RFC 7617) base64-encodes `user:pass` which routinely contains `+`,
	// `/`, and `=`. Without `+/=` the regex stopped at the first special
	// char and the rest of the credential leaked verbatim.
	{name: "bearer_token", regex: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_.+/~-]{20,}={0,2}`), repl: "Bearer [REDACTED]"},
	{name: "basic_auth", regex: regexp.MustCompile(`(?i)basic\s+[A-Za-z0-9_.+/~-]{10,}={0,2}`), repl: "Basic [REDACTED]"},

	// env_export is handled in Redact() via ReplaceAllStringFunc so the var
	// name can be matched against IsSensitiveKey() instead of a brittle regex.
	// A placeholder pattern is kept here only so persisted stats reference this name.
	{name: "env_export", regex: envExportLineRegex, repl: ""},

	// Private keys — match the entire PEM block (header + base64 body +
	// footer). The prior regex only matched the BEGIN line and left the key
	// material visible in the journal. (?s) so '.' spans newlines; non-greedy
	// so multiple blocks in one buffer don't merge.
	{name: "private_key", regex: regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`), repl: "[PRIVATE KEY]"},

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

// RedactEvent redacts sensitive data from event data. Handles string,
// nested map[string]any, []string, []any (the natural shape of
// JSON-unmarshaled arrays), and []map[string]any. Unknown types pass
// through unchanged — which is intentional, as the redactor is only
// responsible for textual data.
func (r *Redactor) RedactEvent(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}

	result := make(map[string]any, len(data))
	for k, v := range data {
		result[k] = r.redactValue(v)
	}
	return result
}

// redactValue dispatches on v's runtime type and recurses through composite
// types so a string buried inside data.tags[0] or data.items[*].message gets
// scrubbed the same as data.message.
func (r *Redactor) redactValue(v any) any {
	switch val := v.(type) {
	case string:
		return r.Redact(val)
	case map[string]any:
		return r.RedactEvent(val)
	case []string:
		out := make([]string, len(val))
		for i, s := range val {
			out[i] = r.Redact(s)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, x := range val {
			out[i] = r.redactValue(x)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(val))
		for i, m := range val {
			out[i] = r.RedactEvent(m)
		}
		return out
	default:
		return v
	}
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

// sensitiveTokens lists keywords that indicate sensitive content when they
// appear as a *whole token* (split on non-alphanumeric and camelCase
// boundaries). Word-boundary matching avoids false positives like `MONKEY`
// (contains "key" as substring) or `PATH` (contains "pat") that the prior
// pure-substring implementation flagged. Keep tokens lowercase; the matcher
// lowercases input before lookup.
//
// Notably absent: bare "key". Standalone "key" produces too many false
// positives (`PRIMARY_KEY`, `KEY_VALUE_STORE`, `cookie_keyset`); the
// sensitive forms (`api_key`, `private_key`, `apikey`, …) are caught via
// sensitiveCompounds below.
var sensitiveTokens = map[string]bool{
	"password":    true,
	"passwd":      true,
	"pwd":         true,
	"secret":      true,
	"token":       true,
	"auth":        true,
	"jwt":         true,
	"bearer":      true,
	"credential":  true,
	"credentials": true,
	"cred":        true,
	"creds":       true,
	"pat":         true, // Personal Access Token; bounded so `path` does not match
	"private":     true,
	"session":     true,
	"apikey":      true, // single-token form (`apikey`, `ApiKey` post-tokenize)
}

// sensitiveCompounds lists multi-word substrings whose presence anywhere in
// the lowercased key signals sensitive content. These cover compound forms
// that may not split into a sensitive token on their own (e.g., `apikey`
// combined into one chunk after lowercasing, or `oauth` which tokenizes to
// the meaningless `oauth` token without a useful component).
var sensitiveCompounds = []string{
	"apikey",
	"api_key",
	"access_token",
	"refresh_token",
	"session_token",
	"private_key",
	"secret_key",
	"access_key",
	"signing_key",
	"auth_key",
	"oauth",
}

// IsSensitiveKey returns true if the key name suggests sensitive data.
//
// Closes F-20: the previous pure-substring implementation produced false
// positives on `MONKEY`/`PRIMARY_KEY` (matched bare "key") and missed
// `cred`/`pat`/`pwd`. Current logic:
//   - Lowercased compound substring match (`api_key`, `oauth`, …).
//   - Word-boundary token match against `sensitiveTokens` after splitting on
//     non-alphanumeric runes AND camelCase transitions.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, c := range sensitiveCompounds {
		if strings.Contains(lower, c) {
			return true
		}
	}
	for _, t := range tokenizeKey(key) {
		if sensitiveTokens[t] {
			return true
		}
	}
	return false
}

// tokenizeKey splits a key name into lowercase tokens by non-alphanumeric
// boundaries AND camelCase transitions. Examples:
//
//	"password"      -> ["password"]
//	"MONKEY"        -> ["monkey"]            (no boundary inside an all-caps run)
//	"PRIMARY_KEY"   -> ["primary","key"]
//	"ApiKey"        -> ["api","key"]         (lower→upper boundary)
//	"OAuthToken"    -> ["o","auth","token"]  (UPPER→Upperlower boundary then lower→upper)
//	"id123name"     -> ["id","123","name"]   (letter↔digit boundary)
func tokenizeKey(s string) []string {
	var tokens []string
	var buf []rune
	flush := func() {
		if len(buf) > 0 {
			tokens = append(tokens, strings.ToLower(string(buf)))
			buf = buf[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 {
			prev := runes[i-1]
			switch {
			case unicode.IsLower(prev) && unicode.IsUpper(r):
				flush()
			case i+1 < len(runes) && unicode.IsUpper(prev) && unicode.IsUpper(r) && unicode.IsLower(runes[i+1]):
				flush()
			case (unicode.IsLetter(prev) && unicode.IsDigit(r)) || (unicode.IsDigit(prev) && unicode.IsLetter(r)):
				flush()
			}
		}
		buf = append(buf, r)
	}
	flush()
	return tokens
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
