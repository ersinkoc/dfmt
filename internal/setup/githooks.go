package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git hook installation (CAP-7).
//
// `dfmt install-hooks` used to os.WriteFile `.git/hooks/<name>`
// unconditionally: a user's own post-commit (or husky's, or lefthook's)
// was overwritten with no backup, a worktree (where `.git` is a file)
// failed with ENOTDIR, a repo with core.hooksPath got hooks git never
// runs while dfmt reported success, and nothing was recorded for
// uninstall, so the hooks kept firing after dfmt was gone.

// GitHookMarker identifies a hook file dfmt wrote; every embedded hook's
// second line starts with it.
const GitHookMarker = "# DFMT git "

// gitHookPrevSuffix is where a user's hook is moved when dfmt takes its
// place. dfmt's hooks run `$0.pre-dfmt` first, so the user's hook keeps
// working, and uninstall moves it back.
const gitHookPrevSuffix = ".pre-dfmt"

// ErrHooksManaged reports that the repository routes hooks through
// core.hooksPath (husky, lefthook, a shared hooks dir). Those files are
// usually tracked in the repository or owned by a hook manager, so dfmt
// does not write into them.
var ErrHooksManaged = errors.New("hooks are managed through core.hooksPath")

// GitHooksDir returns the directory git runs hooks from for the repository
// at projectDir. It asks git, so worktrees and submodules (where `.git` is
// a file) resolve correctly. A core.hooksPath setting yields
// ErrHooksManaged together with that directory.
func GitHooksDir(projectDir string) (string, error) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = projectDir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	dir, err := run("rev-parse", "--git-path", "hooks")
	if err != nil {
		// No git on PATH, or not a repository: fall back to the plain
		// layout when it exists, rather than refusing outright.
		plain := filepath.Join(projectDir, ".git", "hooks")
		if st, serr := os.Stat(filepath.Join(projectDir, ".git")); serr == nil && st.IsDir() {
			return plain, nil
		}
		return "", fmt.Errorf("%s is not a git repository (or git is not on PATH)", projectDir)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectDir, dir)
	}
	if custom, _ := run("config", "--get", "core.hooksPath"); custom != "" {
		return dir, ErrHooksManaged
	}
	return dir, nil
}

// IsDFMTGitHook reports whether data is a hook dfmt wrote.
func IsDFMTGitHook(data []byte) bool {
	return bytes.Contains(data, []byte(GitHookMarker))
}

// InstallGitHook writes content as hook name in dir. An existing hook dfmt
// wrote is replaced; any other hook is moved to `<name>.pre-dfmt`, which
// the dfmt hook runs first. It returns a short description of what it did.
func InstallGitHook(dir, name, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	action := "installed"
	if existing, err := os.ReadFile(dst); err == nil {
		switch {
		case IsDFMTGitHook(existing):
			action = "updated"
		default:
			prev := dst + gitHookPrevSuffix
			if _, err := os.Stat(prev); err == nil {
				return "", fmt.Errorf("%s exists and so does %s; resolve by hand", dst, prev)
			}
			if err := os.Rename(dst, prev); err != nil {
				return "", fmt.Errorf("move existing hook aside: %w", err)
			}
			action = "installed; your existing hook moved to " + filepath.Base(prev) + " and runs first"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(dst, []byte(content), 0o755); err != nil {
		return "", err
	}
	return action, nil
}

// UninstallGitHook removes a hook dfmt wrote and moves back the hook it
// displaced. A hook without the dfmt marker — the user replaced it since —
// is left alone.
func UninstallGitHook(path string) error {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case !IsDFMTGitHook(data):
		return nil
	default:
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	prev := path + gitHookPrevSuffix
	if _, err := os.Stat(prev); err == nil {
		if _, err := os.Stat(path); err == nil {
			return nil // something else took the name; keep both
		}
		return os.Rename(prev, path)
	}
	return nil
}
