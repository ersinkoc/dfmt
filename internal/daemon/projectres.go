package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

	// lastIndexedID is the ID of the highest event the index-tail
	// goroutine has Add()ed for this project. The tail Streams from
	// this cursor each tick to pick up events written by other
	// processes (most importantly the `dfmt mcp` subprocess, which
	// owns its own journal handle to the same .dfmt/journal.jsonl
	// file as the global daemon and otherwise leaves the daemon's
	// in-memory index permanently behind). Index.Add is idempotent
	// (keyed by event ID) so events the daemon itself appended are
	// safely re-processed as no-ops.
	//
	// Stored as *string so atomic.Pointer.CompareAndSwap can advance
	// it without a mutex; nil means "tail has not run yet — start
	// from the journal beginning".
	lastIndexedID atomic.Pointer[string]

	// tailStop cancels the index-tail goroutine on Close. Set by
	// startIndexTail; safe to call even when nil (CancelFunc is
	// idempotent).
	tailStop context.CancelFunc

	// tailWG waits for the tail goroutine to drain on Close. Without
	// this, closeExtraProjects could close the journal handle while
	// the tail goroutine is mid-Stream, and the goroutine's read
	// would race with file close.
	tailWG sync.WaitGroup

	// fsStop cancels the fswatcher consume goroutine on Close. Set by
	// startFSWatch when the project's config opted into Capture.FS;
	// nil otherwise.
	fsStop context.CancelFunc

	// fsWG waits for the fswatcher consume goroutine to drain. Same
	// reasoning as tailWG: closeExtraProjects must not close the
	// journal handle while consumeFSWatch is mid-Append.
	fsWG sync.WaitGroup
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

	// Fast path: default project. Cache a single ProjectResources
	// view so the index-tail goroutine has a stable instance to
	// attach tailStop / tailWG to. The view aliases the daemon's
	// own Journal/Index/etc. — mutating its LastActivityNs does not
	// affect the daemon's process-global counter (Daemon.Touch is
	// the public path for that).
	if sameProjectID(projectID, d.projectPath) {
		d.projectsMu.RLock()
		view := d.defaultRes
		d.projectsMu.RUnlock()
		if view == nil {
			d.projectsMu.Lock()
			if d.defaultRes == nil {
				d.defaultRes = d.defaultResourcesView()
				// Same lifecycle as extraProjects: the tail picks
				// up MCP-subprocess writes for the default project
				// too. Background ctx is correct (lifetime owned
				// by the daemon, cancellation via stopIndexTail in
				// Stop).
				d.defaultRes.startIndexTail(context.Background())
			}
			view = d.defaultRes
			d.projectsMu.Unlock()
		}
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
	// Start the index-tail goroutine immediately so a `dfmt mcp`
	// subprocess that has been writing to the same journal file
	// before this Resources call is picked up on the next tick.
	// Background ctx is correct here — the tail's lifetime is owned
	// by the resource bundle, not the request that triggered the
	// load. Cancellation arrives via stopIndexTail on Close.
	r.startIndexTail(context.Background())
	// Same lifetime story for the per-project fswatcher consumer:
	// no-op when cfg.Capture.FS.Enabled was false.
	r.startFSWatch(context.Background())
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
// FSWatcher is constructed (but not Start()ed) when cfg.Capture.FS.Enabled
// is true. Resources() spawns the consume goroutine via startFSWatch right
// after startIndexTail, mirroring the default project's wiring in Daemon.New
// + Daemon.Start. The default project still uses Daemon.fswatcher /
// Daemon.consumeFSWatch — extra projects use the per-project path here so
// each watcher writes to the right journal.
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
	}

	// Construct (but do not Start) the FSWatcher when the project opted
	// into filesystem capture. Start needs a context tied to the daemon's
	// lifetime, which Resources() supplies via startFSWatch on the same
	// tick that launches the index-tail goroutine. A nil watcher here
	// means "this project does not want fswatch" — config silence is a
	// stronger contract than implicit-on.
	if cfg.Capture.FS.Enabled {
		w, werr := capture.NewFSWatcher(projectPath, cfg.Capture.FS.Ignore, cfg.Capture.FS.DebounceMS)
		if werr != nil {
			logging.Warnf("create fswatcher for %s: %v", projectPath, werr)
		} else {
			r.FSWatcher = w
		}
	}

	return r, nil
}

// indexTailInterval is how often the tail goroutine polls the journal
// for new events. 3 s keeps recall/search lag under a normal-paced
// agent's perception while imposing negligible CPU on idle daemons
// (one stat call per project per 3 s — segment enumeration short-
// circuits when no new bytes arrived).
//
// Configurable via DFMT_INDEX_TAIL_INTERVAL_MS for tests; production
// callers pin to the constant default.
var indexTailInterval = 3 * time.Second

// startIndexTail spawns the per-project goroutine that streams events
// the daemon's index hasn't seen yet and Add()s them. This closes the
// MCP-subprocess drift: `dfmt mcp` opens its own journal handle and
// appends events directly, leaving the global daemon's in-memory
// index permanently behind on every search until the next daemon
// restart triggered a journal-replay rebuild. With the tail running,
// `dfmt_search` and `/api/stats`-via-index converge to the on-disk
// state within indexTailInterval.
//
// Why polling instead of fsnotify: the journal is a single file that
// rotates infrequently; a 3 s poll Reads only the tail bytes since
// the last cursor (Stream "from" skips past the matching ID), so the
// per-tick cost on an idle project is one stat + one short read.
// fsnotify would shave the latency at the cost of a platform-specific
// dependency that this codebase avoids (ADR-0004 stdlib-only). When
// agent-perceptible latency becomes an issue we revisit.
//
// Lifecycle: starts on first call to startIndexTail; stops via
// tailStop on Close. tailWG ensures Close waits for any in-flight
// Stream to drain before the journal handle is closed.
func (r *ProjectResources) startIndexTail(parent context.Context) {
	if r == nil || r.Journal == nil || r.Index == nil {
		return
	}
	if r.tailStop != nil {
		// Already running. Idempotent for callers that may invoke
		// startIndexTail more than once across legacy/global paths.
		return
	}
	ctx, cancel := context.WithCancel(parent)
	r.tailStop = cancel
	r.tailWG.Add(1)
	go func() {
		defer r.tailWG.Done()
		defer func() {
			if rec := recover(); rec != nil {
				logging.Errorf("index tail panic for %s recovered: %v", r.ProjectPath, rec)
			}
		}()
		ticker := time.NewTicker(indexTailInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.tailOnce(ctx)
			}
		}
	}()
}

// tailOnce performs one pass of the journal tail: Stream from
// lastIndexedID forward, Add each event to the index, advance the
// cursor. Errors are logged and the next tick retries — tail is
// best-effort and must never wedge the daemon.
func (r *ProjectResources) tailOnce(ctx context.Context) {
	from := ""
	if cur := r.lastIndexedID.Load(); cur != nil {
		from = *cur
	}
	stream, err := r.Journal.Stream(ctx, from)
	if err != nil {
		logging.Warnf("index tail stream for %s: %v", r.ProjectPath, err)
		return
	}
	var newest string
	for e := range stream {
		r.Index.Add(e)
		newest = e.ID
	}
	if newest != "" {
		// Snapshot to a stack copy so atomic.Pointer holds an
		// independent string. Using &newest directly would expose
		// the loop variable and a future iteration would mutate
		// the value the cursor pointed at.
		id := newest
		r.lastIndexedID.Store(&id)
	}
}

// stopIndexTail cancels the tail goroutine if running and waits for
// it to drain. Must be called before Journal.Close so the goroutine
// doesn't read a closed file.
func (r *ProjectResources) stopIndexTail() {
	if r == nil || r.tailStop == nil {
		return
	}
	r.tailStop()
	r.tailWG.Wait()
	r.tailStop = nil
}

// startFSWatch spawns the per-project filesystem-watcher consume
// goroutine when r.FSWatcher is non-nil (the project opted in via
// cfg.Capture.FS.Enabled). The goroutine forwards each fswatch event
// into r.Journal/r.Index after redacting paths through r.Redactor.
//
// This mirrors Daemon.consumeFSWatch for the default project. The
// reason both exist is lifecycle: the default project's watcher is
// owned by Daemon and torn down in Stop's per-phase ordering;
// extra-project watchers are owned by ProjectResources and torn down
// in closeExtraProjects. Until Daemon.fswatcher migrates onto
// defaultRes, the duplication is intentional.
//
// Idempotent: if r.FSWatcher is nil or fsStop is already set, this
// returns without effect.
func (r *ProjectResources) startFSWatch(parent context.Context) {
	if r == nil || r.FSWatcher == nil || r.Journal == nil {
		return
	}
	if r.fsStop != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	if err := r.FSWatcher.Start(ctx); err != nil {
		logging.Warnf("fswatcher start for %s: %v", r.ProjectPath, err)
		cancel()
		return
	}
	r.fsStop = cancel
	r.fsWG.Add(1)
	go r.consumeFSWatch(ctx)
}

// consumeFSWatch drains r.FSWatcher.Events() into r.Journal and
// r.Index. Returns when the events channel closes or ctx cancels.
// Best-effort like Daemon.consumeFSWatch: a single Append failure is
// logged and the loop continues, since dropping the daemon over a
// transient I/O blip is the wrong tradeoff.
func (r *ProjectResources) consumeFSWatch(ctx context.Context) {
	defer r.fsWG.Done()
	defer func() {
		if rec := recover(); rec != nil {
			logging.Errorf("fswatch consume panic for %s recovered: %v", r.ProjectPath, rec)
		}
	}()
	events := r.FSWatcher.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			if r.Redactor != nil {
				e.Data = r.Redactor.RedactEvent(e.Data)
				for i, tag := range e.Tags {
					e.Tags[i] = r.Redactor.Redact(tag)
				}
				e.Sig = e.ComputeSig()
			}
			if err := r.Journal.Append(ctx, e); err != nil {
				logging.Warnf("fswatch journal append for %s: %v", r.ProjectPath, err)
				continue
			}
			r.Index.Add(e)
		}
	}
}

// stopFSWatch cancels the fswatch consume goroutine, stops the
// watcher, and waits for the consumer to drain. Must run before
// Journal.Close so consumeFSWatch's in-flight Append cannot race the
// close. Idempotent.
func (r *ProjectResources) stopFSWatch() {
	if r == nil || r.fsStop == nil {
		return
	}
	r.fsStop()
	if r.FSWatcher != nil {
		// Stop is fire-and-forget for our purposes — the cancel above
		// already unblocks the consumer; Stop just releases the OS-level
		// watch handles. 5 s budget mirrors closeExtraProjects.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.FSWatcher.Stop(ctx)
		cancel()
	}
	r.fsWG.Wait()
	r.fsStop = nil
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
		// Stop fswatch first so no new events arrive while we wind down.
		// Then stop the tail goroutine so its in-flight Stream cannot
		// race the journal Close below. Order matters: a fswatch event
		// arriving after the tail stops would still be Append()ed but
		// not Add()ed to the index — survivable because the tail-on-
		// reload path picks it up on next daemon start.
		r.stopFSWatch()
		r.stopIndexTail()
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
