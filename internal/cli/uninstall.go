package cli

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ersinkoc/dfmt/internal/selfupdate"
	"github.com/ersinkoc/dfmt/internal/setup"
)

// runUninstall removes everything dfmt put on this host: agent configs
// (the `setup --uninstall` pass), each known project's .dfmt/ and
// instruction-file blocks, the host-wide ~/.dfmt directory, the setup
// manifest, and the binary itself. Journals are user data, so the
// destructive part asks first (--yes skips it; --keep-data and
// --keep-binary narrow it).
func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	var yes, keepData, keepBinary bool
	fs.BoolVar(&yes, "yes", false, "Do not ask for confirmation")
	fs.BoolVar(&keepData, "keep-data", false, "Keep project .dfmt/ directories and ~/.dfmt")
	fs.BoolVar(&keepBinary, "keep-binary", false, "Keep the dfmt executable")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	projects := knownProjectDirs()
	if !yes && !confirmUninstall(projects, keepData, keepBinary) {
		fmt.Fprintln(os.Stderr, "aborted (pass --yes to skip the prompt)")
		return 1
	}

	// Agent configs, hooks, manifest rows, and the daemon first: nothing
	// below may race a daemon that still holds the journals open.
	if rc := runSetupUninstall(); rc != 0 {
		return rc
	}

	if !keepData {
		for _, dir := range projects {
			if err := setup.RemoveProject(dir); err != nil {
				fmt.Fprintf(os.Stderr, "error removing project %s: %v\n", dir, err)
				continue
			}
			fmt.Printf("removed project data: %s\n", dir)
		}
		for _, p := range []string{globalDirNoCreate(), filepath.Dir(setup.ManifestPath())} {
			if p == "" {
				continue
			}
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(os.Stderr, "error removing %s: %v\n", p, err)
				continue
			}
			fmt.Printf("removed: %s\n", p)
		}
	}

	if !keepBinary {
		removeOwnBinary()
	}
	fmt.Println("DFMT fully uninstalled")
	return 0
}

// knownProjectDirs lists projects dfmt touched: those the running global
// daemon has loaded plus those the manifest recorded instruction files
// for. Collected before the daemon is stopped.
func knownProjectDirs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" || d == "<global>" || !filepath.IsAbs(d) || seen[d] {
			return
		}
		if home := setup.HomeDir(); d == home || d == filepath.Dir(d) {
			return
		}
		if st, err := os.Stat(filepath.Join(d, ".dfmt")); err != nil || !st.IsDir() {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	if info := readGlobalDaemonInfo(); info.Alive {
		for _, d := range loadedProjectsViaAPI(info.Port) {
			add(d)
		}
	}
	if m, err := setup.LoadManifest(); err == nil {
		for _, f := range m.Files {
			if f.Kind != setup.FileKindStrip {
				continue
			}
			root := filepath.Dir(f.Path)
			if filepath.Base(root) == ".github" {
				root = filepath.Dir(root)
			}
			add(root)
		}
	}
	return out
}

// globalDirNoCreate resolves ~/.dfmt without project.GlobalDir's
// create-if-missing side effect.
func globalDirNoCreate() string {
	if env := os.Getenv("DFMT_GLOBAL_DIR"); env != "" {
		return env
	}
	home := setup.HomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".dfmt")
}

func confirmUninstall(projects []string, keepData, keepBinary bool) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Println("This will remove DFMT from agent configs and stop the daemon.")
	if !keepData {
		fmt.Printf("It will also DELETE journals and indexes: %s and %d project .dfmt/ dir(s).\n",
			globalDirNoCreate(), len(projects))
		for _, p := range projects {
			fmt.Printf("  %s\n", p)
		}
	}
	if !keepBinary {
		fmt.Println("The dfmt executable will be deleted.")
	}
	fmt.Print("Continue? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

// removeOwnBinary deletes the running executable. Unix can unlink a
// running file; Windows cannot, so it is renamed to <exe>.old, which
// Dispatch's selfupdate.CleanupOld deletes on a later run (none will
// follow after uninstall, so the leftover is reported).
func removeOwnBinary() {
	if isTestBinary() {
		return
	}
	exe, err := selfupdate.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot locate the dfmt binary: %v\n", err)
		return
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		if err := os.Rename(exe, old); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot remove %s: %v\n", exe, err)
			return
		}
		fmt.Printf("renamed %s to %s; delete it by hand\n", exe, old)
		return
	}
	if err := os.Remove(exe); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot remove %s: %v\n", exe, err)
		return
	}
	fmt.Printf("removed binary: %s\n", exe)
}
