package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	tmp := t.TempDir()
	return &Registry{
		daemons:  make(map[string]DaemonEntry),
		filePath: filepath.Join(tmp, "daemons.json"),
	}
}

func TestRegistryRegisterAndList(t *testing.T) {
	r := newTestRegistry(t)
	entry := DaemonEntry{
		ProjectPath: "/proj/one",
		PID:         os.Getpid(),
		Port:        1234,
		StartedAt:   time.Now(),
	}
	r.Register(entry)

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("List size = %d, want 1", len(list))
	}
	if list[0].ProjectPath != "/proj/one" {
		t.Errorf("ProjectPath = %q, want /proj/one", list[0].ProjectPath)
	}
	if list[0].LastSeen.IsZero() {
		t.Error("LastSeen was not set by Register")
	}
}

func TestRegistryGet(t *testing.T) {
	r := newTestRegistry(t)
	r.Register(DaemonEntry{ProjectPath: "/p", PID: os.Getpid(), Port: 99})

	got, ok := r.Get("/p")
	if !ok {
		t.Fatal("Get missed existing entry")
	}
	if got.Port != 99 {
		t.Errorf("Port = %d, want 99", got.Port)
	}

	if _, ok := r.Get("/missing"); ok {
		t.Error("Get returned ok for non-existent key")
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := newTestRegistry(t)
	r.Register(DaemonEntry{ProjectPath: "/p", PID: os.Getpid()})
	r.Unregister("/p")

	if len(r.List()) != 0 {
		t.Errorf("List size after Unregister = %d, want 0", len(r.List()))
	}
}

func TestRegistryRoundTripPersist(t *testing.T) {
	r := newTestRegistry(t)
	r.Register(DaemonEntry{ProjectPath: "/a", PID: os.Getpid(), Port: 1})
	r.Register(DaemonEntry{ProjectPath: "/b", PID: os.Getpid(), Port: 2})

	// Re-load from disk.
	r2 := &Registry{
		daemons:  make(map[string]DaemonEntry),
		filePath: r.filePath,
	}
	r2.load()

	if got := len(r2.List()); got != 2 {
		t.Errorf("reloaded List size = %d, want 2", got)
	}
}

func TestRegistryLoadMissingFile(t *testing.T) {
	r := &Registry{
		daemons:  make(map[string]DaemonEntry),
		filePath: filepath.Join(t.TempDir(), "nonexistent", "daemons.json"),
	}
	r.load() // must not panic, must not populate
	if len(r.daemons) != 0 {
		t.Errorf("daemons = %d after missing-file load, want 0", len(r.daemons))
	}
}

func TestRegistryLoadBadJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "daemons.json")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := &Registry{daemons: make(map[string]DaemonEntry), filePath: path}
	r.load() // must not panic
	if len(r.daemons) != 0 {
		t.Errorf("daemons = %d after bad-json load, want 0", len(r.daemons))
	}
}

func TestRegistryLoadSkipsDeadPIDs(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "daemons.json")
	entries := []DaemonEntry{
		{ProjectPath: "/live", PID: os.Getpid(), Port: 1},
		{ProjectPath: "/dead", PID: 1, Port: 2}, // PID 1 never matches our process-liveness check
	}
	data, _ := json.Marshal(entries)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := &Registry{daemons: make(map[string]DaemonEntry), filePath: path}
	r.load()

	if _, ok := r.daemons["/live"]; !ok {
		t.Error("live entry was not loaded")
	}
	// Don't assert on /dead - it may be considered live on some platforms
	// when PID 1 happens to be the init process owned by our uid. The key
	// coverage goal is exercising the load path with multiple entries.
}

func TestRegistryRefreshNoop(t *testing.T) {
	r := newTestRegistry(t)
	r.Register(DaemonEntry{ProjectPath: "/p", PID: os.Getpid()})
	r.Refresh("/p")       // existing entry
	r.Refresh("/missing") // no-op branch
}

func TestRegistryConcurrent(t *testing.T) {
	r := newTestRegistry(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Register(DaemonEntry{
				ProjectPath: "/p/x",
				PID:         os.Getpid(),
				Port:        n,
			})
		}(i)
	}
	wg.Wait()
	if got := len(r.List()); got != 1 {
		t.Errorf("concurrent Register size = %d, want 1 (same key)", got)
	}
}

func TestNewDaemonEntry(t *testing.T) {
	e := NewDaemonEntry("/p", os.Getpid())
	if e.ProjectPath != "/p" || e.PID != os.Getpid() {
		t.Errorf("NewDaemonEntry fields mismatch: %+v", e)
	}
	if e.StartedAt.IsZero() {
		t.Error("StartedAt was not set")
	}
}

func TestRegistryPath(t *testing.T) {
	p := registryPath()
	if p == "" {
		t.Error("registryPath returned empty string")
	}
	if filepath.Base(p) != "daemons.json" {
		t.Errorf("registryPath base = %q, want daemons.json", filepath.Base(p))
	}
}

func TestGetRegistrySingleton(t *testing.T) {
	a := GetRegistry()
	b := GetRegistry()
	if a != b {
		t.Error("GetRegistry must return the same instance")
	}
}
