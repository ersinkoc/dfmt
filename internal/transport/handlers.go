package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ersinkoc/dfmt/internal/content"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/redact"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// Handlers implements the business logic for all transport layers. Mutable
// fields (project, redactor, store, activity) are guarded by mu so concurrent
// setter calls after Start are race-safe.
type Handlers struct {
	index   *core.Index
	journal core.Journal
	sandbox sandbox.Sandbox

	execSem  chan struct{}
	fetchSem chan struct{}
	// readSem bounds concurrent local-file read paths (Read, Glob, Grep).
	// writeSem bounds Edit and Write so an agent that reaches the loopback
	// port cannot DoS the daemon by spamming concurrent mutations. Depths
	// are sized for an interactive single-user host: enough headroom for
	// realistic agent burstiness, not enough to thrash the disk if the
	// agent (or a prompt-injected loop) goes pathological. See F-19.
	readSem  chan struct{}
	writeSem chan struct{}

	mu       sync.RWMutex
	project  string
	redactor *redact.Redactor
	store    *content.Store
	activity func()

	// fetchResources, when non-nil, supplies per-call project resources.
	// The global daemon (Phase 2 commit 4c) installs this so each tool
	// call routes to the project named by ctx project_id. Legacy
	// single-project daemons leave it nil; resolveBundle synthesizes a
	// bundle from the direct fields above. Guarded by h.mu — written
	// once at startup via SetResourceFetcher.
	fetchResources ResourceFetcher

	// listProjects, when non-nil, returns the set of project paths the
	// host daemon currently has resources cached for. The dashboard's
	// project switcher reads this; legacy single-project daemons leave
	// it nil and the dashboard falls back to the on-disk daemon
	// registry. Set once at startup via SetProjectsLister.
	listProjects ProjectsLister

	// dropProject, when non-nil, evicts a project from the daemon's
	// resource cache. Wired by the global daemon for `dfmt remove`'s
	// in-memory cleanup path; legacy daemons leave it nil and the RPC
	// returns "drop not supported on this daemon".
	dropProject ProjectDropper

	// dedupMu guards the short-lived stash dedup cache. Kept separate from
	// h.mu because stashContent runs on the hot path of every Exec/Read/
	// Fetch and must not contend with project/redactor setters.
	dedupMu    sync.Mutex
	dedupCache map[string]dedupEntry

	// dedupHits counts cache hits since process start. Surfaced in Stats
	// (and the dashboard) so users can see the dedup layer earning its
	// keep — a high count over a session means the agent is re-running
	// commands or re-reading generated files often enough that the
	// dedup window pays for itself.
	dedupHits atomic.Int64

	// sentMu guards sentCache + sentOrder. Cross-call wire dedup tracks
	// which content_ids have already been emitted to *the agent* (vs the
	// storage-side dedupCache which tracks what's already in the chunk
	// store). When a tool response would carry a content_id seen here
	// before, the heavy fields (Stdout/Body/Summary/Matches/Vocabulary)
	// are dropped and the agent gets a thin "(unchanged; same content_id)"
	// acknowledgement. See ADR-0009. The agent can opt out with
	// Return:"raw" when it needs the bytes again.
	sentMu    sync.Mutex
	sentCache map[string]time.Time // content_id -> expiresAt
	sentOrder []string             // FIFO eviction queue, capped at sentCap

	// recallDefaults holds operator-configured fallbacks for Recall
	// requests that omit Budget / Format. Wired from
	// config.Retrieval.{DefaultBudget,DefaultFormat} at daemon
	// construction (ADR-0015 v0.4 punch list). Zero values mean "use
	// the package default" (recallDefaultBudgetBytes / recallDefaultFormat).
	recallDefaults struct {
		mu     sync.RWMutex
		budget int
		format string
	}

	// statsCache memoises the result of Stats() across the dashboard's
	// poll interval. Without it every /api/stats hit re-streams the full
	// journal — at 10 MiB rotated max + active that's hundreds of ms per
	// poll. Mirrors the TTL pattern used by MCPProtocol.compressionStats
	// for its own per-tool aggregation.
	statsCacheMu  sync.RWMutex
	statsCache    *StatsResponse
	statsCachedAt time.Time
}

// statsTTL is how long Stats() returns the memoised result before
// re-streaming the journal. 5 seconds keeps the dashboard's poll loop cheap
// while still showing fresh data at human-perceptible rates. Declared as
// var (not const) so tests can override it to assert cache behavior
// without sleeping.
var statsTTL = 5 * time.Second

// dedupEntry maps a content hash to the chunk-set ID that already holds those
// bytes. expiresAt is absolute time, not a duration — comparing against
// time.Now() is one cheaper conditional than re-deriving every check.
type dedupEntry struct {
	contentID string
	expiresAt time.Time
}

// dedupTTL is the window in which a re-stash of identical bytes returns the
// existing chunk-set ID instead of creating a new one. The 30-second value is
// tuned for "agent retried the same command" / "two tools read the same
// generated file" — long enough to catch real reuse, short enough that the
// store doesn't keep stale pointers alive past the conversation turn that
// produced them.
const dedupTTL = 30 * time.Second

// dedupCap bounds the cache to keep memory predictable on a noisy session.
// Past this size the recorder prunes expired entries first, then drops
// arbitrary live ones — the cache is a best-effort optimization, missing a
// dedup never breaks correctness.
const dedupCap = 64

// sentTTL is the window during which a content_id we've already emitted to
// the agent is considered "still in the agent's context." 10 minutes is
// 20× dedupTTL because wire dedup wants to remember across an entire agent
// turn, not just a short retry burst. ADR-0009.
const sentTTL = 10 * time.Minute

// sentCap caps the wire-dedup cache. 256 entries is 4× dedupCap because the
// wire layer needs a longer memory than the storage layer — the storage
// cache evicts on every fresh stash; the wire cache sees a cache hit only
// when the agent re-reads bytes it has already seen.
const sentCap = 256

// sentUnchangedSummary is the sentinel summary text the agent sees when wire
// dedup short-circuits a response. Constant so callers (and tests) can pin
// equality without depending on the exact phrasing leaking elsewhere.
const sentUnchangedSummary = "(unchanged; same content_id)"

// errNoProject is returned by memory-tool handlers (Remember, Recall, Stats,
// Stream) when the MCP server is running in degraded mode — i.e. dfmt mcp
// was launched outside any project and has no journal/index attached.
// Sandbox tools continue to work; only journal-backed tools surface this
// error so the agent gets a clear message instead of a nil-deref panic.
var errNoProject = errors.New("no dfmt project — open dfmt from a project root or set DFMT_PROJECT to enable memory tools")

// NewHandlers creates a new Handlers instance.
// Every Event written via Remember and every tool-call log event generated by
// Exec/Read/Fetch is passed through the redactor before the journal sees it.
// Callers that need to inject a custom Redactor (e.g. with extra project
// patterns) can call SetRedactor.
func NewHandlers(index *core.Index, journal core.Journal, sb sandbox.Sandbox) *Handlers {
	return &Handlers{
		index:    index,
		journal:  journal,
		sandbox:  sb,
		redactor: redact.NewRedactor(),
		execSem:  make(chan struct{}, 4),
		fetchSem: make(chan struct{}, 8),
		readSem:  make(chan struct{}, 8),
		writeSem: make(chan struct{}, 4),
	}
}

func acquireLimiter(ctx context.Context, sem chan struct{}) (func(), error) {
	if sem == nil {
		return func() {}, nil
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SetRedactor overrides the redactor used by this Handlers instance.
// Pass nil to disable redaction (not recommended outside of tests).
// Clearing the dedup and sent caches ensures no pre-redaction content is
// returned under a reloaded or changed redaction config (N-02 fix).
func (h *Handlers) SetRedactor(r *redact.Redactor) {
	h.mu.Lock()
	h.redactor = r
	h.mu.Unlock()
	h.dedupMu.Lock()
	h.dedupCache = nil
	h.dedupMu.Unlock()
	h.sentMu.Lock()
	h.sentCache = nil
	h.sentOrder = nil
	h.sentMu.Unlock()
}

// getRedactor returns the current redactor under read-lock so callers see a
// consistent view across concurrent SetRedactor calls.
func (h *Handlers) getRedactor() *redact.Redactor {
	h.mu.RLock()
	r := h.redactor
	h.mu.RUnlock()
	return r
}

// redactorFor resolves the redactor for the given context. When a
// per-call resource fetcher is installed (global daemon mode), this
// reads bundle.Redactor — the per-project redactor loaded from
// <project>/.dfmt/redact.yaml. Without this, every project served by
// the global daemon would have its tool-output bytes redacted by the
// default project's patterns; project B's secrets-in-redact.yaml would
// not apply to its own tool calls.
//
// Falls back to h.getRedactor() (the default project's redactor) when:
//   - No fetcher is installed (legacy single-project daemon mode)
//   - The fetcher returns an error (e.g. errProjectIDRequired in
//     degraded mode where no projectID was stamped)
//
// The fallback keeps unit tests that construct Handlers directly via
// NewHandlers + SetRedactor working without changes — they have no
// fetcher and rely on the field set.
func (h *Handlers) redactorFor(ctx context.Context) *redact.Redactor {
	h.mu.RLock()
	f := h.fetchResources
	h.mu.RUnlock()
	if f != nil {
		if bundle, err := f(ProjectIDFrom(ctx)); err == nil && bundle.Redactor != nil {
			return bundle.Redactor
		}
	}
	return h.getRedactor()
}

// wireDedupSize returns the current entry count of the wire-dedup cache
// under the dedicated sentMu lock. Surfaced via /metrics
// (dfmt_wire_dedup_entries) for the operator dashboard. Cheap — just
// len() under a short-held mutex; no map walk.
func (h *Handlers) wireDedupSize() int {
	h.sentMu.Lock()
	defer h.sentMu.Unlock()
	return len(h.sentCache)
}

// contentDedupSize returns the current entry count of the content-store
// dedup cache under dedupMu. Surfaced via /metrics
// (dfmt_content_dedup_entries). Capped by dedupCap (64) so the value
// never exceeds that under healthy operation; a sustained max means the
// daemon is rotating the cache aggressively and may benefit from a
// larger cap.
func (h *Handlers) contentDedupSize() int {
	h.dedupMu.Lock()
	defer h.dedupMu.Unlock()
	return len(h.dedupCache)
}

// recallDefaultBudgetBytes is the package fallback when no operator
// override and no per-call value is supplied. Held as a const so a
// future ADR moving it requires a code change (and a CI signal) rather
// than a silent runtime drift. The 4096-byte value matches the
// historical Recall behavior pre-ADR-0015 wire-up.
const recallDefaultBudgetBytes = 4096

// recallDefaultFormat is the package fallback for Recall response
// shape — markdown is the dashboard- and agent-friendly default.
const recallDefaultFormat = "md"

// SetRecallDefaults overrides the per-call fallbacks Recall uses when
// the request omits Budget or Format. Setting budget=0 leaves the
// budget fallback at the package default; format="" leaves the format
// fallback at the package default. Per-call values still win — the
// override only applies when the caller did not supply one.
//
// Wired from config.Retrieval at daemon construction (ADR-0015 v0.4).
func (h *Handlers) SetRecallDefaults(budget int, format string) {
	h.recallDefaults.mu.Lock()
	h.recallDefaults.budget = budget
	h.recallDefaults.format = format
	h.recallDefaults.mu.Unlock()
}

// recallBudget returns the budget for a Recall request: per-call value
// if set, operator override if set, package default otherwise.
func (h *Handlers) recallBudget(reqBudget int) int {
	if reqBudget > 0 {
		return reqBudget
	}
	h.recallDefaults.mu.RLock()
	b := h.recallDefaults.budget
	h.recallDefaults.mu.RUnlock()
	if b > 0 {
		return b
	}
	return recallDefaultBudgetBytes
}

// recallFormat returns the format for a Recall request: per-call value
// if set, operator override if set, package default otherwise.
func (h *Handlers) recallFormat(reqFormat string) string {
	if reqFormat != "" {
		return reqFormat
	}
	h.recallDefaults.mu.RLock()
	f := h.recallDefaults.format
	h.recallDefaults.mu.RUnlock()
	if f != "" {
		return f
	}
	return recallDefaultFormat
}

// SetContentStore wires the ephemeral content store so Exec/Read/Fetch can
// stash the full (redacted) raw output for later lookup. The store is
// optional — when nil, Handlers return excerpts only.
func (h *Handlers) SetContentStore(s *content.Store) {
	h.mu.Lock()
	h.store = s
	h.mu.Unlock()
}

// getStore returns the current content store under read-lock.
func (h *Handlers) getStore() *content.Store {
	h.mu.RLock()
	s := h.store
	h.mu.RUnlock()
	return s
}

// getProject returns the per-handler default project identifier under read-lock.
// Prefer getProjectFor(ctx) for any new call site — that helper consults the
// request context first and only falls back to the default when no per-call
// project is attached.
func (h *Handlers) getProject() string {
	h.mu.RLock()
	p := h.project
	h.mu.RUnlock()
	return p
}

// getProjectFor returns the project identifier that an in-flight RPC targets.
// Resolution order:
//
//  1. transport.ProjectIDFrom(ctx) — set by the MCP transport when the
//     subprocess pinned a project_id at startup, or by HTTP/CLI clients
//     passing it through the request body.
//  2. h.project — the per-handler default, set once via SetProject. This
//     covers the legacy single-project daemon (one Handlers instance owns
//     one project for its lifetime) and the degraded MCP-without-project
//     mode (h.project == "").
//
// New code paths must call this helper instead of h.project / h.getProject()
// so the global daemon (Phase 2) can multiplex per-call routing without
// rewriting every event source again.
func (h *Handlers) getProjectFor(ctx context.Context) string {
	if pid := ProjectIDFrom(ctx); pid != "" {
		return pid
	}
	return h.getProject()
}

// Bundle groups the per-project resources a handler needs to serve a
// single tool call. It is the routing seam introduced for Phase 2: the
// global daemon's ResourceFetcher returns one of these per call (per
// project_id); the legacy single-project daemon synthesizes one from
// Handlers' direct fields. Either way, handler code reads from the
// bundle instead of h.journal / h.index / etc., so a future refactor
// can swap the source without touching every method body again.
//
// Fields are pointer / interface types so Bundle stays cheap to copy
// (one struct of pointer-sized fields). A zero-value Bundle (Journal
// nil, Index nil, ProjectPath "") is the legitimate "no project /
// degraded mode" signal — handler methods that touch the journal
// must still nil-guard before append.
type Bundle struct {
	Journal      core.Journal
	Index        *core.Index
	Redactor     *redact.Redactor
	ContentStore *content.Store
	Sandbox      sandbox.Sandbox
	ProjectPath  string
}

// ResourceFetcher is the lookup function shape Handlers calls to find
// per-project resources for an inbound RPC. The fetcher receives the
// project_id resolved from ProjectIDFrom(ctx); an empty value is the
// fetcher's signal to fall back to its own notion of a default project
// (the global daemon returns errProjectIDRequired in that case).
//
// Returning an error short-circuits the handler with a -32603 RPC
// error reflecting the project lookup failure (e.g. unknown project,
// permissions overlay parse error).
type ResourceFetcher func(projectID string) (Bundle, error)

// SetResourceFetcher installs a fetcher that handler methods call to
// resolve per-call project resources. The global daemon (Phase 2 commit
// 4c) wires this to its lazy resource cache; the legacy single-project
// daemon leaves it nil so handlers fall back to direct fields and the
// pre-Phase-2 contract holds.
//
// Safe to set once at construction; later mutations are guarded by h.mu
// so an in-flight RPC either sees the old or new fetcher, never a torn
// pointer. There is intentionally no UnsetResourceFetcher — the lifecycle
// is "set once at daemon startup, never cleared".
func (h *Handlers) SetResourceFetcher(f ResourceFetcher) {
	h.mu.Lock()
	h.fetchResources = f
	h.mu.Unlock()
}

// ProjectsLister returns the set of project paths the host daemon
// currently has resources cached for. Phase 2: the dashboard's
// project switcher reads this via handleAPIAllDaemons so the user
// can hop between every project that has touched the global daemon
// in the current session, even ones with no per-process registry
// row of their own.
//
// Returning nil or an empty slice is interpreted as "no projects
// loaded" — the dashboard then surfaces only legacy registry rows
// (back-compat for v0.3.x daemons still running side-by-side).
type ProjectsLister func() []string

// SetProjectsLister installs a lister that surfaces the daemon's
// loaded project paths to the dashboard. Same setter discipline as
// SetResourceFetcher (set once at daemon startup, mu-guarded so an
// in-flight RPC never sees a torn pointer).
func (h *Handlers) SetProjectsLister(f ProjectsLister) {
	h.mu.Lock()
	h.listProjects = f
	h.mu.Unlock()
}

// LoadedProjects returns the project paths the lister knows about,
// or nil when no lister is installed. HTTP handlers use this to
// populate the cross-project dashboard view; tests and the legacy
// single-project daemon path leave the lister nil and continue
// reading the on-disk registry.
func (h *Handlers) LoadedProjects() []string {
	h.mu.RLock()
	f := h.listProjects
	h.mu.RUnlock()
	if f == nil {
		return nil
	}
	return f()
}

// ProjectDropper evicts a single project from the host daemon's
// resource cache (closing its journal, persisting the index where
// safe, stopping watchers). Wired by the global daemon so v0.5.0's
// `dfmt remove` flow can clear stale cache entries without
// restarting the daemon.
//
// Returning an error surfaces to the RPC caller as -32603. Empty
// projectID returns an error rather than silently no-oping; the
// daemon's default project is also a no-op (handled inside the
// dropper itself, not by the seam).
type ProjectDropper func(projectID string) error

// SetProjectDropper installs a dropper that DropProject RPC calls.
// Same setter discipline as SetResourceFetcher / SetProjectsLister:
// set once at daemon startup, mu-guarded so an in-flight RPC never
// sees a torn pointer.
func (h *Handlers) SetProjectDropper(f ProjectDropper) {
	h.mu.Lock()
	h.dropProject = f
	h.mu.Unlock()
}

// DropProjectParams names the project to evict from the daemon's
// resource cache. Empty ProjectID is rejected with -32602 — the
// dropper does not fall back to a default project (the default's
// resources live on Daemon, not in the cache).
type DropProjectParams struct {
	ProjectID string `json:"project_id"`
}

// DropProjectResponse acknowledges the eviction. Dropped is true
// when the project was found in the cache and torn down; false
// when the dropper is unconfigured (legacy daemon) or the project
// was not cached. Either way the RPC succeeds — clients use Dropped
// only for telemetry, not control flow.
type DropProjectResponse struct {
	Dropped bool `json:"dropped"`
}

// DropProject evicts the named project from the daemon's resource
// cache. Used by `dfmt remove` to clear in-memory state alongside
// the on-disk setup-manifest entry; without it, a stale ProjectResources
// would survive in the cache until daemon restart and the dashboard
// switcher would still list the removed project.
//
// Legacy single-project daemons (no dropper installed) return
// {Dropped: false} without error — there is no cache to evict from
// in that mode, and the operation is semantically a no-op.
func (h *Handlers) DropProject(ctx context.Context, params DropProjectParams) (*DropProjectResponse, error) {
	_ = ctx
	if params.ProjectID == "" {
		return nil, errors.New("project_id required")
	}
	h.mu.RLock()
	f := h.dropProject
	h.mu.RUnlock()
	if f == nil {
		return &DropProjectResponse{Dropped: false}, nil
	}
	if err := f(params.ProjectID); err != nil {
		return nil, err
	}
	return &DropProjectResponse{Dropped: true}, nil
}

// resolveBundle returns the resource Bundle a handler method should
// read from for the in-flight RPC. Resolution:
//
//  1. If a fetcher is installed, call it with the ctx project_id. The
//     fetcher's error is propagated; no fallback to direct fields when
//     a fetcher is present (the global daemon explicitly wants to fail
//     unknown-project requests rather than masking them with stale
//     default-project state).
//  2. Otherwise, synthesize a Bundle from direct fields (legacy mode).
//     Direct-field reads are guarded by h.mu — same locking the
//     existing accessors use.
//
// Handlers that touch the journal must still nil-guard Bundle.Journal:
// in degraded MCP mode the legacy daemon may have a nil journal, and
// a misconfigured fetcher could return a Bundle without one.
func (h *Handlers) resolveBundle(ctx context.Context) (Bundle, error) {
	h.mu.RLock()
	f := h.fetchResources
	h.mu.RUnlock()
	if f != nil {
		return f(ProjectIDFrom(ctx))
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return Bundle{
		Journal:      h.journal,
		Index:        h.index,
		Redactor:     h.redactor,
		ContentStore: h.store,
		Sandbox:      h.sandbox,
		ProjectPath:  h.project,
	}, nil
}

// SetActivityFn registers a callback invoked on every inbound RPC. The daemon
// uses this to reset its idle timer — without it, "idle" becomes an uptime cap
// because the timer is never armed again after startup.
func (h *Handlers) SetActivityFn(fn func()) {
	h.mu.Lock()
	h.activity = fn
	h.mu.Unlock()
}

// touch fires the activity callback if one is registered. Called from every
// RPC entry point so long-lived busy sessions don't get killed by the idle
// timeout.
func (h *Handlers) touch() {
	h.mu.RLock()
	fn := h.activity
	h.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// stashContent writes body as a single-chunk set into the content store and
// returns the chunk set ID. Returns "" when the store is not configured or
// the body is empty. Errors are logged to stderr and swallowed — content
// stashing must never fail a sandbox call.
//
// Dedup: identical (project, kind, source, body) tuples seen within
// dedupTTL return the chunk-set ID of the original stash instead of
// writing a fresh copy. Project ID is part of the key so that two
// projects served by the same global daemon do not return a content_id
// pointing into the *other* project's content store — the receiver
// would 404 when fetching the bytes back.
//
// Re-running the same command in one project, opening a generated file
// twice, or hitting the same URL during an agent loop all collapse to
// one entry within that project's scope. The agent-visible content_id
// stays stable across the dedup window. Body bytes are hashed
// post-redaction (caller already redacts) so a redacted secret can't be
// recovered by checking which key the cache picked.
//
// store is the per-project content store resolved via the bundle. The
// previous implementation read h.getStore() directly, which is the
// default-project store — nil in global daemon mode → every Exec /
// Read / Fetch returned content_id="" and the dashboard's "show
// content" links broke for every project the global daemon served.
func (h *Handlers) stashContent(store *content.Store, projectID, kind, source, intent, body string) string {
	if store == nil || body == "" {
		return ""
	}
	dedupKey := stashDedupKey(projectID, kind, source, body)
	if cached := h.dedupLookup(dedupKey); cached != "" {
		return cached
	}
	setID := string(core.NewULID(time.Now()))
	set := &content.ChunkSet{
		ID:      setID,
		Kind:    kind,
		Source:  source,
		Intent:  intent,
		Created: time.Now(),
	}
	if err := store.PutChunkSet(set); err != nil {
		fmt.Fprintf(os.Stderr, "stashContent put set: %v\n", err)
		return ""
	}
	chunk := &content.Chunk{
		ID:       string(core.NewULID(time.Now())),
		ParentID: setID,
		Index:    0,
		Kind:     content.ChunkKindText,
		Body:     body,
		Created:  time.Now(),
	}
	if err := store.PutChunk(chunk); err != nil {
		fmt.Fprintf(os.Stderr, "stashContent put chunk: %v\n", err)
		return ""
	}
	h.dedupRecord(dedupKey, setID)
	return setID
}

// stashDedupKey returns a stable hex digest over (projectID, kind,
// source, body) so two stashes of the same bytes from the same source
// in the same project compare equal even when the intent string
// differs. projectID prefix isolates two projects served by the same
// global daemon — without it project A and project B fetching the
// same URL collapse to one entry pointing at A's store, and B's
// follow-up by-content_id read 404s. NUL separators block the trivial
// "concat collision" where a kind ending in "x" and source starting
// with "x" hash identically to kind ending in "" and source "xx".
func stashDedupKey(projectID, kind, source, body string) string {
	h := sha256.New()
	h.Write([]byte(projectID))
	h.Write([]byte{0})
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}

// dedupLookup returns the cached chunk-set ID for key when one exists and is
// still within TTL. Expired entries are removed lazily so dedupRecord
// doesn't have to walk the whole map on every miss. A hit increments the
// dedupHits counter for telemetry; misses are uncounted (we count the
// observable savings, not the work that didn't happen).
func (h *Handlers) dedupLookup(key string) string {
	h.dedupMu.Lock()
	defer h.dedupMu.Unlock()
	if h.dedupCache == nil {
		return ""
	}
	e, ok := h.dedupCache[key]
	if !ok {
		return ""
	}
	if time.Now().After(e.expiresAt) {
		delete(h.dedupCache, key)
		return ""
	}
	h.dedupHits.Add(1)
	return e.contentID
}

// dedupRecord stores a (key -> contentID) mapping with expiry dedupTTL into
// the future. When the cache is full the recorder evicts expired entries
// first, then drops arbitrary live ones — a missed dedup is never wrong,
// just suboptimal.
func (h *Handlers) dedupRecord(key, contentID string) {
	h.dedupMu.Lock()
	defer h.dedupMu.Unlock()
	if h.dedupCache == nil {
		h.dedupCache = make(map[string]dedupEntry)
	}
	now := time.Now()
	if len(h.dedupCache) >= dedupCap {
		for k, v := range h.dedupCache {
			if now.After(v.expiresAt) {
				delete(h.dedupCache, k)
			}
		}
		for k := range h.dedupCache {
			if len(h.dedupCache) < dedupCap {
				break
			}
			delete(h.dedupCache, k)
		}
	}
	h.dedupCache[key] = dedupEntry{contentID: contentID, expiresAt: now.Add(dedupTTL)}
}

// sentCacheKey composes the cache key from session ID and content_id with a
// NUL separator — the same trick stashDedupKey uses to block prefix
// collisions ("sess" + "" + "x" must not equal "ses" + "" + "sx"). An empty
// session ID falls into a single shared "default" bucket so paths that
// haven't been threaded with WithSessionID still dedupe; this keeps tests
// and any pre-ADR-0011 caller working.
func sentCacheKey(sessionID, contentID string) string {
	return sessionID + "\x00" + contentID
}

// seenBefore reports whether the agent has already received this content_id
// within sentTTL, scoped to the session attached via WithSessionID (ADR-0011).
// Empty contentIDs are never "seen" — callers that pass "" have no content
// to dedupe and short-circuit elsewhere. Expired entries are removed lazily
// so the lookup path stays O(1) on the common (cache-miss) case.
func (h *Handlers) seenBefore(ctx context.Context, contentID string) bool {
	if contentID == "" {
		return false
	}
	key := sentCacheKey(SessionIDFrom(ctx), contentID)
	h.sentMu.Lock()
	defer h.sentMu.Unlock()
	if h.sentCache == nil {
		return false
	}
	expires, ok := h.sentCache[key]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(h.sentCache, key)
		return false
	}
	return true
}

// markSent records that the agent has received this content_id under the
// session attached via WithSessionID (ADR-0011), with expiry sentTTL into
// the future. FIFO eviction: when the cache is full we drop the oldest
// entry (front of sentOrder) regardless of TTL. Re-adding an existing
// (sessionID, contentID) pair refreshes its expiry but does not re-queue
// it — preventing the same key from occupying multiple slots in the FIFO
// under a busy retry loop.
func (h *Handlers) markSent(ctx context.Context, contentID string) {
	if contentID == "" {
		return
	}
	key := sentCacheKey(SessionIDFrom(ctx), contentID)
	h.sentMu.Lock()
	defer h.sentMu.Unlock()
	if h.sentCache == nil {
		h.sentCache = make(map[string]time.Time, sentCap)
	}
	if _, exists := h.sentCache[key]; !exists {
		if len(h.sentOrder) >= sentCap {
			oldest := h.sentOrder[0]
			h.sentOrder = h.sentOrder[1:]
			delete(h.sentCache, oldest)
		}
		h.sentOrder = append(h.sentOrder, key)
	}
	h.sentCache[key] = time.Now().Add(sentTTL)
}

// redactString returns s with sensitive data scrubbed, or s unchanged if no
// redactor is configured. ctx is used to resolve the per-project
// redactor in global daemon mode; nil ctx falls back to the default
// redactor.
func (h *Handlers) redactString(ctx context.Context, s string) string {
	r := h.redactorFor(ctx)
	if r == nil || s == "" {
		return s
	}
	return r.Redact(s)
}

// redactData walks a map[string]any and redacts any string values in place.
// Returns a new map rather than mutating the caller's to keep the API safe
// for concurrent reuse of the original data.
func (h *Handlers) redactData(ctx context.Context, data map[string]any) map[string]any {
	r := h.redactorFor(ctx)
	if r == nil {
		return data
	}
	return r.RedactEvent(data)
}

// redactEventForRender returns a copy of e with Actor, Tags, and Data scrubbed
// through the redactor. Closes V-01: redaction on the journal-write path
// catches secrets the regex knows about at the moment of write, but a pattern
// added later (or a near-miss the original write missed) would leak forever
// through dfmt_recall snapshots without this second pass at render time.
//
// The returned Event shares the original's typed fields (ID, TS, Type,
// Priority, Project, Sig) — those are server-set or hash-bound and need no
// redaction. Only the agent-controllable string surfaces are reprocessed.
func (h *Handlers) redactEventForRender(ctx context.Context, e core.Event) core.Event {
	r := h.redactorFor(ctx)
	if r == nil {
		return e
	}
	out := e
	if e.Actor != "" {
		out.Actor = r.Redact(e.Actor)
	}
	if len(e.Tags) > 0 {
		redactedTags := make([]string, len(e.Tags))
		for i, t := range e.Tags {
			redactedTags[i] = r.Redact(t)
		}
		out.Tags = redactedTags
	}
	if e.Data != nil {
		out.Data = r.RedactEvent(e.Data)
	}
	return out
}

// SetProject sets the project identifier stamped on all events written by this handler.
func (h *Handlers) SetProject(p string) {
	h.mu.Lock()
	h.project = p
	h.mu.Unlock()
}

// logEvent appends a tool call event to the journal and index.
// Data is redacted before persistence so secrets in tool arguments/output
// don't land in journal.jsonl.
func (h *Handlers) logEvent(ctx context.Context, eventType, summary string, data map[string]any) {
	bundle, err := h.resolveBundle(ctx)
	if err != nil || bundle.Journal == nil {
		return
	}
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  h.getProjectFor(ctx),
		Type:     core.EventType(eventType),
		Priority: core.PriP4,
		Source:   core.SrcMCP,
		Data:     h.redactData(ctx, data),
		Tags:     []string{h.redactString(ctx, summary)},
	}
	e.Sig = e.ComputeSig()
	if err := bundle.Journal.Append(ctx, e); err != nil {
		fmt.Fprintf(os.Stderr, "logEvent: journal append: %v\n", err)
	}
	if bundle.Index != nil {
		bundle.Index.Add(e)
	}
}

// RememberParams are the parameters for the Remember method.
// formatEventData formats event data for display, highlighting token fields.
func formatEventData(data map[string]any) string {
	if data == nil {
		return ""
	}
	// Check for token fields
	inputTokens, _ := getInt(data, core.KeyInputTokens)
	outputTokens, _ := getInt(data, core.KeyOutputTokens)
	cachedTokens, _ := getInt(data, core.KeyCachedTokens)
	model, hasModel := data[core.KeyModel]

	if inputTokens > 0 || outputTokens > 0 || cachedTokens > 0 {
		var parts []string
		if inputTokens > 0 {
			parts = append(parts, fmt.Sprintf("in:%d", inputTokens))
		}
		if outputTokens > 0 {
			parts = append(parts, fmt.Sprintf("out:%d", outputTokens))
		}
		if cachedTokens > 0 {
			parts = append(parts, fmt.Sprintf("cached:%d", cachedTokens))
		}
		if hasModel && model != nil {
			parts = append(parts, fmt.Sprintf("@%v", model))
		}
		return " " + strings.Join(parts, " ")
	}

	// Default: use first few keys as summary
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	// Show up to 3 key=value pairs
	summary := ""
	for i := 0; i < len(keys) && i < 3; i++ {
		if summary != "" {
			summary += " "
		}
		summary += fmt.Sprintf("%s:%v", keys[i], data[keys[i]])
	}
	if len(keys) > 3 {
		summary += " ..."
	}
	return " " + summary
}

// getInt extracts an integer from a map[string]any.
func getInt(data map[string]any, key string) (int, bool) {
	if data == nil {
		return 0, false
	}
	val, ok := data[key]
	if !ok {
		return 0, false
	}
	switch v := val.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// StreamParams are the parameters for the Stream method.
type StreamParams struct {
	ProjectID string `json:"project_id,omitempty"`
	From      string `json:"from,omitempty"`
}

// Stream streams events from the journal.
func (h *Handlers) Stream(ctx context.Context, params StreamParams) (<-chan core.Event, error) {
	h.touch()
	bundle, err := h.resolveBundle(ctx)
	if err != nil {
		return nil, err
	}
	if bundle.Journal == nil {
		return nil, errNoProject
	}
	return bundle.Journal.Stream(ctx, params.From)
}

// ── Sandbox wrappers ───────────────────────────────────────────────

// ExecParams are the parameters for sandbox execution.
func (h *Handlers) redactMatches(ctx context.Context, in []sandbox.ContentMatch) []sandbox.ContentMatch {
	if h.redactorFor(ctx) == nil || len(in) == 0 {
		return in
	}
	out := make([]sandbox.ContentMatch, len(in))
	for i, m := range in {
		m.Text = h.redactString(ctx, m.Text)
		out[i] = m
	}
	return out
}

// ReadParams are the parameters for sandbox file reading.
