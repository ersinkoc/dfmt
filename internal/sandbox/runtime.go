package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Runtime represents a detected language runtime.
type Runtime struct {
	Lang      string // Language name (bash, node, python, etc.)
	Executable string // Resolved path to the executable
	Version   string // Detected version string
	Available bool   // Whether the runtime is available
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
			Lang:      lang,
			Executable: path,
			Version:   version,
			Available: true,
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
func (r *Runtimes) getVersion(ctx context.Context, path string) string {
	versionCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(versionCtx, path, "--version")

	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}

	lines := strings.Split(string(out), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return "unknown"
}

// lookPath is a testable wrapper around exec.LookPath.
var lookPath = func(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

// DetectRuntimes is a convenience function to probe all runtimes.
func DetectRuntimes(ctx context.Context) (*Runtimes, error) {
	r := NewRuntimes()
	if err := r.Probe(ctx); err != nil {
		return nil, err
	}
	return r, nil
}
