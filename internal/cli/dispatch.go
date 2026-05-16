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

// ParseGlobals walks args once, extracting top-level dfmt flags
// (--json/-json and --project <value>) and applying them to the
// package-level globals (flagJSON / flagProject). It returns the
// cleaned arg slice with those tokens removed.
//
// Strict mode: an absent value after --project, or a value that
// itself looks like a flag (leading "-"), returns an error so that
// cmd/dfmt/main.go can exit 1 with a clear message instead of
// silently proceeding with a misparsed command line. Callers that
// prefer historical leniency (Dispatch, when invoked directly by
// tests) use the stripGlobalFlags wrapper below, which discards the
// error and returns the partial cleaned list.
//
// Per-subcommand flags pass through untouched. Single-pass; the
// caller pays one allocation for the cleaned slice.
func ParseGlobals(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json", "-json":
			flagJSON = true
		case "--project":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--project requires a value")
			}
			val := args[i+1]
			if len(val) > 0 && val[0] == '-' {
				return out, fmt.Errorf("invalid --project value: %q", val)
			}
			_ = os.Setenv("DFMT_PROJECT", val)
			flagProject = val
			i++
		default:
			out = append(out, args[i])
		}
	}
	return out, nil
}

// stripGlobalFlags is the lenient wrapper around ParseGlobals used by
// Dispatch — it discards the strict-mode error so test callers that
// invoke `Dispatch([]string{"status", "--project"})` (no value) keep
// the pre-consolidation behavior of silently dropping the malformed
// flag rather than surfacing it. cmd/dfmt/main.go uses ParseGlobals
// directly so the operator sees the error.
func stripGlobalFlags(args []string) []string {
	cleaned, _ := ParseGlobals(args)
	return cleaned
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

// flagJSON and flagProject hold the parsed global-flag state for the
// session. cmd/dfmt and Dispatch both reach this state through
// ParseGlobals; we avoid package-level flag.BoolVar because flag.Parse
// is never called in this binary — all subcommands use their own
// FlagSet, so a package-level flag would never be parsed.
var (
	flagJSON    bool
	flagProject string
)

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
