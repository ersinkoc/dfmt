package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ersinkoc/dfmt/internal/capture"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/content"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/redact"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// ProjectResources groups the per-project state a daemon needs to serve
// tool calls for a single project root: its config snapshot, journal,
// search index, content store, redactor, sandbox, and (optionally) the
// filesystem watcher. One ProjectResources is loaded per project the
// global daemon serves; the default project (the one passed to New)
// keeps its handles on the Daemon struct directly so the existing
// Start/Stop/idle/register/fswatch lifecycles stay byte-for-byte
// unchanged. ProjectResources is lazily loaded for any *additional*
// project a tool call targets (Phase 2).
//
// Each field mirrors the corresponding daemon-owned field with one
// exception: FSWatcher is opt-in — Capture.FS.Enabled drives it, and
// extra-project watchers are skipped on first build because hooking
// them into the default-project journal stream would be wrong (each
// project owns its own journal). When that becomes a need, the watcher
// goroutine moves into ProjectResources alongside the journal.
type ProjectResources struct {
	ProjectPath  string
	Config       *config.Config
	Journal      core.Journal
	Index        *core.Index
	IndexPath    string
	NeedsRebuild bool
	Redactor     *redact.Redactor
	ContentStore *content.Store
	Sandbox      sandbox.Sandbox
	FSWatcher    *capture.FSWatcher

	// LastActivityNs records the wall-clock UnixNano of the last RPC
	// targeting this project. Audit-only for now — the daemon's idle
	// monitor uses Daemon.lastActivityNs (process-global). Per-project
	// timeout will read this if/when we shift to staggered shutdown.
	LastActivityNs atomic.Int64
}

// errProjectIDRequired is returned by Resources when the caller passed
// an empty project ID and the daemon has no default project to fall
// back on (degraded mode). Surfaces as -32603 to MCP clients.
var errProjectIDRequired = errors.New("project_id required: daemon has no default project")

// resolveProjectID normalizes the requested project ID for cache lookup.
// On Windows the lookup is case-insensitive (filesystem semantics);
// on Unix it is exact. Empty input is returned as-is — the caller
// decides whether that's a fallback to the default project or an error.
func resolveProjectID(projectID string) string {
	if projectID == "" {
		return ""
	}
	abs, err := filepath.Abs(projectID)
	if err != nil {
		abs = projectID
	}
	if runtime.GOOS == goosWindows {
		return strings.ToLower(abs)
	}
	return abs
}

// sameProjectID compares two project IDs with platform-appropriate
// case sensitivity. Used to detect whether a per-call ID matches the
// daemon's default project (in which case we use the existing direct
// fields instead of the cache).
func sameProjectID(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return resolveProjectID(a) == resolveProjectID(b)
}

// Resources returns the per-project resource bundle for projectID,
// loading and caching it on first request. The returned bundle is
// safe to use concurrently across handler goroutines for the targeted
// project; the cache itself is guarded by Daemon.projectsMu.
//
// Resolution:
//
//  1. projectID == "" → fall back to the default project (the one
//     passed to New). Returns errProjectIDRequired when the daemon
//     has no default project (degraded mode).
//  2. projectID == defaultProjectPath → reuse the daemon's direct
//     fields (no cache entry needed; fastest path).
//  3. Otherwise → look up the extraProjects cache; on miss, load
//     and store. Concurrent first-time loads are coordinated so
//     only one goroutine pays the load cost.
//
// LastActivityNs is bumped on every successful resolve so per-project
// idle metrics stay accurate even though the global idle monitor is
// process-wide.
func (d *Daemon) Resources(projectID string) (*ProjectResources, error) {
	if projectID == "" {
		if d.projectPath == "" {
			return nil, errProjectIDRequired
		}
		projectID = d.projectPath
	}

	// Fast path: default project. The daemon already owns these
	// handles — wrap them in a ProjectResources view without touching
	// the cache. The wrapper is constructed fresh each call, but its
	// fields point at the daemon's existing instances, so the
	// allocation cost is one struct per RPC at worst.
	if sameProjectID(projectID, d.projectPath) {
		view := d.defaultResourcesView()
		view.LastActivityNs.Store(d.lastActivityNs.Load())
		return view, nil
	}

	key := resolveProjectID(projectID)

	// Cache hit
	d.projectsMu.RLock()
	if r, ok := d.extraProjects[key]; ok {
		d.projectsMu.RUnlock()
		return r, nil
	}
	d.projectsMu.RUnlock()

	// Cache miss: take the write lock and re-check (another goroutine
	// may have populated between the RLock release and the Lock acquire).
	d.projectsMu.Lock()
	defer d.projectsMu.Unlock()
	if r, ok := d.extraProjects[key]; ok {
		return r, nil
	}

	r, err := loadProjectResources(projectID, d.config)
	if err != nil {
		return nil, fmt.Errorf("load resources for %s: %w", projectID, err)
	}
	if d.extraProjects == nil {
		d.extraProjects = make(map[string]*ProjectResources)
	}
	d.extraProjects[key] = r
	return r, nil
}

// defaultResourcesView wraps the daemon's primary fields into a
// ProjectResources view. The struct is value-copied for the LastActivityNs
// snapshot at call time; pointer fields (Journal, Index, etc.) alias the
// daemon's own. Mutating the view's atomic counter does not affect the
// daemon's lastActivityNs — that's what Daemon.Touch is for.
func (d *Daemon) defaultResourcesView() *ProjectResources {
	return &ProjectResources{
		ProjectPath:  d.projectPath,
		Config:       d.config,
		Journal:      d.journal,
		Index:        d.index,
		IndexPath:    d.indexPath,
		NeedsRebuild: d.needsRebuild,
		Redactor:     d.redactor,
		ContentStore: nil, // owned by handlers, not Daemon
		Sandbox:      nil, // owned by handlers, not Daemon
		FSWatcher:    d.fswatcher,
	}
}

// loadProjectResources opens journal/index/content/redact/policy state
// for an additional project the daemon has been asked to serve. It is
// the multi-project equivalent of the per-project loading code at the
// top of Daemon.New, factored out so both call sites converge on the
// same defaults and warning surface.
//
// Failure semantics: any I/O error during the load is returned to the
// caller (Resources). Partial state (journal opened but index load
// failed, etc.) is best-effort cleaned up before returning.
//
// Behavior matches Daemon.New for these knobs:
//   - .dfmt/ directory created with mode 0o700 if missing.
//   - journal.OpenJournal honors cfg.Storage.{JournalMaxBytes, Durability,
//     MaxBatchMS, CompressRotated}.
//   - core.LoadIndexWithCursor; on miss/corrupt, a fresh index is
//     constructed and NeedsRebuild is set so the caller (or a future
//     wire-through) can replay journal events.
//   - sandbox.LoadPolicyMerged for project-local permissions.yaml,
//     warnings logged.
//   - sandbox.NewSandboxWithPolicy with cfg.Exec.PathPrepend.
//   - content.NewStore at .dfmt/content/, soft-fail with warning.
//   - redact.LoadProjectRedactor for project-local redact.yaml,
//     warnings logged.
//
// FSWatcher is intentionally NOT started here; spawning per-project
// watchers across the cache would compete for the daemon's single
// fswatch consumer goroutine. The default project keeps its watcher;
// extra projects fall back to journal-only event sources (MCP/CLI).
func loadProjectResources(projectPath string, fallbackCfg *config.Config) (*ProjectResources, error) {
	// Resolve to absolute so cache key and on-disk paths are canonical.
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", projectPath, err)
	}
	projectPath = abs

	dfmtDir := filepath.Join(projectPath, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dfmtDir, err)
	}

	// Each project's .dfmt/config.yaml wins over the daemon's startup
	// cfg. Without this, project B inherited project A's retention,
	// budget, redact, and sandbox knobs the moment the global daemon
	// served them both — silently overriding the YAML the user wrote
	// into B/.dfmt/config.yaml.
	//
	// fallbackCfg keeps the old behavior when config.Load returns an
	// error (typo'd YAML) or finds nothing on disk: we still need
	// non-nil knobs so journal options / sandbox rules don't crash on
	// zero-value reads. The error is surfaced via logging.Warnf so
	// `dfmt doctor` callers can see the project went degraded.
	cfg := fallbackCfg
	if loaded, lerr := config.Load(projectPath); lerr == nil && loaded != nil {
		cfg = loaded
	} else if lerr != nil {
		logging.Warnf("config for %s: %v (using daemon defaults)", projectPath, lerr)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	// Journal.
	journalPath := filepath.Join(dfmtDir, "journal.jsonl")
	journal, err := core.OpenJournal(journalPath, core.JournalOptions{
		Path:     journalPath,
		MaxBytes: cfg.Storage.JournalMaxBytes,
		Durable:  cfg.Storage.Durability == "durable",
		BatchMS:  cfg.Storage.MaxBatchMS,
		Compress: cfg.Storage.CompressRotated,
	})
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}

	// Index. Mirrors Daemon.New: load existing, mark for rebuild on miss.
	indexPath := filepath.Join(dfmtDir, "index.gob")
	cursorPath := filepath.Join(dfmtDir, "index.cursor")
	index, _, needsRebuild, ierr := core.LoadIndexWithCursor(indexPath, cursorPath)
	if ierr != nil {
		logging.Warnf("load index for %s: %v", projectPath, ierr)
		needsRebuild = true
	}
	indexParams := core.IndexParams{
		K1:           cfg.Index.BM25K1,
		B:            cfg.Index.BM25B,
		HeadingBoost: cfg.Index.HeadingBoost,
	}
	if index == nil || needsRebuild {
		index = core.NewIndexWithParams(indexParams)
	} else {
		index.SetParams(indexParams)
	}

	// Permissions overlay + sandbox.
	if perr := sandbox.ValidatePathPrepend(cfg.Exec.PathPrepend); perr != nil {
		_ = journal.Close()
		return nil, fmt.Errorf("invalid path_prepend: %w", perr)
	}
	polRes, polErr := sandbox.LoadPolicyMerged(projectPath)
	if polErr != nil {
		logging.Warnf("permissions for %s: %v", projectPath, polErr)
	}
	for _, w := range polRes.Warnings {
		logging.Warnf("permissions for %s: %s", projectPath, w)
	}
	sb := sandbox.NewSandboxWithPolicy(projectPath, polRes.Policy).
		WithPathPrepend(cfg.Exec.PathPrepend)

	// Redactor overlay.
	redactor, redactRes, redactErr := redact.LoadProjectRedactor(projectPath)
	if redactErr != nil {
		logging.Warnf("redact for %s: %v", projectPath, redactErr)
	}
	for _, w := range redactRes.Warnings {
		logging.Warnf("redact for %s: %s", projectPath, w)
	}

	// Ephemeral content store. Soft-fail mirrors Daemon.New.
	var store *content.Store
	contentDir := filepath.Join(dfmtDir, "content")
	if s, cerr := content.NewStore(content.StoreOptions{Path: contentDir}); cerr == nil {
		store = s
	} else {
		logging.Warnf("create content store for %s: %v", projectPath, cerr)
	}

	r := &ProjectResources{
		ProjectPath:  projectPath,
		Config:       cfg,
		Journal:      journal,
		Index:        index,
		IndexPath:    indexPath,
		NeedsRebuild: needsRebuild,
		Redactor:     redactor,
		ContentStore: store,
		Sandbox:      sb,
		// FSWatcher intentionally nil; see top-of-function note.
	}
	return r, nil
}

// closeExtraProjects shuts down every cached additional-project
// resource bundle. Called from Daemon.Stop after the default project's
// journal/index are persisted. Failures are logged but never fatal —
// Stop must complete even if a cache entry leaks state.
//
// Each extra project's index is persisted before its journal closes so
// the next daemon start does not have to replay the full journal for
// recall to work — for a global daemon serving N projects, that
// rebuild cost was paid once per project per restart in v0.4.0–v0.4.3.
// Skip persist when NeedsRebuild was true at load time: that index
// contains only events that arrived after daemon start (the historical
// journal has not been replayed yet), so writing cursor=HEAD would
// silently mark older events as indexed and search would miss them on
// the next run.
func (d *Daemon) closeExtraProjects() {
	d.projectsMu.Lock()
	defer d.projectsMu.Unlock()
	// Bound the per-project checkpoint+persist work so a wedged journal
	// can't hang Stop. 5 s matches the default-project Stop budget.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for k, r := range d.extraProjects {
		if r == nil {
			continue
		}
		if r.Journal != nil && r.Index != nil && r.IndexPath != "" && !r.NeedsRebuild {
			hiID, cerr := r.Journal.Checkpoint(ctx)
			if cerr != nil {
				logging.Warnf("checkpoint journal for %s: %v", k, cerr)
			} else if perr := core.PersistIndex(r.Index, r.IndexPath, hiID); perr != nil {
				logging.Warnf("persist index for %s: %v", k, perr)
			}
		}
		if r.Journal != nil {
			if err := r.Journal.Close(); err != nil {
				logging.Warnf("close journal for %s: %v", k, err)
			}
		}
	}
	d.extraProjects = nil
}
