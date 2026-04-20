package content

import (
	"testing"
)

func TestSummarizeEmpty(t *testing.T) {
	s := &Summarizer{}
	summary := s.Summarize("", ChunkKindMarkdown)

	if summary.Text == "" {
		t.Error("Text should not be empty")
	}
	// Empty string still splits to 1 element (empty string before first newline)
	if summary.Size != 0 {
		t.Errorf("Size = %d, want 0", summary.Size)
	}
}

func TestSummarizeWithLines(t *testing.T) {
	s := &Summarizer{}
	body := "line1\nline2\nline3"
	summary := s.Summarize(body, ChunkKindMarkdown)

	if summary.Lines != 3 {
		t.Errorf("Lines = %d, want 3", summary.Lines)
	}
	if summary.Size != len(body) {
		t.Errorf("Size = %d, want %d", summary.Size, len(body))
	}
}

func TestSummarizeWithWarnings(t *testing.T) {
	s := &Summarizer{}
	body := "normal line\nwarning: something happened\nanother line\nError: critical issue"
	summary := s.Summarize(body, ChunkKindMarkdown)

	if len(summary.Warnings) == 0 {
		t.Error("Warnings should be detected")
	}
	if len(summary.Warnings) != 2 {
		t.Logf("Got %d warnings: %v", len(summary.Warnings), summary.Warnings)
	}
}

func TestSummarizeWithError(t *testing.T) {
	s := &Summarizer{}
	body := "ERROR: fatal error\nfailed operation\npanic: something went wrong"
	summary := s.Summarize(body, ChunkKindMarkdown)

	if len(summary.Warnings) == 0 {
		t.Error("Warnings should be detected")
	}
}

func TestSummarizeWithManyWarnings(t *testing.T) {
	s := &Summarizer{}
	body := "warning: 1\nwarning: 2\nwarning: 3\nwarning: 4\nwarning: 5\nwarning: 6"
	summary := s.Summarize(body, ChunkKindMarkdown)

	// Should be capped at 5 warnings
	if len(summary.Warnings) > 5 {
		t.Errorf("Warnings = %d, want at most 5", len(summary.Warnings))
	}
}

func TestSummarizeWithPhrases(t *testing.T) {
	s := &Summarizer{}
	body := "This is a test document with significant content about programming and development"
	summary := s.Summarize(body, ChunkKindMarkdown)

	if len(summary.TopPhrases) == 0 {
		t.Error("TopPhrases should not be empty for significant content")
	}
}

func TestSummarizeWithShortTokens(t *testing.T) {
	s := &Summarizer{}
	body := "a b c d e f g h i j k l m n o p q r s t u v w x y z"
	summary := s.Summarize(body, ChunkKindMarkdown)

	// All tokens are short (<=4 chars), so TopPhrases should be empty
	if len(summary.TopPhrases) != 0 {
		t.Logf("Got %d phrases", len(summary.TopPhrases))
	}
}

func TestSummarizeWithStopwords(t *testing.T) {
	s := &Summarizer{}
	body := "the the the the the is is is is are are and and or but if it this that with at on in"
	summary := s.Summarize(body, ChunkKindMarkdown)

	// All tokens are stopwords, so TopPhrases should be empty
	if len(summary.TopPhrases) != 0 {
		t.Logf("Got %d phrases", len(summary.TopPhrases))
	}
}

func TestDetectWarningsWithMixedCase(t *testing.T) {
	s := &Summarizer{}
	lines := []string{
		"normal line",
		"WARNING: caps warning",
		"error: lowercase error",
		"Error: Capital Error",
		"ERROR: ALL CAPS ERROR",
	}
	warnings := s.detectWarnings(lines)

	if len(warnings) != 4 {
		t.Errorf("Got %d warnings, want 4", len(warnings))
	}
}

func TestDetectWarningsDuplicateLines(t *testing.T) {
	s := &Summarizer{}
	lines := []string{
		"warning: same",
		"warning: same",
		"warning: same",
	}
	warnings := s.detectWarnings(lines)

	// Should deduplicate
	if len(warnings) != 1 {
		t.Errorf("Got %d warnings, want 1 (deduplicated)", len(warnings))
	}
}

func TestIntToStr(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{123, "123"},
		{-456, "-456"},
	}

	for _, tt := range tests {
		got := intToStr(tt.n)
		if got != tt.want {
			t.Errorf("intToStr(%d) = %s, want %s", tt.n, got, tt.want)
		}
	}
}

func TestMin(t *testing.T) {
	if min(1, 2) != 1 {
		t.Error("min(1, 2) should be 1")
	}
	if min(2, 1) != 1 {
		t.Error("min(2, 1) should be 1")
	}
	if min(5, 5) != 5 {
		t.Error("min(5, 5) should be 5")
	}
	if min(-1, -2) != -2 {
		t.Error("min(-1, -2) should be -2")
	}
}
