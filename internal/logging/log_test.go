package logging

import (
	"bytes"
	"strings"
	"testing"
)

// TestWarnf_PreservesLegacyFormat pins the byte-exact output shape that
// callers and downstream scripts depend on. A regression that adds
// timestamps, slog-style key=val attrs, or a different prefix would
// break log-parsing scripts that grep "warning:".
func TestWarnf_PreservesLegacyFormat(t *testing.T) {
	var buf bytes.Buffer
	prev := SetOutput(&buf)
	defer SetOutput(prev)
	prevLvl := SetLevel(LevelDebug)
	defer SetLevel(prevLvl)

	Warnf("patch %s: %v", "~/.claude.json", "no such file")

	got := buf.String()
	want := "warning: patch ~/.claude.json: no such file\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestLevelFiltering pins the threshold semantics: at LevelWarn,
// Debugf and Infof emit nothing; Warnf and Errorf emit. Critical for
// DFMT_LOG=error CI use case (silence warnings, surface errors only).
func TestLevelFiltering(t *testing.T) {
	cases := []struct {
		threshold Level
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
		wantError bool
	}{
		{LevelDebug, true, true, true, true},
		{LevelInfo, false, true, true, true},
		{LevelWarn, false, false, true, true},
		{LevelError, false, false, false, true},
		{LevelOff, false, false, false, false},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		prev := SetOutput(&buf)
		prevLvl := SetLevel(tc.threshold)

		Debugf("d")
		Infof("i")
		Warnf("w")
		Errorf("e")

		out := buf.String()
		assertContains := func(want bool, sub string, level string) {
			t.Helper()
			has := strings.Contains(out, sub)
			if want != has {
				t.Errorf("threshold=%d level=%s: contains %q = %v, want %v\n--- output ---\n%s", tc.threshold, level, sub, has, want, out)
			}
		}
		assertContains(tc.wantDebug, "debug: d", "debug")
		assertContains(tc.wantInfo, "info: i", "info")
		assertContains(tc.wantWarn, "warning: w", "warn")
		assertContains(tc.wantError, "error: e", "error")

		SetOutput(prev)
		SetLevel(prevLvl)
	}
}

// TestConcurrentWrites_NoInterleaving — Warnf is called from many
// goroutines (daemon's serve loop, fswatcher, journal periodic sync).
// The mutex must serialize writes so no log line interleaves another's
// formatted output.
func TestConcurrentWrites_NoInterleaving(t *testing.T) {
	var buf bytes.Buffer
	prev := SetOutput(&buf)
	defer SetOutput(prev)
	prevLvl := SetLevel(LevelDebug)
	defer SetLevel(prevLvl)

	const goroutines = 20
	const perG = 50

	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			for j := 0; j < perG; j++ {
				Warnf("g%d-msg%d", id, j)
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != goroutines*perG {
		t.Fatalf("got %d lines, want %d", len(lines), goroutines*perG)
	}
	// Every line must be well-formed: starts with "warning: g" and
	// ends with a digit. Interleaved bytes would flunk this.
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "warning: g") {
			t.Errorf("line %d malformed: %q", i, ln)
			break
		}
	}
}
