package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/safefs"
)

// DaemonEntry represents a running daemon in the registry.
type DaemonEntry struct {
	ProjectPath string    `json:"project_path"`
	PID         int       `json:"pid"`
	Port        int       `json:"port,omitempty"`        // Windows TCP port
	SocketPath  string    `json:"socket_path,omitempty"` // Unix socket
	StartedAt   time.Time `json:"started_at"`
	LastSeen    time.Time `json:"last_seen"`
}

// Registry tracks all running daemons globally.
type Registry struct {
	mu       sync.Mutex
	daemons  map[string]DaemonEntry
	filePath string
}

// Global registry instance.
var (
	globalRegistry     *Registry
	globalRegistryOnce sync.Once
)

// GetRegistry returns the global daemon registry.
func GetRegistry() *Registry {
	globalRegistryOnce.Do(func() {
		globalRegistry = &Registry{
			daemons:  make(map[string]DaemonEntry),
			filePath: registryPath(),
		}
		globalRegistry.load()
	})
	return globalRegistry
}

// registryPath returns the path to the global registry file.
func registryPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		// Fallback to temp directory
		home = os.TempDir()
	}
	return filepath.Join(home, ".dfmt", "daemons.json")
}

// load loads the registry from disk.
func (r *Registry) load() {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.filePath)
	if err != nil {
		return // No registry yet
	}

	var entries []DaemonEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return
	}

	r.daemons = make(map[string]DaemonEntry)
	for _, e := range entries {
		// Verify daemon is still running
		if isProcessRunning(e.PID) {
			r.daemons[e.ProjectPath] = e
		}
	}
}

// saveNoLock saves the registry to disk. Caller must NOT hold the lock.
func (r *Registry) saveNoLock() {
	entries := make([]DaemonEntry, 0, len(r.daemons))
	for _, e := range r.daemons {
		entries = append(entries, e)
	}

	// Ensure directory exists. 0o700 to match the registry file (0600) — the
	// directory enumerates "~/.dfmt" usage and should not be readable by other
	// local users on shared hosts.
	dir := filepath.Dir(r.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}

	// 0600: the registry enumerates every project path the user has ever
	// opened with DFMT — a privacy leak worth closing off from other local
	// users. Atomic tmp+rename closes F-25 (concurrent registration from
	// two daemons starting up simultaneously) and CheckNoSymlinks closes
	// the symlink-plant attack.
	_ = safefs.WriteFileAtomic(dir, r.filePath, data, 0o600)
}

// Register adds a daemon to the registry.
func (r *Registry) Register(entry DaemonEntry) {
	r.mu.Lock()
	entry.LastSeen = time.Now()
	r.daemons[entry.ProjectPath] = entry
	r.saveNoLock()
	r.mu.Unlock()
}

// Unregister removes a daemon from the registry.
func (r *Registry) Unregister(projectPath string) {
	r.mu.Lock()
	delete(r.daemons, projectPath)
	r.saveNoLock()
	r.mu.Unlock()
}

// List returns all registered daemons.
func (r *Registry) List() []DaemonEntry {
	r.mu.Lock()

	// Refresh last seen for running daemons, collect dead ones
	var deadPaths []string
	for path, e := range r.daemons {
		if isProcessRunning(e.PID) {
			e.LastSeen = time.Now()
			r.daemons[path] = e
		} else {
			// Daemon died, mark for removal
			deadPaths = append(deadPaths, path)
		}
	}

	// Remove dead daemons
	for _, path := range deadPaths {
		delete(r.daemons, path)
	}

	entries := make([]DaemonEntry, 0, len(r.daemons))
	for _, e := range r.daemons {
		entries = append(entries, e)
	}

	// Copy for save outside lock
	entriesForSave := make([]DaemonEntry, len(entries))
	copy(entriesForSave, entries)

	r.mu.Unlock()

	// Save outside lock to avoid deadlock
	if len(deadPaths) > 0 {
		r.saveNoLock()
	}

	return entries
}

// Get returns a daemon entry by project path.
func (r *Registry) Get(projectPath string) (DaemonEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.daemons[projectPath]
	return e, ok
}

// Refresh updates the last seen time for a daemon.
func (r *Registry) Refresh(projectPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.daemons[projectPath]; ok {
		e.LastSeen = time.Now()
		r.daemons[projectPath] = e
	}
}

// NewDaemonEntry creates a new daemon entry for the current platform.
func NewDaemonEntry(projectPath string, pid int) DaemonEntry {
	entry := DaemonEntry{
		ProjectPath: projectPath,
		PID:         pid,
		StartedAt:   time.Now(),
		LastSeen:    time.Now(),
	}

	if runtime.GOOS == "windows" {
		// Port file is now JSON ({"port":N,"token":"..."}); Sscanf("%d") against
		// JSON always matched zero fields, so every Windows daemon registered
		// with port 0 in the global registry. Reuse the dual-path reader so
		// both JSON and legacy integer forms work.
		portFile := filepath.Join(projectPath, ".dfmt", "port")
		if port, err := readPortFile(portFile); err == nil {
			entry.Port = port
		}
	} else {
		entry.SocketPath = project.SocketPath(projectPath)
	}

	return entry
}
