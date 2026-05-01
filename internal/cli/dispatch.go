package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	remaining := args[1:]

	switch cmd {
	case "init":
		return runInit(remaining)
	case "quickstart":
		return runQuickstart(remaining)
	case "remember", "note":
		return runRemember(remaining)
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

func runInit(args []string) int {
	var dir string
	var agentOverride string
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.StringVar(&dir, "dir", "", "Project directory")
	fs.StringVar(&agentOverride, "agent", "", "Comma-separated agent IDs to write project files for (default: detected). Use this to commit shared CLAUDE.md/AGENTS.md without needing every agent locally installed.")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if dir == "" {
		dir, _ = os.Getwd()
	}

	if err := setup.EnsureProjectInitialized(dir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Inject the DFMT routing block into each agent's project instruction
	// file (CLAUDE.md, etc.). Without this, MCP registration succeeds
	// but agents don't know they should prefer dfmt_* over native tools
	// — the original "init completes but agent ignores DFMT" complaint.
	// --agent override forces writes for non-detected agents (shared-
	// repo use case). Diagnostics-only on failure.
	var explicitIDs []string
	if agentOverride != "" {
		for _, id := range strings.Split(agentOverride, ",") {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				explicitIDs = append(explicitIDs, trimmed)
			}
		}
	}
	writeProjectInstructionFiles(dir, explicitIDs)

	// Mark this project as trusted in ~/.claude.json — but ONLY if Claude
	// Code is actually present on this machine. The previous unconditional
	// patch created/modified ~/.claude.json on every `dfmt init` run, even
	// for users on Cursor/Codex/Zed/etc. who don't have Claude Code
	// installed. That polluted the home directory with an unrelated tool's
	// state file. Failure remains non-fatal: a stale Claude install
	// shouldn't block init.
	if setup.IsClaudeCodeInstalled() {
		if err := setup.PatchClaudeCodeUserJSON(dir, false); err != nil {
			logging.Warnf("patch ~/.claude.json: %v", err)
		}
	}

	fmt.Printf("Initialized DFMT in %s\n", dir)
	fmt.Println()
	fmt.Println("Next: run `dfmt setup` to wire DFMT into your AI agent(s),")
	fmt.Println("then `dfmt doctor` to verify.")
	return 0
}

// writeProjectInstructionFiles upserts the DFMT routing block into each
// agent's project-level instruction file (CLAUDE.md and friends) and
// records each successful write in the setup manifest with
// Kind=FileKindStrip so `dfmt setup --uninstall` removes only our block,
// not the user's whole file. Idempotent — re-running replaces the
// existing block in place. Failures are reported but do NOT fail init:
// MCP registration still works without the doc nudge, just less
// reliably.
//
// agentIDs is an explicit override list. When non-empty the function
// writes blocks for exactly those IDs regardless of local detection
// — useful for shared repos where teammates may use agents not
// installed on this machine ("git commit me your CLAUDE.md and
// AGENTS.md"). When empty, falls back to setup.Detect() so the user
// gets blocks for whatever they actually use.
func writeProjectInstructionFiles(projectDir string, agentIDs []string) {
	// Best-effort manifest load. If it fails we still write the files —
	// the user gets the doc nudge; uninstall just won't auto-strip.
	m, mErr := setup.LoadManifest()
	if mErr != nil {
		logging.Warnf("load manifest: %v", mErr)
	}

	// Resolve the ID list once so the loop body is identical between
	// detected and override paths.
	var ids []string
	if len(agentIDs) == 0 {
		for _, a := range setup.Detect() {
			ids = append(ids, a.ID)
		}
	} else {
		ids = agentIDs
	}

	seen := make(map[string]bool)
	tracked := false
	for _, id := range ids {
		path, err := setup.UpsertProjectInstructions(projectDir, id)
		if path == "" {
			continue
		}
		if seen[path] {
			continue // shared file (multiple agents → AGENTS.md)
		}
		seen[path] = true
		if err != nil {
			logging.Warnf("write %s: %v", path, err)
			continue
		}
		fmt.Printf("Wrote DFMT block to %s\n", path)
		if m != nil {
			m.AddFile(setup.FileEntry{
				Path:    path,
				Agent:   id,
				Version: "v1",
				Kind:    setup.FileKindStrip,
			})
			tracked = true
		}
	}

	if m != nil && tracked {
		// Bump timestamp + version so a subsequent LoadManifest sees a
		// well-formed record. RecordAgent does this for agent entries;
		// instruction files reuse the same Files slice so a single Save
		// is enough.
		if m.Version == 0 {
			m.Version = 1
		}
		if err := setup.SaveManifest(m); err != nil {
			logging.Warnf("save manifest: %v", err)
		}
	}
}

// runQuickstart wires up a fresh project end-to-end without forcing the user
// to remember the three-step ritual (init → setup → doctor). It is the
// recommended entry point for first-time installs and the answer to "how do
// I get started?" — agent-neutral, idempotent, safe to re-run.
//
// The flow:
//  1. ensureProjectInitialized(cwd) — create .dfmt/, default config, ignore
//     entry, project Claude settings (if not in $HOME).
//  2. setup.DetectWithOverride(nil) — auto-discover installed agents.
//  3. configureAgent(...) for each detected agent — non-fatal per-agent
//     failures so one missing config dir doesn't abort the rest.
//  4. Light doctor pass — config loadable, .dfmt/ writable. We deliberately
//     do NOT spin up the daemon here: that happens lazily on first MCP/CLI
//     call, and a daemon-running check at quickstart time would either
//     show a bogus failure or unnecessarily start a daemon the user may
//     not want yet.
//  5. Print a per-agent "now do this" block so the user knows which app to
//     restart and what to ask it.
//
// Exit code is 0 on partial success (init OK, at least one agent configured)
// and 1 only when init itself failed or no agents could be configured.
func runQuickstart(args []string) int {
	var dir string
	var agentOverride string
	fs := flag.NewFlagSet("quickstart", flag.ContinueOnError)
	fs.StringVar(&dir, "dir", "", "Project directory (default: cwd)")
	fs.StringVar(&agentOverride, "agent", "", "Configure specific agent(s) only (comma-separated)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if dir == "" {
		dir, _ = os.Getwd()
	}

	fmt.Println("DFMT quickstart")
	fmt.Println("===============")
	fmt.Println()

	// Step 1: init.
	fmt.Printf("[1/3] Initializing project at %s...\n", dir)
	if err := setup.EnsureProjectInitialized(dir); err != nil {
		fmt.Fprintf(os.Stderr, "      error: %v\n", err)
		return 1
	}
	// Inject DFMT routing block into each detected agent's project
	// instruction file. See writeProjectInstructionFiles for rationale.
	// quickstart honors the same --agent override as setup so the
	// project-doc and MCP-config writes converge on the same agent
	// set when the operator picks one explicitly.
	var qsExplicitIDs []string
	if agentOverride != "" {
		for _, id := range strings.Split(agentOverride, ",") {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				qsExplicitIDs = append(qsExplicitIDs, trimmed)
			}
		}
	}
	writeProjectInstructionFiles(dir, qsExplicitIDs)
	if setup.IsClaudeCodeInstalled() {
		if err := setup.PatchClaudeCodeUserJSON(dir, false); err != nil {
			fmt.Fprintf(os.Stderr, "      warning: patch ~/.claude.json: %v\n", err)
		}
	}
	fmt.Println("      done")
	fmt.Println()

	// Step 2: detect + configure agents.
	var override []string
	if agentOverride != "" {
		override = strings.Split(agentOverride, ",")
	}
	agents := setup.DetectWithOverride(override)

	fmt.Println("[2/3] Detecting AI agents...")
	if len(agents) == 0 {
		fmt.Println("      no agents detected")
		fmt.Println()
		fmt.Println("DFMT is initialized but no agent was configured. Either install one of:")
		fmt.Println("  Claude Code, Cursor, VS Code, Codex, Gemini, Windsurf, Zed, Continue, OpenCode")
		fmt.Println("then re-run `dfmt quickstart`, or point your agent's MCP config at:")
		fmt.Printf("  %s mcp\n", setup.ResolveDFMTCommand())
		return 1
	}
	for _, a := range agents {
		fmt.Printf("      found: %s (%s)\n", a.Name, a.ID)
	}
	fmt.Println()

	configured := make([]string, 0, len(agents))
	failed := make(map[string]error)
	for _, agent := range agents {
		fmt.Printf("      configuring %s...", agent.Name)
		if err := configureAgent(agent); err != nil {
			fmt.Printf(" failed (%v)\n", err)
			failed[agent.Name] = err
			continue
		}
		fmt.Println(" done")
		configured = append(configured, agent.Name)
	}
	fmt.Println()

	// Step 3: light verify (no daemon spin-up).
	fmt.Println("[3/3] Verifying...")
	if cfg, err := config.Load(dir); err != nil || cfg == nil {
		fmt.Printf("      ✗ config not loadable: %v\n", err)
		return 1
	}
	fmt.Println("      ✓ config loadable")

	if fi, err := os.Stat(filepath.Join(dir, ".dfmt")); err != nil || !fi.IsDir() {
		fmt.Printf("      ✗ .dfmt directory missing\n")
		return 1
	}
	fmt.Println("      ✓ .dfmt directory present")
	fmt.Println()

	// Final report.
	if len(configured) == 0 {
		fmt.Println("Quickstart finished, but no agent could be configured:")
		for name, err := range failed {
			fmt.Printf("  - %s: %v\n", name, err)
		}
		fmt.Println()
		fmt.Println("Try `dfmt setup --agent <name>` to retry a single agent, or check")
		fmt.Println("file permissions on the agent's config directory.")
		return 1
	}

	fmt.Printf("Configured %d agent(s): %s\n", len(configured), strings.Join(configured, ", "))
	if len(failed) > 0 {
		fmt.Printf("Skipped %d agent(s):\n", len(failed))
		for name, err := range failed {
			fmt.Printf("  - %s: %v\n", name, err)
		}
	}
	fmt.Println()
	fmt.Println("You're done. Now:")
	fmt.Println("  1. Restart your AI agent so it re-reads the MCP config.")
	fmt.Println("  2. Ask it to read a file or run a command in this project.")
	fmt.Println("  3. Run `dfmt stats` here — non-zero events_total confirms wire-up.")
	fmt.Println()
	fmt.Println("Health check any time: `dfmt doctor`")
	fmt.Println("Uninstall:             `dfmt setup --uninstall`")
	return 0
}

func runRemember(args []string) int {
	var typ, source string
	var actor string
	var dataJSON string
	var inputTokens, outputTokens, cachedTokens int
	var model string

	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	fs.StringVar(&typ, "type", "note", "Event type")
	fs.StringVar(&source, "source", "cli", "Event source")
	fs.StringVar(&actor, "actor", "", "Actor")
	fs.StringVar(&dataJSON, "data", "", "JSON data")
	fs.IntVar(&inputTokens, "input-tokens", 0, "LLM input tokens")
	fs.IntVar(&outputTokens, "output-tokens", 0, "LLM output tokens")
	fs.IntVar(&cachedTokens, "cached-tokens", 0, "Cached tokens (savings)")
	fs.StringVar(&model, "model", "", "LLM model name")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
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

	var data map[string]any
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			fmt.Fprintf(os.Stderr, "error: --data is not valid JSON: %v\n", err)
			return 1
		}
	}

	// Add token data if provided
	if inputTokens > 0 || outputTokens > 0 || cachedTokens > 0 || model != "" {
		if data == nil {
			data = make(map[string]any)
		}
		if inputTokens > 0 {
			data["input_tokens"] = inputTokens
		}
		if outputTokens > 0 {
			data["output_tokens"] = outputTokens
		}
		if cachedTokens > 0 {
			data["cached_tokens"] = cachedTokens
		}
		if model != "" {
			data["model"] = model
		}
	}

	params := transport.RememberParams{
		Type:   typ,
		Source: source,
		Actor:  actor,
		Data:   data,
		Tags:   fs.Args(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cl.Remember(ctx, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(resp))
	} else {
		fmt.Printf("Recorded: %s at %s\n", resp.ID, resp.TS)
	}

	return 0
}

func runSearch(args []string) int {
	var limit int
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.IntVar(&limit, "limit", 10, "Max results")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	query := fs.Arg(0)
	if query == "" {
		fmt.Fprintf(os.Stderr, "error: query required\n")
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

	resp, err := cl.Search(ctx, transport.SearchParams{
		Query: query,
		Limit: limit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(resp))
	} else {
		for _, hit := range resp.Results {
			fmt.Printf("[%.2f] %s\n", hit.Score, hit.ID)
		}
	}

	return 0
}

func runRecall(args []string) int {
	var budget int
	var format string
	var save bool

	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.IntVar(&budget, "budget", 4096, "Byte budget")
	fs.StringVar(&format, "format", "md", "Output format (md, json, xml)")
	fs.BoolVar(&save, "save", false, "Write snapshot to .dfmt/last-recall.md (0600) instead of stdout")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := cl.Recall(ctx, transport.RecallParams{
		Budget: budget,
		Format: format,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if save {
		// Write via Go so permissions are enforced (0600) and we don't rely on
		// shell redirect portability. PreCompact hooks call this.
		outPath := filepath.Join(proj, ".dfmt", "last-recall.md")
		if err := os.WriteFile(outPath, []byte(resp.Snapshot), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", outPath, err)
			return 1
		}
		return 0
	}

	fmt.Println(resp.Snapshot)
	return 0
}

func runStatus(_ []string) int {
	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	running := client.DaemonRunning(proj)

	if flagJSON {
		// fmt %q produces Go-escaped strings, not JSON — low-byte control
		// characters and "\xHH" escapes in a Windows path would produce
		// invalid JSON. Encode via json.Marshal instead.
		out, _ := json.Marshal(map[string]any{
			"project":        proj,
			"daemon_running": running,
			"socket":         project.SocketPath(proj),
		})
		fmt.Println(string(out))
	} else {
		fmt.Printf("Project: %s\n", proj)
		if running {
			fmt.Println("Daemon: running")
		} else {
			fmt.Println("Daemon: not running")
		}
	}

	return 0
}

func runDaemon(args []string) int {
	var foreground bool
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.BoolVar(&foreground, "foreground", false, "Run in foreground")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Check if already running
	if client.DaemonRunning(proj) {
		fmt.Printf("Daemon already running for %s\n", proj)
		return 0
	}

	cfg, err := config.Load(proj)
	if err != nil {
		// Surface the error — Load now runs Validate (commit 891833b), so a
		// malformed project YAML would otherwise start the daemon with the
		// zero-value config and silently ignore user settings.
		fmt.Fprintf(os.Stderr, "error: load config for %s: %v\n", proj, err)
		return 1
	}

	if foreground {
		return runDaemonForeground(proj, cfg)
	}

	// Start daemon in background
	pid, err := startDaemonBackground(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting daemon: %v\n", err)
		return 1
	}

	fmt.Printf("Daemon started (PID %d) for %s\n", pid, proj)
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

	cmd := exec.Command(exePath, "daemon", "--foreground")
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

func runStop(_ []string) int {
	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if !client.DaemonRunning(proj) {
		fmt.Println("Daemon not running")
		return 0
	}

	// Read PID file and signal the daemon to stop. Ordering matters:
	// 1) signal the process, 2) poll until it terminates, 3) only then
	// clean up pid/lock/socket files. Removing the socket while the daemon
	// still holds it breaks existing clients and leaves the live process
	// orphaned. Windows os.Interrupt is a no-op (syscall.Signal is not
	// supported for arbitrary PIDs), so we invoke taskkill there.
	pidPath := filepath.Join(proj, ".dfmt", "daemon.pid")
	pidData, perr := os.ReadFile(pidPath)
	var pid int
	if perr == nil {
		fmt.Sscanf(string(pidData), "%d", &pid)
	}
	if pid > 0 {
		signalStopProcess(pid, false)
		fmt.Printf("Sent stop signal to PID %d\n", pid)
		// Wait up to 5s for graceful shutdown.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !client.DaemonRunning(proj) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Verify the daemon actually died before clearing state. The previous
	// behavior — unconditionally removing pid/lock/socket after 5s —
	// produced a port-conflict scenario when a hung daemon refused to
	// terminate: the next start would acquire the lock and bind a new
	// listener while the old daemon was still running on the same port,
	// causing intermittent "daemon not responding" errors with no obvious
	// cause. We now escalate to a forced kill and retry the wait. If the
	// daemon STILL refuses to die (kernel-mode hang, zombied parent), we
	// surface a clear error and leave the state files alone so the user
	// can investigate manually.
	if pid > 0 && client.DaemonRunning(proj) {
		fmt.Printf("Daemon still running after graceful stop; escalating to forced kill\n")
		signalStopProcess(pid, true)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if !client.DaemonRunning(proj) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if client.DaemonRunning(proj) {
			fmt.Fprintf(os.Stderr,
				"error: daemon (PID %d) refused to stop. State files left intact.\n"+
					"Manual recovery: kill the process and re-run `dfmt stop`.\n", pid)
			return 1
		}
	}

	// Daemon is gone — now safe to remove state files.
	_ = os.Remove(pidPath)
	_ = os.Remove(filepath.Join(proj, ".dfmt", "lock"))
	_ = os.Remove(project.SocketPath(proj))

	fmt.Printf("Daemon stopped for %s\n", proj)
	return 0
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

func runList(_ []string) int {
	daemons := client.GetRegistry().List()

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
			_ = f.Close()
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
			_ = f.Close()
			return true, fmt.Sprintf("%d bytes", fi.Size())
		}},
		{"Port file consistent with daemon liveness", func() (bool, string) {
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

func runTask(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: dfmt task <subject> | task done <id>\n")
		return 1
	}

	if args[0] == "done" {
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: dfmt task done <id>\n")
			return 1
		}
		return runRemember([]string{"-type", string(core.EvtTaskDone), "-data", fmt.Sprintf(`{"id":%q}`, args[1])})
	}

	// Flags must precede the trailing positional arg; otherwise flag.Parse
	// stops at the first non-flag and -type is dropped, silently journaling
	// the task as type "note".
	return runRemember([]string{"-type", string(core.EvtTaskCreate), "-data", fmt.Sprintf(`{"subject":%q}`, args[0])})
}

func runConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: dfmt config [get <key> | set <key> <value>]\n")
		return 1
	}
	proj, _ := getProject()
	cfg, err := config.Load(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	switch args[0] {
	case "get":
		if len(args) != 2 {
			fmt.Fprintf(os.Stderr, "usage: dfmt config get <key>\n")
			return 1
		}
		val, ok := getConfigField(cfg, args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "error: unknown config key %q\n", args[1])
			return 1
		}
		fmt.Println(val)

	case "set":
		if len(args) != 3 {
			fmt.Fprintf(os.Stderr, "usage: dfmt config set <key> <value>\n")
			return 1
		}
		if proj == "" {
			fmt.Fprintf(os.Stderr, "error: no project path; run from a project directory or set --project\n")
			return 1
		}
		if err := setConfigField(cfg, args[1], args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if err := cfg.Save(proj); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("ok: %s = %s\n", args[1], args[2])

	default:
		fmt.Fprintf(os.Stderr, "usage: dfmt config [get <key> | set <key> <value>]\n")
		return 1
	}
	return 0
}

// getConfigField returns the YAML value of a dot-delimited key path.
func getConfigField(cfg *config.Config, key string) (string, bool) {
	switch key {
	case "version":
		return fmt.Sprintf("%d", cfg.Version), true
	case "capture.fs.enabled":
		return fmt.Sprintf("%v", cfg.Capture.FS.Enabled), true
	case "capture.fs.ignore":
		return fmt.Sprintf("%v", cfg.Capture.FS.Ignore), true
	case "capture.fs.debounce_ms":
		return fmt.Sprintf("%d", cfg.Capture.FS.DebounceMS), true
	case "storage.durability":
		return cfg.Storage.Durability, true
	case "storage.max_batch_ms":
		return fmt.Sprintf("%d", cfg.Storage.MaxBatchMS), true
	case "storage.journal_max_bytes":
		return fmt.Sprintf("%d", cfg.Storage.JournalMaxBytes), true
	case "storage.compress_rotated":
		return fmt.Sprintf("%v", cfg.Storage.CompressRotated), true
	case "retrieval.default_budget":
		return fmt.Sprintf("%d", cfg.Retrieval.DefaultBudget), true
	case "retrieval.default_format":
		return cfg.Retrieval.DefaultFormat, true
	case "index.bm25_k1":
		return fmt.Sprintf("%g", cfg.Index.BM25K1), true
	case "index.bm25_b":
		return fmt.Sprintf("%g", cfg.Index.BM25B), true
	case "index.heading_boost":
		return fmt.Sprintf("%g", cfg.Index.HeadingBoost), true
	case "transport.http.enabled":
		return fmt.Sprintf("%v", cfg.Transport.HTTP.Enabled), true
	case "transport.http.bind":
		return cfg.Transport.HTTP.Bind, true
	case "transport.socket.enabled":
		return fmt.Sprintf("%v", cfg.Transport.Socket.Enabled), true
	case "lifecycle.idle_timeout":
		return cfg.Lifecycle.IdleTimeout, true
	case "lifecycle.shutdown_timeout":
		return cfg.Lifecycle.ShutdownTimeout, true
	case "privacy.telemetry":
		return fmt.Sprintf("%v", cfg.Privacy.Telemetry), true
	case "privacy.remote_sync":
		return cfg.Privacy.RemoteSync, true
	case "privacy.allow_nonlocal_http":
		return fmt.Sprintf("%v", cfg.Privacy.AllowNonlocalHTTP), true
	case "logging.level":
		return cfg.Logging.Level, true
	case "logging.format":
		return cfg.Logging.Format, true
	case "exec.path_prepend":
		return fmt.Sprintf("%v", cfg.Exec.PathPrepend), true
	default:
		return "", false
	}
}

// setConfigField parses and sets a dot-delimited key path.
func setConfigField(cfg *config.Config, key, value string) error {
	switch key {
	case "version":
		return fmt.Errorf("version is read-only")
	case "capture.fs.enabled":
		cfg.Capture.FS.Enabled = value == "true"
	case "capture.fs.debounce_ms":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", value, err)
		}
		cfg.Capture.FS.DebounceMS = v
	case "storage.durability":
		cfg.Storage.Durability = value
	case "storage.max_batch_ms":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", value, err)
		}
		cfg.Storage.MaxBatchMS = v
	case "storage.journal_max_bytes":
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int64 %q: %w", value, err)
		}
		cfg.Storage.JournalMaxBytes = v
	case "storage.compress_rotated":
		cfg.Storage.CompressRotated = value == "true"
	case "retrieval.default_budget":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", value, err)
		}
		cfg.Retrieval.DefaultBudget = v
	case "retrieval.default_format":
		cfg.Retrieval.DefaultFormat = value
	case "index.bm25_k1":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float64 %q: %w", value, err)
		}
		cfg.Index.BM25K1 = v
	case "index.bm25_b":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float64 %q: %w", value, err)
		}
		cfg.Index.BM25B = v
	case "index.heading_boost":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float64 %q: %w", value, err)
		}
		cfg.Index.HeadingBoost = v
	case "transport.http.enabled":
		cfg.Transport.HTTP.Enabled = value == "true"
	case "transport.http.bind":
		cfg.Transport.HTTP.Bind = value
	case "transport.socket.enabled":
		cfg.Transport.Socket.Enabled = value == "true"
	case "lifecycle.idle_timeout":
		cfg.Lifecycle.IdleTimeout = value
	case "lifecycle.shutdown_timeout":
		cfg.Lifecycle.ShutdownTimeout = value
	case "privacy.telemetry":
		cfg.Privacy.Telemetry = value == "true"
	case "privacy.remote_sync":
		cfg.Privacy.RemoteSync = value
	case "privacy.allow_nonlocal_http":
		cfg.Privacy.AllowNonlocalHTTP = value == "true"
	case "logging.level":
		cfg.Logging.Level = value
	case "logging.format":
		cfg.Logging.Format = value
	case "exec.path_prepend":
		return fmt.Errorf("exec.path_prepend is a list; use config get to inspect and manual edit to change")
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func runStats(args []string) int {
	_ = args
	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// NewClient auto-starts daemon if needed
	cl, err := client.NewClient(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// CLI bypasses the daemon-side stats cache: a human running `dfmt
	// stats` twice in a row interprets identical numbers as "DFMT
	// stopped recording", but the cache TTL would hold the same value
	// for 5 seconds. Dashboard polling still gets the cached path
	// because it sets NoCache=false (default).
	resp, err := cl.Stats(ctx, transport.StatsParams{NoCache: true})
	if err != nil {
		if flagJSON {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Println("Could not fetch stats from daemon.")
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(resp))
	} else {
		fmt.Println("DFMT Session Statistics")
		fmt.Println("========================")
		fmt.Printf("Total Events: %d\n\n", resp.EventsTotal)

		if len(resp.EventsByType) > 0 {
			fmt.Println("Events by Type:")
			for t, c := range resp.EventsByType {
				fmt.Printf("  %s: %d\n", t, c)
			}
			fmt.Println()
		}

		if len(resp.EventsByPriority) > 0 {
			fmt.Println("Events by Priority:")
			for p, c := range resp.EventsByPriority {
				fmt.Printf("  %s: %d\n", p, c)
			}
			fmt.Println()
		}

		// LLM token metrics — only meaningful when callers wire usage through dfmt_remember.
		if resp.TotalInputTokens > 0 || resp.TotalOutputTokens > 0 || resp.TokenSavings > 0 {
			fmt.Println("LLM Token Metrics (from dfmt_remember):")
			fmt.Printf("  Input Tokens:  %d\n", resp.TotalInputTokens)
			fmt.Printf("  Output Tokens: %d\n", resp.TotalOutputTokens)
			fmt.Printf("  Cache Savings: %d\n", resp.TokenSavings)
			if resp.CacheHitRate > 0 {
				fmt.Printf("  Cache Hit Rate: %.1f%%\n", resp.CacheHitRate)
			}
			fmt.Println()
		} else if resp.EventsTotal > 0 {
			// Surface why the LLM block is missing instead of letting users
			// guess. The fields are not auto-tracked; the host harness has to
			// pipe usage through dfmt_remember on its side. Closes a recurring
			// "are these stats broken?" question raised during system audits.
			fmt.Println("LLM Token Metrics: not tracked")
			fmt.Println("  Input/output/cache tokens are opt-in: pass input_tokens, output_tokens, and")
			fmt.Println("  cached_tokens to dfmt_remember from the agent harness when it finishes a turn.")
			fmt.Println("  Without that the MCP layer cannot observe API usage on its own — only the byte")
			fmt.Println("  savings below are computed automatically.")
			fmt.Println()
		}

		// MCP byte savings — automatic from sandbox tool calls.
		if resp.TotalRawBytes > 0 {
			fmt.Println("MCP Byte Savings (from intent-filtered tool calls):")
			fmt.Printf("  Raw Bytes:        %d\n", resp.TotalRawBytes)
			fmt.Printf("  Returned Bytes:   %d\n", resp.TotalReturnedBytes)
			fmt.Printf("  Bytes Saved:      %d\n", resp.BytesSaved)
			fmt.Printf("  Compression:      %.1f%%\n", resp.CompressionRatio*100)
			fmt.Println()
		}

		if resp.SessionStart != "" && resp.SessionEnd != "" {
			fmt.Printf("Session: %s -> %s\n", resp.SessionStart, resp.SessionEnd)
		}

		if resp.EventsTotal == 0 {
			fmt.Println("")
			fmt.Println("No events recorded yet. Start using dfmt to record your work.")
		} else {
			fmt.Println()
			fmt.Println("Tip: `dfmt dashboard` opens a live web view of these stats.")
		}
	}

	return 0
}

// runDashboard prints the dashboard URL and optionally opens it in the
// user's default browser. Without this command the dashboard route at
// /dashboard exists in the HTTP server but the user has no way to find
// the loopback port — it's chosen ephemerally and written to .dfmt/port
// in JSON. Closes the long-standing "dashboard zaten çalışmıyor"
// observation: the route was live, just invisible.
//
// Platform support: on Windows the daemon binds 127.0.0.1:<ephemeral>
// so the dashboard is browser-reachable as soon as the daemon is up.
// On Unix the daemon serves HTTP over a Unix socket which browsers
// can't dial; the command prints a friendly hint instead of a URL
// that wouldn't load.
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

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if runtime.GOOS != "windows" {
		// Unix has two modes:
		//   1. Default: daemon binds a Unix socket — browsers cannot
		//      dial that, so printing a 127.0.0.1 URL would mislead.
		//      Print a friendly hint with curl --unix-socket fallback.
		//   2. Opt-in: transport.http.enabled=true + a loopback bind.
		//      The daemon serves HTTP over TCP and the rest of this
		//      function (port-file read → URL print → optional open)
		//      works the same as on Windows.
		// We load the config to decide; a load error is treated as
		// not-opted-in (safer default than crashing the user out of
		// the helpful hint they'd otherwise see).
		cfg, _ := config.Load(proj)
		tcpOptIn := cfg != nil && cfg.Transport.HTTP.Enabled && cfg.Transport.HTTP.Bind != ""
		if !tcpOptIn {
			fmt.Println("Dashboard not browser-accessible on this platform: the daemon")
			fmt.Println("serves HTTP over a Unix socket, which browsers cannot dial.")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  - `dfmt stats` for a CLI snapshot")
			fmt.Printf("  - curl --unix-socket %s http://x/dashboard\n", project.SocketPath(proj))
			fmt.Println()
			fmt.Println("Or enable TCP loopback in .dfmt/config.yaml:")
			fmt.Println("  transport:")
			fmt.Println("    http:")
			fmt.Println("      enabled: true")
			fmt.Println("      bind: 127.0.0.1:8765")
			return 1
		}
	}

	// Auto-start daemon if not running. NewClient performs the spawn +
	// readiness handshake, so by the time it returns the port file is
	// guaranteed to exist with a valid port.
	cl, err := client.NewClient(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	_ = cl // we only need the side-effect of ensuring the daemon is up

	portFile := filepath.Join(proj, ".dfmt", "port")
	data, err := os.ReadFile(portFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read port file %s: %v\n", portFile, err)
		return 1
	}
	var pf transport.PortFile
	if jerr := json.Unmarshal(data, &pf); jerr != nil || pf.Port <= 0 {
		// Older daemons wrote a bare integer; tolerate that format too.
		var port int
		if _, perr := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &port); perr == nil && port > 0 {
			pf.Port = port
		} else {
			fmt.Fprintf(os.Stderr, "error: parse port file %s: %v\n", portFile, jerr)
			return 1
		}
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/dashboard", pf.Port)
	fmt.Println(url)

	if openBrowser {
		if err := openInBrowser(url); err != nil {
			logging.Warnf("open browser: %v", err)
			return 1
		}
	}
	return 0
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

func runTail(args []string) int {
	var follow bool
	var from string
	var limit int
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	fs.BoolVar(&follow, "follow", false, "Follow new events (Ctrl+C to stop)")
	fs.StringVar(&from, "from", "", "Cursor to stream from (empty = all events)")
	fs.IntVar(&limit, "limit", 0, "Max events to print (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
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

	ctx := context.Background()
	if follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	events, err := cl.StreamEvents(ctx, from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	count := 0
	for e := range events {
		dataStr := ""
		if e.Data != nil {
			if path, ok := e.Data["path"].(string); ok {
				dataStr = " " + path
			}
		}
		tags := ""
		if len(e.Tags) > 0 {
			tags = fmt.Sprintf(" #%s", strings.Join(e.Tags, " #"))
		}
		fmt.Printf("[%s] %s%s%s\n", e.Priority, e.TS.Format(time.RFC3339), tags, dataStr)
		count++
		if limit > 0 && count >= limit {
			return 0
		}
		if !follow {
			// Non-follow: drain and return.
			continue
		}
	}
	return 0
}

func runShellInit(args []string) int {
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

func runInstallHooks(_ []string) int {
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

func runSetup(args []string) int {
	var dryRun bool
	var agentOverride string
	var force bool
	var uninstall bool
	var verify bool

	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.BoolVar(&dryRun, "dry-run", false, "Show planned changes")
	fs.StringVar(&agentOverride, "agent", "", "Configure specific agent only")
	fs.BoolVar(&force, "force", false, "Overwrite existing config")
	fs.BoolVar(&uninstall, "uninstall", false, "Remove dfmt configuration")
	fs.BoolVar(&verify, "verify", false, "Verify setup")
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

	// Write MCP config
	mcpPath := filepath.Join(claudeDir, "mcp.json")
	if err := setup.BackupFile(mcpPath); err != nil {
		return fmt.Errorf("backup %s: %w", mcpPath, err)
	}

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"dfmt": map[string]any{
				"command": setup.ResolveDFMTCommand(),
				"args":    []string{"mcp"},
			},
		},
	}
	data, _ := json.MarshalIndent(mcpConfig, "", "  ")
	// 0600: mcp.json tells the host what command to launch as an MCP server.
	if err := os.WriteFile(mcpPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", mcpPath, err)
	}

	// Update manifest
	m, err := setup.LoadManifest()
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	m.AddFile(setup.FileEntry{
		Path:    mcpPath,
		Agent:   "claude-code",
		Version: "1",
	})

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
	if err := setup.BackupFile(mcpPath); err != nil {
		return fmt.Errorf("backup %s: %w", mcpPath, err)
	}

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"dfmt": map[string]any{
				"command": setup.ResolveDFMTCommand(),
				"args":    []string{"mcp"},
			},
		},
	}
	data, _ := json.MarshalIndent(mcpConfig, "", "  ")
	if err := os.WriteFile(mcpPath, data, 0o600); err != nil {
		return err
	}

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

	return nil
}

func runExec(args []string) int {
	var lang string
	var intent string

	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.StringVar(&lang, "lang", "bash", "Language (bash, sh, node, python, etc.)")
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: code required\n")
		return 1
	}

	code := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Connect to daemon for journal logging and token savings
	cl, err := client.NewClient(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Try HTTP daemon call first (journal logged)
	execResp, err := cl.Exec(ctx, transport.ExecParams{
		Code:   code,
		Lang:   lang,
		Intent: intent,
	})
	if err != nil {
		// Fallback to direct sandbox if daemon not available. Honor
		// cfg.Exec.PathPrepend so the CLI fallback isn't stricter than
		// the daemon path.
		var pp []string
		if c, cerr := config.Load(proj); cerr == nil && c != nil {
			pp = c.Exec.PathPrepend
		}
		resp, err := sandbox.NewSandboxWithPolicy(proj, loadProjectPolicy(proj)).
			WithPathPrepend(pp).Exec(ctx, sandbox.ExecReq{
			Code:   code,
			Lang:   lang,
			Intent: intent,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flagJSON {
			fmt.Println(mustMarshalJSON(resp))
		} else {
			fmt.Print(resp.Stdout)
		}
		return 0
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(execResp))
	} else {
		if execResp.Summary != "" {
			fmt.Print(execResp.Summary)
		} else {
			fmt.Print(execResp.Stdout)
		}
		if execResp.Stderr != "" {
			fmt.Fprintf(os.Stderr, "stderr: %s\n", execResp.Stderr)
		}
	}

	return 0
}

func runMCP(_ []string) int {
	// MCP over stdio - read MCP JSON-RPC from stdin, write to stdout.
	//
	// Project resolution: getProject() walks up looking for .dfmt/ or .git/.
	// When neither is found we deliberately do NOT fall back to cwd-as-project
	// — that previously scattered .dfmt/journal.jsonl, index.gob and
	// config.yaml into wherever Claude Code happened to spawn dfmt
	// (typically ~). Instead we run in degraded mode: sandbox tools
	// (exec/read/fetch/glob/grep/edit/write) keep working since they only
	// need a working directory, and memory tools (remember/search/recall/
	// stats) return "no project" at handler level via nil-journal guards.
	proj, projErr := getProject()
	if projErr != nil {
		proj = ""
	}

	// Sandbox needs a cwd regardless of project status — inherit from the
	// process when there's no project so relative paths still resolve.
	sandboxWD := proj
	if sandboxWD == "" {
		if cwd, gerr := os.Getwd(); gerr == nil {
			sandboxWD = cwd
		}
	}

	var (
		journal   core.Journal
		index     *core.Index
		indexPath string
	)

	if proj != "" {
		// Auto-init the project on every MCP startup. Same idempotent
		// steps as `dfmt init`. Failure of any single step is non-fatal.
		if ierr := setup.EnsureProjectInitialized(proj); ierr != nil {
			logging.Warnf("auto-init %s: %v", proj, ierr)
		}
		dfmtDir := filepath.Join(proj, ".dfmt")

		journalPath := filepath.Join(dfmtDir, "journal.jsonl")
		journalOpts := core.JournalOptions{
			Path:     journalPath,
			MaxBytes: 10 * 1024 * 1024,
			Durable:  true,
			BatchMS:  100,
			Compress: true,
		}
		j, jerr := core.OpenJournal(journalPath, journalOpts)
		if jerr != nil {
			logging.Warnf("open journal: %v", jerr)
		}
		journal = j

		indexPath = filepath.Join(dfmtDir, "index.gob")
		cursorPath := filepath.Join(dfmtDir, "index.cursor")
		idx, _, needsRebuild, lerr := core.LoadIndexWithCursor(indexPath, cursorPath)
		if lerr != nil || needsRebuild || idx == nil {
			// Replay the journal so a tokenizer-version bump or corrupt cursor
			// doesn't silently empty the searchable index for this MCP session.
			var rebuilt *core.Index
			var rerr error
			if journal != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							rerr = fmt.Errorf("panic during rebuild: %v", r)
						}
					}()
					rebuilt, _, rerr = core.RebuildIndexFromJournal(context.Background(), journal)
				}()
			}
			if rebuilt != nil && rerr == nil {
				idx = rebuilt
			} else {
				if rerr != nil {
					logging.Warnf("rebuild index from journal: %v", rerr)
				}
				idx = core.NewIndex()
			}
		}
		index = idx
	} else {
		fmt.Fprintln(os.Stderr, "dfmt mcp: no project found — running in degraded mode (sandbox tools only). Open dfmt from a project root or set DFMT_PROJECT to enable memory tools.")
	}

	// Persist index on exit so events ingested during the MCP session survive
	// into the next run. The journal is the durable source of truth, but the
	// in-memory index would otherwise be rebuilt from scratch every session.
	defer func() {
		if journal != nil {
			if hiID, cerr := journal.Checkpoint(context.Background()); cerr == nil {
				if perr := core.PersistIndex(index, indexPath, hiID); perr != nil {
					// Surface the error so operators notice — on Windows a
					// locked target file would otherwise silently drop the
					// snapshot and force a full rebuild next launch.
					logging.Warnf("persist index: %v", perr)
				}
			} else {
				logging.Warnf("journal checkpoint: %v", cerr)
			}
			_ = journal.Close()
		}
	}()

	// Create sandbox and handlers. Pull cfg.Exec.PathPrepend so a project
	// with toolchain dirs configured doesn't hit exit 127 when the daemon
	// inherited a stripped PATH. Config-load failure is non-fatal — only
	// path_prepend is lost; other defaults still apply.
	var pathPrepend []string
	if proj != "" {
		if cfg, cerr := config.Load(proj); cerr == nil && cfg != nil {
			pathPrepend = cfg.Exec.PathPrepend
		}
	}
	// MCP fallback path: the policy is keyed off the project root, not the
	// per-call sandboxWD (which can differ when the agent runs the daemon
	// from outside the project tree). loadProjectPolicy emits warnings to
	// stderr — they end up in the agent's MCP server log.
	sb := sandbox.NewSandboxWithPolicy(sandboxWD, loadProjectPolicy(proj)).
		WithPathPrepend(pathPrepend)
	handlers := transport.NewHandlers(index, journal, sb)
	handlers.SetProject(proj)
	mcp := transport.NewMCPProtocol(handlers)

	// Per-process cancellable context. Canceled on stdin EOF (deferred
	// cancel below) or on SIGINT/SIGTERM (signal goroutine). Threaded into
	// every mcp.Handle call so a long-running tool invocation honors
	// graceful shutdown — pre-fix, handleToolsCall used context.Background()
	// and a Ctrl-C waited for the handler's own per-call timeout.
	mcpCtx, mcpCancel := context.WithCancel(context.Background())
	defer mcpCancel()
	mcpSig := make(chan os.Signal, 1)
	signal.Notify(mcpSig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(mcpSig)
	go func() {
		select {
		case <-mcpSig:
			mcpCancel()
		case <-mcpCtx.Done():
		}
	}()

	// Read/write MCP JSON-RPC. Use bufio.Reader with a per-message cap so
	// an oversized line produces a -32700 parse error and the session
	// continues, instead of bufio.Scanner's ErrTooLong which kills the
	// entire stdio loop.
	const mcpMaxLineBytes = 1 << 20 // 1 MiB per JSON-RPC message
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	writeParseError := func() {
		resp := transport.MCPResponse{
			JSONRPC: "2.0",
			Error: &transport.RPCError{
				Code:    -32700,
				Message: "Parse error",
			},
		}
		_ = json.NewEncoder(writer).Encode(resp)
		_ = writer.Flush()
	}

	// readCapped reads one line up to max bytes. If max is exceeded, the
	// remaining bytes of that line are discarded until the next newline so
	// the next call starts at a fresh message boundary.
	readCapped := func(max int) (line []byte, overflow bool, err error) {
		buf := make([]byte, 0, 512)
		for {
			b, rerr := reader.ReadByte()
			if rerr != nil {
				if rerr == io.EOF && len(buf) == 0 {
					return nil, false, rerr
				}
				return buf, false, rerr
			}
			if b == '\n' {
				return buf, false, nil
			}
			if len(buf) >= max {
				// Drain to next newline so the next iteration starts clean.
				for {
					b2, derr := reader.ReadByte()
					if derr != nil || b2 == '\n' {
						return nil, true, nil
					}
				}
			}
			buf = append(buf, b)
		}
	}

	for {
		line, overflow, err := readCapped(mcpMaxLineBytes)
		if overflow {
			writeParseError()
			continue
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "read error: %v\n", err)
			}
			break
		}
		if len(line) == 0 {
			continue
		}

		// Parse MCP request
		var req transport.MCPRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeParseError()
			continue
		}

		// Handle via MCP protocol with panic recovery. Without this guard a
		// nil-deref or out-of-bounds inside any handler would tear down the
		// entire stdio loop and the agent would silently lose all dfmt tools
		// mid-session — exactly the "MCP fail olunca patlayan sistem" failure
		// mode this project exists to prevent.
		var resp *transport.MCPResponse
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "mcp handle panic recovered: %v\n", r)
					if req.ID != nil {
						resp = &transport.MCPResponse{
							JSONRPC: "2.0",
							Error: &transport.RPCError{
								Code:    -32603,
								Message: fmt.Sprintf("Internal error: %v", r),
							},
							ID: req.ID,
						}
					}
					// Notifications (req.ID == nil) get no response on panic;
					// per JSON-RPC 2.0 they never get one.
				}
			}()
			resp, _ = mcp.Handle(mcpCtx, &req)
		}()

		// JSON-RPC notifications (no ID) yield a nil response and MUST NOT
		// produce any bytes on stdout — writing {} or null would confuse
		// the client's request/response correlation.
		if resp == nil {
			continue
		}

		// Write response
		if err := json.NewEncoder(writer).Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "write error: %v\n", err)
			break
		}
		_ = writer.Flush()
	}

	return 0
}

func runRead(args []string) int {
	var intent string
	var offset, limit int64

	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.Int64Var(&offset, "offset", 0, "Byte offset")
	fs.Int64Var(&limit, "limit", 0, "Max bytes to read")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: path required\n")
		return 1
	}
	path := fs.Arg(0)

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	readResp, err := cl.Read(ctx, transport.ReadParams{
		Path:   path,
		Intent: intent,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		// Fallback to direct sandbox
		resp, err := sandbox.NewSandboxWithPolicy(proj, loadProjectPolicy(proj)).Read(ctx, sandbox.ReadReq{
			Path:   path,
			Intent: intent,
			Offset: offset,
			Limit:  limit,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flagJSON {
			fmt.Println(mustMarshalJSON(resp))
		} else {
			fmt.Print(resp.Content)
		}
		return 0
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(readResp))
	} else {
		if len(readResp.Matches) > 0 {
			for _, m := range readResp.Matches {
				fmt.Printf("%s\n", m.Text)
			}
		} else {
			fmt.Print(readResp.Content)
		}
	}

	return 0
}

func runFetch(args []string) int {
	var intent string
	var method string
	var body string
	var timeout int

	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.StringVar(&method, "method", "GET", "HTTP method")
	fs.StringVar(&body, "body", "", "Request body")
	fs.IntVar(&timeout, "timeout", 30, "Timeout in seconds")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: URL required\n")
		return 1
	}
	url := fs.Arg(0)

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

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	fetchResp, err := cl.Fetch(ctx, transport.FetchParams{
		URL:    url,
		Intent: intent,
		Method: method,
		Body:   body,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(fetchResp))
	} else {
		if len(fetchResp.Matches) > 0 {
			for _, m := range fetchResp.Matches {
				fmt.Printf("%s\n", m.Text)
			}
		} else {
			fmt.Print(fetchResp.Body)
		}
	}

	return 0
}

func runGlob(args []string) int {
	var intent string

	fs := flag.NewFlagSet("glob", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: pattern required\n")
		return 1
	}
	pattern := fs.Arg(0)

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	globResp, err := cl.Glob(ctx, transport.GlobParams{
		Pattern: pattern,
		Intent:  intent,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(globResp))
	} else {
		for _, f := range globResp.Files {
			fmt.Println(f)
		}
	}

	return 0
}

func runGrep(args []string) int {
	var intent string
	var files string
	var caseInsensitive bool

	fs := flag.NewFlagSet("grep", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.StringVar(&files, "files", "*", "File pattern")
	fs.BoolVar(&caseInsensitive, "i", false, "Case insensitive")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: pattern required\n")
		return 1
	}
	pattern := fs.Arg(0)

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	grepResp, err := cl.Grep(ctx, transport.GrepParams{
		Pattern:         pattern,
		Files:           files,
		Intent:          intent,
		CaseInsensitive: caseInsensitive,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(grepResp))
	} else {
		for _, m := range grepResp.Matches {
			fmt.Printf("%s:%d: %s\n", m.File, m.Line, m.Content)
		}
	}

	return 0
}

func runEdit(args []string) int {
	var oldString string

	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	fs.StringVar(&oldString, "old", "", "String to replace (required)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "error: path and new-string required\n")
		return 1
	}
	path := fs.Arg(0)
	newString := fs.Arg(1)

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	editResp, err := cl.Edit(ctx, transport.EditParams{
		Path:      path,
		OldString: oldString,
		NewString: newString,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(editResp))
	} else {
		fmt.Println(editResp.Summary)
	}

	return 0
}

func runWrite(args []string) int {
	var content string

	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	fs.StringVar(&content, "content", "", "Content to write")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: path required\n")
		return 1
	}
	path := fs.Arg(0)

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	writeResp, err := cl.Write(ctx, transport.WriteParams{
		Path:    path,
		Content: content,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(writeResp))
	} else {
		fmt.Println(writeResp.Summary)
	}

	return 0
}

func mustMarshalJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

// loadProjectPolicy returns the merged sandbox policy for `proj`, composed
// from DefaultPolicy() plus any operator override at
// `<proj>/.dfmt/permissions.yaml`. Used by the local-fallback sandbox paths
// in this file when the daemon isn't available — the daemon path uses the
// same call directly in internal/daemon/daemon.go.
//
// Errors and hard-deny override warnings are emitted to stderr; the CLI
// fallback never hard-fails on policy load (a typo in permissions.yaml
// shouldn't keep the agent from running).
func loadProjectPolicy(proj string) sandbox.Policy {
	if proj == "" {
		return sandbox.DefaultPolicy()
	}
	res, err := sandbox.LoadPolicyMerged(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: permissions: %v\n", err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: permissions: %s\n", w)
	}
	return res.Policy
}
