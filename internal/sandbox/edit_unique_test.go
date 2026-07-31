package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: Edit used strings.Replace(content, old, new, 1), so an
// old_string occurring more than once silently rewrote the first occurrence
// and reported Success: true. The anchors an agent picks — an error string, a
// field name, a repeated call — are exactly the text that recurs, so the
// wrong-site rewrite was both likely and invisible.

func editTestFile(t *testing.T, body string) (*SandboxImpl, string, string) {
	t.Helper()
	dir := t.TempDir()
	name := "target.go"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return NewSandbox(dir), dir, name
}

func readBack(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(data)
}

func TestEditRefusesAmbiguousOldString(t *testing.T) {
	const body = "count := 0\ncount := 0\ncount := 0\n"
	sb, dir, name := editTestFile(t, body)

	_, err := sb.Edit(context.Background(), EditReq{
		Path:      name,
		OldString: "count := 0",
		NewString: "count := 1",
	})
	if err == nil {
		t.Fatal("Edit succeeded on an old_string that appears 3 times; want a refusal")
	}
	if !strings.Contains(err.Error(), "3 times") {
		t.Errorf("error = %v, want it to state the occurrence count", err)
	}
	if !strings.Contains(err.Error(), "replace_all") {
		t.Errorf("error = %v, want it to name the way forward (replace_all)", err)
	}
	if got := readBack(t, dir, name); got != body {
		t.Errorf("file was modified by a refused edit:\n%q", got)
	}
}

func TestEditReplaceAllRewritesEveryOccurrence(t *testing.T) {
	sb, dir, name := editTestFile(t, "a := 1\nb := 1\nc := 1\n")

	resp, err := sb.Edit(context.Background(), EditReq{
		Path:       name,
		OldString:  ":= 1",
		NewString:  ":= 2",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("Edit with ReplaceAll: %v", err)
	}
	if resp.Replacements != 3 {
		t.Errorf("Replacements = %d, want 3", resp.Replacements)
	}
	if !strings.Contains(resp.Summary, "3 occurrences") {
		t.Errorf("Summary = %q, want the count so a broad sweep is visible", resp.Summary)
	}
	if got := readBack(t, dir, name); got != "a := 2\nb := 2\nc := 2\n" {
		t.Errorf("file = %q, want every occurrence rewritten", got)
	}
}

func TestEditUniqueMatchStillWorks(t *testing.T) {
	sb, dir, name := editTestFile(t, "alpha\nbeta\ngamma\n")

	resp, err := sb.Edit(context.Background(), EditReq{
		Path:      name,
		OldString: "beta",
		NewString: "BETA",
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if resp.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", resp.Replacements)
	}
	if got := readBack(t, dir, name); got != "alpha\nBETA\ngamma\n" {
		t.Errorf("file = %q", got)
	}
}

// TestEditRejectsEmptyOldString covers the direct-sandbox path: the transport
// validator refuses this, but strings.Count("", ...) semantics make it worth
// pinning here too — an empty anchor would otherwise insert at offset 0.
func TestEditRejectsEmptyOldString(t *testing.T) {
	sb, dir, name := editTestFile(t, "unchanged\n")

	if _, err := sb.Edit(context.Background(), EditReq{
		Path:      name,
		OldString: "",
		NewString: "injected",
	}); err == nil {
		t.Fatal("Edit accepted an empty old_string")
	}
	if got := readBack(t, dir, name); got != "unchanged\n" {
		t.Errorf("file = %q, want it untouched", got)
	}
}
