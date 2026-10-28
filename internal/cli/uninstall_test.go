package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ersinkoc/dfmt/internal/setup"
)

func TestUninstallRemovesProjectDataAndGlobalDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("DFMT_GLOBAL_DIR", filepath.Join(home, ".dfmt"))
	t.Setenv("DFMT_DISABLE_AUTOSTART", "1")

	proj := filepath.Join(home, "proj")
	for _, d := range []string{filepath.Join(proj, ".dfmt"), filepath.Join(home, ".dfmt")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	claudeMD := filepath.Join(proj, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &setup.Manifest{Version: 1, Files: []setup.FileEntry{{Path: claudeMD, Kind: setup.FileKindStrip}}}
	if err := setup.SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	if rc := runUninstall([]string{"--yes", "--keep-binary"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	for _, gone := range []string{filepath.Join(proj, ".dfmt"), filepath.Join(home, ".dfmt"), setup.ManifestPath()} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v)", gone, err)
		}
	}
	if b, _ := os.ReadFile(claudeMD); string(b) != "mine\n" {
		t.Errorf("user content changed: %q", b)
	}
}

func TestUninstallKeepDataLeavesDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("DFMT_GLOBAL_DIR", filepath.Join(home, ".dfmt"))
	t.Setenv("DFMT_DISABLE_AUTOSTART", "1")
	if err := os.MkdirAll(filepath.Join(home, ".dfmt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if rc := runUninstall([]string{"--yes", "--keep-data", "--keep-binary"}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if _, err := os.Stat(filepath.Join(home, ".dfmt")); err != nil {
		t.Errorf("--keep-data removed ~/.dfmt: %v", err)
	}
}

func TestUninstallWithoutYesRefusesNonInteractive(t *testing.T) {
	t.Setenv("DFMT_DISABLE_AUTOSTART", "1")
	if rc := runUninstall([]string{"--keep-binary"}); rc == 0 {
		t.Error("expected refusal without --yes on a non-terminal stdin")
	}
}
