//go:build !windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWriteNewFileFollowsUmask: dfmt_write created files 0600 and parent
// directories 0700, so a script or asset the agent wrote was unreadable to
// the group, a web server or a container — unlike the native Write tool.
// New files now get 0666 &^ umask; an existing file keeps its mode.
func TestWriteNewFileFollowsUmask(t *testing.T) {
	// Tests run single-threaded here; set a known umask for the assertion.
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	dir := t.TempDir()
	sb := NewSandbox(dir)
	if _, err := sb.Write(context.Background(), WriteReq{Path: "sub/dir/new.sh", Content: "echo hi\n"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "sub/dir/new.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("new file mode = %o, want 644", got)
	}
	di, _ := os.Stat(filepath.Join(dir, "sub"))
	if got := di.Mode().Perm(); got != 0o755 {
		t.Errorf("new directory mode = %o, want 755", got)
	}

	// An existing restrictive file stays restrictive.
	secret := filepath.Join(dir, "secret.env")
	if err := os.WriteFile(secret, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Write(context.Background(), WriteReq{Path: "secret.env", Content: "A=2\n"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if fi, _ := os.Stat(secret); fi.Mode().Perm() != 0o600 {
		t.Errorf("existing file widened to %o", fi.Mode().Perm())
	}
	// No temp files left behind.
	if left, _ := filepath.Glob(filepath.Join(dir, "sub/dir/.safefs-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}
