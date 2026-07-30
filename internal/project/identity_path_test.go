package project

import (
	"path/filepath"
	"testing"
)

// GlobalIdentityPath is the location the whole stale-daemon mechanism keys
// on: the daemon writes it at Start, Stop removes it, and every client reads
// it before deciding whether the running daemon is current. If the writer
// and the reader ever disagreed about where it lives, the daemon would look
// permanently stale and be restarted on every command.
func TestGlobalIdentityPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)

	want := filepath.Join(dir, GlobalIdentityFileName)
	if got := GlobalIdentityPath(); got != want {
		t.Errorf("GlobalIdentityPath() = %q, want %q", got, want)
	}
}

// It must sit in the same directory as the PID and lock files — the cleanup
// paths (cleanupStaleGlobalDaemon, stopGlobalDaemon) remove them as a set.
func TestGlobalIdentityPathSharesTheGlobalDir(t *testing.T) {
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())

	if got, want := filepath.Dir(GlobalIdentityPath()), filepath.Dir(GlobalPIDPath()); got != want {
		t.Errorf("identity dir = %q, pid dir = %q; cleanup removes them as a set", got, want)
	}
	if got, want := filepath.Dir(GlobalIdentityPath()), filepath.Dir(GlobalLockPath()); got != want {
		t.Errorf("identity dir = %q, lock dir = %q", got, want)
	}
}

// The filename is part of the on-disk contract an operator may inspect and
// that older/newer daemons must agree on.
func TestGlobalIdentityFileName(t *testing.T) {
	if GlobalIdentityFileName != "daemon.json" {
		t.Errorf("GlobalIdentityFileName = %q, want daemon.json", GlobalIdentityFileName)
	}
}
