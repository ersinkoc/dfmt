package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ersinkoc/dfmt/internal/osutil"
	"github.com/ersinkoc/dfmt/internal/project"
)

// Dispatch routes subcommands.
func Dispatch(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}

	cmd := args[0]
	remaining := stripGlobalFlags(args[1:])

	switch cmd {
	case "init":
		return runInit(remaining)
	case "remove", "teardown":
		return runRemove(remaining)
	case "quickstart":
		return runQuickstart(remaining)
	case "remember":
		return runRemember("remember", remaining)
	case "note":
		return runRemember("note", remaining)
	case "search":
		return runSearch(remaining)
	case "recall":
		return runRecall(remaining)
	case "status":
		return runStatus(remaining)
	case "daemon":
		return runDaemon(remaining)
	case "stop":
		return runStop(remaining)
	case "list":
		return runList(remaining)
	case "doctor":
		return runDoctor(remaining)
	case "task":
		return runTask(remaining)
	case "config":
		return runConfig(remaining)
	case "stats":
		return runStats(remaining)
	case "dashboard":
		return runDashboard(remaining)
	case "tail":
		return runTail(remaining)
	case "shell-init":
		return runShellInit(remaining)
	case "install-hooks":
		return runInstallHooks(remaining)
	case "capture":
		return runCapture(remaining)
	case "hook":
		return runHook(remaining)
	case "setup":
		return runSetup(remaining)
	case "exec":
		return runExec(remaining)
	case "read":
		return runRead(remaining)
	case "fetch":
		return runFetch(remaining)
	case "glob":
		return runGlob(remaining)
	case "grep":
		return runGrep(remaining)
	case "edit":
		return runEdit(remaining)
	case "write":
		return runWrite(remaining)
	case "mcp":
		return runMCP(remaining)
	case "help", "--help", "-h":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
}

func printUsage() {
	fmt.Println(`dfmt - Don't Fuck My Tokens

Usage:
  dfmt init                       Initialize a project
  dfmt quickstart                 Init + setup + verify in one shot
  dfmt remember [flags] <body>    Record an event (use --type for types like llm.response)
  dfmt note <body>               Record a note
  dfmt search <query>            Search events
  dfmt recall                    Build session snapshot
  dfmt status                    Check daemon status
  dfmt stop                      Stop daemon
  dfmt list                      List running daemons
  dfmt doctor                    Run diagnostics
  dfmt task <body>              Create a task
  dfmt task done <id>           Mark task done
  dfmt config get/set <key>     Get/set config
  dfmt stats                     Show session stats
  dfmt dashboard [--open]        Print/open the live web dashboard URL
  dfmt tail                      Stream events
  dfmt shell-init <shell>        Print shell integration
  dfmt install-hooks            Install git hooks
  dfmt capture git|shell ...    Capture event
  dfmt exec <code> [--lang L]   Run code in sandbox
  dfmt mcp                       Start MCP server (stdio)
  dfmt setup                    Configure agents
  dfmt setup --verify           Verify setup
  dfmt setup --uninstall        Remove configuration

Flags:
  --json    JSON output
  --project <path>    Project path (default: auto-detect)`)
}

// stripGlobalFlags removes top-level dfmt flags (--json/-json and
// --project <value>) from a subcommand arg slice. Production callers
// go through cmd/dfmt/main.go which already strips these, but tests
// (and external callers that import internal/cli) invoke Dispatch
// directly and rely on the historical behavior where subcommands
// silently ignored unknown args. Now that several subcommands enforce
// FlagSet parsing we strip them here so `Dispatch([]string{"status",
// "-json"})` keeps working without each FlagSet redeclaring -json.
//
// Per-subcommand flags (anything not in the global set) are passed
// through untouched. The function is allocation-free when no global
// flag is present.
func stripGlobalFlags(args []string) []string {
	for _, a := range args {
		if a == "--json" || a == "-json" || a == "--project" {
			goto strip
		}
	}
	return args
strip:
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json", "-json":
			flagJSON = true
		case "--project":
			if i+1 < len(args) {
				_ = os.Setenv("DFMT_PROJECT", args[i+1])
				flagProject = args[i+1]
				i++
			}
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// helpRequested reports whether the caller passed a help flag.
//
// Several subcommands historically treated their first positional arg
// as the operation's input without inspecting it — `dfmt task --help`
// recorded "--help" as the task subject; `dfmt install-hooks --help`
// installed hooks because runInstallHooks ignored args entirely. This
// helper lets the affected commands short-circuit before mutating
// state.
//
// Accepts the three conventional spellings; runs in O(len(args)) since
// the args list is always tiny.
func helpRequested(args []string) bool {
	for _, a := range args {
		switch a {
		case "--help", "-h", "-help", "help":
			return true
		}
	}
	return false
}

var (
	flagJSON    bool
	flagProject string
)

// SetGlobalJSON is called from cmd/dfmt once --json has been stripped off
// os.Args. We avoid package-level flag.BoolVar because flag.Parse is never
// called in this binary — all subcommands use their own flagset.
func SetGlobalJSON(v bool) { flagJSON = v }

// SetGlobalProject is called from cmd/dfmt once --project has been stripped
// off os.Args.
func SetGlobalProject(p string) { flagProject = p }

func getProject() (string, error) {
	if flagProject != "" {
		return flagProject, nil
	}
	// Honor DFMT_PROJECT so child processes launched via exec inherit the
	// parent's --project selection.
	if env := os.Getenv("DFMT_PROJECT"); env != "" {
		return env, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	proj, err := project.Discover(cwd)
	if err != nil {
		return "", fmt.Errorf("no project found: %w", err)
	}
	return proj, nil
}

// samePathCLI is a thin alias over osutil.SamePath kept for the
// readable call-site naming inside the cli package. Pre-osutil this
// was an inline duplicate of setup.samePath; now both delegate to a
// single implementation that lives outside both packages.
func samePathCLI(a, b string) bool { return osutil.SamePath(a, b) }

func mustMarshalJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}
