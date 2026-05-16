package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/osutil"
	"github.com/ersinkoc/dfmt/internal/project"
)

func runDashboard(args []string) int {
	var openBrowser bool
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.BoolVar(&openBrowser, "open", false, "Open the dashboard in the default browser")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	// Phase 2: if the host-wide global daemon is up, that's the only
	// dashboard URL — every project shares it. Short-circuit before
	// touching the registry (which only knows about legacy per-project
	// daemons). Only Windows reaches this branch today because Unix
	// global daemons bind to a Unix socket only; the platform hint
	// below still applies.
	if globalURL := globalDashboardURL(); globalURL != "" {
		fmt.Println(globalURL)
		if openBrowser {
			if err := openInBrowser(globalURL); err != nil {
				logging.Warnf("open browser: %v", err)
				return 1
			}
		}
		return 0
	}

	reg := client.GetRegistry()
	var entries []client.DaemonEntry
	if reg != nil {
		entries = reg.List()
	}

	// Resolve current project (best effort -- the dashboard command runs
	// fine outside any initialized project; we just won't have a primary
	// URL to print).
	curProject, projErr := getProject()

	// On Unix without TCP opt-in, the daemon serves HTTP over a Unix
	// socket which browsers can't dial. Emit the platform hint and bail
	// before claiming we have a useful URL. (Same logic as before; only
	// the trigger condition moved earlier in the flow.)
	if !osutil.IsWindows() && projErr == nil {
		cfg, _ := config.Load(curProject)
		tcpOptIn := cfg != nil && cfg.Transport.HTTP.Enabled && cfg.Transport.HTTP.Bind != ""
		if !tcpOptIn {
			fmt.Println("Dashboard not browser-accessible on this platform: the daemon")
			fmt.Println("serves HTTP over a Unix socket, which browsers cannot dial.")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  - `dfmt stats` for a CLI snapshot")
			fmt.Printf("  - curl --unix-socket %s http://x/dashboard\n", project.SocketPath(curProject))
			fmt.Println()
			fmt.Println("Or enable TCP loopback in .dfmt/config.yaml:")
			fmt.Println("  transport:")
			fmt.Println("    http:")
			fmt.Println("      enabled: true")
			fmt.Println("      bind: 127.0.0.1:8765")
			return 1
		}
	}

	// Mode 4: nothing running anywhere.
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No DFMT daemon is running.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Start one with:")
		fmt.Fprintln(os.Stderr, "  dfmt daemon                # in an already-initialized project")
		fmt.Fprintln(os.Stderr, "  dfmt quickstart            # init + agent setup if not yet done")
		return 1
	}

	// Build URL list. Entries with empty Port are Unix-socket-only --
	// drop them silently because we already printed the platform hint
	// above when we knew about them.
	type dashRow struct {
		project string
		url     string
	}
	rows := make([]dashRow, 0, len(entries))
	var primary string
	for _, e := range entries {
		if e.Port <= 0 {
			continue
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/dashboard", e.Port)
		rows = append(rows, dashRow{project: e.ProjectPath, url: url})
		if projErr == nil && samePathCLI(e.ProjectPath, curProject) {
			primary = url
		}
	}

	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "Daemons are running but none expose an HTTP port (Unix socket only).")
		fmt.Fprintln(os.Stderr, "Enable transport.http in .dfmt/config.yaml to make the dashboard browser-reachable.")
		return 1
	}

	// Mode 1: caller's project has a daemon -- print just that URL so
	// scripting (`dfmt dashboard --open`) keeps working as before.
	if primary != "" {
		fmt.Println(primary)
		if openBrowser {
			if err := openInBrowser(primary); err != nil {
				logging.Warnf("open browser: %v", err)
				return 1
			}
		}
		return 0
	}

	// Mode 2/3: no daemon for current project (or no current project).
	// Print every running daemon so the user can pick one. Each URL
	// already serves the cross-project switcher, so any of them lands
	// the user in the same dashboard UI.
	if projErr == nil {
		fmt.Fprintf(os.Stderr, "No daemon is running for %s.\n", curProject)
		fmt.Fprintln(os.Stderr, "Start one with `dfmt daemon`, or pick from these running daemons:")
	} else {
		fmt.Fprintln(os.Stderr, "Running daemons:")
	}
	fmt.Fprintln(os.Stderr)
	for _, r := range rows {
		fmt.Printf("  %s   %s\n", r.url, r.project)
	}
	if openBrowser {
		// --open with multiple candidates is ambiguous: don't pick for
		// the user. They can re-run with --project to disambiguate.
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "--open requires a single daemon; pass --project <path> to pick one.")
		return 1
	}
	return 0
}

// openInBrowser launches the OS-default browser pointed at url. Each
// platform has a different conventional helper; this uses the same
// helper that desktop environments dispatch to when the user clicks a
// link, so behavior matches the user's defaults (e.g., a default
// browser change is honored on the next call).
//
// Safety: url is constructed by runDashboard from a fixed scheme
// (http://127.0.0.1:<int>/dashboard) so there is no shell-injection
// surface — the int port can't introduce metacharacters. A future
// caller passing user input here would need to validate the scheme
// first.
func openInBrowser(url string) error {
	// Skip browser launches during tests — launching a browser in a test
	// environment causes unwanted pop-ups and test isolation violations.
	if flag.Lookup("test.v") != nil {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 is the documented protocol-handler entry point and
		// avoids `cmd /c start`'s window-title quoting quirk where the
		// first quoted argument becomes the window title rather than
		// the URL.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
