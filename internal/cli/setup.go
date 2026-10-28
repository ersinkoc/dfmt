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

	hooksDir, err := setup.GitHooksDir(proj)
	if errors.Is(err, setup.ErrHooksManaged) {
		// husky, lefthook, a shared hooks dir: those files are tracked in
		// the repo or owned by a hook manager. Say what to add instead of
		// writing hooks git would never run from .git/hooks.
		fmt.Fprintf(os.Stderr, "Git hooks in this repository run from %s (core.hooksPath), which dfmt does not modify.\n", hooksDir)
		fmt.Fprintln(os.Stderr, "To record commits, add this line to your post-commit hook there:")
		fmt.Fprintf(os.Stderr, "  %s\n", gitCommitCaptureLine)
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	m, err := setup.LoadManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load manifest: %v\n", err)
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
		// Manifest row first, as for agent configs: a failed save must not
		// leave a hook that uninstall cannot find.
		m.AddFile(setup.FileEntry{Path: dst, Agent: "git", Version: "1", Kind: setup.FileKindGitHook})
		if err := setup.SaveManifest(m); err != nil {
			fmt.Fprintf(os.Stderr, "error: save manifest: %v\n", err)
			return 1
		}
		action, err := setup.InstallGitHook(hooksDir, hook, content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", hook, err)
			return 1
		}
		fmt.Printf("%s: %s\n", hook, action)
	}

	fmt.Println("Git hooks installed")
	return 0
}

// gitCommitCaptureLine is the one line a hook-manager-owned post-commit
// hook needs to record commits.
const gitCommitCaptureLine = `command -v dfmt >/dev/null && ` +
	`dfmt capture git commit "$(git rev-parse HEAD)" "$(git log -1 --format=%s)" 2>/dev/null &`

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
		if errors.Is(err, errSkipCapture) {
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

	// acquireBackend, like every other daemon-backed command: it brings
	// the global daemon up when it is not running. client.NewClient does
	// not, so after an idle-exit every git hook's capture failed to dial
	// and — the hooks discard stderr — commits, checkouts and pushes were
	// dropped without a trace until some other command restarted it.
	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)
	params.ProjectID = proj

	if _, err := backend.Remember(ctx, params); err != nil {
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

// runHook answers `dfmt hook claude-code pretooluse`, which installs from
// before v0.9.1 still invoke until the next `dfmt setup` / MCP start strips
// the hook (setup.PurgeClaudeCodePreToolUseHook). It prints `{}` — the empty,
// valid PreToolUse reply, i.e. "no opinion" — and exits without touching the
// daemon. The old body emitted fields Claude Code does not read, dialed the
// daemon on every native tool call, and fired a journal write from a
// goroutine that main's exit usually killed.
func runHook(_ []string) int {
	_, _ = io.Copy(io.Discard, io.LimitReader(os.Stdin, 1<<20))
	fmt.Fprintln(os.Stdout, `{}`)
	return 0
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
	// worked even when one agent's config write hit a permission
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
		// No manifest rows does not mean nothing was written: the shared
		// ~/.claude.json and settings.json entries are not manifest rows,
		// and a daemon may still be running. Fall through to those.
		fmt.Println("No tracked files; checking shared agent configs")
	}

	// Stop the host-wide daemon before stripping configs. Without this,
	// the global daemon keeps holding open journal/index handles for
	// every project it had cached — uninstall finishes, but the
	// operator sees a stray dfmt process in `tasklist` for up to the
	// configured idle timeout. Also avoids the awkward window where
	// agent MCP configs are gone but the daemon is still serving stale
	// requests routed through them.
	if globalDaemonPresent() {
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

		case setup.FileKindGitHook:
			if err := setup.UninstallGitHook(f.Path); err != nil {
				fmt.Fprintf(os.Stderr, "error removing git hook %s: %v\n", f.Path, err)
				continue
			}
			fmt.Printf("removed git hook: %s\n", f.Path)

		case setup.FileKindAgentConfig:
			target, ok := setup.AgentTargetFor(f.Agent)
			if !ok {
				logging.Warnf("unknown agent %q for %s; skipping", f.Agent, f.Path)
				continue
			}
			target.Path = f.Path
			if err := target.Uninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "error removing dfmt from %s: %v\n", f.Path, err)
				continue
			}
			fmt.Printf("removed dfmt server: %s\n", f.Path)

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

	// Same for the hooks and permissions merged into Claude Code's
	// settings.json files (SET-4): left in place, Claude Code keeps
	// running `dfmt hook claude-code pretooluse` before every tool call
	// after dfmt is gone.
	for _, p := range claudeSettingsToStrip(m) {
		changed, err := setup.StripClaudeSettings(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error removing dfmt hooks from %s: %v\n", p, err)
			continue
		}
		if changed {
			fmt.Printf("removed dfmt hooks: %s\n", p)
		}
	}

	// Clear manifest
	if err := setup.SaveManifest(&setup.Manifest{Version: 1}); err != nil {
		logging.Warnf("clear manifest: %v", err)
	}
	fmt.Println("Uninstall complete")
	return 0
}

// claudeSettingsToStrip lists the Claude Code settings.json files dfmt may
// have merged hooks into: the user-global one, and the project-level one
// of every project the manifest knows (by its instruction-file rows, which
// sit at the project root or in its .github directory).
func claudeSettingsToStrip(m *setup.Manifest) []string {
	paths := []string{filepath.Join(setup.HomeDir(), ".claude", "settings.json")}
	seen := map[string]bool{paths[0]: true}
	for _, f := range m.Files {
		if f.Kind != setup.FileKindStrip {
			continue
		}
		root := filepath.Dir(f.Path)
		if filepath.Base(root) == ".github" {
			root = filepath.Dir(root)
		}
		p := filepath.Join(root, ".claude", "settings.json")
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
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
		if f.Kind == setup.FileKindAgentConfig {
			// Re-read the entry: a file that exists but lost the dfmt
			// server (an agent rewriting its own config, a hand edit)
			// used to pass with a check mark.
			if target, ok := setup.AgentTargetFor(f.Agent); ok {
				target.Path = f.Path
				if verr := target.Verify(); verr != nil {
					fmt.Printf("✗ %s (%s: %v)\n", f.Path, f.Agent, verr)
					allOk = false
				} else {
					fmt.Printf("✓ %s (%s)\n", f.Path, f.Agent)
				}
				continue
			}
		}
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
// PurgeClaudeCodePreToolUseHook for the global file, EnsureProjectInitialized
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

	// Global: dfmt writes nothing to ~/.claude/settings.json any more
	// (writeProjectClaudeSettings refuses it on purpose); only the retired
	// PreToolUse hook is removed.
	globalDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(globalDir); err == nil {
		if werr := setup.PurgeClaudeCodePreToolUseHook(globalDir); werr != nil {
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
	default:
		return configureAgentTarget(agent.ID)
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
	// without a matching uninstall row.
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
	// Remove the PreToolUse hook earlier versions wrote to
	// ~/.claude/settings.json; it never had an effect (see
	// setup.PurgeClaudeCodePreToolUseHook).
	if err := setup.PurgeClaudeCodePreToolUseHook(claudeDir); err != nil {
		logging.Warnf("remove legacy PreToolUse hook from %s: %v", claudeDir, err)
	}
	m.RecordAgent("claude-code", claudeDir)
	if err := setup.SaveManifest(m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	return nil
}

// configureAgentTarget splices the dfmt server into the config file the
// agent actually reads (setup.AgentTargetFor). Before 0.9.0 every agent but
// Claude Code got `<dir>/mcp.json`, which only Cursor reads; that legacy
// file is cleaned up here so it does not linger as a decoy.
func configureAgentTarget(agentID string) error {
	target, ok := setup.AgentTargetFor(agentID)
	if !ok {
		return fmt.Errorf("unsupported agent: %s", agentID)
	}
	// V-14 (F-Setup-3): record the manifest row before writing, so a
	// SaveManifest failure can never leave an entry uninstall cannot find.
	m, err := setup.LoadManifest()
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	m.AddFile(setup.FileEntry{
		Path:    target.Path,
		Agent:   agentID,
		Version: "1",
		Kind:    setup.FileKindAgentConfig,
	})
	m.RecordAgent(agentID, filepath.Dir(target.Path))
	if err := setup.SaveManifest(m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}
	if err := target.Install(); err != nil {
		return fmt.Errorf("configure %s: %w", target.Path, err)
	}
	if legacy := setup.LegacyAgentConfigPath(agentID); legacy != "" && !samePathCLI(legacy, target.Path) {
		if err := setup.UnmergeMCPServerEntry(legacy); err != nil {
			logging.Warnf("clean legacy %s: %v", legacy, err)
		}
		// Drop its manifest row too. It stayed behind after the decoy was
		// cleaned, so `dfmt doctor` reported the agent as "1/3 files
		// missing" and told the user to run `dfmt setup --force`, which
		// could never bring back a file dfmt no longer writes.
		if m, err := setup.LoadManifest(); err == nil && m.RemoveFile(legacy) {
			if err := setup.SaveManifest(m); err != nil {
				logging.Warnf("save manifest: %v", err)
			}
		}
	}
	return nil
}
