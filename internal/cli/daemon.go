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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/osutil"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/transport"
)

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
		if osutil.IsWindows() {
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
		waitForExit(pid, rpcTimeout, proj)
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
	waitForGlobalExit(pid, rpcTimeout)

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
	if osutil.IsWindows() {
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
			if osutil.IsWindows() {
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
			if osutil.IsWindows() {
				fmt.Printf("  [%d] %s (port %d, uptime %s)\n", d.PID, d.ProjectPath, d.Port, uptime)
			} else {
				fmt.Printf("  [%d] %s (socket, uptime %s)\n", d.PID, d.ProjectPath, uptime)
			}
		}
		fmt.Printf("\n%d daemon(s) running\n", len(daemons))
	}
	return 0
}
