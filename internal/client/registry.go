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
	mu      sync.Mutex
	daemons map[string]DaemonEntry
	// filePath, when non-empty, pins the registry to a specific path
	// and bypasses registryPath() resolution. Production callers leave
	// this empty so DFMT_GLOBAL_DIR overrides take effect on every
	// save/load. Tests that need a sandbox path use newTestRegistry
	// to set it explicitly.
	filePath string
}

// path returns the file the registry should read/write. The
// per-instance filePath wins when set (tests); otherwise we resolve
// via registryPath() which honors DFMT_GLOBAL_DIR. Resolving on
// every call rather than caching at construction lets a test that
// flips DFMT_GLOBAL_DIR mid-suite observe the new value — the
// production singleton was previously a hidden coupling that meant
// the FIRST test to call GetRegistry() pinned the path forever.
func (r *Registry) path() string {
	if r.filePath != "" {
		return r.filePath
	}
	return registryPath()
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
			daemons: make(map[string]DaemonEntry),
		}
		globalRegistry.load()
	})
	return globalRegistry
}

// registryPath returns the path to the global registry file.
// Honors DFMT_GLOBAL_DIR for parity with project.GlobalDir() — same
// override the global daemon respects for its port/lock/pid files,
// so tests pinning DFMT_GLOBAL_DIR to a sandbox dir keep the daemon
// registry inside that sandbox too. Without this override the
// per-project tests in internal/daemon would write live registry
// rows into the developer's real ~/.dfmt/daemons.json on every run.
func registryPath() string {
	if env := os.Getenv("DFMT_GLOBAL_DIR"); env != "" {
		return filepath.Join(env, "daemons.json")
	}
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

	data, err := os.ReadFile(r.path())
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

// snapshotEntriesLocked returns a slice copy of the daemons map.
// Caller MUST hold r.mu. Used to capture state under the lock so the
// disk save can run lock-free without re-iterating the map.
func (r *Registry) snapshotEntriesLocked() []DaemonEntry {
	entries := make([]DaemonEntry, 0, len(r.daemons))
	for _, e := range r.daemons {
		entries = append(entries, e)
	}
	return entries
}

// saveSnapshot persists the given entries slice to disk. The slice MUST be
// a snapshot taken under r.mu so this function does not need the lock —
// no shared map is read here. (The previous saveNoLock iterated r.daemons
// directly; concurrent List + Register/Unregister tripped Go's "fatal
// error: concurrent map read and map write" — V-03.)
func (r *Registry) saveSnapshot(entries []DaemonEntry) {
	path := r.path()
	// Ensure directory exists. 0o700 to match the registry file (0600) — the
	// directory enumerates "~/.dfmt" usage and should not be readable by other
	// local users on shared hosts.
	dir := filepath.Dir(path)
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
	_ = safefs.WriteFileAtomic(dir, path, data, 0o600)
}

// Register adds a daemon to the registry.
func (r *Registry) Register(entry DaemonEntry) {
	r.mu.Lock()
	entry.LastSeen = time.Now()
	r.daemons[entry.ProjectPath] = entry
	snapshot := r.snapshotEntriesLocked()
	r.mu.Unlock()
	r.saveSnapshot(snapshot)
}

// Unregister removes a daemon from the registry.
func (r *Registry) Unregister(projectPath string) {
	r.mu.Lock()
	delete(r.daemons, projectPath)
	snapshot := r.snapshotEntriesLocked()
	r.mu.Unlock()
	r.saveSnapshot(snapshot)
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

	entries := r.snapshotEntriesLocked()
	r.mu.Unlock()

	// V-03: pass the snapshot rather than calling a function that re-reads
	// the map after we unlock. Concurrent Register/Unregister would otherwise
	// race the read-iterate against a write under the lock.
	if len(deadPaths) > 0 {
		r.saveSnapshot(entries)
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
		if port, _, err := readPortFile(portFile); err == nil {
			entry.Port = port
		}
	} else {
		entry.SocketPath = project.SocketPath(projectPath)
	}

	return entry
}
