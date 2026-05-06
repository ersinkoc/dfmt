package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveLegacyDaemonScaffoldingDeletesPerProjectArtifacts is the
// migration-helper unit test. The Phase 2 setup-refresh contract says
// per-project port/socket/pid/lock files get reaped so a stale legacy
// transport endpoint can no longer accept connections after the
// global daemon comes up. Project state (config, journal, index) is
// preserved.
func TestRemoveLegacyDaemonScaffoldingDeletesPerProjectArtifacts(t *testing.T) {
	tmp := t.TempDir()
	dot := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dot, 0o700); err != nil {
		t.Fatalf("mkdir .dfmt: %v", err)
	}

	// Seed legacy transport scaffolding plus a journal we expect to
	// SURVIVE (this is the user's data — the migration must not touch
	// it).
	for _, name := range []string{"port", "daemon.sock", "daemon.pid", "lock"} {
		if err := os.WriteFile(filepath.Join(dot, name), []byte("seed"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	journal := filepath.Join(dot, "journal.jsonl")
	if err := os.WriteFile(journal, []byte("event\n"), 0o600); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	removeLegacyDaemonScaffolding(tmp)

	for _, name := range []string{"port", "daemon.sock", "daemon.pid", "lock"} {
		if _, err := os.Stat(filepath.Join(dot, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, stat err = %v", name, err)
		}
	}
	if _, err := os.Stat(journal); err != nil {
		t.Errorf("journal should be preserved, got: %v", err)
	}
}

// TestRemoveLegacyDaemonScaffoldingTolerantOfMissingFiles guards
// against a daemon that crashed after partial cleanup — only some of
// the expected files exist on disk. The helper must not error out
// halfway and leave residue behind.
func TestRemoveLegacyDaemonScaffoldingTolerantOfMissingFiles(t *testing.T) {
	tmp := t.TempDir()
	dot := filepath.Join(tmp, ".dfmt")
	if err := os.MkdirAll(dot, 0o700); err != nil {
		t.Fatalf("mkdir .dfmt: %v", err)
	}
	// Only seed two of four — port and lock are absent.
	for _, name := range []string{"daemon.sock", "daemon.pid"} {
		if err := os.WriteFile(filepath.Join(dot, name), []byte("seed"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	removeLegacyDaemonScaffolding(tmp) // must not panic

	for _, name := range []string{"daemon.sock", "daemon.pid"} {
		if _, err := os.Stat(filepath.Join(dot, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, stat err = %v", name, err)
		}
	}
}

// TestIsProcessRunningDeadPID rejects a clearly-dead PID. The
// migration loop relies on this to avoid waiting 3 seconds per
// already-gone daemon.
func TestIsProcessRunningDeadPID(t *testing.T) {
	if isProcessRunning(0) {
		t.Error("PID 0 should not be considered running")
	}
	if isProcessRunning(-1) {
		t.Error("negative PID should not be considered running")
	}
}
