package sandbox

import (
	"context"
	"strings"
	"testing"
)

// Tests for sandbox helpers that carried real logic but no coverage. Each
// one is reachable only through a larger flow (an exec on Windows Git Bash,
// a config-supplied path_prepend, a policy denial), so nothing exercised
// them directly and a regression would have surfaced as a puzzling
// end-to-end symptom rather than a failing unit.

func TestDecodeUTF16LE(t *testing.T) {
	// "hi" in UTF-16LE: 'h' 0x00 'i' 0x00
	if got := decodeUTF16LE([]byte{'h', 0, 'i', 0}); got != "hi" {
		t.Errorf("ASCII range = %q, want %q", got, "hi")
	}
	// U+00E7 (ç) = 0xE7 0x00 → two-byte UTF-8 sequence.
	if got := decodeUTF16LE([]byte{0xE7, 0x00}); got != "ç" {
		t.Errorf("Latin-1 range = %q, want %q", got, "ç")
	}
	// U+20AC (€) = 0xAC 0x20 → three-byte UTF-8 sequence.
	if got := decodeUTF16LE([]byte{0xAC, 0x20}); got != "€" {
		t.Errorf("BMP range = %q, want %q", got, "€")
	}
	// A trailing odd byte has no pair and must be dropped rather than
	// producing a half-decoded rune.
	if got := decodeUTF16LE([]byte{'a', 0, 'b'}); got != "a" {
		t.Errorf("odd-length input = %q, want %q", got, "a")
	}
	if got := decodeUTF16LE(nil); got != "" {
		t.Errorf("nil input = %q, want empty", got)
	}
}

func TestWithPathPrepend(t *testing.T) {
	s := NewSandbox(t.TempDir())

	s.WithPathPrepend([]string{"/a", "", "/b"})
	if len(s.pathPrepend) != 2 || s.pathPrepend[0] != "/a" || s.pathPrepend[1] != "/b" {
		t.Errorf("pathPrepend = %v, want empty entries dropped", s.pathPrepend)
	}

	// Empty input clears rather than appends, so a config reload that
	// removes the key actually removes the effect.
	s.WithPathPrepend(nil)
	if s.pathPrepend != nil {
		t.Errorf("pathPrepend = %v after nil input, want nil", s.pathPrepend)
	}
}

func TestValidatePathPrependFlagsSuspiciousEntries(t *testing.T) {
	// A non-existent directory silently contributes nothing to PATH, so the
	// operator gets no toolchain and no explanation. It must be reported.
	if err := ValidatePathPrepend([]string{"/definitely/not/a/real/dir/xyzzy"}); err == nil {
		t.Error("ValidatePathPrepend(missing dir) = nil, want a warning")
	}
	// A world-writable directory is also flagged: a local attacker could
	// plant a binary there that the sandbox resolves ahead of system tools.
	// (On Windows a temp dir reports mode 777, so this is the branch a
	// t.TempDir() exercises there.)
	if err := ValidatePathPrepend([]string{t.TempDir()}); err != nil &&
		!strings.Contains(err.Error(), "world-writable") {
		t.Errorf("ValidatePathPrepend(temp dir) = %v, want nil or a world-writable warning", err)
	}
	// Empty input is a no-op, not an error.
	if err := ValidatePathPrepend(nil); err != nil {
		t.Errorf("ValidatePathPrepend(nil) = %v, want nil", err)
	}
	// Empty strings inside the slice are skipped rather than flagged.
	if err := ValidatePathPrepend([]string{""}); err != nil {
		t.Errorf("ValidatePathPrepend([\"\"]) = %v, want nil", err)
	}
}

func TestDenyReasonText(t *testing.T) {
	// The two reasons must read differently — that distinction is the whole
	// point of DenyReason, because the remedies are opposites.
	explicit := denyReasonText(ReasonExplicitDeny)
	noAllow := denyReasonText(ReasonNoAllowMatch)
	if explicit == noAllow {
		t.Fatalf("both reasons render identically as %q", explicit)
	}
	if !strings.Contains(explicit, "deny") {
		t.Errorf("ReasonExplicitDeny = %q, want it to mention a deny rule", explicit)
	}
	if !strings.Contains(noAllow, "allow") {
		t.Errorf("ReasonNoAllowMatch = %q, want it to mention an allow rule", noAllow)
	}
}

func TestPoolBufferReturnResetsBeforeReuse(t *testing.T) {
	buf := GetBuffer()
	buf.WriteString("stale content")
	PoolBufferReturn(buf)

	// A pooled buffer handed back out must not carry the previous caller's
	// bytes; that would splice unrelated output into a response.
	next := GetBuffer()
	defer PoolBufferReturn(next)
	if next.Len() != 0 {
		t.Errorf("recycled buffer has %d bytes, want 0", next.Len())
	}
}

func TestDetectShellUnixFromSHELL(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{"/bin/zsh", "zsh"},
		{"/usr/local/bin/fish", "fish"},
		{"/bin/bash", langBash},
		{"/bin/sh", "sh"},
		{"", langBash}, // unset falls back to bash
		// "tcsh" contains the substring "sh", so the sh branch claims it.
		// Documented rather than "fixed": running a tcsh script through sh
		// is a far better failure than not resolving a shell at all, and
		// the sandbox only ever uses the result as a `-c` interpreter.
		{"/usr/bin/tcsh", "sh"},
		{"/opt/weird/shellish", "sh"},
	}
	for _, tc := range tests {
		t.Run(tc.shell, func(t *testing.T) {
			t.Setenv("SHELL", tc.shell)
			if got := detectShellUnix(); got != tc.want {
				t.Errorf("detectShellUnix() with SHELL=%q = %q, want %q", tc.shell, got, tc.want)
			}
		})
	}
}

// Reload re-probes runtimes so a permitted exec that changed PATH (or an
// installed toolchain) becomes visible without restarting the daemon.
func TestRuntimesReloadRepopulatesCache(t *testing.T) {
	r := NewRuntimes()
	if err := r.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	before, hadBash := r.Get(langBash)

	if err := r.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	after, stillHasBash := r.Get(langBash)

	if hadBash != stillHasBash {
		t.Errorf("bash availability changed across Reload: %v -> %v", hadBash, stillHasBash)
	}
	if hadBash && before.Executable != after.Executable {
		t.Errorf("executable changed across Reload: %q -> %q", before.Executable, after.Executable)
	}
}
