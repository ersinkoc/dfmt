package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/setup"
)

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

// runRemove undoes dfmt init for a project: removes .dfmt/, .claude/settings.json
// dfmt block, and AGENTS.md/CLAUDE.md dfmt block. Does NOT touch agent MCP configs
// (use `dfmt setup --uninstall` for that). Safe to re-run — idempotent.
func runRemove(args []string) int {
	dir, _ := os.Getwd()
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.StringVar(&dir, "dir", "", "Project directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	// Ask the daemon to evict its in-memory cache for this project
	// BEFORE we delete .dfmt/. Without this, a running global daemon
	// would keep open file handles to the soon-to-be-deleted journal/
	// index, the dashboard switcher would still list the removed
	// project, and the next Resources(dir) call would race against the
	// missing directory.
	//
	// Best-effort: only attempt when a daemon is reachable; never spawn
	// one just for cleanup. Errors are logged but never block the
	// on-disk removal — the cache evicts itself the next time the
	// daemon restarts.
	abs, _ := filepath.Abs(dir)
	if abs != "" && client.DaemonRunning(abs) {
		if cl, cerr := client.NewClient(abs); cerr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			if _, derr := cl.DropProject(ctx, abs); derr != nil {
				fmt.Fprintf(os.Stderr, "warn: daemon DropProject(%s): %v\n", abs, derr)
			}
			cancel()
		}
	}

	if err := setup.RemoveProject(dir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Removed DFMT from %s\n", dir)
	fmt.Println("Note: run `dfmt setup --uninstall` to also remove agent MCP configs.")
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

	// Pre-step: purge legacy DFMT fossils from any pre-existing Claude
	// settings.json (project + user-global). Without this, downstream
	// merges in EnsureProjectInitialized / configureClaudeCode preserve
	// stale entries (old deny-triplet, dotted MCP names, drifted hook
	// command paths). Failures are non-fatal — surface as warnings so
	// quickstart still completes if a settings file is unreadable.
	resolved := setup.ResolveDFMTCommand()
	for _, p := range []string{
		filepath.Join(dir, ".claude", "settings.json"),
		filepath.Join(setup.HomeDir(), ".claude", "settings.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if rep, perr := setup.PurgeLegacyClaudeSettings(p, resolved); perr != nil {
			fmt.Fprintf(os.Stderr, "      warning: purge %s: %v\n", p, perr)
		} else if len(rep.Removed)+len(rep.Adjusted) > 0 {
			fmt.Printf("      purged %s: removed=%d adjusted=%d\n",
				p, len(rep.Removed), len(rep.Adjusted))
		}
	}

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
