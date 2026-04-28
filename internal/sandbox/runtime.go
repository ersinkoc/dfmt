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

const versionUnknown = "unknown"

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
var lookPath = func(name string) (string, error) {
	if runtime.GOOS == "windows" && strings.EqualFold(name, "bash") {
		if p, ok := findGitBashWindows(); ok {
			return p, nil
		}
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
