package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
)

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









