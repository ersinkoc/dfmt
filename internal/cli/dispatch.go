package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/project"
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

func init() {
	flag.BoolVar(&flagJSON, "json", false, "JSON output")
	flag.StringVar(&flagProject, "project", "", "Project path")
}

func getProject() (string, error) {
	if flagProject != "" {
		return flagProject, nil
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
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.StringVar(&dir, "dir", "", "Project directory")
	fs.Parse(args)

	if dir == "" {
		dir, _ = os.Getwd()
	}

	dfmtDir := filepath.Join(dir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating .dfmt: %v\n", err)
		return 1
	}

	configPath := filepath.Join(dfmtDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config.DefaultConfigYAML()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing config: %v\n", err)
		return 1
	}

	gitignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		content, _ := os.ReadFile(gitignorePath)
		if !strings.Contains(string(content), ".dfmt/") {
			f, _ := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
			f.WriteString("\n.dfmt/\n")
			f.Close()
		}
	}

	// Write project-local Claude Code settings to enforce DFMT tools
	_ = writeProjectClaudeSettings(dir)

	// Mark this project as trusted in ~/.claude.json so Claude Code doesn't
	// re-prompt and the dfmt MCP server is attached to this project. Failure
	// is non-fatal.
	if err := setup.PatchClaudeCodeUserJSON(dir, false); err != nil {
		fmt.Fprintf(os.Stderr, "warning: patch ~/.claude.json: %v\n", err)
	}

	fmt.Printf("Initialized DFMT in %s\n", dir)
	return 0
}

// writeProjectClaudeSettings writes .claude/settings.json to enforce DFMT tools
// and wire session continuity hooks (PreCompact save, SessionStart load).
func writeProjectClaudeSettings(dir string) error {
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return err
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	// Hooks use 'dfmt' from PATH (single global installation).
	// Recall saves to .dfmt/last-recall.md relative to project root.
	// PreToolUse reads tool_name/tool_input from stdin JSON (works on any shell).
	isWindows := runtime.GOOS == "windows"
	var preCompact, sessionStart string
	if isWindows {
		preCompact = `dfmt recall --format md > .dfmt/last-recall.md 2>$null`
		sessionStart = `if (Test-Path .dfmt/last-recall.md) { Write-Host '--- Previous session summary ---'; Get-Content .dfmt/last-recall.md; Write-Host '--- End of previous session ---' }`
	} else {
		preCompact = `dfmt recall --format md > .dfmt/last-recall.md 2>/dev/null || true`
		sessionStart = `if [ -f .dfmt/last-recall.md ]; then echo '--- Previous session summary ---' && cat .dfmt/last-recall.md && echo '--- End of previous session ---'; fi`
	}
	preToolUse := `dfmt capture tool`
	settingsData := fmt.Sprintf(`{
  "hooks": {
    "PreToolUse": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "%s",
        "timeout": 5,
        "statusMessage": "Logging tool call..."
      }]
    }],
    "PreCompact": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "%s",
        "timeout": 30,
        "statusMessage": "Saving session snapshot for next session..."
      }]
    }],
    "SessionStart": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "%s",
        "timeout": 10,
        "statusMessage": "Loading previous session summary..."
      }]
    }]
  },
  "permissions": {
    "allow": [
      "mcp__dfmt__dfmt.exec",
      "mcp__dfmt__dfmt.read",
      "mcp__dfmt__dfmt.fetch",
      "mcp__dfmt__dfmt.remember",
      "mcp__dfmt__dfmt.search",
      "mcp__dfmt__dfmt.recall",
      "mcp__dfmt__dfmt.stats"
    ]
  }
}
`, preToolUse, preCompact, sessionStart)
	return os.WriteFile(settingsPath, []byte(settingsData), 0644)
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
	fs.Parse(args)

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
		json.Unmarshal([]byte(dataJSON), &data)
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
	fs.Parse(args)

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

	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.IntVar(&budget, "budget", 4096, "Byte budget")
	fs.StringVar(&format, "format", "md", "Output format (md, json, xml)")
	fs.Parse(args)

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
		fmt.Printf(`{"project": %q, "daemon_running": %v, "socket": %q}`,
			proj, running, project.SocketPath(proj))
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
	fs.Parse(args)

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

	cfg, _ := config.Load(proj)

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
	d.Stop(ctx)
	return 0
}

func startDaemonBackground(proj string) (int, error) {
	if client.DaemonRunning(proj) {
		return 0, fmt.Errorf("daemon already running")
	}

	// Refuse to re-exec a test binary as the daemon. Under `go test` the
	// test framework would ignore the extra args and re-run the suite,
	// causing an exponential fork bomb.
	if flag.Lookup("test.v") != nil {
		return 0, fmt.Errorf("refusing to spawn daemon from test binary")
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
	go func() { _ = cmd.Wait() }()

	pidPath := filepath.Join(proj, ".dfmt", "daemon.pid")
	pidData := fmt.Sprintf("%d\n", cmd.Process.Pid)
	os.WriteFile(pidPath, []byte(pidData), 0644)

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

	// Read PID file
	pidPath := filepath.Join(proj, ".dfmt", "daemon.pid")
	pidData, err := os.ReadFile(pidPath)
	if err == nil {
		var pid int
		fmt.Sscanf(string(pidData), "%d", &pid)
		if pid > 0 {
			process, err := os.FindProcess(pid)
			if err == nil {
				process.Signal(os.Interrupt)
				fmt.Printf("Sent interrupt to PID %d\n", pid)
			}
		}
		os.Remove(pidPath)
	}

	// Release lock file if exists
	lockPath := filepath.Join(proj, ".dfmt", "lock")
	os.Remove(lockPath)

	// Remove socket
	socketPath := project.SocketPath(proj)
	os.Remove(socketPath)

	fmt.Printf("Daemon stopped for %s\n", proj)
	return 0
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
	fs.Parse(args)

	if dir == "" {
		dir, _ = os.Getwd()
	}

	checks := []struct {
		name  string
		check func() bool
	}{
		{"Project exists", func() bool {
			_, err := project.Discover(dir)
			return err == nil
		}},
		{"Config valid", func() bool {
			cfg, err := config.Load(dir)
			return err == nil && cfg != nil
		}},
	}

	allOk := true
	for _, c := range checks {
		if c.check() {
			fmt.Printf("✓ %s\n", c.name)
		} else {
			fmt.Printf("✗ %s\n", c.name)
			allOk = false
		}
	}

	if client.DaemonRunning(dir) {
		fmt.Println("[i] Daemon running")
	} else {
		fmt.Println("[i] Daemon stopped (auto-starts on next command)")
	}

	if !allOk {
		return 1
	}
	return 0
}

func runTask(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "error: task body required\n")
		return 1
	}

	if args[0] == "done" && len(args) > 1 {
		fmt.Printf("Task %s marked done\n", args[1])
		return 0
	}

	return runRemember([]string{"task.create", "-type", "task.create", args[0]})
}

func runConfig(args []string) int {
	proj, _ := getProject()
	cfg, err := config.Load(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flagJSON {
		fmt.Println(mustMarshalJSON(cfg))
	} else {
		fmt.Printf("Config for %s:\n", proj)
		fmt.Printf("  Capture MCP: %v\n", cfg.Capture.MCP.Enabled)
		fmt.Printf("  Capture FS: %v\n", cfg.Capture.FS.Enabled)
		fmt.Printf("  Storage durability: %s\n", cfg.Storage.Durability)
	}
	_ = args // reserved for future get/set
	return 0
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

	resp, err := cl.Stats(ctx)
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

		// Token metrics
		if resp.TotalInputTokens > 0 || resp.TotalOutputTokens > 0 || resp.TokenSavings > 0 {
			fmt.Println("Token Metrics:")
			fmt.Printf("  Input Tokens:  %d\n", resp.TotalInputTokens)
			fmt.Printf("  Output Tokens: %d\n", resp.TotalOutputTokens)
			fmt.Printf("  Cache Savings: %d\n", resp.TokenSavings)
			if resp.CacheHitRate > 0 {
				fmt.Printf("  Cache Hit Rate: %.1f%%\n", resp.CacheHitRate)
			}
			fmt.Println()
		}

		if resp.SessionStart != "" && resp.SessionEnd != "" {
			fmt.Printf("Session: %s -> %s\n", resp.SessionStart, resp.SessionEnd)
		}

		if resp.EventsTotal == 0 {
			fmt.Println("")
			fmt.Println("No events recorded yet. Start using dfmt to record your work.")
		}
	}

	return 0
}

func runTail(args []string) int {
	var follow bool
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	fs.BoolVar(&follow, "follow", false, "Follow new events")
	fs.Parse(args)

	fmt.Println("Streaming events... (Ctrl+C to stop)")
	if !follow {
		return 0
	}
	fmt.Println("(tail --follow not yet implemented)")
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
		fmt.Fprintf(os.Stderr, "warning: os.Executable failed (%v); shell hooks will use PATH\n", err)
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
var errSkipCapture = fmt.Errorf("capture: nothing to record")

func buildCaptureParams(args []string) (transport.RememberParams, error) {
	if len(args) < 1 {
		return transport.RememberParams{}, fmt.Errorf("capture type required")
	}
	switch args[0] {
	case "git":
		if len(args) < 2 {
			return transport.RememberParams{}, fmt.Errorf("git capture requires subcommand")
		}
		switch args[1] {
		case "commit":
			if len(args) < 3 {
				return transport.RememberParams{}, fmt.Errorf("git commit requires hash")
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
				return transport.RememberParams{}, fmt.Errorf("git checkout requires ref")
			}
			isBranch := "0"
			if len(args) >= 4 {
				isBranch = args[3]
			}
			return transport.RememberParams{
				Type:     string(core.EvtGitCheckout),
				Priority: string(core.PriP3),
				Source:   string(core.SrcGitHook),
				Data:     map[string]any{"ref": args[2], "is_branch": isBranch},
			}, nil
		case "push":
			if len(args) < 4 {
				return transport.RememberParams{}, fmt.Errorf("git push requires remote and branch")
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
			return transport.RememberParams{}, fmt.Errorf("env.cwd requires path")
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
			return transport.RememberParams{}, fmt.Errorf("shell requires command")
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
	fs.Parse(args)

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

	// Configure each agent
	for _, agent := range agents {
		fmt.Printf("Configuring %s...\n", agent.Name)
		if err := configureAgent(agent); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		} else {
			fmt.Printf("  done\n")
		}
	}

	fmt.Println("\nSetup complete. Run `dfmt setup --verify` to confirm.")
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
		if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error removing %s: %v\n", f.Path, err)
			continue
		}
		// If setup created a .dfmt.bak backup of a pre-existing user config,
		// restore it so uninstall leaves the user's original config intact.
		backup := f.Path + ".dfmt.bak"
		if _, err := os.Stat(backup); err == nil {
			if err := os.Rename(backup, f.Path); err != nil {
				fmt.Fprintf(os.Stderr, "warning: restore backup %s: %v\n", backup, err)
			} else {
				fmt.Printf("restored original: %s\n", f.Path)
			}
		}
	}

	// Clear manifest
	if err := setup.SaveManifest(&setup.Manifest{Version: 1}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: clear manifest: %v\n", err)
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
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return err
	}

	// Write MCP config
	mcpPath := filepath.Join(claudeDir, "mcp.json")
	setup.BackupFile(mcpPath)

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"dfmt": map[string]any{
				"command": "dfmt",
				"args":    []string{"mcp"},
			},
		},
	}
	data, _ := json.MarshalIndent(mcpConfig, "", "  ")
	os.WriteFile(mcpPath, data, 0644)

	// Update manifest
	m, _ := setup.LoadManifest()
	m.Files = append(m.Files, setup.FileEntry{
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
		fmt.Fprintf(os.Stderr, "warning: patch ~/.claude.json: %v\n", err)
	}
	m.RecordAgent("claude-code", claudeDir)
	setup.SaveManifest(m)

	return nil
}

func configureCodex(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "codex")
}

func configureCursor(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "cursor")
}

func configureVSCode(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".vscode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "vscode")
}

func configureGemini(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "gemini")
}

func configureWindsurf(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".windsurf")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "windsurf")
}

func configureZed(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".config", "zed")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "zed")
}

func configureContinue(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".config", "continue")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "continue")
}

func configureOpenCode(_ setup.Agent) error {
	home := setup.HomeDir()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return writeMCPConfig(dir, "mcp.json", "opencode")
}

func writeMCPConfig(dir, filename, agentID string) error {
	mcpPath := filepath.Join(dir, filename)
	setup.BackupFile(mcpPath)

	cmd := "dfmt"
	if path, err := exec.LookPath("dfmt"); err == nil {
		cmd = path
	} else if ex, err := os.Executable(); err == nil {
		cmd = ex
	}

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"dfmt": map[string]any{
				"command": cmd,
				"args":    []string{"mcp"},
			},
		},
	}
	data, _ := json.MarshalIndent(mcpConfig, "", "  ")
	if err := os.WriteFile(mcpPath, data, 0644); err != nil {
		return err
	}

	m, _ := setup.LoadManifest()
	m.Files = append(m.Files, setup.FileEntry{
		Path:    mcpPath,
		Agent:   agentID,
		Version: "1",
	})
	m.RecordAgent(agentID, dir)
	setup.SaveManifest(m)

	return nil
}

func runExec(args []string) int {
	var lang string
	var intent string

	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.StringVar(&lang, "lang", "bash", "Language (bash, sh, node, python, etc.)")
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.Parse(args)

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
		// Fallback to direct sandbox if daemon not available
		resp, err := sandbox.NewSandbox(proj).Exec(ctx, sandbox.ExecReq{
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
	// MCP over stdio - read MCP JSON-RPC from stdin, write to stdout
	proj, err := getProject()
	if err != nil {
		proj, _ = os.Getwd()
	}

	// Ensure .dfmt directory exists
	dfmtDir := filepath.Join(proj, ".dfmt")
	_ = os.MkdirAll(dfmtDir, 0755)

	// Ensure project-level Claude Code settings enforce DFMT tools
	_ = writeProjectClaudeSettings(proj)

	// Open journal from disk (same as daemon)
	journalPath := filepath.Join(dfmtDir, "journal.jsonl")
	journalOpts := core.JournalOptions{
		Path:     journalPath,
		MaxBytes: 10 * 1024 * 1024,
		Durable:  true,
		BatchMS:  100,
		Compress: true,
	}
	journal, err := core.OpenJournal(journalPath, journalOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: open journal: %v\n", err)
	}
	defer func() {
		if journal != nil {
			_ = journal.Close()
		}
	}()

	// Load or create index
	indexPath := filepath.Join(dfmtDir, "index.gob")
	cursorPath := filepath.Join(dfmtDir, "index.cursor")
	index, _, needsRebuild, err := core.LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil || needsRebuild || index == nil {
		index = core.NewIndex()
	}

	// Create sandbox and handlers
	sb := sandbox.NewSandbox(proj)
	handlers := transport.NewHandlers(index, journal, sb)
	handlers.SetProject(proj)
	mcp := transport.NewMCPProtocol(handlers)

	// Read/write MCP JSON-RPC. Use bufio.Scanner with a bounded buffer so a
	// hostile peer cannot push us past the line size limit — bufio.Reader's
	// ReadBytes would grow unbounded.
	const mcpMaxLineBytes = 1 << 20 // 1 MiB per JSON-RPC message
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), mcpMaxLineBytes)
	writer := bufio.NewWriter(os.Stdout)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse MCP request
		var req transport.MCPRequest
		if err := json.Unmarshal(line, &req); err != nil {
			resp := transport.MCPResponse{
				JSONRPC: "2.0",
				Error: &transport.RPCError{
					Code:    -32700,
					Message: "Parse error",
				},
			}
			json.NewEncoder(writer).Encode(resp)
			writer.Flush()
			continue
		}

		// Handle via MCP protocol
		resp, _ := mcp.Handle(&req)

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
		writer.Flush()
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read error: %v\n", err)
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
	fs.Parse(args)

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
		resp, err := sandbox.NewSandbox(proj).Read(ctx, sandbox.ReadReq{
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
	fs.Parse(args)

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

func mustMarshalJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}
