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
		{"password", true},
		{"api_key", true},
		{"secret", true},
		{"token", true},
		{"private", true},
		{"username", false},
		{"email", false},
		{"name", false},
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
