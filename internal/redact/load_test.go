package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectRedactor_NoOverride(t *testing.T) {
	tmp := t.TempDir()
	r, res, err := LoadProjectRedactor(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("redactor is nil")
	}
	if res.OverrideFound {
		t.Error("OverrideFound true on missing file")
	}
	if res.PatternsLoaded != 0 {
		t.Errorf("PatternsLoaded = %d, want 0", res.PatternsLoaded)
	}
	wantPath := filepath.Join(tmp, ".dfmt", "redact.yaml")
	if res.OverridePath != wantPath {
		t.Errorf("OverridePath = %q, want %q", res.OverridePath, wantPath)
	}
}

func TestLoadProjectRedactor_LoadsCustom(t *testing.T) {
	tmp := t.TempDir()
	dfmtDir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `patterns:
  - name: company-api
    pattern: 'CO-[A-Z0-9]{8}'
    replacement: '[CO-KEY]'
  - name: internal-id
    pattern: 'INT-\d{4,}'
`
	if err := os.WriteFile(filepath.Join(dfmtDir, "redact.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	r, res, err := LoadProjectRedactor(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OverrideFound {
		t.Fatal("expected OverrideFound=true")
	}
	if res.PatternsLoaded != 2 {
		t.Errorf("PatternsLoaded = %d, want 2; warnings=%v", res.PatternsLoaded, res.Warnings)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", res.Warnings)
	}

	// Apply both patterns. The custom replacement is honored.
	out := r.Redact("token CO-A1B2C3D4 leaks")
	if !strings.Contains(out, "[CO-KEY]") {
		t.Errorf("custom pattern not applied: %q", out)
	}
	// The default replacement covers entries that omit replacement.
	out2 := r.Redact("ticket INT-12345 next")
	if !strings.Contains(out2, "[REDACTED-INTERNAL-ID]") {
		t.Errorf("default replacement not applied: %q", out2)
	}
}

func TestLoadProjectRedactor_BadRegexWarnsAndSkips(t *testing.T) {
	tmp := t.TempDir()
	dfmtDir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `patterns:
  - name: good
    pattern: 'OK-\d+'
    replacement: '[OK]'
  - name: broken
    pattern: '[unterminated'
    replacement: '[X]'
`
	if err := os.WriteFile(filepath.Join(dfmtDir, "redact.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, res, err := LoadProjectRedactor(tmp)
	if err != nil {
		t.Fatalf("expected no top-level error, got %v", err)
	}
	if res.PatternsLoaded != 1 {
		t.Errorf("PatternsLoaded = %d, want 1 (good pattern only)", res.PatternsLoaded)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("expected 1 warning for broken regex, got %d: %v",
			len(res.Warnings), res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "broken") {
		t.Errorf("warning should mention bad pattern's name: %q", res.Warnings[0])
	}
}

func TestLoadProjectRedactor_MissingFieldWarnsAndSkips(t *testing.T) {
	tmp := t.TempDir()
	dfmtDir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `patterns:
  - name: ok
    pattern: 'X+'
  - name: missing-pattern
    replacement: '[Y]'
  - pattern: 'no-name'
    replacement: '[Z]'
`
	if err := os.WriteFile(filepath.Join(dfmtDir, "redact.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, res, err := LoadProjectRedactor(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.PatternsLoaded != 1 {
		t.Errorf("PatternsLoaded = %d, want 1", res.PatternsLoaded)
	}
	if len(res.Warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d: %v", len(res.Warnings), res.Warnings)
	}
}

func TestLoadProjectRedactor_MalformedYAMLErrors(t *testing.T) {
	tmp := t.TempDir()
	dfmtDir := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "patterns:\n  - name: x\n      pattern: y\n" // bad indent
	if err := os.WriteFile(filepath.Join(dfmtDir, "redact.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadProjectRedactor(tmp)
	if err == nil {
		t.Fatal("expected YAML parse error")
	}
}

func TestLoadProjectRedactor_EmptyProjectPath(t *testing.T) {
	r, res, err := LoadProjectRedactor("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("redactor must not be nil for empty path")
	}
	if res.OverrideFound || res.OverridePath != "" {
		t.Errorf("empty path should produce blank result, got %+v", res)
	}
}
