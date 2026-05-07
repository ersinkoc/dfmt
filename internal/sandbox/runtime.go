package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	versionUnknown = "unknown"
	goosWindows    = "windows"
)

// canonicalizePath converts path to a consistent format for error messages.
// On Windows: converts to forward slashes and lowercases drive letter (C:\foo → c:/foo).
// On Unix: returns path as-is.
func canonicalizePath(path string) string {
	if runtime.GOOS == goosWindows {
		// Convert backslashes to forward slashes for consistency
		path = strings.ReplaceAll(path, `\`, "/")
		// Lowercase drive letter if present (e.g., C:/foo → c:/foo)
		if len(path) >= 2 && path[1] == ':' {
			return strings.ToLower(string(path[0])) + path[1:]
		}
	}
	return path
}

// DetectShell returns the detected shell type on the current OS.
// On Windows: checks for PowerShell, CMD, Git Bash, or WSL bash.
// On Unix: checks for bash, zsh, fish, or sh.
func DetectShell() string {
	if runtime.GOOS == goosWindows {
		return detectShellWindows()
	}
	return detectShellUnix()
}

// detectShellWindows detects the shell on Windows.
// Priority: Git Bash > PowerShell > CMD
func detectShellWindows() string {
	// Prefer Git Bash if available (common on Windows dev machines)
	if p, err := lookPath("bash"); err == nil && p != "" {
		return "bash"
	}
	// Check MSYSTEM (Git Bash, MSYS2, MinGW environments)
	if msys := os.Getenv("MSYSTEM"); msys != "" {
		return "bash"
	}
	// PowerShell Core (pwsh) takes priority over Windows PowerShell
	if p, err := lookPath("pwsh"); err == nil && p != "" {
		return "pwsh"
	}
	// Check for PowerShell via PSModulePath
	if psPath := os.Getenv("PSModulePath"); psPath != "" {
		if p, err := lookPath("powershell"); err == nil && p != "" {
			return "powershell"
		}
	}
	// Check COMSPEC for CMD
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		if strings.Contains(strings.ToLower(comspec), "cmd.exe") {
			return "cmd"
		}
	}
	// Fallback: try powershell.exe
	if p, err := lookPath("powershell"); err == nil && p != "" {
		return "powershell"
	}
	return "bash" // Default to bash
}

// detectShellUnix detects the shell on Unix-like systems.
func detectShellUnix() string {
	// Check SHELL environment variable
	if shellEnv := os.Getenv("SHELL"); shellEnv != "" {
		lower := strings.ToLower(shellEnv)
		if strings.Contains(lower, "zsh") {
			return "zsh"
		}
		if strings.Contains(lower, "fish") {
			return "fish"
		}
		if strings.Contains(lower, "bash") {
			return "bash"
		}
		if strings.Contains(lower, "sh") {
			return "sh"
		}
	}
	return "bash" // Default to bash
}

// Runtime represents a detected language runtime.
type Runtime struct {
	Lang       string // Language name (bash, node, python, etc.)
	Executable string // Resolved path to the executable
	Version    string // Detected version string
	Available  bool   // Whether the runtime is available
}

// Runtimes manages detected language runtimes.
type Runtimes struct {
	mu    sync.RWMutex
	cache map[string]Runtime
}

// NewRuntimes creates a new Runtimes manager.
func NewRuntimes() *Runtimes {
	return &Runtimes{
		cache: make(map[string]Runtime),
	}
}

// Probe searches for available runtimes.
func (r *Runtimes) Probe(ctx context.Context) error {
	langs := []string{
		"bash", "sh", "node", "python", "python3", "go",
		"ruby", "perl", "php", "R", "elixir",
	}

	for _, lang := range langs {
		path, err := lookPath(lang)
		if err != nil {
			r.setRuntime(Runtime{Lang: lang, Available: false})
			continue
		}

		version := r.getVersion(ctx, path)
		r.setRuntime(Runtime{
			Lang:       lang,
			Executable: path,
			Version:    version,
			Available:  true,
		})
	}

	return nil
}

// Get returns a runtime by language name.
func (r *Runtimes) Get(lang string) (Runtime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.cache[lang]
	return rt, ok
}

func (r *Runtimes) setRuntime(rt Runtime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[rt.Lang] = rt
}

// getVersion runs <exe> --version and returns the first line.
// 2s timeout: 500ms was too short on cold starts / slow filesystems and made
// every runtime show up as "unknown" in CI.
func (r *Runtimes) getVersion(ctx context.Context, path string) string {
	versionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(versionCtx, path, "--version")

	out, err := cmd.Output()
	if err != nil {
		return versionUnknown
	}

	lines := strings.Split(string(out), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return versionUnknown
}

// lookPath is a testable wrapper around exec.LookPath. On Windows, the
// `bash` lookup is special-cased: Git Bash takes priority over whatever
// exec.LookPath would return (typically C:\Windows\System32\bash.exe,
// the WSL launcher). Reason: WSL bash drops into Linux-PATH semantics
// where unsuffixed Windows binaries (`go`, `node`, `python`) don't
// resolve, so every dfmt_exec on a Windows box that has both Git Bash
// and WSL installed silently 127s on the agent's first toolchain
// invocation. Git Bash uses Windows-PATH semantics and resolves the
// .exe form transparently.
//
// On Windows, lookPath also tries .exe/.cmd/.bat extensions since
// exec.LookPath doesn't always handle these on all Windows configs.
//
// Each call to lookPath performs a fresh exec.LookPath (no internal
// caching). The Runtimes cache stores results of lookPath calls during
// Probe, but if PATH may have changed (e.g., after a permitted exec
// modified .bashrc), call Runtimes.Reload to re-probe with a fresh env.
var lookPath = func(name string) (string, error) {
	if runtime.GOOS == goosWindows {
		// Special case: bash → Git Bash (WSL bash has different PATH semantics)
		if strings.EqualFold(name, "bash") {
			if p, ok := findGitBashWindows(); ok {
				return p, nil
			}
		}
		// Try the raw name first
		if path, err := exec.LookPath(name); err == nil {
			return filepath.Clean(path), nil
		}
		// Windows-specific: try with common extensions
		for _, ext := range []string{".exe", ".cmd", ".bat"} {
			// Skip if name is shorter than extension
			if len(name) < len(ext) {
				continue
			}
			// Already has extension? Skip
			if strings.EqualFold(name[len(name)-len(ext):], ext) {
				continue
			}
			if path, err := exec.LookPath(name + ext); err == nil {
				return filepath.Clean(path), nil
			}
		}
		return "", os.ErrNotExist
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

// gitBashCandidates lists the canonical install paths for Git for
// Windows' bash.exe, in priority order. The MINGW64 layout
// (`<root>\usr\bin\bash.exe`) is what `git --version` resolves to on a
// stock Git for Windows install; the older `<root>\bin\bash.exe`
// layout is included for legacy installs. Both 64-bit and 32-bit
// program-files locations are covered. Exposed as a package var so
// tests can replace it.
var gitBashCandidates = []string{
	`C:\Program Files\Git\usr\bin\bash.exe`,
	`C:\Program Files\Git\bin\bash.exe`,
	`C:\Program Files (x86)\Git\usr\bin\bash.exe`,
	`C:\Program Files (x86)\Git\bin\bash.exe`,
}

// findGitBashWindows returns the first existing bash.exe from
// gitBashCandidates, or ok=false if none exists. Exposed as a var so
// tests can stub it independently of the candidate list.
var findGitBashWindows = func() (string, bool) {
	for _, p := range gitBashCandidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return filepath.Clean(p), true
		}
	}
	return "", false
}

// DetectRuntimes is a convenience function to probe all runtimes.
func DetectRuntimes(ctx context.Context) (*Runtimes, error) {
	r := NewRuntimes()
	if err := r.Probe(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Reload clears the runtime cache and re-probes for available runtimes.
// Call this when the environment (e.g., PATH) may have changed, such as
// after an allowed exec modifies .bashrc or similar shell init files.
// This prevents a cached binary path from staleness after env mutations.
func (r *Runtimes) Reload(ctx context.Context) error {
	r.mu.Lock()
	r.cache = make(map[string]Runtime)
	r.mu.Unlock()
	return r.Probe(ctx)
}
