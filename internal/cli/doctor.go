package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/redact"
	"github.com/ersinkoc/dfmt/internal/sandbox"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// doctorCheck is one row of the `dfmt doctor` health report. The fn
// closure does the inspection; doctor's main loop is just iterate +
// render. Keeping this at file scope (instead of inline inside
// runDoctor) lets coreChecks be a pure data builder we can extend.
type doctorCheck struct {
	name string
	fn   func() (ok bool, detail string)
}

// doctorPaths bundles the project-relative paths runDoctor's checks
// close over. Computing them once up-front and passing them through a
// struct avoids re-deriving them per-check and avoids 8-parameter helper
// signatures.
type doctorPaths struct {
	dir         string
	dfmtDir     string
	pid         string
	port        string
	journal     string
	index       string
	lock        string
	daemonAlive bool // pre-computed liveness — see runDoctor
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
	paths := doctorPaths{
		dir:     dir,
		dfmtDir: dfmtDir,
		pid:     filepath.Join(dfmtDir, "daemon.pid"),
		port:    filepath.Join(dfmtDir, "port"),
		journal: filepath.Join(dfmtDir, "journal.jsonl"),
		index:   filepath.Join(dfmtDir, "index.gob"),
		lock:    filepath.Join(dfmtDir, "lock"),
		// Pre-compute liveness once so all checks see a consistent view —
		// otherwise a daemon that exits mid-doctor would produce
		// contradictory "running but stale lock" output.
		daemonAlive: client.DaemonRunning(dir),
	}

	allOk := runChecks(coreChecks(paths))

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

	if paths.daemonAlive {
		fmt.Println("[i] Daemon running")
	} else {
		fmt.Println("[i] Daemon stopped (auto-starts on next command)")
	}

	if !allOk {
		return 1
	}
	return 0
}

// runChecks executes the project-state row of the doctor report and
// prints a ✓/✗ line per check. Returns false if any check failed so the
// caller can flip the overall exit code.
func runChecks(checks []doctorCheck) bool {
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
	return allOk
}

// coreChecks returns the project-state rows runDoctor prints first —
// project discovery, config loading, .dfmt directory, Go toolchain age,
// journal/index file health, port/PID/lock consistency, and the two
// optional override files (redact, permissions). Each closure captures
// `p` so paths are resolved once at the call site.
func coreChecks(p doctorPaths) []doctorCheck {
	return []doctorCheck{
		{"Project exists", func() (bool, string) {
			proj, err := project.Discover(p.dir)
			if err != nil {
				return false, err.Error()
			}
			return true, proj
		}},
		{"Config valid", func() (bool, string) {
			cfg, err := config.Load(p.dir)
			if err != nil {
				return false, err.Error()
			}
			if cfg == nil {
				return false, "config nil"
			}
			return true, fmt.Sprintf("durability=%s", cfg.Storage.Durability)
		}},
		{".dfmt directory", func() (bool, string) {
			fi, err := os.Stat(p.dfmtDir)
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
			if _, err := os.Stat(p.journal); os.IsNotExist(err) {
				return true, "(none yet — created on first event)"
			}
			f, err := os.Open(p.journal)
			if err != nil {
				return false, err.Error()
			}
			if err := f.Close(); err != nil {
				return false, fmt.Sprintf("journal exists but could not close: %v", err)
			}
			return true, ""
		}},
		{"Index file readable", func() (bool, string) {
			fi, err := os.Stat(p.index)
			if os.IsNotExist(err) {
				return true, "(none yet — built on first daemon start)"
			}
			if err != nil {
				return false, err.Error()
			}
			f, err := os.Open(p.index)
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
			if _, err := os.Stat(p.port); os.IsNotExist(err) {
				if p.daemonAlive {
					return false, "daemon is alive but port file missing"
				}
				return true, "(no daemon, no port file — OK)"
			}
			if !p.daemonAlive {
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
				if p.daemonAlive {
					return false, "global daemon is alive but ~/.dfmt/daemon.pid is missing"
				}
			}
			data, err := os.ReadFile(p.pid)
			if os.IsNotExist(err) {
				if p.daemonAlive {
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
			if !p.daemonAlive {
				return false, fmt.Sprintf("stale PID %d (process not running; auto-cleaned on next start)", pid)
			}
			return true, fmt.Sprintf("PID %d", pid)
		}},
		{"Redact override (.dfmt/redact.yaml)", func() (bool, string) {
			// ADR-0014. Same shape as the permissions row below.
			_, res, err := redact.LoadProjectRedactor(p.dir)
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
			res, err := sandbox.LoadPolicyMerged(p.dir)
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
			if _, err := os.Stat(p.lock); os.IsNotExist(err) {
				return true, "(no lock — OK)"
			}
			if p.daemonAlive {
				return true, "(held by running daemon)"
			}
			// Lock file present, daemon dead: try to acquire it. If we
			// can, the OS released the flock when the daemon died — the
			// file is benign and a fresh daemon will reuse it. If we can
			// NOT acquire, something else is holding it; the next Start
			// will fail.
			lock, lerr := daemon.AcquireLock(p.dir)
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
}

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
