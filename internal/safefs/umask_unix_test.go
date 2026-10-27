//go:build !windows

package safefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileAtomicUmaskAppliesUmask(t *testing.T) {
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := WriteFileAtomicUmask(dir, p, []byte("x")); err != nil {
		t.Fatalf("WriteFileAtomicUmask: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %o, want 640 (0666 &^ 027)", got)
	}
	if data, _ := os.ReadFile(p); string(data) != "x" {
		t.Errorf("content = %q", data)
	}
}
