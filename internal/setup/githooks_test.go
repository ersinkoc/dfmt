package setup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testDFMTHook = "#!/bin/sh\n" + GitHookMarker + "post-commit hook\nexit 0\n"

// A user's hook is moved aside, never overwritten, and comes back on
// uninstall; dfmt's own hook is replaced in place on re-install.
func TestGitHookInstallChainsAndRestores(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "post-commit")
	user := "#!/bin/sh\necho mine\n"
	if err := os.WriteFile(dst, []byte(user), 0o755); err != nil {
		t.Fatal(err)
	}

	action, err := InstallGitHook(dir, "post-commit", testDFMTHook)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(action, "pre-dfmt") {
		t.Errorf("action %q does not say the user's hook was moved", action)
	}
	if got, _ := os.ReadFile(dst + gitHookPrevSuffix); string(got) != user {
		t.Fatalf("user's hook not preserved verbatim at .pre-dfmt: %q", got)
	}

	if action, err := InstallGitHook(dir, "post-commit", testDFMTHook); err != nil || action != "updated" {
		t.Fatalf("re-install = %q, %v; want updated", action, err)
	}
	if got, _ := os.ReadFile(dst + gitHookPrevSuffix); string(got) != user {
		t.Fatal("re-install clobbered the preserved user hook")
	}

	if err := UninstallGitHook(dst); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != user {
		t.Errorf("uninstall did not restore the user's hook: %q", got)
	}
	if _, err := os.Stat(dst + gitHookPrevSuffix); !os.IsNotExist(err) {
		t.Error(".pre-dfmt left behind after restore")
	}
}

// A hook the user rewrote after install no longer carries the marker and
// must survive uninstall.
func TestGitHookUninstallLeavesForeignHook(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "pre-push")
	if err := os.WriteFile(dst, []byte("#!/bin/sh\necho theirs\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := UninstallGitHook(dst); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("foreign hook removed: %v", err)
	}
}

func gitInit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", append([]string{"init", "-q"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func TestGitHooksDirPlainRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	dir, err := GitHooksDir(repo)
	if err != nil {
		t.Fatalf("GitHooksDir: %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(repo, ".git", "hooks"))
	got, _ := filepath.EvalSymlinks(dir)
	if runtime.GOOS != "windows" && got != want {
		t.Errorf("hooks dir = %s, want %s", dir, want)
	}
}

// core.hooksPath (husky, lefthook): hooks git runs live elsewhere, usually
// tracked in the repo. Writing .git/hooks there reported success for hooks
// git never runs.
func TestGitHooksDirHooksPathIsManaged(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	cmd := exec.Command("git", "config", "core.hooksPath", ".husky")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	dir, err := GitHooksDir(repo)
	if !errors.Is(err, ErrHooksManaged) {
		t.Fatalf("err = %v, want ErrHooksManaged", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), ".husky") {
		t.Errorf("dir = %s, want the core.hooksPath directory", dir)
	}
}
