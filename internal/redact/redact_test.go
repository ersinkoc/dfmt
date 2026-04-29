package redact

import (
	"regexp"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	r := NewRedactor()

	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{"GitHub token", "ghp_abc123xyz456def789ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV", "GITHUB_TOKEN"},
		{"OpenAI key", "sk-abcd1234efgh5678ijkl9012mnop3456qrst7890uvwx1234yzAB5678CD", "OPENAI_KEY"},
		{"AWS key", "AKIAIOSFODNN7EXAMPLE", "AWS_KEY"},
		{"Stripe key", "sk_live_Abc1Def2Ghi3Jkl4Mno5Pqr6Stu7VwX8Yz9", "STRIPE_KEY"},
		{"JWT token", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ.abc123", "JWT"},
		{"Private key", "-----BEGIN RSA PRIVATE KEY-----", "PRIVATE KEY"},
		{"Bearer token", "Bearer abc123xyz456def789xyz", "Bearer [REDACTED]"},
		{"Basic auth", "Basic abc123xyz456", "Basic [REDACTED]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.input)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("Redact(%q) = %q, want to contain %q", tt.input, got, tt.contains)
			}
		})
	}
}

// TestRedact_AWSSecretCrossLineAndCamelCase covers F-13. The prior pattern
// required marker and value on the same line and only matched marker
// variants prefixed with `aws_`. Real AWS credential dumps land in YAML,
// JSON, or tabular AWS CLI output where the marker and the 40-char base64
// value sit on separate lines — and `SecretAccessKey` / `secretAccessKey`
// camelCase forms have no `aws_` prefix. The widened pattern matches all
// these layouts; the 40-char value bound + non-base64 boundary keeps
// false positives bounded.
func TestRedact_AWSSecretCrossLineAndCamelCase(t *testing.T) {
	r := NewRedactor()

	// 40-char base64 stand-in. Mixing `+/=` makes sure the value class
	// regex isn't accidentally narrower than the AWS spec.
	const value = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" // exactly 40 chars

	if got := len(value); got != 40 {
		t.Fatalf("test fixture broken: value len = %d, want 40", got)
	}

	cases := []struct {
		name  string
		input string
	}{
		{"yaml-cross-line", "aws_secret_access_key:\n  " + value + "\n"},
		{"json-pretty-print", "{\n  \"SecretAccessKey\": \"" + value + "\"\n}"},
		{"cli-tabular", "secret_access_key   " + value + "\n"},
		{"camelCase-no-prefix", "secretAccessKey: " + value},
		{"PascalCase-no-prefix", "SecretAccessKey = " + value},
		{"yaml-deep-indent", "credentials:\n  aws:\n    secret_access_key:\n      " + value + "\n"},
		{"backtick-quoted", "secret_key=`" + value + "`"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Redact(c.input)
			if strings.Contains(got, value) {
				t.Errorf("AWS secret leaked: redact(%q) = %q", c.input, got)
			}
			// Either marker is acceptable — `[AWS_SECRET]` from the aws_secret
			// rule, or `[REDACTED]` from env_export when the value is in
			// `KEY=VALUE` shape and `IsSensitiveKey` claims it. What matters
			// is that the 40-char base64 didn't survive.
			if !strings.Contains(got, "[AWS_SECRET]") && !strings.Contains(got, "[REDACTED]") {
				t.Errorf("no redaction marker present: redact(%q) = %q", c.input, got)
			}
		})
	}
}

// TestRedact_AWSSecretFalsePositiveBounds documents the boundary behaviour:
// a 40-char base64 with NO marker nearby, or a 41-char base64, must NOT be
// classified as an AWS secret (avoids false positives on JWT segments,
// random hashes, etc.).
func TestRedact_AWSSecretFalsePositiveBounds(t *testing.T) {
	r := NewRedactor()
	cases := []struct {
		name  string
		input string
	}{
		// No marker at all — should not be flagged as aws_secret.
		{"bare-40char-base64", "abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		// 41 chars — value boundary should reject (still 40 chars at most).
		{"41-char-no-marker", "abcdefghijklmnopqrstuvwxyz0123456789ABCDE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Redact(c.input)
			if strings.Contains(got, "[AWS_SECRET]") {
				t.Errorf("false positive: redact(%q) = %q tagged AWS_SECRET", c.input, got)
			}
		})
	}
}

// TestRedact_BearerBasicCoversBase64SpecialChars covers F-12. The prior
// char class `[A-Za-z0-9_.-]` excluded `+`, `/`, and `=` — so a bearer or
// basic token containing those (which is routine for real base64 output)
// matched only up to the first special char, leaving the rest of the
// credential in the redacted output. The fix widens the class to the full
// base64 alphabet plus `~` (RFC 6750 b64token).
func TestRedact_BearerBasicCoversBase64SpecialChars(t *testing.T) {
	r := NewRedactor()

	cases := []struct {
		name     string
		input    string
		mustLack string // any substring of the original token leaking is a fail
	}{
		// `+` mid-token: prior regex truncated at `+`, leaking `DEF+ghiJKL...`.
		{"bearer-with-plus", "Authorization: Bearer abcDEF+ghiJKLmnoPQR456", "DEF+ghiJKL"},
		// `/` mid-token (URL-unsafe base64).
		{"bearer-with-slash", "Authorization: Bearer abcDEFghi/jklMNOpqr456", "/jklMNO"},
		// Trailing `=` padding.
		{"bearer-with-padding", "Authorization: Bearer abcDEFghiJKLmnoPQR456==", "PQR456=="},
		// Base64 of `user:supersecretpassword` includes `+`.
		{"basic-base64-plus", "Authorization: Basic dXNlcjpzdXBlcg+pass+more", "+pass+more"},
		// Basic with both `/` and `=`.
		{"basic-base64-slash-eq", "Authorization: Basic dXNlcjpwYXNz/ABCDEFG==", "/ABCDEFG=="},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Redact(c.input)
			if strings.Contains(got, c.mustLack) {
				t.Errorf("token tail leaked: redact(%q) = %q (must not contain %q)", c.input, got, c.mustLack)
			}
			// Sanity: the marker word ("Bearer" / "Basic") plus REDACTED
			// should still be present so observability isn't lost.
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("redacted output missing [REDACTED] marker: %q", got)
			}
		})
	}
}

func TestRedactEnvExport(t *testing.T) {
	r := NewRedactor()

	cases := []struct {
		name       string
		input      string
		mustHave   string
		mustLack   string
		mustKeep   string // content that should remain unchanged (e.g. the var name)
	}{
		{"export SECRET", "export SECRET=hunter2", "SECRET=[REDACTED]", "hunter2", "SECRET"},
		{"inline PASSWORD", "DB_PASSWORD=supersecret", "DB_PASSWORD=[REDACTED]", "supersecret", "DB_PASSWORD"},
		{"API_KEY", "MY_API_KEY=abc123def456", "MY_API_KEY=[REDACTED]", "abc123def456", "MY_API_KEY"},
		{"AUTH_TOKEN", "export GITHUB_AUTH_TOKEN=xyz", "GITHUB_AUTH_TOKEN=[REDACTED]", "xyz", "GITHUB_AUTH_TOKEN"},
		{"non-sensitive preserved", "HOME=/tmp", "HOME=/tmp", "", "HOME"},
		{"DEBUG unchanged", "DEBUG=true", "DEBUG=true", "", "DEBUG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Redact(tc.input)
			if !strings.Contains(got, tc.mustHave) {
				t.Errorf("input=%q got=%q, want to contain %q", tc.input, got, tc.mustHave)
			}
			if tc.mustLack != "" && strings.Contains(got, tc.mustLack) {
				t.Errorf("input=%q got=%q must NOT contain secret %q", tc.input, got, tc.mustLack)
			}
			if tc.mustKeep != "" && !strings.Contains(got, tc.mustKeep) {
				t.Errorf("input=%q got=%q must preserve var name %q", tc.input, got, tc.mustKeep)
			}
		})
	}
}

func TestRedactNoSecrets(t *testing.T) {
	r := NewRedactor()

	safe := []string{
		"hello world",
		"this is a normal log message",
		"user@example.com",
		"https://example.com/path",
	}

	for _, s := range safe {
		got := r.Redact(s)
		if got != s {
			t.Errorf("Redact(%q) = %q, should be unchanged", s, got)
		}
	}
}

func TestRedactEvent(t *testing.T) {
	r := NewRedactor()

	event := map[string]any{
		"message": "ghp_abc123xyz456def789ghijklmnopqrstuvwxyz should be hidden",
		"user":    "testuser",
		"count":   42,
	}

	got := r.RedactEvent(event)

	if msg, ok := got["message"].(string); ok {
		if !strings.Contains(msg, "[GITHUB_TOKEN]") {
			t.Errorf("RedactEvent message = %q, want to contain [GITHUB_TOKEN]", msg)
		}
	} else {
		t.Error("message field should be a string")
	}

	if user := got["user"].(string); user != "testuser" {
		t.Errorf("user field = %q, want unchanged", user)
	}

	if count := got["count"].(int); count != 42 {
		t.Errorf("count field = %d, want unchanged", count)
	}
}

func TestRedactMap(t *testing.T) {
	r := NewRedactor()

	input := map[string]string{
		"token": "sk_live_Abc1Def2Ghi3Jkl4Mno5Pqr6Stu7VwX8Yz9",
		"name":  "test",
	}

	got := r.RedactMap(input)

	if token := got["token"]; !strings.Contains(token, "[STRIPE_KEY]") {
		t.Errorf("token = %q, want [STRIPE_KEY]", token)
	}
	if name := got["name"]; name != "test" {
		t.Errorf("name = %q, want unchanged", name)
	}
}

func TestRedactWithStats(t *testing.T) {
	r := NewRedactor()

	input := "ghp_abc123xyz456def789ghijklmnopqrstuvwxyz github token and sk-abcd1234efgh5678ijkl9012mnop3456qrst7890uvwx1234yz openai key"
	got, stats := r.RedactWithStats(input)

	if len(got) >= len(input) {
		t.Error("redacted string should be shorter")
	}
	if stats.RedactedCount < 2 {
		t.Errorf("RedactedCount = %d, want at least 2", stats.RedactedCount)
	}
	if stats.OriginalSize != len(input) {
		t.Errorf("OriginalSize = %d, want %d", stats.OriginalSize, len(input))
	}
}

func TestIsSensitiveKey(t *testing.T) {
	tests := []struct {
		key       string
		sensitive bool
	}{
		// Existing positives — must keep matching.
		{"password", true},
		{"api_key", true},
		{"secret", true},
		{"token", true},
		{"private", true},
		// Existing negatives — must keep NOT matching.
		{"username", false},
		{"email", false},
		{"name", false},

		// F-20: previously false negatives — must now match.
		{"pwd", true},
		{"pwd_hash", true},
		{"cred", true},
		{"creds", true},
		{"cred_id", true},
		{"pat", true},      // Personal Access Token
		{"pat_token", true}, // both tokens hit
		{"oauth_token", true},
		{"OAuthToken", true},
		{"ApiKey", true},
		{"apikey", true},
		{"PrivateKey", true},
		{"AccessToken", true},
		{"sessionToken", true},
		{"refresh_token", true},
		{"signing_key", true},

		// F-20: previously false positives — must now NOT match.
		{"MONKEY", false},        // contained "key" as substring
		{"monkey", false},
		{"PRIMARY_KEY", false},   // DB primary key, not a secret
		{"key_value_store", false},
		{"keystore", false},
		{"path", false},          // contains "pat"
		{"PATH", false},
		{"mypath", false},
		{"keyboard", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsSensitiveKey(tt.key)
			if got != tt.sensitive {
				t.Errorf("IsSensitiveKey(%q) = %v, want %v", tt.key, got, tt.sensitive)
			}
		})
	}
}

// TestTokenizeKey covers the camelCase + boundary splitter that backs
// IsSensitiveKey. Pinning the splits explicitly so future changes don't
// silently regress F-20's false-positive elimination.
func TestTokenizeKey(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"password", []string{"password"}},
		{"MONKEY", []string{"monkey"}},
		{"PRIMARY_KEY", []string{"primary", "key"}},
		{"ApiKey", []string{"api", "key"}},
		{"OAuthToken", []string{"o", "auth", "token"}},
		{"HTTPRequest", []string{"http", "request"}},
		{"id123name", []string{"id", "123", "name"}},
		{"", nil},
		{"___", nil},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := tokenizeKey(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("tokenizeKey(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("tokenizeKey(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestAddPattern(t *testing.T) {
	r := NewRedactor()

	err := r.AddPattern("test", `test_pattern_\d+`, "[TEST]")
	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	got := r.Redact("test_pattern_12345")
	if !strings.Contains(got, "[TEST]") {
		t.Errorf("Redact with custom pattern = %q, want [TEST]", got)
	}
}

func TestAddPatternInvalidRegex(t *testing.T) {
	r := NewRedactor()

	err := r.AddPattern("test", "[invalid", "[TEST]")
	if err == nil {
		t.Error("AddPattern should fail for invalid regex")
	}
}

func TestNewRedactorWithCustom(t *testing.T) {
	customPatterns := []*redactPattern{
		{name: "custom", regex: regexp.MustCompile(`CUSTOM_\d+`), repl: "[CUSTOM]"},
	}
	r := NewRedactorWithCustom(customPatterns)

	// Should redact custom pattern
	got := r.Redact("CUSTOM_12345")
	if !strings.Contains(got, "[CUSTOM]") {
		t.Errorf("Redact with custom pattern = %q, want [CUSTOM]", got)
	}

	// Should still redact common patterns
	got2 := r.Redact("ghp_abc123xyz456def789ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV")
	if !strings.Contains(got2, "[GITHUB_TOKEN]") {
		t.Errorf("Redact should still redact common patterns, got %q", got2)
	}
}

func TestRedactEventNil(t *testing.T) {
	r := NewRedactor()

	got := r.RedactEvent(nil)
	if got != nil {
		t.Errorf("RedactEvent(nil) = %v, want nil", got)
	}
}

func TestRedactEventNested(t *testing.T) {
	r := NewRedactor()

	event := map[string]any{
		"outer": map[string]any{
			"inner": "ghp_abc123xyz456def789ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV",
		},
	}

	got := r.RedactEvent(event)
	outer, ok := got["outer"].(map[string]any)
	if !ok {
		t.Fatal("outer should be map[string]any")
	}
	inner, ok := outer["inner"].(string)
	if !ok {
		t.Fatal("inner should be string")
	}
	if !strings.Contains(inner, "[GITHUB_TOKEN]") {
		t.Errorf("nested redact failed, got %q", inner)
	}
}

func TestRedactEventArray(t *testing.T) {
	r := NewRedactor()

	event := map[string]any{
		"tokens": []string{
			"ghp_abc123xyz456def789ghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV",
			"normal",
		},
	}

	got := r.RedactEvent(event)
	tokens, ok := got["tokens"].([]string)
	if !ok {
		t.Fatal("tokens should be []string")
	}
	if !strings.Contains(tokens[0], "[GITHUB_TOKEN]") {
		t.Errorf("array redact failed for token, got %q", tokens[0])
	}
	if tokens[1] != "normal" {
		t.Errorf("array element should be unchanged, got %q", tokens[1])
	}
}

func TestRedactWithStatsMultiple(t *testing.T) {
	r := NewRedactor()

	input := "AKIAIOSFODNN7EXAMPLE aws key and another AKIAIOSFODNN7EXAMPLE"
	got, stats := r.RedactWithStats(input)

	if !strings.Contains(got, "[AWS_KEY]") {
		t.Errorf("redaction failed, got %q", got)
	}
	if stats.RedactedTypes["aws_key"] != 2 {
		t.Errorf("aws_key count = %d, want 2", stats.RedactedTypes["aws_key"])
	}
}

func TestRedactWithStatsNoRedaction(t *testing.T) {
	r := NewRedactor()

	input := "hello world"
	got, stats := r.RedactWithStats(input)

	if got != input {
		t.Errorf("unmodified string = %q, want %q", got, input)
	}
	if stats.RedactedCount != 0 {
		t.Errorf("RedactedCount = %d, want 0", stats.RedactedCount)
	}
}

// TestRedactExpandedProviders covers the patterns added when the prior audit
// flagged that several modern provider key formats reached the journal
// verbatim (Anthropic, AWS STS/role/group/user, GitHub fine-grained PAT,
// Slack, Google, Stripe restricted, Discord, Twilio, SendGrid, Mailgun,
// webhook URLs, DB connection strings).
//
// Each case is phrased as prose ("see <secret> in the log") rather than a
// `NAME=value` shell-export line so the env_export pattern doesn't overwrite
// the per-provider label with the generic [REDACTED]. (For NAME=value lines
// of sensitive variables, env_export still does the right thing — see
// TestRedactEnvExport.) The test asserts the original secret never survives;
// when a per-provider label IS expected, label is set and asserted too.
func TestRedactExpandedProviders(t *testing.T) {
	r := NewRedactor()

	cases := []struct {
		name   string
		input  string
		label  string // expected redaction label (empty = "any redaction is fine, just make sure secret is gone")
		secret string // substring of the original secret that must NOT survive
	}{
		{
			name:   "anthropic api03 in prose",
			input:  "the key sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_-AaBbCc was rotated",
			label:  "[ANTHROPIC_KEY]",
			secret: "AbCdEfGhIjKlMnOpQrSt",
		},
		{
			name:   "anthropic admin01 in prose",
			input:  "rotated sk-ant-admin01-XYZ_123-456_aaaaaaaaaaaaaaaaaaaaaaaaaaa today",
			label:  "[ANTHROPIC_KEY]",
			secret: "XYZ_123-456",
		},
		{
			name:   "openai project key in prose",
			input:  "got sk-proj-Aa1Bb2Cc3Dd4Ee5Ff6Gg7Hh8Ii9Jj0Kk1Ll2Mm3Nn4Oo5Pp6 from billing",
			label:  "[OPENAI_KEY]",
			secret: "Aa1Bb2Cc3Dd4",
		},
		{
			name:   "github fine-grained PAT in prose",
			input:  "ci uses github_pat_11ABCDEFG0AaBbCcDdEeFfGgHhIiJjKkLlMmNnOoPpQqRrSsTtUuVvWwXxYyZz0123456789aabbcc for releases",
			label:  "[GITHUB_PAT]",
			secret: "11ABCDEFG0",
		},
		{
			name:   "aws ASIA temp in prose",
			input:  "session creds ASIAIOSFODNN7EXAMPLE expire soon",
			label:  "[AWS_KEY]",
			secret: "ASIAIOSFODNN7EXAMPLE",
		},
		{
			name:   "aws AROA role in prose",
			input:  "the role id AROAJ2UCCR6FAKE12345 is assumed by the lambda",
			label:  "[AWS_KEY]",
			secret: "AROAJ2UCCR6FAKE12345",
		},
		{
			name:   "google api key in prose",
			input:  "see AIzaSyB-DUMMY1234567890abcdEFGHIJKLMNOpqrs in the maps client",
			label:  "[GOOGLE_API_KEY]",
			secret: "AIzaSyB-DUMMY",
		},
		{
			name:   "slack bot token in prose",
			input:  "header carries xoxb-1234567890-1234567890-AbCdEfGhIjKlMnOpQrStUvWx today",
			label:  "[SLACK_TOKEN]",
			secret: "1234567890-AbCd",
		},
		{
			name:   "slack app token in prose",
			input:  "uses xapp-1-A012ABCDE-1234567890-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa to socket-mode",
			label:  "[SLACK_TOKEN]",
			secret: "A012ABCDE",
		},
		{
			name:   "stripe restricted in prose",
			input:  "saw rk_live_AbCdEfGhIjKlMnOpQrStUvWxYz in the dashboard",
			label:  "[STRIPE_RK]",
			secret: "AbCdEfGhIjKlMnOpQrStUvWxYz",
		},
		{
			name:   "stripe test in prose",
			input:  "the test mode key sk_test_ABCDEFGHIJKLMNOPQRSTUVWX was rotated",
			label:  "[STRIPE_KEY]",
			secret: "ABCDEFGHIJKLMNOPQRSTUVWX",
		},
		{
			name:   "discord bot token in prose",
			// Real-shape Discord bot token: 24-char base64 user-id, dot, 6+ char timestamp, dot, 27+ char hmac.
			input:  "header Bot MTI3OTk2NjYzNDk1NDM5MTg4OQ.GxYzAB.aabbccddeeffgghhiijjkkllmm0123XX please",
			label:  "[DISCORD_TOKEN]",
			secret: "MTI3OTk2NjYzNDk1NDM5MTg4OQ",
		},
		{
			name:   "twilio key in prose",
			input:  "rotate twilio key SK0123456789abcdef0123456789abcdef in the integrations panel",
			label:  "[TWILIO_KEY]",
			secret: "SK0123456789abcdef0123456789abcdef",
		},
		{
			name:   "sendgrid key in prose",
			input:  "header carries SG.aaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb today",
			label:  "[SENDGRID_KEY]",
			secret: "SG.aaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		{
			name:   "mailgun key in prose",
			input:  "the mailgun key key-abcdef0123456789abcdef0123456789 was leaked",
			label:  "[MAILGUN_KEY]",
			secret: "key-abcdef0123456789abcdef0123456789",
		},
		{
			name:   "slack webhook in prose",
			input:  "post to https://hooks.slack.com/services/T01ABCDEFGH/B01ABCDEFGH/AbCdEfGhIjKlMnOpQrStUvWxYz for alerts",
			label:  "[SLACK_WEBHOOK]",
			secret: "T01ABCDEFGH/B01ABCDEFGH/AbCdEfGhIjKlMnOpQrStUvWxYz",
		},
		{
			name:   "discord webhook in prose",
			input:  "see https://discord.com/api/webhooks/123456789012345678/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa for details",
			label:  "[DISCORD_WEBHOOK]",
			secret: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			// db_url_creds: connection-string credentials should be replaced
			// with [REDACTED] in-place while preserving scheme/host. env_export
			// also catches the DATABASE_URL=… line as a whole, but we run
			// db_url_creds first so the per-credential redaction wins on at
			// least one of the two assertions (that the password is gone).
			name:   "postgres connection string in prose",
			input:  "the worker connects to postgres://app_user:s3cr3tP%40ssword@db.internal/prod on boot",
			label:  "", // env_export not triggered (no NAME=value), so db_url_creds emits its own form
			secret: "s3cr3tP%40ssword",
		},
		{
			name:   "mongodb+srv connection in prose",
			input:  "we point at mongodb+srv://reader:HiddenPass1@cluster0.mongodb.net/test for analytics",
			label:  "",
			secret: "HiddenPass1",
		},
		{
			name:   "redis with auth in prose",
			input:  "cache layer redis://default:topsecret@localhost:6379/0 is healthy",
			label:  "",
			secret: "topsecret@localhost",
		},
		{
			// rediss:// (TLS) — coverage added alongside amqps:// to close the
			// R14 diff-scan gap. The TLS variants share userinfo shape, so the
			// regex alternation is rediss? / amqps?.
			name:   "rediss tls connection in prose",
			input:  "tls cache rediss://user:tlspass1@redis.internal:6380/2 is healthy",
			label:  "",
			secret: "tlspass1@redis",
		},
		{
			// amqps:// (TLS variant of AMQP). The R14 phase A pattern only
			// listed amqp; adding amqps was flagged as DIFF-002 in the diff
			// scan and is closed here.
			name:   "amqps tls connection in prose",
			input:  "broker amqps://svc:rabbitSecret@rabbit.internal:5671/vhost is up",
			label:  "",
			secret: "rabbitSecret@rabbit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Redact(tc.input)
			if tc.label != "" && !strings.Contains(got, tc.label) {
				t.Errorf("Redact(%q) = %q, want to contain label %q", tc.input, got, tc.label)
			}
			if strings.Contains(got, tc.secret) {
				t.Errorf("Redact(%q) = %q, secret %q must not survive redaction", tc.input, got, tc.secret)
			}
		})
	}
}

// TestRedactExpandedNoFalsePositives ensures the new patterns do not flag
// strings that look superficially similar but aren't credentials. A pattern
// that scrubs every string containing "key-" or every URL is worse than no
// pattern at all.
func TestRedactExpandedNoFalsePositives(t *testing.T) {
	r := NewRedactor()

	safe := []string{
		"see the design doc at https://example.com/path/key-results-q3",
		"the function returns a sk- error code on failure",
		"AKIA is the prefix for AWS keys",       // bare prefix only, no 16-char body
		"my postgres://localhost is up",         // no credentials in URL
		"connect to mongodb+srv://cluster.foo/", // no credentials
		"SG.is.a.fine.abbreviation.for.sendgrid", // wrong segment lengths
	}

	for _, s := range safe {
		got := r.Redact(s)
		if got != s {
			t.Errorf("false positive: Redact(%q) = %q, expected unchanged", s, got)
		}
	}
}

// TestRedactPasswordField covers V-02. Pre-fix, the generic_secret pattern
// only matched *_key / *_token names and IsSensitiveKey was wired only into
// the env-export path, so JSON `"password": "hunter2"`, YAML `password: …`,
// and free-form `Database PASSWORD: secret` log lines all dripped into the
// journal verbatim. The new password_field pattern catches all three shapes.
func TestRedactPasswordField(t *testing.T) {
	r := NewRedactor()

	const secret = "hunter2"
	cases := []struct {
		name  string
		input string
	}{
		{"json double-quoted", `{"password": "hunter2"}`},
		{"json single-quoted value", `{"password": 'hunter2'}`},
		{"json caps", `{"PASSWORD": "hunter2"}`},
		{"yaml block", `password: hunter2`},
		{"yaml caps", `PASSWORD: hunter2`},
		{"yaml passwd alias", `passwd: hunter2`},
		{"yaml pwd alias", `pwd: hunter2`},
		{"yaml passphrase alias", `passphrase: hunter2`},
		{"log line", `connecting to db with password=hunter2`},
		{"log line with colon", `Database PASSWORD: hunter2 used`},
		{"camelcase suffix", `userPassword: hunter2`},
		{"snake_case prefix", `db_password: hunter2`},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.input)
			if strings.Contains(got, secret) {
				t.Errorf("password value leaked: Redact(%q) = %q", tt.input, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("redaction marker missing: Redact(%q) = %q", tt.input, got)
			}
		})
	}
}

// TestRedactPasswordField_NoFalsePositives makes sure the password pattern
// does not blast through prose or short placeholder values.
func TestRedactPasswordField_NoFalsePositives(t *testing.T) {
	r := NewRedactor()
	safe := []string{
		"please reset your password by visiting the link",
		`password: ""`,            // empty placeholder; below the 4-char floor
		`password: <placeholder>`, // angle-bracket marker; we want this redacted? — no, length<4 trips OK either way; document
		`# password is the kw, value follows on next line`,
	}
	for _, s := range safe {
		got := r.Redact(s)
		if strings.Contains(got, "[REDACTED]") {
			t.Errorf("false positive: Redact(%q) = %q", s, got)
		}
	}
}

// TestRedactAnthropicBeforeOpenAI confirms ordering: an Anthropic key is
// labelled [ANTHROPIC_KEY], not [OPENAI_KEY], even though both prefixes
// start with "sk-".
func TestRedactAnthropicBeforeOpenAI(t *testing.T) {
	r := NewRedactor()
	input := "ANTHROPIC=sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_-XyZqr0"
	got := r.Redact(input)
	if !strings.Contains(got, "[ANTHROPIC_KEY]") {
		t.Errorf("expected ANTHROPIC label, got %q", got)
	}
	if strings.Contains(got, "[OPENAI_KEY]") {
		t.Errorf("must NOT label Anthropic key as OpenAI: got %q", got)
	}
}

func TestStatsFields(t *testing.T) {
	stats := Stats{
		RedactedCount: 5,
		RedactedTypes: map[string]int{"test": 5},
		OriginalSize:  100,
		RedactedSize:  80,
	}

	if stats.RedactedCount != 5 {
		t.Errorf("RedactedCount = %d, want 5", stats.RedactedCount)
	}
	if stats.RedactedTypes["test"] != 5 {
		t.Errorf("RedactedTypes[test] = %d, want 5", stats.RedactedTypes["test"])
	}
}
