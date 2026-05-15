package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/redact"
	"github.com/ersinkoc/dfmt/internal/sandbox"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
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


func runStatus(args []string) int {
	// FlagSet up front so `dfmt status --help` prints usage rather than
	// silently running the diagnostic side-effects.
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: status takes no positional arguments\n")
		return 2
	}
	// v0.6.3: every command brings the daemon up if it isn't already.
	// Errors here are non-fatal — runStatus must still report the
	// "not running" state cleanly when spawning is disallowed (test
	// binary, DISABLE_AUTOSTART) or fails.
	_ = ensureGlobalDaemon()

	// `dfmt status` is useful even outside a DFMT-initialized project —
	// the host-wide global daemon may be serving other projects, and the
	// operator wants to know whether anything is up. Pre-Phase-2 the
	// command errored out with "no project found" before checking the
	// daemon, which made the global daemon invisible from any directory
	// the operator hadn't touched yet. Treat the missing-project case
	// as a degraded report: skip per-project fields, still show global
	// daemon liveness + dashboard URL + last crash.
	proj, projErr := getProject()
	running := false
	if projErr == nil {
		running = client.DaemonRunning(proj)
	} else if globalDashboardURL() != "" {
		running = true
	}

	// Dashboard URL is derived from the global daemon registry's Port
	// field. Empty (Unix-socket-bound daemons without TCP opt-in) means
	// no browser-reachable URL exists -- the dashboard is still served
	// over the socket but agents that aren't dfmt-aware can't dial it.
	// Listed only when a daemon for this project is alive.
	dashboardURL := ""
	if running {
		if projErr == nil {
			dashboardURL = lookupDashboardURL(proj)
		} else {
			// No project resolved, but the global daemon is reachable —
			// surface its URL directly so the operator can still navigate
			// to the dashboard.
			dashboardURL = globalDashboardURL()
		}
	}

	// last_crash is reported in JSON only when the file actually exists,
	// to keep the happy-path payload from carrying empty fields. Format:
	// { "path": "...", "modified": "<RFC3339>", "size": N }.
	var lastCrash map[string]any
	if fi, statErr := os.Stat(project.GlobalCrashPath()); statErr == nil {
		lastCrash = map[string]any{
			"path":     project.GlobalCrashPath(),
			"modified": fi.ModTime().UTC().Format(time.RFC3339),
			"size":     fi.Size(),
		}
	}

	// Surface the actual transport endpoint a client would dial. In
	// global daemon mode the per-project socket path is a stale
	// artifact (legacy v0.3.x lived there); the real listener is at
	// the host-wide path. Reporting the per-project path in global
	// mode misled operators debugging "why won't my client connect".
	socketPath := ""
	if projErr == nil {
		socketPath = project.SocketPath(proj)
	}
	if globalDashboardURL() != "" {
		if runtime.GOOS == "windows" {
			socketPath = project.GlobalPortPath()
		} else {
			socketPath = project.GlobalSocketPath()
		}
	}

	if flagJSON {
		// fmt %q produces Go-escaped strings, not JSON — low-byte control
		// characters and "\xHH" escapes in a Windows path would produce
		// invalid JSON. Encode via json.Marshal instead.
		payload := map[string]any{
			"project":        proj,
			"daemon_running": running,
			"socket":         socketPath,
			"dashboard":      dashboardURL,
		}
		if projErr != nil {
			payload["project_error"] = projErr.Error()
		}
		if lastCrash != nil {
			payload["last_crash"] = lastCrash
		}
		out, _ := json.Marshal(payload)
		fmt.Println(string(out))
	} else {
		if projErr == nil {
			fmt.Printf("Project: %s\n", proj)
		} else {
			fmt.Printf("Project: (none — %v)\n", projErr)
		}
		if running {
			fmt.Println("Daemon: running")
			if dashboardURL != "" {
				// Surfacing the dashboard URL on every `dfmt status` lets
				// the user reach the running dashboard without invoking
				// `dfmt dashboard` (which is now a no-spawn URL helper --
				// same information, just a hint nobody has to memorize).
				fmt.Printf("Dashboard: %s\n", dashboardURL)
			}
		} else {
			fmt.Println("Daemon: not running")
		}
		if lastCrash != nil {
			// Yellow-flag, not red — the daemon may have already restarted
			// fine; we just want operators to know there's a saved trace.
			fmt.Printf("Last crash: %s (cat %s)\n",
				lastCrash["modified"], lastCrash["path"])
		}
	}

	return 0
}

// lookupDashboardURL returns the http://127.0.0.1:<port>/dashboard URL
// for the running daemon. Phase 2: prefers the host-wide global daemon's
// port (every project shares the same dashboard URL), falls back to a
// legacy per-project daemon's registry entry only if no global daemon
// is up. Returns "" when no TCP-bound daemon exists — Unix-socket-only
// daemons have no browser-reachable URL even if they're alive.
func lookupDashboardURL(projectPath string) string {
	if u := globalDashboardURL(); u != "" {
		return u
	}

	// Step 2: legacy per-project daemon, looked up via the registry.
	reg := client.GetRegistry()
	if reg == nil {
		return ""
	}
	for _, e := range reg.List() {
		if !samePathCLI(e.ProjectPath, projectPath) {
			continue
		}
		if e.Port <= 0 {
			return ""
		}
		return fmt.Sprintf("http://127.0.0.1:%d/dashboard", e.Port)
	}
	return ""
}

// registerProjectWithGlobalDaemon fires one Stats RPC at the host-
// wide daemon, with the project_id stamped, so the daemon's
// Resources(projectID) loads this project into extraProjects. The
// dashboard's cross-project switcher reads /api/all-daemons which
// surfaces every cached project, so this ping is what makes an
// MCP-only project (one that no `dfmt <cmd>` ever touched) visible
// in the picker.
//
// Best-effort:
//   - Never spawns a daemon. Ping is gated on globalDashboardURL
//     returning non-empty (a dial-checked liveness signal).
//   - Never blocks MCP startup. Caller invokes via `go`.
//   - Errors are silently dropped — a failed registration only
//     means the dashboard won't show this project until something
//     else (CLI command, second MCP call) loads it. The MCP
//     subprocess's own journal/index keep working regardless.
//
// This is the v0.4.x interim fix for the deeper architectural issue
// (MCP subprocesses bypass the daemon entirely for journal writes).
// v0.5.0 will turn `dfmt mcp` into a thin proxy that forwards every
// tool call to the daemon over HTTP/socket, eliminating the
// duplicate journal handle and the registration dance both.
func registerProjectWithGlobalDaemon(projectPath string) {
	defer func() { _ = recover() }()
	if globalDashboardURL() == "" {
		return // no daemon up; skip
	}
	prev := os.Getenv("DFMT_DISABLE_AUTOSTART")
	_ = os.Setenv("DFMT_DISABLE_AUTOSTART", "1")
	defer func() {
		if prev == "" {
			_ = os.Unsetenv("DFMT_DISABLE_AUTOSTART")
		} else {
			_ = os.Setenv("DFMT_DISABLE_AUTOSTART", prev)
		}
	}()
	cl, err := client.NewClient(projectPath)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = cl.Stats(ctx, transport.StatsParams{NoCache: true})
}

// loadedProjectsViaAPI calls /api/all-daemons against the global
// daemon at the given port and returns the project paths it
// reports. Used by `dfmt list` to enumerate every project the
// daemon's resource cache currently holds. Returns nil on any
// error so the caller can fall back to a single "<global>" row.
func loadedProjectsViaAPI(port int) []string {
	if port <= 0 {
		return nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/all-daemons", port)
	httpClient := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var rows []map[string]any
	if jerr := json.Unmarshal(body, &rows); jerr != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if p, ok := r["project_path"].(string); ok && p != "" {
			out = append(out, p)
		}
	}
	return out
}

// readGlobalDaemonPID reads ~/.dfmt/daemon.pid and returns the
// daemon's PID, or 0 on any read / parse error. Best-effort —
// callers display the value to the operator but don't depend on it
// for liveness (the port file + fastDialOK already serve that role).
func readGlobalDaemonPID() int {
	data, err := os.ReadFile(project.GlobalPIDPath())
	if err != nil {
		return 0
	}
	var pid int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	return pid
}

// globalDashboardURL returns the http://127.0.0.1:<port>/dashboard
// URL of the host-wide global daemon, or "" if no global daemon is
// reachable. Used by `dfmt status` and `dfmt dashboard` so both
// surfaces agree on what the dashboard URL is at any moment.
func globalDashboardURL() string {
	port, _, err := readGlobalPortFile()
	if err != nil || port <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/dashboard", port)
}

// readGlobalPortFile reads the host-wide daemon's port + token from
// ~/.dfmt/port (or DFMT_GLOBAL_DIR override). Returns the same
// (port, token, error) shape as the per-project port reader so
// callers can swap which file they consult without touching parse logic.
func readGlobalPortFile() (int, string, error) {
	data, err := os.ReadFile(project.GlobalPortPath())
	if err != nil {
		return 0, "", err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return 0, "", errors.New("empty port file")
	}
	if trimmed[0] == '{' {
		var pf transport.PortFile
		if jerr := json.Unmarshal(trimmed, &pf); jerr != nil {
			return 0, "", fmt.Errorf("parse port file: %w", jerr)
		}
		return pf.Port, pf.Token, nil
	}
	port, err := strconv.Atoi(string(trimmed))
	if err != nil {
		return 0, "", fmt.Errorf("parse port file: %w", err)
	}
	return port, "", nil
}

// samePathCLI compares two paths case-insensitively on Windows, exactly
// elsewhere. Duplicated from setup.samePath to avoid importing the
// setup package into the dispatch hot path; the constant "windows" is
// inlined because the cli package has no goosWindows of its own.
func samePathCLI(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func runDaemon(args []string) int {
	// v0.5.0: `dfmt daemon` is exclusively the host-wide global daemon.
	// The `--legacy` flag from v0.4.x (per-project daemon bound to
	// --project) is gone; callers passing it get a clean error rather
	// than a silent fallback. `--global` is kept as an accepted alias
	// for v0.4.0-rc scripts that wrote it explicitly.
	var foreground bool
	var globalAlias bool
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.BoolVar(&foreground, "foreground", false, "Run in foreground")
	fs.BoolVar(&globalAlias, "global", true, "Run as host-wide global daemon (default; --global is a no-op kept for v0.4.0-rc compat)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	_ = globalAlias // accepted for back-compat; global is the only mode in v0.5.0.

	cfg, err := config.Load("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		return 1
	}
	if foreground {
		return runGlobalDaemonForeground(cfg)
	}
	// State-aware short-circuit. The pre-fix path keyed off
	// globalDashboardURL() alone — port file present meant "running",
	// absent meant "spawn". That misclassified two real-world states:
	//
	//   * a stuck pre-existing dfmt.exe that holds the OS lock but
	//     never finished writing the port file (we'd spawn a sibling
	//     that immediately dies on the lock, then print the dead PID
	//     as a success line),
	//   * an orphan listener whose pid file is missing (we'd spawn a
	//     sibling against a healthy daemon).
	//
	// inspectGlobalDaemon classifies all four states; reuse it so this
	// command and ensureGlobalDaemon agree on what "running" means.
	switch status, existingPID := inspectGlobalDaemon(); status {
	case globalDaemonRunning, globalDaemonOrphan:
		if globalURL := globalDashboardURL(); globalURL != "" {
			if existingPID > 0 {
				fmt.Printf("Global daemon already running (PID %d). Dashboard: %s\n", existingPID, globalURL)
			} else {
				fmt.Printf("Global daemon already running. Dashboard: %s\n", globalURL)
			}
		} else if existingPID > 0 {
			fmt.Printf("Global daemon already running (PID %d).\n", existingPID)
		} else {
			fmt.Println("Global daemon already running.")
		}
		return 0
	case globalDaemonStuck:
		fmt.Fprintf(os.Stderr,
			"Global daemon (PID %d) is alive but its listener is not responding.\n"+
				"Recovery: `dfmt stop`, or on Windows `taskkill /PID %d /F` / on Unix `kill %d`.\n"+
				"Then retry `dfmt daemon`.\n",
			existingPID, existingPID, existingPID)
		return 1
	case globalDaemonDead:
		cleanupStaleGlobalDaemon()
	}

	pid, err := startGlobalDaemonBackground()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting global daemon: %v\n", err)
		return 1
	}

	// Wait for the spawned child to actually bind its listener before
	// declaring success. Without this the command prints "started
	// (PID N)" milliseconds before the child dies (typical cause:
	// lock contention from a wedged dfmt.exe whose pid file we never
	// wrote, so inspectGlobalDaemon classified it as Dead), and the
	// next `dfmt dashboard` reports "no daemon running" — the exact
	// "şaka mı bu" complaint that prompted this fix.
	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		if client.DaemonRunning("") {
			if globalURL := globalDashboardURL(); globalURL != "" {
				fmt.Printf("Global daemon started (PID %d). Dashboard: %s\n", pid, globalURL)
			} else {
				fmt.Printf("Global daemon started (PID %d)\n", pid)
			}
			return 0
		}
		time.Sleep(daemonReadyPoll)
	}
	fmt.Fprintf(os.Stderr,
		"Spawned global daemon (PID %d) but it did not become ready within %v.\n"+
			"Likely causes: lock contention from a wedged dfmt.exe, port-bind failure, or permissions.\n"+
			"Run `dfmt doctor` for details; on Windows `tasklist | findstr dfmt` will show stuck processes.\n",
		pid, daemonReadyTimeout)
	return 1
}

// daemonReadyTimeout caps how long ensureGlobalDaemon waits for a
// freshly-spawned daemon child to bind its socket / TCP port. 4 s is
// generous on a warm host (cold start ~150–400 ms in practice) but
// short enough that a misconfigured environment surfaces as a clean
// error rather than a hang. The poll cadence (50 ms) keeps the typical
// case fast.
const (
	daemonReadyTimeout = 4 * time.Second
	daemonReadyPoll    = 50 * time.Millisecond
)

// globalDaemonStatus classifies the on-disk state of
// ~/.dfmt/{daemon.pid,port,daemon.sock,lock} against the actually-running
// process. ensureGlobalDaemon uses it to decide between connect /
// clean-and-spawn / surface-error rather than naively spawning whenever
// the dial happens to fail. The pre-fix behavior would let a stuck old
// daemon plus a fresh spawn coexist briefly while the old one finished
// its tail-end shutdown — the user-visible "two dfmt.exe in tasklist"
// case this enum exists to prevent.
type globalDaemonStatus int

const (
	// globalDaemonDead: no live PID and no responsive listener. Spawn a
	// new daemon after wiping any stale port/pid/lock files so the new
	// daemon's writes don't shadow leftover bytes.
	globalDaemonDead globalDaemonStatus = iota
	// globalDaemonRunning: PID is alive AND the listener accepts a fast
	// dial. Connect; do not spawn.
	globalDaemonRunning
	// globalDaemonStuck: PID is alive but the listener does not accept
	// a fast dial. Probably hung in shutdown / hot loop / OS-paged-out.
	// Refuse to spawn — the OS-level lock would reject the new daemon
	// anyway, and silently failing-then-timing-out looks like a generic
	// "daemon not ready" error to the operator. Surface a pointed
	// message so they can `dfmt stop` / taskkill the stale PID.
	globalDaemonStuck
	// globalDaemonOrphan: listener accepts but the PID file is missing
	// or points to a dead process. The daemon is up (just bookkeeping
	// drift) — connect and warn rather than refuse or spawn a sibling.
	globalDaemonOrphan
)

// inspectGlobalDaemon classifies the global daemon's on-disk state against
// the process table. See globalDaemonStatus for the semantics. The returned
// pid is whatever ~/.dfmt/daemon.pid claims (0 if missing/parse fail) — used
// for diagnostic messages, not for control flow.
func inspectGlobalDaemon() (globalDaemonStatus, int) {
	pid := readGlobalDaemonPID()
	listener := client.DaemonRunning("")
	pidAlive := pid > 0 && isProcessRunning(pid)
	switch {
	case listener && pidAlive:
		return globalDaemonRunning, pid
	case listener && !pidAlive:
		return globalDaemonOrphan, pid
	case !listener && pidAlive:
		return globalDaemonStuck, pid
	default:
		return globalDaemonDead, pid
	}
}

// cleanupStaleGlobalDaemon removes ~/.dfmt/{daemon.pid,port,daemon.sock,lock}
// after we've confirmed via inspectGlobalDaemon that no daemon is running and
// no PID is alive. Safe to call only on globalDaemonDead state — Windows has
// released the file handles by the time the process is gone, and Unix flock
// is process-scoped and self-releases on death. Best-effort: errors (file
// already gone, perms) are absorbed because the caller already knows the
// daemon is dead and the spawn-and-rebind that follows will surface any
// real permission problem with a clearer error than "remove failed".
func cleanupStaleGlobalDaemon() {
	dir := project.GlobalDir()
	for _, name := range []string{
		project.GlobalPIDFileName,
		project.GlobalPortFileName,
		project.GlobalSocketName,
		project.GlobalLockFileName,
	} {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// ensureGlobalDaemon makes sure a host-wide daemon is running and
// ready to accept connections. If one is already up it returns
// immediately; otherwise it spawns a detached child and polls until
// the daemon is reachable or the timeout fires.
//
// Test binaries short-circuit to a no-op: tests that need a daemon
// stand one up explicitly via fixtures, and we must never fork the
// test harness as a sibling process.
//
// Returns nil on success (daemon up). Errors are logged on stderr
// but also returned so callers in degraded mode (status, list) can
// proceed without a daemon.
//
// State-aware: before deciding to spawn, inspectGlobalDaemon classifies
// the on-disk + process-table state. A "stuck" daemon (PID alive,
// listener silent) gets a pointed error rather than a silent spawn-and-
// timeout — the OS lock would reject the spawn anyway, and the operator
// would just see "daemon did not become ready" without knowing which
// PID to kill. A "dead" daemon gets a stale-state cleanup pass before
// spawn so the new daemon's port-file write isn't shadowed by leftovers.
func ensureGlobalDaemon() error {
	switch status, pid := inspectGlobalDaemon(); status {
	case globalDaemonRunning:
		return nil
	case globalDaemonOrphan:
		// Listener answers — connect. Bookkeeping drift is the operator's
		// to fix at leisure; refusing the dial would block all dfmt
		// commands over a cosmetic file-state issue. The daemon will
		// rewrite ~/.dfmt/daemon.pid on its next Start anyway.
		logging.Warnf(
			"global daemon listener is up but ~/.dfmt/%s is missing or stale; continuing",
			project.GlobalPIDFileName)
		return nil
	case globalDaemonStuck:
		return fmt.Errorf(
			"global daemon (PID %d) is alive but its listener is not responding; "+
				"this usually means the process is hung; recovery on Windows: "+
				"`dfmt stop` or `taskkill /PID %d /F`; on Unix: `dfmt stop` or `kill %d`; "+
				"then retry the command", pid, pid, pid)
	case globalDaemonDead:
		cleanupStaleGlobalDaemon()
	}
	if isTestBinary() {
		// Tests must not spawn detached siblings — every test that
		// needs a daemon configures one in-process via fixtures.
		return errors.New("test binary: not spawning daemon")
	}
	if os.Getenv("DFMT_DISABLE_AUTOSTART") == "1" {
		return errors.New("DFMT_DISABLE_AUTOSTART=1: not spawning daemon")
	}
	if _, err := startGlobalDaemonBackground(); err != nil {
		return fmt.Errorf("spawn detached daemon: %w", err)
	}
	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		if client.DaemonRunning("") {
			return nil
		}
		time.Sleep(daemonReadyPoll)
	}
	return fmt.Errorf("daemon did not become ready within %v", daemonReadyTimeout)
}

// acquireBackend is the v0.6.3 single-binary entry point every
// short-lived daemon-touching subcommand calls before doing its real
// work. Resolution:
//
//  1. If a global daemon is already running, build a ClientBackend
//     wrapping client.NewClient and return it.
//  2. Otherwise, spawn a detached daemon child via
//     ensureGlobalDaemon and connect to it as a client.
//  3. In test binaries (no spawn allowed), fall back to in-process
//     promotion via daemon.PromoteInProcess. The returned *Daemon
//     is non-nil and the caller MUST stop it (or wait for shutdown)
//     before exiting — production code paths never see this branch.
//
// The pre-v0.6.3 behavior was to ALWAYS in-process-promote when no
// daemon was up, which forced the foreground command to block on
// idle-exit. v0.6.3 reverses that: short-lived commands now spawn-
// and-exit, leaving the daemon persistently in the background, which
// matches the user-visible contract "ne ile gelsem acsın, kill
// etmedikçe hep var olacak" (any command brings it up; it persists
// until killed).
//
// runMCP keeps the in-process self-promote path via
// acquireBackendForLongRunner — MCP is itself a long-running process
// so a separate daemon child would mean two dfmt.exe in tasklist
// rather than one.
//
// Failure modes return (nil, nil): the caller should report the
// stderr-logged error and exit. The *daemon.Daemon return is non-nil
// only in the test-binary fallback path.
func acquireBackend(projectPath string) (transport.Backend, *daemon.Daemon) {
	if !client.DaemonRunning(projectPath) {
		if err := ensureGlobalDaemon(); err != nil {
			// Test-binary or DISABLE_AUTOSTART: fall back to the legacy
			// in-process promote so unit tests that exercise this code
			// path still get a working backend.
			if isTestBinary() || os.Getenv("DFMT_DISABLE_AUTOSTART") == "1" {
				return acquireBackendForLongRunner(projectPath)
			}
			fmt.Fprintf(os.Stderr, "acquireBackend: %v\n", err)
			return nil, nil
		}
	}
	cl, cerr := client.NewClient(projectPath)
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "acquireBackend: connect failed: %v\n", cerr)
		return nil, nil
	}
	return client.NewBackend(cl), nil
}

// acquireBackendForLongRunner is the runMCP-and-tests variant of
// acquireBackend: when no daemon is running, it promotes the current
// process to daemon role via daemon.PromoteInProcess instead of
// spawning a detached child. The caller MUST keep the *Daemon alive
// (and eventually call waitForDaemonShutdown) so the daemon role
// outlives the original work.
//
// runMCP uses this because the MCP stdio loop is itself long-lived —
// a sibling daemon child would mean two dfmt.exe in tasklist for the
// duration of the agent session.
//
// Tests use this because they cannot fork sibling processes (the
// test binary is not the dfmt binary; re-execing it would re-run the
// suite).
func acquireBackendForLongRunner(projectPath string) (transport.Backend, *daemon.Daemon) {
	if client.DaemonRunning(projectPath) {
		cl, cerr := client.NewClient(projectPath)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "acquireBackend: connect failed: %v\n", cerr)
			return nil, nil
		}
		return client.NewBackend(cl), nil
	}

	d, perr := daemon.PromoteInProcess(context.Background(), config.Default())
	if perr != nil {
		var lerr *daemon.LockError
		if errors.As(perr, &lerr) {
			cl, cerr := client.NewClient(projectPath)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "acquireBackend: lock-race fallback: %v\n", cerr)
				return nil, nil
			}
			return client.NewBackend(cl), nil
		}
		fmt.Fprintf(os.Stderr, "acquireBackend: promote failed: %v\n", perr)
		return nil, nil
	}
	h := d.Handlers()
	return h, d
}

// waitForDaemonShutdown blocks until the daemon stops on its own
// (idle-exit timer) or the process receives SIGINT/SIGTERM. After
// shutdown signal, runs Stop within the configured grace window so
// listeners, project resources, and the lock are torn down cleanly.
//
// Used by runMCP after stdin EOF and by other long-lived subcommands
// after their primary work completes — both want the daemon role to
// keep serving inbound RPCs from other dfmt invocations on the same
// host as long as something is alive.
//
// In test binaries (`go test`) the wait is short-circuited and the
// daemon is stopped immediately. Without this, every test that
// exercises a runMCP / runStats / etc. code path would block on a
// 30-minute idle-exit timer.
func waitForDaemonShutdown(d *daemon.Daemon) {
	if isTestBinary() {
		stopCtx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
		defer cancel()
		_ = d.Stop(stopCtx)
		return
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case <-sigCh:
	case <-d.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
	defer cancel()
	_ = d.Stop(stopCtx)
}

// isTestBinary reports whether the current process is a Go test binary.
// Tests should never block on the daemon idle-exit timer — they exit
// the daemon immediately and rely on test-fixture teardown for the
// rest. The check mirrors client.isTestBinary's logic.
func isTestBinary() bool {
	if flag.Lookup("test.v") != nil {
		return true
	}
	base := strings.ToLower(filepath.Base(os.Args[0]))
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

// runGlobalDaemonForeground brings up the host-wide daemon attached to
// the current terminal. The lifecycle mirrors runDaemonForeground but
// uses daemon.NewGlobal so no per-project journal/index is loaded at
// startup — every RPC resolves its target via Daemon.Resources(pid).
func runGlobalDaemonForeground(cfg *config.Config) int {
	d, err := daemon.NewGlobal(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating global daemon: %v\n", err)
		return 1
	}

	ctx := context.Background()
	if err := d.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error starting global daemon: %v\n", err)
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down global daemon...")
	stopCtx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
	defer cancel()
	d.Stop(stopCtx)
	return 0
}

func runDaemonForeground(proj string, cfg *config.Config) int {
	d, err := daemon.New(proj, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating daemon: %v\n", err)
		return 1
	}

	ctx := context.Background()
	if err := d.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error starting daemon: %v\n", err)
		return 1
	}

	// Wait for interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down...")
	stopCtx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
	defer cancel()
	d.Stop(stopCtx)
	return 0
}

// startGlobalDaemonBackground spawns the host-wide daemon as a
// detached child. Lock acquisition (~/.dfmt/lock) enforces singleton
// semantics — a second invocation while one is already running fails
// at lock time, not here, and the user sees the LockError message.
func startGlobalDaemonBackground() (int, error) {
	if flag.Lookup("test.v") != nil {
		return 0, errors.New("refusing to spawn daemon from test binary")
	}

	exePath, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("find executable: %w", err)
	}

	exeBase := strings.ToLower(filepath.Base(exePath))
	if strings.HasSuffix(exeBase, ".test") || strings.HasSuffix(exeBase, ".test.exe") {
		return 0, fmt.Errorf("refusing to spawn daemon from test binary: %s", exePath)
	}

	cmd := exec.Command(exePath, "daemon", "--global", "--foreground")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Fully detach from the parent's console / process group so the
	// daemon survives after the foreground `dfmt <cmd>` returns.
	// Without this, on Windows the child inherits the parent's console
	// and on Unix it stays in the parent's process group — closing the
	// terminal that ran the spawn would kill the daemon.
	detachSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start global daemon: %w", err)
	}
	// Detach: do NOT call cmd.Wait or hold the *exec.Cmd reference.
	// Reaping is the OS's job once we've severed the parent/child
	// relationship via setsid/DETACHED_PROCESS. Releasing also frees
	// the underlying file descriptors so the parent can exit cleanly.
	_ = cmd.Process.Release()

	// PID file lives under ~/.dfmt/, written by the daemon itself once it
	// successfully acquires the lock. We don't write it from here because
	// the child may fail (e.g. lock contention) and we don't want a stale
	// PID pointing at a process that exited within milliseconds.
	return cmd.Process.Pid, nil
}

func startDaemonBackground(proj string) (int, error) {
	if client.DaemonRunning(proj) {
		return 0, errors.New("daemon already running")
	}

	// Refuse to re-exec a test binary as the daemon. Under `go test` the
	// test framework would ignore the extra args and re-run the suite,
	// causing an exponential fork bomb.
	if flag.Lookup("test.v") != nil {
		return 0, errors.New("refusing to spawn daemon from test binary")
	}

	exePath, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("find executable: %w", err)
	}

	exeBase := strings.ToLower(filepath.Base(exePath))
	if strings.HasSuffix(exeBase, ".test") || strings.HasSuffix(exeBase, ".test.exe") {
		return 0, fmt.Errorf("refusing to spawn daemon from test binary: %s", exePath)
	}

	// Same project-on-cmdline rationale as client.startDaemon: --project
	// is a global flag (handled by cmd/dfmt/main.go before dispatch) so
	// task managers and `tasklist`/`ps` can identify which daemon is
	// attached to which project without needing `dfmt list`.
	cmd := exec.Command(exePath, "--project", proj, "daemon", "--foreground")
	cmd.Dir = proj
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start daemon: %w", err)
	}
	// Reap the child when it exits so we don't leak process handles.
	// recover guards against future refactors where Wait could be replaced
	// by something panicking; today cmd.Wait itself does not panic.
	go func() {
		defer func() { _ = recover() }()
		_ = cmd.Wait()
	}()

	pidPath := filepath.Join(proj, ".dfmt", "daemon.pid")
	pidData := fmt.Sprintf("%d\n", cmd.Process.Pid)
	_ = os.WriteFile(pidPath, []byte(pidData), 0o600)

	return cmd.Process.Pid, nil
}

func runStop(args []string) int {
	// FlagSet up front so `dfmt stop --help` prints usage rather than
	// silently issuing kill signals.
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: stop takes no positional arguments\n")
		return 2
	}
	// Phase 2: prefer the host-wide global daemon when one is up.
	// `dfmt stop` previously read only the per-project PID file; in
	// global mode that file lives at ~/.dfmt/daemon.pid, so the
	// command always saw pid=0 and falsely reported success without
	// touching the running process.
	if globalURL := globalDashboardURL(); globalURL != "" {
		return stopGlobalDaemon()
	}

	// Legacy per-project daemon path (v0.3.x straddle setups).
	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if !client.DaemonRunning(proj) {
		fmt.Println("Daemon not running")
		return 0
	}

	pidPath := filepath.Join(proj, ".dfmt", "daemon.pid")
	pid := readPIDFile(pidPath)
	if pid > 0 {
		signalStopProcess(pid, false)
		fmt.Printf("Sent stop signal to PID %d\n", pid)
		waitForExit(pid, 5*time.Second, proj)
	}

	if pid > 0 && client.DaemonRunning(proj) {
		fmt.Printf("Daemon still running after graceful stop; escalating to forced kill\n")
		signalStopProcess(pid, true)
		waitForExit(pid, 3*time.Second, proj)
		if client.DaemonRunning(proj) {
			fmt.Fprintf(os.Stderr,
				"error: daemon (PID %d) refused to stop. State files left intact.\n"+
					"Manual recovery: kill the process and re-run `dfmt stop`.\n", pid)
			return 1
		}
	}

	_ = os.Remove(pidPath)
	_ = os.Remove(filepath.Join(proj, ".dfmt", "lock"))
	_ = os.Remove(project.SocketPath(proj))

	fmt.Printf("Daemon stopped for %s\n", proj)
	return 0
}

// stopGlobalDaemon shuts the host-wide daemon down. SIGINT (taskkill
// /T on Windows) first, escalate to SIGKILL/taskkill /F if it doesn't
// exit within the graceful window. State files at ~/.dfmt/{port,
// daemon.pid, lock} are removed only after the process actually dies
// — leaving them in place around a hung daemon would let the next
// `dfmt daemon` spawn a second listener on a fresh port while the
// original kept holding the lock.
func stopGlobalDaemon() int {
	pid := readGlobalDaemonPID()
	if pid <= 0 {
		// PID file gone but globalDashboardURL was non-empty above —
		// daemon is up but didn't write its PID, or someone deleted it.
		// We don't have a reliable way to find the process from here,
		// so surface the inconsistency rather than pretending to stop it.
		fmt.Fprintln(os.Stderr,
			"error: global daemon is reachable but ~/.dfmt/daemon.pid is missing.\n"+
				"Manual recovery: locate the dfmt process via `tasklist`/`ps`, kill it,\n"+
				"and remove ~/.dfmt/{port,lock,daemon.pid}.")
		return 1
	}

	signalStopProcess(pid, false)
	fmt.Printf("Sent stop signal to global daemon (PID %d)\n", pid)
	waitForGlobalExit(pid, 5*time.Second)

	if isProcessRunning(pid) {
		fmt.Printf("Global daemon still running after graceful stop; escalating to forced kill\n")
		signalStopProcess(pid, true)
		waitForGlobalExit(pid, 3*time.Second)
		if isProcessRunning(pid) {
			fmt.Fprintf(os.Stderr,
				"error: global daemon (PID %d) refused to stop. State files left intact.\n"+
					"Manual recovery: kill the process and re-run `dfmt stop`.\n", pid)
			return 1
		}
	}

	_ = os.Remove(project.GlobalPIDPath())
	_ = os.Remove(project.GlobalLockPath())
	_ = os.Remove(project.GlobalPortPath())
	_ = os.Remove(project.GlobalSocketPath())

	fmt.Printf("Global daemon stopped (was PID %d)\n", pid)
	return 0
}

// readPIDFile parses an integer PID from a daemon.pid file. Returns
// 0 on any error so callers can treat "no PID" and "bad PID" the same
// way. Used by both legacy per-project and global stop paths.
func readPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var pid int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	return pid
}

// waitForExit polls client.DaemonRunning(proj) until the daemon goes
// away or the deadline fires. Used by the legacy per-project stop
// path; the global stop path uses waitForGlobalExit which polls the
// PID directly.
func waitForExit(pid int, timeout time.Duration, proj string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !client.DaemonRunning(proj) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = pid
}

// waitForGlobalExit polls the daemon's PID until it disappears from
// the process table or timeout fires. We can't use
// client.DaemonRunning here because that would re-spawn a fresh
// daemon on the auto-spawn path of NewClient.
func waitForGlobalExit(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isProcessRunning(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// signalStopProcess asks the OS to terminate pid. Implemented per-platform
// because os.Process.Signal(os.Interrupt) is a no-op on Windows.
//
// When force is true we escalate: SIGKILL on Unix, taskkill /F on Windows.
// Without force, we ask politely (SIGINT / taskkill without /F) so the
// daemon can run its Stop() handler — flushing the journal, persisting the
// index, releasing the lock cleanly. Force is only invoked after a graceful
// attempt has timed out.
func signalStopProcess(pid int, force bool) {
	if runtime.GOOS == "windows" {
		args := []string{"/PID", fmt.Sprintf("%d", pid), "/T"}
		if force {
			args = append(args, "/F")
		}
		_ = exec.Command("taskkill", args...).Run()
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// os.Kill is the cross-platform alias for SIGKILL on Unix; on Windows
	// the os.Signal interface is satisfied but Process.Signal returns an
	// error (taskkill /F path above is what actually kills on Windows).
	// Using os.Kill instead of syscall.SIGKILL keeps this file buildable
	// on Windows where syscall.SIGKILL is not defined.
	sig := os.Interrupt // os.Signal-typed; widening on assignment below works
	if force {
		sig = os.Kill
	}
	_ = process.Signal(sig)
}

func runList(args []string) int {
	// FlagSet up front so `dfmt list --help` prints usage rather than
	// silently spawning the daemon and listing rows.
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: list takes no positional arguments\n")
		return 2
	}
	// v0.6.3: bring the daemon up before listing — without this, the
	// first `dfmt list` after a fresh login showed "No running daemons"
	// even though the user explicitly invoked dfmt to find out.
	_ = ensureGlobalDaemon()

	daemons := client.GetRegistry().List()

	// Phase 2: synthesize a registry-shaped row for the host-wide
	// global daemon when one is up. The global daemon doesn't write
	// to ~/.dfmt/daemons.json (Mode field forward-compat is deferred
	// to v0.5.0), so without this `dfmt list` reports "No running
	// daemons" while the daemon is plainly alive and serving every
	// project. We pull project paths from the dashboard URL helper
	// and the global PID file; ProjectPath is set to "<global>" to
	// distinguish from per-project legacy rows.
	if globalURL := globalDashboardURL(); globalURL != "" {
		gpid := readGlobalDaemonPID()
		port := 0
		if p, _, err := readGlobalPortFile(); err == nil {
			port = p
		}
		// One synthetic row per loaded project, all sharing the
		// daemon's PID/port. Operators reading `dfmt list` see
		// every project the global daemon has cached, mirroring
		// the dashboard switcher.
		loaded := loadedProjectsViaAPI(port)
		if len(loaded) == 0 {
			loaded = []string{"<global>"}
		}
		for _, p := range loaded {
			daemons = append(daemons, client.DaemonEntry{
				ProjectPath: p,
				PID:         gpid,
				Port:        port,
				StartedAt:   time.Now(),
				LastSeen:    time.Now(),
			})
		}
	}

	if len(daemons) == 0 {
		if flagJSON {
			fmt.Println("[]")
		} else {
			fmt.Println("No running daemons")
			fmt.Println("\nStart a daemon with: dfmt daemon")
			fmt.Println("Or use any dfmt command - it will auto-start the daemon")
		}
		return 0
	}

	if flagJSON {
		// JSON output
		fmt.Println("[")
		for i, d := range daemons {
			comma := ","
			if i == len(daemons)-1 {
				comma = ""
			}
			if runtime.GOOS == "windows" {
				fmt.Printf(`  {"project": %q, "pid": %d, "port": %d}%s`+"\n",
					d.ProjectPath, d.PID, d.Port, comma)
			} else {
				fmt.Printf(`  {"project": %q, "pid": %d, "socket": %q}%s`+"\n",
					d.ProjectPath, d.PID, d.SocketPath, comma)
			}
		}
		fmt.Println("]")
	} else {
		fmt.Println("Running DFMT daemons:")
		fmt.Println("")
		for _, d := range daemons {
			uptime := time.Since(d.StartedAt).Round(time.Second)
			if runtime.GOOS == "windows" {
				fmt.Printf("  [%d] %s (port %d, uptime %s)\n", d.PID, d.ProjectPath, d.Port, uptime)
			} else {
				fmt.Printf("  [%d] %s (socket, uptime %s)\n", d.PID, d.ProjectPath, uptime)
			}
		}
		fmt.Printf("\n%d daemon(s) running\n", len(daemons))
	}
	return 0
}

func runDoctor(args []string) int {
	var dir string
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.StringVar(&dir, "dir", "", "Project directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if dir == "" {
		dir, _ = os.Getwd()
	}

	// v0.6.3: ensure the daemon is up before reporting health — the
	// "daemon not running" diagnostic should only fire when spawning is
	// genuinely impossible, not as a default state on a clean shell.
	_ = ensureGlobalDaemon()

	dfmtDir := filepath.Join(dir, ".dfmt")
	pidPath := filepath.Join(dfmtDir, "daemon.pid")
	portPath := filepath.Join(dfmtDir, "port")
	journalPath := filepath.Join(dfmtDir, "journal.jsonl")
	indexPath := filepath.Join(dfmtDir, "index.gob")
	lockPath := filepath.Join(dfmtDir, "lock")

	// Pre-compute liveness once so all checks see a consistent view —
	// otherwise a daemon that exits mid-doctor would produce contradictory
	// "running but stale lock" output.
	daemonAlive := client.DaemonRunning(dir)

	type check struct {
		name string
		fn   func() (ok bool, detail string)
	}

	checks := []check{
		{"Project exists", func() (bool, string) {
			p, err := project.Discover(dir)
			if err != nil {
				return false, err.Error()
			}
			return true, p
		}},
		{"Config valid", func() (bool, string) {
			cfg, err := config.Load(dir)
			if err != nil {
				return false, err.Error()
			}
			if cfg == nil {
				return false, "config nil"
			}
			return true, fmt.Sprintf("durability=%s", cfg.Storage.Durability)
		}},
		{".dfmt directory", func() (bool, string) {
			fi, err := os.Stat(dfmtDir)
			if err != nil {
				return false, err.Error()
			}
			if !fi.IsDir() {
				return false, ".dfmt is not a directory"
			}
			return true, ""
		}},
		{"Go toolchain (build)", func() (bool, string) {
			// runtime.Version() reports the toolchain that built THIS
			// binary — operators rebuilding from source see whether the
			// embedded stdlib carries the 1.26.2 patches for the
			// crypto/x509 + crypto/tls CVEs (GO-2026-4866 / 4870 /
			// 4946 / 4947). Doctor downgrades to a non-failing warning
			// when older — the binary still works, but dashboard TLS
			// (if anyone enables it) would inherit unpatched code.
			v := runtime.Version()
			if !goToolchainAtLeast(v, 1, 26, 2) {
				return true, fmt.Sprintf("%s (older than go1.26.2 — stdlib CVEs unpatched; rebuild with newer toolchain)", v)
			}
			return true, v
		}},
		{"Journal openable", func() (bool, string) {
			if _, err := os.Stat(journalPath); os.IsNotExist(err) {
				return true, "(none yet — created on first event)"
			}
			f, err := os.Open(journalPath)
			if err != nil {
				return false, err.Error()
			}
			if err := f.Close(); err != nil {
				return false, fmt.Sprintf("journal exists but could not close: %v", err)
			}
			return true, ""
		}},
		{"Index file readable", func() (bool, string) {
			fi, err := os.Stat(indexPath)
			if os.IsNotExist(err) {
				return true, "(none yet — built on first daemon start)"
			}
			if err != nil {
				return false, err.Error()
			}
			f, err := os.Open(indexPath)
			if err != nil {
				return false, err.Error()
			}
			if err := f.Close(); err != nil {
				return false, fmt.Sprintf("index exists but could not close: %v", err)
			}
			return true, fmt.Sprintf("%d bytes", fi.Size())
		}},
		{"Port file consistent with daemon liveness", func() (bool, string) {
			// Phase 2: prefer the global port file when a host-wide
			// daemon is up. The legacy per-project file is still
			// checked as a fallback so v0.3.x straddle setups don't
			// flip the row red just because they're running both.
			if globalDashboardURL() != "" {
				if _, err := os.Stat(project.GlobalPortPath()); err == nil {
					return true, "(global daemon)"
				}
			}
			if _, err := os.Stat(portPath); os.IsNotExist(err) {
				if daemonAlive {
					return false, "daemon is alive but port file missing"
				}
				return true, "(no daemon, no port file — OK)"
			}
			if !daemonAlive {
				return false, "stale port file from crashed daemon (will be overwritten on next start)"
			}
			return true, ""
		}},
		{"PID file consistent with daemon liveness", func() (bool, string) {
			// Same global-first ordering as the port-file check.
			if globalDashboardURL() != "" {
				if pid := readGlobalDaemonPID(); pid > 0 {
					return true, fmt.Sprintf("global daemon PID %d", pid)
				}
				if daemonAlive {
					return false, "global daemon is alive but ~/.dfmt/daemon.pid is missing"
				}
			}
			data, err := os.ReadFile(pidPath)
			if os.IsNotExist(err) {
				if daemonAlive {
					return false, "daemon is alive but PID file missing"
				}
				return true, "(no daemon, no PID file — OK)"
			}
			if err != nil {
				return false, err.Error()
			}
			var pid int
			fmt.Sscanf(string(data), "%d", &pid)
			if pid <= 0 {
				return false, "PID file is malformed"
			}
			if !daemonAlive {
				return false, fmt.Sprintf("stale PID %d (process not running; auto-cleaned on next start)", pid)
			}
			return true, fmt.Sprintf("PID %d", pid)
		}},
		{"Redact override (.dfmt/redact.yaml)", func() (bool, string) {
			// ADR-0014. Same shape as the permissions row below.
			_, res, err := redact.LoadProjectRedactor(dir)
			if err != nil {
				return false, err.Error()
			}
			if !res.OverrideFound {
				return true, "(none — using default patterns)"
			}
			detail := fmt.Sprintf("loaded %d pattern(s)", res.PatternsLoaded)
			if len(res.Warnings) > 0 {
				detail += fmt.Sprintf("; %d warning(s): %s",
					len(res.Warnings), strings.Join(res.Warnings, "; "))
			}
			return true, detail
		}},
		{"Permissions override (.dfmt/permissions.yaml)", func() (bool, string) {
			// ADR-0014. The override file is optional; report what state
			// the daemon will see at startup.
			res, err := sandbox.LoadPolicyMerged(dir)
			if err != nil {
				return false, err.Error()
			}
			if !res.OverrideFound {
				return true, "(none — using DefaultPolicy)"
			}
			detail := fmt.Sprintf("loaded %d rule(s)", res.OverrideRules)
			if len(res.Warnings) > 0 {
				detail += fmt.Sprintf("; %d hard-deny mask(s): %s",
					len(res.Warnings), strings.Join(res.Warnings, "; "))
			}
			return true, detail
		}},
		{"Lock file consistent with daemon liveness", func() (bool, string) {
			if _, err := os.Stat(lockPath); os.IsNotExist(err) {
				return true, "(no lock — OK)"
			}
			if daemonAlive {
				return true, "(held by running daemon)"
			}
			// Lock file present, daemon dead: try to acquire it. If we
			// can, the OS released the flock when the daemon died — the
			// file is benign and a fresh daemon will reuse it. If we can
			// NOT acquire, something else is holding it; the next Start
			// will fail.
			lock, lerr := daemon.AcquireLock(dir)
			if lerr == nil {
				_ = lock.Release()
				return true, "(orphan file, but flock released — next start will reclaim)"
			}
			return false, fmt.Sprintf("lock held by another process: %v", lerr)
		}},
		{"Last crash (~/.dfmt/last-crash.log)", func() (bool, string) {
			// Phase 2: the global daemon writes here on panic via
			// recoverAndLogCrash. Absence is the happy state. Presence
			// is informational only — the doctor stays green so a crash
			// from yesterday doesn't make every diagnostic look
			// failing — but the timestamp is printed so the operator
			// knows whether to investigate.
			fi, err := os.Stat(project.GlobalCrashPath())
			if os.IsNotExist(err) {
				return true, "(none — clean run)"
			}
			if err != nil {
				return false, err.Error()
			}
			age := time.Since(fi.ModTime()).Truncate(time.Second)
			return true, fmt.Sprintf("present (%s old; cat %s)", age, project.GlobalCrashPath())
		}},
	}

	allOk := true
	for _, c := range checks {
		ok, detail := c.fn()
		marker := "✓"
		if !ok {
			marker = "✗"
			allOk = false
		}
		if detail != "" {
			fmt.Printf("%s %s — %s\n", marker, c.name, detail)
		} else {
			fmt.Printf("%s %s\n", marker, c.name)
		}
	}

	// Per-agent verification — the previous doctor only inspected project
	// state (.dfmt/, journal, lock). The MCP wire-up is the part that
	// silently rots across upgrades: a user reinstalls dfmt to a different
	// path, or wipes the agent's config, and `dfmt doctor` would still
	// happily report "all good" while the agent fails to launch the MCP
	// server. The block below checks each detected agent's manifest files
	// and the resolvability of the dfmt binary the agents reference.
	if !checkAgentWireUp() {
		allOk = false
	}

	// Instruction-file staleness: a project may have been `dfmt init`-ed
	// on a previous DFMT version whose canonical block body has since
	// drifted (table additions, wording changes). The MCP server still
	// works — only the agent's prompt is stale — so this is a warning,
	// not a failure. Surfaces the cure ("run `dfmt init` to refresh")
	// inline so the user doesn't have to consult docs.
	checkInstructionBlockStaleness()

	// Sandbox toolchain visibility: the recurring "exit 127, command not
	// found" symptom when the daemon was auto-started from a shell whose
	// PATH did not include the user's Go / Node / Python install. This
	// check probes the effective sandbox PATH and, if anything is
	// missing, scans well-known install locations and prints a
	// copy-pasteable `exec.path_prepend:` block. Warning-only — agents
	// that don't run subprocesses are unaffected.
	checkSandboxToolchains(dir)

	if daemonAlive {
		fmt.Println("[i] Daemon running")
	} else {
		fmt.Println("[i] Daemon stopped (auto-starts on next command)")
	}

	if !allOk {
		return 1
	}
	return 0
}

// checkAgentWireUp prints one line per detected agent describing whether
// the manifest-recorded MCP config files are still on disk AND whether
// the recorded MCP `command` path matches the binary running this very
// `dfmt doctor` invocation. Returns false if any check failed so
// runDoctor can flip its exit code.
//
// Three failure modes per agent now surface separately:
//
//  1. Manifest file missing on disk          → ✗ with `missing: <path>`.
//  2. File present, mcpServers.dfmt absent   → ✗ with `dfmt entry missing`.
//  3. File present, mcpServers.dfmt.command  → ✗ with `command stale`.
//     points at a different binary
//
// Mode 3 is the silent-rot case the previous version of this check
// couldn't see: a user reinstalls dfmt to a different path, every
// agent's config still points at the old binary, doctor reported
// "all good" while every agent actually fails to launch the MCP
// server on its next restart.
// checkInstructionBlockStaleness compares each manifest-tracked
// instruction file's current block body against the canonical body
// shipped by this dfmt binary. Drift is non-fatal — the MCP server
// keeps working with a stale block — so the function prints warnings
// and returns nothing.
//
// The check covers only Kind=FileKindStrip entries (project doc
// injections); Kind=delete (~/.claude/mcp.json etc.) are out of scope
// — those are full-file artifacts, not blocks within user content.
//
// Comparison is whitespace-tolerant on the *boundary* (we trim
// trailing newlines from both sides) but strict on the body. A single
// reordered table row or new sentence flips the answer. False
// positives are acceptable here because the cure ("run `dfmt init` to
// refresh") is one command and idempotent.
// checkSandboxToolchains probes the *running daemon* — not the doctor
// process — for the language toolchains the agent is most likely to
// invoke (`go`, `node`, `python`). The earlier implementation looked
// up tools against the doctor's own PATH, which on Windows hosts
// running their daemon under WSL bash or via an agent harness with a
// stripped env produced false positives: doctor reported ✓ while the
// sandbox subprocess actually returned exit 127.
//
// Probe matrix per tool:
//
//   - `command -v <tool>`           — bare name, the agent's most
//     common invocation form.
//   - `command -v <tool>.exe`       — Windows binary form. WSL bash
//     sees Windows toolchains under
//     /mnt/c/... but only the .exe
//     suffixed name resolves under
//     Linux-PATH semantics; the
//     bare name 127s. Detecting this
//     pattern lets doctor explain
//     why path_prepend won't help and
//     point at Git Bash / .exe suffix
//     as the actual fix.
//
// Warning-only — never flips the doctor exit code, because plenty of
// valid setups don't need any of these.
func checkSandboxToolchains(dir string) {
	if !client.DaemonRunning(dir) {
		fmt.Println("[i] Sandbox toolchain probe skipped — daemon not running. Tools will be probed on the next dfmt call.")
		return
	}
	cl, err := client.NewClient(dir)
	if err != nil {
		fmt.Printf("[!] Sandbox toolchain probe skipped — could not connect to daemon: %v\n", err)
		return
	}

	tools := []string{"go", "node", "python"}
	if runtime.GOOS != "windows" {
		tools = append(tools, "python3")
	}

	type probe struct {
		tool       string
		bareOK     bool
		bareStdout string
		exeOK      bool
		exeStdout  string
	}
	var (
		bareMissing []string
		wslExeOnly  []string // bare 127's but .exe works → WSL-bash mismatch
	)

	for _, t := range tools {
		var p probe
		p.tool = t
		p.bareStdout, p.bareOK = probeSandboxTool(cl, t)
		if !p.bareOK {
			// Only check .exe variant when the bare name failed and we're
			// on a host where Windows binaries are reachable (Windows host
			// or any host whose daemon may be WSL-bashing into /mnt/c).
			if runtime.GOOS == "windows" {
				p.exeStdout, p.exeOK = probeSandboxTool(cl, t+".exe")
			}
		}

		switch {
		case p.bareOK:
			fmt.Printf("✓ Sandbox can call %s — %s\n", t, p.bareStdout)
		case p.exeOK:
			fmt.Printf("[!] Sandbox sees %s.exe but NOT %s — daemon is using a Linux-PATH shell (likely WSL bash). Agents calling '%s ...' will get exit 127.\n", t, t, t)
			fmt.Printf("        path: %s\n", p.exeStdout)
			wslExeOnly = append(wslExeOnly, t)
		default:
			fmt.Printf("[!] Sandbox cannot find %s — exec calls for %s will return exit 127.\n", t, t)
			bareMissing = append(bareMissing, t)
		}
	}

	if len(wslExeOnly) > 0 {
		fmt.Println("    The daemon is in WSL bash, which doesn't auto-suffix .exe like Windows cmd does. Two ways to fix:")
		fmt.Println("      - Reorder PATH so Git Bash (`C:\\Program Files\\Git\\usr\\bin`) wins over WSL bash, then restart the daemon (`dfmt stop`).")
		fmt.Println("      - Or have the agent invoke the .exe form (`go.exe version`, `node.exe --version`).")
	}

	if len(bareMissing) == 0 {
		return
	}
	suggestions := suggestToolchainDirs(bareMissing)
	if len(suggestions) == 0 {
		fmt.Println("    (no install candidates found in the usual locations; install the toolchain or set PATH in the shell that starts dfmt)")
		return
	}
	fmt.Println("    Add these to .dfmt/config.yaml so the sandbox can see them:")
	fmt.Println("")
	fmt.Println("    exec:")
	fmt.Println("      path_prepend:")
	for _, d := range suggestions {
		fmt.Printf("        - %q\n", d)
	}
	fmt.Println("")
	fmt.Println("    Then restart the daemon: `dfmt stop` (auto-restarts on next call).")
}

// probeSandboxTool runs `command -v <name>` through the daemon's exec
// pipeline and returns the resolved path (stdout) plus whether the
// probe succeeded (exit 0). Bash builtin `command -v` is used so we
// don't rely on /usr/bin/which being installed inside the daemon's
// shell environment. 3-second timeout — version probes shouldn't take
// longer; a slower one means a hung subprocess that doctor must not
// wait on.
func probeSandboxTool(cl *client.Client, name string) (path string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := cl.Exec(ctx, transport.ExecParams{
		Code:    "command -v " + name,
		Intent:  "tool probe",
		Timeout: 2,
	})
	if err != nil || resp == nil || resp.Exit != 0 {
		return "", false
	}
	return strings.TrimSpace(resp.Stdout), true
}

// suggestToolchainDirs scans well-known install locations for the
// missing toolchains and returns the directories that contain a usable
// binary. Order is deterministic so doctor output is reproducible
// across runs.
func suggestToolchainDirs(missing []string) []string {
	candidates := toolchainCandidateDirs()
	want := make(map[string]struct{}, len(missing))
	for _, m := range missing {
		want[m] = struct{}{}
	}
	seen := make(map[string]struct{})
	out := []string{}
	for _, d := range candidates {
		if _, dup := seen[d]; dup {
			continue
		}
		for tool := range want {
			bin := tool
			if runtime.GOOS == "windows" {
				bin = tool + ".exe"
			}
			full := filepath.Join(d, bin)
			if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
				seen[d] = struct{}{}
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// toolchainCandidateDirs returns the platform-specific list of
// directories DFMT will probe when suggesting path_prepend entries.
// Kept short on purpose: a hit-rate of 80% across common installers is
// the bar; exotic setups can configure path_prepend manually.
func toolchainCandidateDirs() []string {
	if runtime.GOOS == "windows" {
		dirs := []string{
			`C:\Program Files\Go\bin`,
			`C:\Go\bin`,
			`C:\Program Files\nodejs`,
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			// Python's per-user installer drops here; minor versions
			// vary so glob-by-prefix.
			pat := filepath.Join(local, "Programs", "Python")
			if entries, err := os.ReadDir(pat); err == nil {
				for _, e := range entries {
					if e.IsDir() && strings.HasPrefix(e.Name(), "Python3") {
						dirs = append(dirs, filepath.Join(pat, e.Name()))
					}
				}
			}
		}
		return dirs
	}
	return []string{
		"/usr/local/go/bin",
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/opt/homebrew/opt/python@3/libexec/bin",
		"/usr/bin",
	}
}

func checkInstructionBlockStaleness() {
	m, err := setup.LoadManifest()
	if err != nil || m == nil {
		// Manifest read errors are surfaced by other checks; don't
		// double-report here.
		return
	}

	driftCount := 0
	for _, f := range m.Files {
		if f.Kind != setup.FileKindStrip {
			continue
		}
		canonical := setup.ProjectBlockBodyForAgent(f.Agent)
		if canonical == "" {
			// Agent has no registered body (e.g., the entry is from
			// a future-version that knew about an agent this binary
			// doesn't). Skip silently.
			continue
		}
		got, found, err := setup.ExtractDFMTBlock(f.Path)
		if err != nil {
			fmt.Printf("✗ Instruction block %s — %v\n", f.Path, err)
			driftCount++
			continue
		}
		if !found {
			fmt.Printf("✗ Instruction block %s — markers missing (run `dfmt init` to restore)\n", f.Path)
			driftCount++
			continue
		}
		if strings.TrimRight(got, "\n") != strings.TrimRight(canonical, "\n") {
			fmt.Printf("⚠ Instruction block %s — drift from canonical body (run `dfmt init` to refresh)\n", f.Path)
			driftCount++
		}
	}

	if driftCount == 0 && len(m.Files) > 0 {
		// Only print the all-good line when there's something to check
		// — silent on a fresh project with no instruction files.
		anyStrip := false
		for _, f := range m.Files {
			if f.Kind == setup.FileKindStrip {
				anyStrip = true
				break
			}
		}
		if anyStrip {
			fmt.Println("✓ Instruction blocks current")
		}
	}
}

func checkAgentWireUp() bool {
	allOk := true
	agents := setup.Detect()
	if len(agents) == 0 {
		fmt.Println("[i] No AI agents detected (run `dfmt setup` after installing one)")
		return true
	}

	m, _ := setup.LoadManifest()
	byAgent := make(map[string][]setup.FileEntry)
	if m != nil {
		for _, f := range m.Files {
			byAgent[f.Agent] = append(byAgent[f.Agent], f)
		}
	}

	expectedCmd := setup.ResolveDFMTCommand()

	fmt.Println()
	fmt.Println("AI agent wire-up:")
	for _, a := range agents {
		files := byAgent[a.ID]
		if len(files) == 0 {
			fmt.Printf("✗ %s — detected but not configured (run `dfmt setup`)\n", a.Name)
			allOk = false
			continue
		}
		missing := 0
		var missingPaths []string
		var stalePaths []string // present but command path is wrong
		for _, f := range files {
			if _, err := os.Stat(f.Path); err != nil {
				missing++
				missingPaths = append(missingPaths, f.Path)
				continue
			}
			// File on disk — try to verify the embedded command path. We
			// only inspect *.json files; settings.json and hooks files in
			// other formats just get the presence check.
			if !strings.HasSuffix(strings.ToLower(f.Path), ".json") {
				continue
			}
			ok, found := verifyMCPCommandPath(f.Path, expectedCmd)
			if !ok {
				stalePaths = append(stalePaths, fmt.Sprintf("%s (found: %s)", f.Path, found))
			}
		}
		switch {
		case missing > 0:
			fmt.Printf("✗ %s — %d/%d files missing (run `dfmt setup --force` to restore)\n",
				a.Name, missing, len(files))
			for _, p := range missingPaths {
				fmt.Printf("    missing: %s\n", p)
			}
			allOk = false
		case len(stalePaths) > 0:
			fmt.Printf("✗ %s — %d file(s) in place but command path stale (run `dfmt setup --force`)\n",
				a.Name, len(files))
			for _, p := range stalePaths {
				fmt.Printf("    stale: %s\n", p)
			}
			fmt.Printf("    expected: %s\n", expectedCmd)
			allOk = false
		default:
			fmt.Printf("✓ %s — %d file(s) in place\n", a.Name, len(files))
		}
	}

	// Final sanity: the binary running this doctor pass must itself be
	// stat-able. If it isn't we're in surreal territory (the binary was
	// deleted while running) — surface it loudly because every above
	// "✓ command matches" line just compared agents to a binary that
	// vanished.
	if _, err := os.Stat(expectedCmd); err != nil {
		fmt.Printf("✗ DFMT binary — %s not stat-able (%v); rebuild + `dfmt setup --force`\n",
			expectedCmd, err)
		allOk = false
	} else {
		fmt.Printf("✓ DFMT binary — %s\n", expectedCmd)
	}

	return allOk
}

// verifyMCPCommandPath reads an MCP config file and confirms the command
// stored under mcpServers.dfmt.command resolves to the same on-disk binary
// as expectedCmd. The comparison is case-insensitive on Windows (NTFS is
// case-preserving but case-insensitive — agents and dfmt may write the
// same path with different casing) and tolerant of unresolved symlinks
// (we compare the raw strings after a Clean, not a stat-based identity).
//
// Returns:
//   - ok=true, found="" when the paths match.
//   - ok=true, found="" when the file is present but doesn't carry an
//     mcpServers.dfmt entry that we can examine. We treat that as "out
//     of scope" rather than failure — settings.json, hooks files, and
//     other manifest-tracked files don't all carry MCP entries.
//   - ok=false, found=<actual> on a real mismatch.
//   - ok=false, found="<read error>" / "<json error>" if the file can't
//     be parsed; we still surface it so the user knows something's
//     wrong.
func verifyMCPCommandPath(path, expectedCmd string) (bool, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Sprintf("<read error: %v>", err)
	}
	if len(data) == 0 {
		// Empty file — defensive: not a configured MCP file.
		return true, ""
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Sprintf("<json parse error: %v>", err)
	}
	servers, _ := raw["mcpServers"].(map[string]any)
	if servers == nil {
		// File doesn't host MCP servers (e.g., a settings.json with hooks
		// only). Out of scope for this check.
		return true, ""
	}
	dfmtEntry, _ := servers["dfmt"].(map[string]any)
	if dfmtEntry == nil {
		// File has mcpServers but no dfmt — explicit gap.
		return false, "<dfmt entry missing>"
	}
	gotCmd, _ := dfmtEntry["command"].(string)
	if gotCmd == "" {
		return false, "<command field missing>"
	}
	if pathsEqual(gotCmd, expectedCmd) {
		return true, ""
	}
	return false, gotCmd
}

// pathsEqual normalises two filesystem paths and compares them. Windows
// NTFS is case-insensitive; on POSIX paths must match byte-for-byte.
// We Clean both sides so trailing-slash and "/." quirks don't trigger
// a false stale report.
func pathsEqual(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// goToolchainAtLeast parses a Go version string ("go1.26.1",
// "go1.27.0-rc1", "devel go1.27") and reports whether it is at least
// major.minor.patch. Unparseable strings (e.g. "devel go1.27" with no
// patch) are treated as "at least" so unreleased toolchains don't
// trigger spurious warnings — the doctor check is meant to catch
// stale shipped releases, not flag developers using tip.
func goToolchainAtLeast(version string, wantMajor, wantMinor, wantPatch int) bool {
	// Strip optional "go" prefix and any pre-release suffix like "-rc1".
	v := strings.TrimPrefix(strings.TrimSpace(version), "go")
	if i := strings.IndexAny(v, " -"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return true
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	patch := 0
	if len(parts) == 3 {
		// Trailing "+" or other oddities — best-effort parse.
		if p, err := strconv.Atoi(strings.TrimRight(parts[2], "+")); err == nil {
			patch = p
		}
	}
	if maj != wantMajor {
		return maj > wantMajor
	}
	if min != wantMinor {
		return min > wantMinor
	}
	return patch >= wantPatch
}



func runShellInit(args []string) int {
	// Help short-circuit before the "unknown shell" diagnostic — pre-fix,
	// `dfmt shell-init --help` printed a misleading "unknown shell: --help"
	// error instead of the supported-shells list.
	if helpRequested(args) {
		fmt.Println("usage: dfmt shell-init <bash|zsh|fish>")
		return 0
	}
	shell := "bash"
	if len(args) > 0 {
		shell = args[0]
	}

	// Resolve the absolute path of the installing dfmt so sourced hooks
	// invoke *this* binary rather than whatever `dfmt` is on PATH.
	dfmtBin, err := os.Executable()
	if err != nil {
		logging.Warnf("os.Executable failed (%v); shell hooks will use PATH", err)
		dfmtBin = ""
	} else {
		dfmtBin = filepath.ToSlash(dfmtBin)
	}

	switch shell {
	case "bash":
		fmt.Println("# Add to ~/.bashrc:")
		fmt.Println("source /dev/stdin << 'EOF'")
		fmt.Println(installShellHookContent(readHookFile("bash.sh"), dfmtBin))
		fmt.Println("EOF")
	case "zsh":
		fmt.Println("# Add to ~/.zshrc:")
		fmt.Println("source /dev/stdin << 'EOF'")
		fmt.Println(installShellHookContent(readHookFile("zsh.sh"), dfmtBin))
		fmt.Println("EOF")
	case "fish":
		fmt.Println("# Add to ~/.config/fish/config.fish:")
		fmt.Println(installShellHookContent(readHookFile("fish.fish"), dfmtBin))
	default:
		fmt.Fprintf(os.Stderr, "unknown shell: %s\n", shell)
		return 1
	}
	return 0
}

func runInstallHooks(args []string) int {
	// Pre-fix `dfmt install-hooks --help` installed the hooks because the
	// function ignored args entirely. Route through FlagSet so --help prints
	// usage and unknown flags surface as parse errors instead of silent
	// state mutation.
	fs := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: install-hooks takes no positional arguments\n")
		return 2
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	hooksDir := filepath.Join(proj, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	hooks := []string{"post-commit", "post-checkout", "pre-push"}
	for _, hook := range hooks {
		content := readHookFile("git-" + hook + ".sh")
		if content == "" {
			fmt.Fprintf(os.Stderr, "error: missing embedded hook git-%s.sh\n", hook)
			return 1
		}
		// Hooks use 'dfmt' from PATH (not pinned to a specific binary)
		content = installHookContent(content, "")
		dst := filepath.Join(hooksDir, hook)
		if err := os.WriteFile(dst, []byte(content), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", dst, err)
			return 1
		}
		fmt.Printf("Installed %s\n", hook)
	}

	fmt.Println("Git hooks installed")
	return 0
}

// installHookContent keeps hooks using dfmt from PATH.
// The dfmtBin parameter is ignored — hooks always use 'dfmt' from PATH.
func installHookContent(raw, dfmtBin string) string {
	_ = dfmtBin // unused
	return raw
}

// installShellHookContent keeps shell-init templates using dfmt from PATH.
// The dfmtBin parameter is ignored — hooks always use 'dfmt' from PATH
// so the single global installation is used regardless of which binary installed them.
func installShellHookContent(raw, dfmtBin string) string {
	_ = dfmtBin // unused
	return raw
}

// errSkipCapture signals that buildCaptureParams intentionally produced no event
// (e.g. PreToolUse hook fired with no usable args/stdin) and the caller should
// exit 0 without sending anything to the daemon.
var errSkipCapture = errors.New("capture: nothing to record")

func buildCaptureParams(args []string) (transport.RememberParams, error) {
	if len(args) < 1 {
		return transport.RememberParams{}, errors.New("capture type required")
	}
	switch args[0] {
	case "git":
		if len(args) < 2 {
			return transport.RememberParams{}, errors.New("git capture requires subcommand")
		}
		switch args[1] {
		case "commit":
			if len(args) < 3 {
				return transport.RememberParams{}, errors.New("git commit requires hash")
			}
			msg := ""
			if len(args) >= 4 {
				msg = args[3]
			}
			return transport.RememberParams{
				Type:     string(core.EvtGitCommit),
				Priority: string(core.PriP2),
				Source:   string(core.SrcGitHook),
				Data:     map[string]any{"hash": args[2], "message": msg},
			}, nil
		case "checkout":
			if len(args) < 3 {
				return transport.RememberParams{}, errors.New("git checkout requires ref")
			}
			isBranch := "0"
			if len(args) >= 4 {
				isBranch = args[3]
			}
			return transport.RememberParams{
				Type: string(core.EvtGitCheckout),
				// Match the classifier default (PriP2) so rendered tier
				// labels agree with Recall sort order.
				Priority: string(core.PriP2),
				Source:   string(core.SrcGitHook),
				Data:     map[string]any{"ref": args[2], "is_branch": isBranch},
			}, nil
		case "push":
			if len(args) < 4 {
				return transport.RememberParams{}, errors.New("git push requires remote and branch")
			}
			return transport.RememberParams{
				Type:     string(core.EvtGitPush),
				Priority: string(core.PriP2),
				Source:   string(core.SrcGitHook),
				Data:     map[string]any{"remote": args[2], "branch": args[3]},
			}, nil
		default:
			return transport.RememberParams{}, fmt.Errorf("unknown git subcommand: %s", args[1])
		}
	case "env.cwd":
		if len(args) < 2 {
			return transport.RememberParams{}, errors.New("env.cwd requires path")
		}
		return transport.RememberParams{
			Type:     string(core.EvtEnvCwd),
			Priority: string(core.PriP4),
			Source:   string(core.SrcShell),
			Data:     map[string]any{"cwd": args[1]},
		}, nil
	case "tool":
		// PreToolUse hook capture: logs tool calls to journal.
		// Usage: dfmt capture tool                  (preferred — read JSON from stdin)
		//        dfmt capture tool <name> <input>   (legacy — accepts pre-expanded template args)
		//
		// Claude Code always passes {"tool_name":..., "tool_input":...} as JSON on stdin
		// for PreToolUse hooks. We prefer stdin because shell-template expansion of
		// ${toolName}/${toolInput} is unreliable: PowerShell expands $toolName as its own
		// (undefined) variable to "" before our binary ever runs. Args are kept as a
		// fallback for the bash case where the templates were already substituted.
		toolName := ""
		input := ""
		if len(args) >= 2 {
			toolName = args[1]
		}
		if len(args) >= 3 {
			input = args[2]
		}
		needStdin := toolName == "" || strings.Contains(toolName, "${")
		if needStdin {
			hookInput, err := readHookStdin()
			if err != nil || hookInput.ToolName == "" {
				// No usable input from either args or stdin — drop silently
				// rather than journaling empty noise on every tool call.
				return transport.RememberParams{}, errSkipCapture
			}
			toolName = hookInput.ToolName
			if hookInput.ToolInput != nil {
				if jsonBytes, jsonErr := json.Marshal(hookInput.ToolInput); jsonErr == nil {
					input = string(jsonBytes)
				}
			}
		}
		return transport.RememberParams{
			Type:     string(core.EvtNote),
			Priority: string(core.PriP3),
			Source:   string(core.SrcMCP),
			Data:     map[string]any{"tool": toolName, "input": input},
			Tags:     []string{toolName},
		}, nil
	case "shell":
		if len(args) < 2 {
			return transport.RememberParams{}, errors.New("shell requires command")
		}
		cwd := ""
		if len(args) >= 3 {
			cwd = args[2]
		}
		return transport.RememberParams{
			Type:     string(core.EvtNote),
			Priority: string(core.PriP4),
			Source:   string(core.SrcShell),
			Data:     map[string]any{"cmd": args[1], "cwd": cwd},
		}, nil
	default:
		return transport.RememberParams{}, fmt.Errorf("unknown capture type: %s", args[0])
	}
}

func runCapture(args []string) int {
	// Help short-circuit before buildCaptureParams emits "unknown capture
	// type: --help". The accepted inputs are documented in printUsage; we
	// echo a one-liner here so `dfmt capture --help` is self-sufficient.
	if helpRequested(args) {
		fmt.Println("usage: dfmt capture <git|shell> ...")
		return 0
	}
	params, err := buildCaptureParams(args)
	if err != nil {
		if err == errSkipCapture {
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	cl, err := client.NewClient(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cl.Remember(ctx, params); err != nil {
		fmt.Fprintf(os.Stderr, "error: remember: %v\n", err)
		return 1
	}
	return 0
}

func readHookFile(name string) string {
	b, err := hookFilesFS.ReadFile("hooks/" + name)
	if err != nil {
		return ""
	}
	return string(b)
}

// HookStdinInput represents the JSON input Claude Code passes to hooks via stdin.
type HookStdinInput struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// readHookStdin reads and parses JSON from stdin for hook commands.
// Returns HookStdinInput on success or empty struct on failure.
// Stdin is bounded to 1 MiB; a larger payload is rejected so a malicious
// or buggy client cannot push us past the limit.
func readHookStdin() (HookStdinInput, error) {
	const hookStdinMaxBytes = 1 << 20
	var input HookStdinInput
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, hookStdinMaxBytes))
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	return input, nil
}

// runHook handles PreToolUse hooks from Claude Code.
// Usage: dfmt hook claude-code pretooluse
// Reads JSON from stdin: {"tool_name": "...", "tool_input": {...}}
// Writes JSON to stdout for Claude Code to consume.
func runHook(args []string) int {
	if len(args) < 2 || args[1] != "pretooluse" {
		fmt.Fprintln(os.Stdout, `{"block":false}`)
		return 0
	}

	input, err := readHookStdin()
	if err != nil || input.ToolName == "" {
		fmt.Fprintln(os.Stdout, `{"block":false}`)
		return 0
	}

	if shouldRedirect(input.ToolName) {
		redirect := buildRedirectResponse(input.ToolName, input.ToolInput)
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(redirect)
	} else {
		fmt.Fprintln(os.Stdout, `{"block":false}`)
	}

	go logHookEventToDaemon(input)
	return 0
}

// shouldRedirect returns true for mapped tools when the daemon is running.
func shouldRedirect(toolName string) bool {
	switch toolName {
	case "Bash", "Read", "WebFetch", "Glob", "Grep", "Edit", "Write":
		if proj, err := getProject(); err == nil && client.DaemonRunning(proj) {
			return true
		}
	}
	return false
}

// buildRedirectResponse creates a redirect spec for the given tool call.
func buildRedirectResponse(toolName string, toolInput map[string]any) map[string]any {
	sub := toolSubcommand(toolName)
	mcpTool := "mcp__dfmt__dfmt_" + sub

	var dfmtParams map[string]any
	switch toolName {
	case "Bash":
		dfmtParams = map[string]any{"code": toolInput["command"], "lang": "bash"}
	case "Read":
		dfmtParams = map[string]any{"path": toolInput["path"]}
	case "WebFetch":
		dfmtParams = map[string]any{"url": toolInput["url"]}
	case "Glob":
		dfmtParams = map[string]any{"pattern": toolInput["pattern"]}
	case "Grep":
		dfmtParams = map[string]any{"pattern": toolInput["pattern"], "files": toolInput["files"]}
	case "Edit":
		dfmtParams = map[string]any{
			"path":       toolInput["path"],
			"old_string": toolInput["old_string"],
			"new_string": toolInput["new_string"],
		}
	case "Write":
		dfmtParams = map[string]any{"path": toolInput["path"], "content": toolInput["content"]}
	default:
		dfmtParams = toolInput
	}

	return map[string]any{
		"redirect": map[string]any{
			"tool":       mcpTool,
			"tool_input": dfmtParams,
		},
	}
}

// toolSubcommand maps native tool name to dfmt subcommand.
func toolSubcommand(toolName string) string {
	switch toolName {
	case "Bash":
		return "exec"
	case "Read":
		return "read"
	case "WebFetch":
		return "fetch"
	case "Glob":
		return "glob"
	case "Grep":
		return "grep"
	case "Edit":
		return "edit"
	case "Write":
		return "write"
	default:
		return toolName
	}
}

// logHookEventToDaemon sends a note event for stats tracking (non-blocking).
func logHookEventToDaemon(input HookStdinInput) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	proj, err := getProject()
	if err != nil {
		return
	}
	cl, err := client.NewClient(proj)
	if err != nil {
		return
	}

	toolJSON, _ := json.Marshal(input.ToolInput)
	params := transport.RememberParams{
		Type:     string(core.EvtNote),
		Priority: string(core.PriP3),
		Source:   string(core.SrcMCP),
		Data:     map[string]any{"tool": input.ToolName, "input": string(toolJSON)},
		Tags:     []string{input.ToolName},
	}
	_, _ = cl.Remember(ctx, params)
}

func runSetup(args []string) int {
	var dryRun bool
	var agentOverride string
	var force bool
	var uninstall bool
	var verify bool
	var refresh bool

	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.BoolVar(&dryRun, "dry-run", false, "Show planned changes")
	fs.StringVar(&agentOverride, "agent", "", "Configure specific agent only")
	fs.BoolVar(&force, "force", false, "Overwrite existing config")
	fs.BoolVar(&uninstall, "uninstall", false, "Remove dfmt configuration")
	fs.BoolVar(&verify, "verify", false, "Verify setup")
	fs.BoolVar(&refresh, "refresh", false, "Purge legacy fossils from Claude settings.json files (global + per-project) and rewrite fresh DFMT entries")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if uninstall {
		return runSetupUninstall()
	}
	if verify {
		return runSetupVerify()
	}
	if refresh {
		return runSetupRefresh()
	}

	// Detect agents
	var override []string
	if agentOverride != "" {
		override = strings.Split(agentOverride, ",")
	}
	agents := setup.DetectWithOverride(override)
	if len(agents) == 0 {
		fmt.Println("No agents detected. Use --agent to specify.")
		return 0
	}

	fmt.Println("Detected agents:")
	for _, a := range agents {
		fmt.Printf("  - %s (%s) confidence=%.0f%%\n", a.Name, a.ID, a.Confidence*100)
	}

	if dryRun {
		fmt.Println("\nDry run - no changes made")
		return 0
	}

	if !force {
		fmt.Print("\nConfigure these agents? [y/N] ")
		var resp string
		fmt.Scanln(&resp)
		if resp != "y" && resp != "Y" {
			fmt.Println("Aborted")
			return 1
		}
	}

	// Configure each agent. Track success / failure counts so the final
	// message can be specific instead of "Setup complete." on a partial
	// failure (the previous output left the user thinking everything
	// worked even when one agent's writeMCPConfig hit a permission
	// error).
	configured := make([]string, 0, len(agents))
	failed := make(map[string]error, 0)
	for _, agent := range agents {
		fmt.Printf("Configuring %s...\n", agent.Name)
		if err := configureAgent(agent); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			failed[agent.Name] = err
		} else {
			fmt.Printf("  done\n")
			configured = append(configured, agent.Name)
		}
	}

	// Summary block — agent-neutral, gives the user the exact next step.
	fmt.Println()
	if len(configured) > 0 {
		fmt.Printf("Configured %d agent(s): %s\n",
			len(configured), strings.Join(configured, ", "))
	}
	if len(failed) > 0 {
		fmt.Printf("Failed: %d agent(s)\n", len(failed))
		for name, err := range failed {
			fmt.Printf("  - %s: %v\n", name, err)
		}
	}
	if len(configured) == 0 {
		fmt.Println("\nNothing was configured. Re-run with --agent NAME to target a specific agent,")
		fmt.Println("or point your agent's MCP config at this binary manually:")
		fmt.Printf("  %s\n", setup.ResolveDFMTCommand())
		return 1
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Restart your AI agent so it re-reads the MCP config.")
	fmt.Println("  2. In the agent, ask it to read a file or run a command.")
	fmt.Println("  3. Run `dfmt stats` here — non-zero events_total confirms the wire-up.")
	fmt.Println()
	fmt.Println("Verify any time with `dfmt doctor` (project health) or `dfmt setup --verify`")
	fmt.Println("(agent file presence). Uninstall with `dfmt setup --uninstall`.")
	return 0
}

func runSetupUninstall() int {
	m, err := setup.LoadManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading manifest: %v\n", err)
		return 1
	}

	if len(m.Files) == 0 {
		fmt.Println("Nothing to uninstall")
		return 0
	}

	// Stop the host-wide daemon before stripping configs. Without this,
	// the global daemon keeps holding open journal/index handles for
	// every project it had cached — uninstall finishes, but the
	// operator sees a stray dfmt process in `tasklist` for up to the
	// configured idle timeout. Also avoids the awkward window where
	// agent MCP configs are gone but the daemon is still serving stale
	// requests routed through them.
	if globalDashboardURL() != "" {
		fmt.Println("Stopping global daemon before uninstall...")
		if rc := stopGlobalDaemon(); rc != 0 {
			fmt.Fprintln(os.Stderr,
				"warning: failed to stop the global daemon cleanly. Uninstall continues;\n"+
					"manual cleanup may be needed: dfmt stop, then remove ~/.dfmt/.")
		}
	}

	fmt.Printf("Removing %d files...\n", len(m.Files))
	for _, f := range m.Files {
		switch f.Kind {
		case setup.FileKindStrip:
			// Instruction file: strip only our marker-delimited block,
			// preserve the rest. StripDFMTBlock no-ops on missing file
			// or absent markers, removes the file if empty after strip.
			if err := setup.StripDFMTBlock(f.Path); err != nil {
				fmt.Fprintf(os.Stderr, "error stripping %s: %v\n", f.Path, err)
				continue
			}
			fmt.Printf("stripped DFMT block: %s\n", f.Path)

		case "", setup.FileKindDelete:
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "error removing %s: %v\n", f.Path, err)
				continue
			}
			// If setup created a .dfmt.bak backup of a pre-existing
			// user config, restore it so uninstall leaves the user's
			// original config intact.
			backup := f.Path + ".dfmt.bak"
			if _, err := os.Stat(backup); err == nil {
				if err := os.Rename(backup, f.Path); err != nil {
					logging.Warnf("restore backup %s: %v", backup, err)
				} else {
					fmt.Printf("restored original: %s\n", f.Path)
				}
			}

		default:
			logging.Warnf("unknown manifest kind %q for %s; skipping", f.Kind, f.Path)
		}
	}

	// Strip dfmt's keys from ~/.claude.json. The manifest deliberately
	// excludes that file because it's a shared user config — full delete
	// would be wrong — but the `mcpServers.dfmt` and per-project
	// `projects[*].mcpServers.dfmt` entries we wrote on setup must come
	// out, otherwise Claude Code will keep trying to launch a binary that
	// no longer exists. Closes F-G-INFO-2 from the security audit.
	if err := setup.UnpatchClaudeCodeUserJSON(); err != nil {
		logging.Warnf("clean ~/.claude.json: %v", err)
	}

	// Clear manifest
	if err := setup.SaveManifest(&setup.Manifest{Version: 1}); err != nil {
		logging.Warnf("clear manifest: %v", err)
	}
	fmt.Println("Uninstall complete")
	return 0
}

func runSetupVerify() int {
	m, err := setup.LoadManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading manifest: %v\n", err)
		return 1
	}

	fmt.Println("Verifying setup...")
	allOk := true
	for _, f := range m.Files {
		if _, err := os.Stat(f.Path); err != nil {
			fmt.Printf("✗ %s (missing)\n", f.Path)
			allOk = false
		} else {
			fmt.Printf("✓ %s\n", f.Path)
		}
	}

	if !allOk {
		return 1
	}
	fmt.Println("All files present")
	return 0
}

// runSetupRefresh purges legacy DFMT fossils from every known Claude
// settings.json file and rewrites the current template into each. The
// candidate set is:
//
//   - ~/.claude/settings.json (user-global)
//   - every <project>/.claude/settings.json tracked in the setup manifest
//   - the cwd's .claude/settings.json (if a project lives there)
//
// Each file is purged via PurgeLegacyClaudeSettings (which writes a
// one-shot .dfmt.bak) and then re-written with the current template:
// WriteClaudeCodeSettingsHook for the global file, EnsureProjectInitialized
// for each project (which calls writeProjectClaudeSettings under the hood).
func runSetupRefresh() int {
	resolved := setup.ResolveDFMTCommand()
	home := setup.HomeDir()

	type target struct {
		path        string
		projectRoot string // empty for global
	}

	seenPath := map[string]bool{}
	var targets []target

	// Global first.
	globalPath := filepath.Join(home, ".claude", "settings.json")
	targets = append(targets, target{path: globalPath})
	seenPath[globalPath] = true

	// Manifest-tracked project settings.
	m, err := setup.LoadManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load manifest: %v\n", err)
		return 1
	}
	for _, fe := range m.Files {
		if filepath.Base(fe.Path) != "settings.json" {
			continue
		}
		if filepath.Base(filepath.Dir(fe.Path)) != ".claude" {
			continue
		}
		if seenPath[fe.Path] {
			continue
		}
		projRoot := filepath.Dir(filepath.Dir(fe.Path))
		// Skip the user-global file (it has no project root).
		if samePathCLI(projRoot, home) {
			continue
		}
		targets = append(targets, target{path: fe.Path, projectRoot: projRoot})
		seenPath[fe.Path] = true
	}

	// cwd as a fallback project, if not already covered.
	if cwd, werr := os.Getwd(); werr == nil {
		cwdSettings := filepath.Join(cwd, ".claude", "settings.json")
		if !seenPath[cwdSettings] && !samePathCLI(cwd, home) {
			if _, sterr := os.Stat(cwdSettings); sterr == nil {
				targets = append(targets, target{path: cwdSettings, projectRoot: cwd})
				seenPath[cwdSettings] = true
			}
		}
	}

	fmt.Printf("Refreshing %d Claude settings file(s)...\n\n", len(targets))

	totalRemoved, totalAdjusted, totalSkipped := 0, 0, 0
	for _, t := range targets {
		if _, err := os.Stat(t.path); os.IsNotExist(err) {
			fmt.Printf("· %s (skipped: not present)\n", t.path)
			continue
		}
		rep, perr := setup.PurgeLegacyClaudeSettings(t.path, resolved)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", t.path, perr)
			continue
		}
		if len(rep.Removed) == 0 && len(rep.Adjusted) == 0 && len(rep.Skipped) == 0 {
			fmt.Printf("✓ %s (no fossils)\n", t.path)
		} else {
			fmt.Printf("✓ %s: removed=%d adjusted=%d skipped=%d",
				t.path, len(rep.Removed), len(rep.Adjusted), len(rep.Skipped))
			if rep.Backup != "" {
				fmt.Printf(" backup=%s", rep.Backup)
			}
			fmt.Println()
		}
		totalRemoved += len(rep.Removed)
		totalAdjusted += len(rep.Adjusted)
		totalSkipped += len(rep.Skipped)
	}

	// Re-write current templates so anything missing is restored.
	fmt.Println("\nWriting fresh templates...")

	// Global: PreToolUse hook only — writeProjectClaudeSettings refuses
	// to touch ~/.claude/settings.json on purpose.
	globalDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(globalDir); err == nil {
		if werr := setup.WriteClaudeCodeSettingsHook(globalDir); werr != nil {
			fmt.Fprintf(os.Stderr, "  warn: write %s: %v\n", globalPath, werr)
		} else {
			fmt.Printf("  ✓ %s\n", globalPath)
		}
	}

	// Per-project.
	refreshedProjects := map[string]bool{}
	for _, t := range targets {
		if t.projectRoot == "" || refreshedProjects[t.projectRoot] {
			continue
		}
		if werr := setup.EnsureProjectInitialized(t.projectRoot); werr != nil {
			fmt.Fprintf(os.Stderr, "  warn: refresh %s: %v\n", t.projectRoot, werr)
		} else {
			fmt.Printf("  ✓ %s\n", t.path)
		}
		refreshedProjects[t.projectRoot] = true
	}

	fmt.Printf("\nDone. Total: removed=%d adjusted=%d skipped=%d\n",
		totalRemoved, totalAdjusted, totalSkipped)

	// Phase 2 migration: stop any per-project legacy daemons so the
	// next `dfmt <command>` call spawns the host-wide global daemon.
	// Skipped under DFMT_DISABLE_AUTOSTART so test binaries that
	// shell into runSetupRefresh don't block on signal+wait loops.
	if os.Getenv("DFMT_DISABLE_AUTOSTART") == "" {
		stopLegacyDaemonsForMigration()
	}
	return 0
}

// stopLegacyDaemonsForMigration enumerates the daemon registry and
// shuts down any per-project legacy daemons so that subsequent CLI
// invocations land on the new global daemon. Per-project journals,
// indexes, configs and content stores are untouched — only the
// transport-level scaffolding (port/socket/pid/lock files) is
// removed. Errors are logged but do not fail the refresh; an
// operator who wants the strictest guarantee can re-run `dfmt stop`
// against any holdouts and confirm via `dfmt list`.
func stopLegacyDaemonsForMigration() {
	daemons := client.GetRegistry().List()
	if len(daemons) == 0 {
		fmt.Println("\nNo legacy daemons to stop. The next dfmt command will start a global daemon.")
		return
	}

	fmt.Printf("\nMigrating %d legacy per-project daemon(s) to global mode...\n", len(daemons))

	stopped := 0
	for _, e := range daemons {
		// Best-effort graceful shutdown: SIGINT on Unix, taskkill /T on
		// Windows. If the process is already gone the signal call is a
		// no-op and the post-poll cleanup still removes any leftover
		// scaffolding.
		signalStopProcess(e.PID, false)
		deadline := time.Now().Add(3 * time.Second)
		gone := false
		for time.Now().Before(deadline) {
			if !isProcessRunning(e.PID) {
				gone = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		// Force kill if it didn't shut down on its own. We deliberately
		// do this rather than leaving a zombie — leaving a stale port
		// listener would block the global daemon from owning the project's
		// future RPCs cleanly on Windows.
		if !gone {
			signalStopProcess(e.PID, true)
		}

		// Per-project transport scaffolding gets purged so a stray client
		// using the legacy address path can no longer connect to a dead
		// socket. config.yaml / journal.jsonl / index.gob are intentionally
		// left in place — they're the user's data.
		removeLegacyDaemonScaffolding(e.ProjectPath)

		// Drop the registry row by hand. Unregister also triggers a save,
		// which is what we want — the next CLI invocation should not see
		// a dead row pointing at a PID that's no longer running.
		client.GetRegistry().Unregister(e.ProjectPath)
		stopped++
		fmt.Printf("  ✓ %s (PID %d)\n", e.ProjectPath, e.PID)
	}

	fmt.Printf("Stopped %d daemon(s). Project state preserved.\n", stopped)
	fmt.Println("Next dfmt command will spawn a single global daemon serving every project.")
}

// isProcessRunning is a CLI-side wrapper around the platform-specific
// liveness check exposed by the daemon package. It exists here so the
// migration loop above doesn't need to import daemon (which would pull
// in journal/index/etc.) just to check a PID.
func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return daemon.ProcessExists(pid)
}

// removeLegacyDaemonScaffolding deletes the per-project transport
// files left behind by a stopped legacy daemon: port file, daemon
// socket, PID file and lock file. The function silently ignores
// "not found" errors — a daemon that died uncleanly may have already
// removed some of these; the rest get reaped here.
func removeLegacyDaemonScaffolding(projectPath string) {
	dot := filepath.Join(projectPath, ".dfmt")
	for _, name := range []string{"port", "daemon.sock", "daemon.pid", "lock"} {
		_ = os.Remove(filepath.Join(dot, name))
	}
}

func configureAgent(agent setup.Agent) error {
	switch agent.ID {
	case "claude-code":
		return configureClaudeCode(agent)
	case "cursor":
		return configureCursor(agent)
	case "vscode":
		return configureVSCode(agent)
	case "codex":
		return configureCodex(agent)
	case "gemini":
		return configureGemini(agent)
	case "windsurf":
		return configureWindsurf(agent)
	case "zed":
		return configureZed(agent)
	case "continue":
		return configureContinue(agent)
	case "opencode":
		return configureOpenCode(agent)
	default:
		return fmt.Errorf("unsupported agent: %s", agent.ID)
	}
}

func configureClaudeCode(_ setup.Agent) error {
	home := setup.HomeDir()
	claudeDir := filepath.Join(home, ".claude")
	// 0o700 on first-create. Idempotent for existing dirs (Claude Code may
	// already own ~/.claude with its own perms — MkdirAll won't change them).
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return err
	}

	// V-14 (F-Setup-3): manifest-first — record the file entry before the
	// write so a SaveManifest failure can never leave an injected file
	// without a matching uninstall row. See writeMCPConfig for the same
	// pattern; same reasoning applies here.
	mcpPath := filepath.Join(claudeDir, "mcp.json")
	m, err := setup.LoadManifest()
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	m.AddFile(setup.FileEntry{
		Path:    mcpPath,
		Agent:   "claude-code",
		Version: "1",
	})
	if err := setup.SaveManifest(m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	// Write legacy ~/.claude/mcp.json. V-04: merge-aware so we don't clobber
	// other MCP servers configured under the same key. Uses TargetOSWindows
	// for the embedded command path — Claude Code on Windows reads this
	// file and expects a Windows-style command — matching the prior shape.
	if err := setup.MergeMCPServerEntry(mcpPath, setup.TargetOSWindows); err != nil {
		return fmt.Errorf("merge %s: %w", mcpPath, err)
	}

	// Also patch ~/.claude.json so the Claude Code CLI picks up the dfmt
	// MCP server at user scope. Failure is non-fatal: the legacy mcp.json
	// above still works for older Claude Code versions.
	//
	// NOTE: ~/.claude.json is deliberately NOT added to the setup manifest.
	// The manifest-based uninstall calls os.Remove on every tracked path,
	// and ~/.claude.json is a *shared* config file owned by the user — we
	// only want to strip our own keys, not delete the whole file. A proper
	// uninstall of these keys is tracked separately (see install.sh/ps1
	// and the dfmt.bak backup written on first patch).
	if err := setup.PatchClaudeCodeUserJSON("", true); err != nil {
		logging.Warnf("patch ~/.claude.json: %v", err)
	}
	// Also write the PreToolUse hook to ~/.claude/settings.json so Claude Code
	// intercepts native tool calls and redirects them through dfmt. The hook
	// uses a matcher regex so it only fires for the tools we handle.
	if err := setup.WriteClaudeCodeSettingsHook(claudeDir); err != nil {
		logging.Warnf("write PreToolUse hook to %s: %v", claudeDir, err)
	}
	m.RecordAgent("claude-code", claudeDir)
	if err := setup.SaveManifest(m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	return nil
}

func configureCodex(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "codex")
}

func configureCursor(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "cursor")
}

func configureVSCode(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".vscode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "vscode")
}

func configureGemini(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "gemini")
}

func configureWindsurf(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".windsurf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "windsurf")
}

func configureZed(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".config", "zed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "zed")
}

func configureContinue(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".config", "continue")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "continue")
}

func configureOpenCode(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "opencode")
}

func writeMCPConfig(dir, filename, agentID string) error {
	mcpPath := filepath.Join(dir, filename)

	// V-14 (F-Setup-3): record the manifest entry BEFORE writing the file.
	// Pre-fix the order was write → SaveManifest, so a SaveManifest failure
	// (rare — disk full, perm change mid-flight, …) left an MCP config on
	// disk that `dfmt setup --uninstall` could not find a row for and
	// therefore could not clean up. With manifest-first, a write failure
	// leaves a stale-but-harmless manifest entry pointing at a non-
	// existent path, which uninstall handles gracefully (os.Remove +
	// IsNotExist short-circuit).
	m, err := setup.LoadManifest()
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	m.AddFile(setup.FileEntry{
		Path:    mcpPath,
		Agent:   agentID,
		Version: "1",
	})
	m.RecordAgent(agentID, dir)
	if err := setup.SaveManifest(m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	// V-04: merge-aware write — splice `mcpServers.dfmt` into the existing
	// config and preserve every other key. The previous implementation
	// replaced the file outright, silently destroying any other MCP server
	// (playwright, context7, github, …) the user had configured for this
	// agent. MergeMCPServerEntry also routes through safefs (V-20) and
	// captures a one-shot pristine .dfmt.bak (V-04 reinstall safety).
	if err := setup.MergeMCPServerEntry(mcpPath, setup.TargetOSUnix); err != nil {
		return fmt.Errorf("merge mcp config %s: %w", mcpPath, err)
	}

	return nil
}









func mustMarshalJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

