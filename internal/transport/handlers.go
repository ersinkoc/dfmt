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
	statsCacheMu  sync.Mutex
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

// getProject returns the project identifier under read-lock.
func (h *Handlers) getProject() string {
	h.mu.RLock()
	p := h.project
	h.mu.RUnlock()
	return p
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
// Dedup: identical (kind, source, body) tuples seen within dedupTTL return
// the chunk-set ID of the original stash instead of writing a fresh copy.
// Re-running the same command, opening a generated file twice, or hitting
// the same URL during an agent loop all collapse to one entry. The
// agent-visible content_id stays stable across the dedup window, which lets
// downstream tooling key off it without churn. Body bytes are hashed
// post-redaction (caller already redacts) so a redacted secret can't be
// recovered by checking which key the cache picked.
func (h *Handlers) stashContent(kind, source, intent, body string) string {
	store := h.getStore()
	if store == nil || body == "" {
		return ""
	}
	dedupKey := stashDedupKey(kind, source, body)
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

// stashDedupKey returns a stable hex digest over (kind, source, body) so two
// stashes of the same bytes from the same source compare equal even when
// the intent string differs. NUL separators block the trivial "concat
// collision" where a kind ending in "x" and source starting with "x" hash
// identically to kind ending in "" and source "xx".
func stashDedupKey(kind, source, body string) string {
	h := sha256.New()
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
// redactor is configured.
func (h *Handlers) redactString(s string) string {
	r := h.getRedactor()
	if r == nil || s == "" {
		return s
	}
	return r.Redact(s)
}

// redactData walks a map[string]any and redacts any string values in place.
// Returns a new map rather than mutating the caller's to keep the API safe
// for concurrent reuse of the original data.
func (h *Handlers) redactData(data map[string]any) map[string]any {
	r := h.getRedactor()
	if r == nil {
		return data
	}
	return r.RedactEvent(data)
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
	if h.journal == nil {
		return
	}
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  h.getProject(),
		Type:     core.EventType(eventType),
		Priority: core.PriP4,
		Source:   core.SrcMCP,
		Data:     h.redactData(data),
		Tags:     []string{h.redactString(summary)},
	}
	e.Sig = e.ComputeSig()
	if err := h.journal.Append(ctx, e); err != nil {
		fmt.Fprintf(os.Stderr, "logEvent: journal append: %v\n", err)
	}
	if h.index != nil {
		h.index.Add(e)
	}
}

// RememberParams are the parameters for the Remember method.
type RememberParams struct {
	Type     string         `json:"type"`
	Priority string         `json:"priority"`
	Source   string         `json:"source"`
	Actor    string         `json:"actor,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Refs     []string       `json:"refs,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	// Direct token fields for MCP tools
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	CachedTokens int    `json:"cached_tokens,omitempty"`
	Model        string `json:"model,omitempty"`
	Message      string `json:"message,omitempty"`
}

// RememberResponse is the response for the Remember method.
type RememberResponse struct {
	ID string `json:"id"`
	TS string `json:"ts"`
}

// Remember adds an event to the journal and index.
func (h *Handlers) Remember(ctx context.Context, params RememberParams) (*RememberResponse, error) {
	h.touch()
	if h.journal == nil {
		return nil, errNoProject
	}
	// Handle direct token fields and the Message field — both are
	// advertised as MCP `dfmt_remember` parameters but the previous
	// version of this handler silently dropped Message. Result: the
	// recall snapshot showed a tag-only line ("[p3] #audit #preserve")
	// and dfmt_search could not find any word from the message body —
	// only tags. Closes the audit-discovered defect "Remember.Message
	// silently dropped from indexed event."
	data := params.Data
	hasTokenFields := params.InputTokens > 0 || params.OutputTokens > 0 || params.CachedTokens > 0 || params.Model != ""
	if hasTokenFields || params.Message != "" {
		if data == nil {
			data = make(map[string]any)
		}
		if params.InputTokens > 0 {
			data[core.KeyInputTokens] = params.InputTokens
		}
		if params.OutputTokens > 0 {
			data[core.KeyOutputTokens] = params.OutputTokens
		}
		if params.CachedTokens > 0 {
			data[core.KeyCachedTokens] = params.CachedTokens
		}
		if params.Model != "" {
			data[core.KeyModel] = params.Model
		}
		if params.Message != "" {
			// Use the literal "message" key — that is the convention
			// the recall renderer (`internal/retrieve`) and the test
			// fixtures already follow.
			data["message"] = params.Message
		}
	}

	// Redact sensitive strings before the event is persisted or indexed.
	redactedTags := params.Tags
	if h.getRedactor() != nil && len(redactedTags) > 0 {
		redactedTags = make([]string, len(params.Tags))
		for i, t := range params.Tags {
			redactedTags[i] = h.redactString(t)
		}
	}

	// F-21: server-side override of Source and Priority. Both are
	// agent-controllable on the wire; without this the agent (or a prompt-
	// injected loop running through it) could write events claiming source
	// "githook" or "fswatch" and priority "p1" — exactly the bands `dfmt
	// recall` keeps under tight budget. The agent IS calling via MCP, so
	// Source is a fact, not a parameter. p1 is reserved for non-agent
	// paths (decisions logged by the operator, incident events). Anything
	// outside the agent-allowed band is coerced to p3.
	priority := core.Priority(params.Priority)
	switch priority {
	case core.PriP2, core.PriP3, core.PriP4:
		// keep
	default:
		priority = core.PriP3
	}

	// Create event
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  h.getProject(),
		Type:     core.EventType(params.Type),
		Priority: priority,
		Source:   core.SrcMCP,
		Actor:    params.Actor,
		Data:     h.redactData(data),
		Refs:     params.Refs,
		Tags:     redactedTags,
	}
	e.Sig = e.ComputeSig()

	// Append to journal
	if err := h.journal.Append(ctx, e); err != nil {
		return nil, fmt.Errorf("journal append: %w", err)
	}

	// Add to index (index is paired with journal — both nil in degraded mode,
	// but the journal nil-guard above already short-circuits that path; this
	// guard only matters for the daemon test seam where index is omitted).
	if h.index != nil {
		h.index.Add(e)
	}

	return &RememberResponse{
		ID: e.ID,
		TS: e.TS.Format(time.RFC3339Nano),
	}, nil
}

// SearchParams are the parameters for the Search method.
type SearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
	Layer string `json:"layer,omitempty"` // "bm25", "trigram", "fuzzy"
}

// SearchResponse is the response for the Search method.
type SearchResponse struct {
	Results []SearchHit `json:"results"`
	Layer   string      `json:"layer"`
}

// SearchHit represents a single search result. Excerpt carries a short
// content snippet (≤80 bytes, rune-aligned) so agents can decide
// whether to drill into the hit without a follow-up dfmt_recall round
// trip — net wire saving even after the per-hit byte cost. Empty when
// the indexed event predates the excerpt feature.
type SearchHit struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Layer   int     `json:"layer"`
	Type    string  `json:"type,omitempty"`
	Source  string  `json:"source,omitempty"`
	Excerpt string  `json:"excerpt,omitempty"`
}

// Search queries the index.
func (h *Handlers) Search(ctx context.Context, params SearchParams) (_ *SearchResponse, err error) {
	defer recordToolCall("search", ctx, &err, time.Now())
	h.touch()
	if params.Limit == 0 {
		params.Limit = 10
	}

	if h.index == nil {
		return &SearchResponse{Results: nil, Layer: params.Layer}, nil
	}

	var hits []core.ScoredHit
	resolvedLayer := params.Layer

	switch params.Layer {
	case "trigram":
		hits = h.index.SearchTrigram(params.Query, params.Limit)
	case "fuzzy":
		// Fuzzy search would go here
		hits = nil
	default:
		// Default: BM25 first, then trigram fallback when BM25 returns
		// nothing. BM25 misses synthetic markers (AUDIT_PROBE_XJ7Q3),
		// UUIDs, and other tokens that the Porter stemmer drops or
		// splits awkwardly; trigram match restores them. Reporting the
		// resolved layer back lets clients distinguish a true miss
		// from a fallback hit.
		hits = h.index.SearchBM25(params.Query, params.Limit)
		if len(hits) > 0 {
			resolvedLayer = "bm25"
		} else {
			hits = h.index.SearchTrigram(params.Query, params.Limit)
			if len(hits) > 0 {
				resolvedLayer = "trigram"
			}
		}
	}

	results := make([]SearchHit, len(hits))
	for i, hit := range hits {
		results[i] = SearchHit{
			ID:      hit.ID,
			Score:   hit.Score,
			Layer:   hit.Layer,
			Excerpt: h.index.Excerpt(hit.ID),
		}
	}

	return &SearchResponse{
		Results: results,
		Layer:   resolvedLayer,
	}, nil
}

// RecallParams are the parameters for the Recall method.
type RecallParams struct {
	Budget int    `json:"budget,omitempty"`
	Format string `json:"format,omitempty"`
}

// RecallResponse is the response for the Recall method.
type RecallResponse struct {
	Snapshot string `json:"snapshot"`
	Format   string `json:"format"`
}

// Recall builds a session snapshot with tier-ordered greedy fill.
func (h *Handlers) Recall(ctx context.Context, params RecallParams) (_ *RecallResponse, err error) {
	defer recordToolCall("recall", ctx, &err, time.Now())
	h.touch()
	if h.journal == nil {
		return nil, errNoProject
	}
	budget := h.recallBudget(params.Budget)
	format := h.recallFormat(params.Format)

	// Per-tier streaming with FIFO eviction (closes review finding #7).
	//
	// Previous stopgap: read up to recallMaxBufferedEvents=5000 events
	// off the journal stream, then sort the truncated slice by priority.
	// On long-running projects with >5000 events that meant P1
	// decisions past index 5000 were silently dropped — the priority
	// sort had nothing to elevate.
	//
	// New behavior: classify each event as we stream and place it in
	// its tier's bucket. Each bucket has its own cap; on overflow we
	// FIFO-evict the oldest in-bucket event (Recall serves the "most
	// relevant", which for tiers means more recent within-tier).
	// Memory is bounded by the sum of tier caps, independent of
	// journal length. P1 events from any journal position survive as
	// long as the total P1 count fits p1Cap.
	//
	// streamCtx is a child of ctx with its own cancel so the journal's
	// stream goroutine exits cleanly if Recall returns early (e.g.,
	// caller cancellation, downstream render error).
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	stream, err := h.journal.Stream(streamCtx, "")
	if err != nil {
		return nil, fmt.Errorf("stream journal: %w", err)
	}

	const (
		p1Cap = 5000 // decisions/task-done — rare; keep nearly all
		p2Cap = 1000 // commits/errors/elevated notes
		p3Cap = 500  // file edits / audit findings
		p4Cap = 500  // tool calls / unelevated notes
	)
	classifier := core.NewClassifier()
	caps := [4]int{p1Cap, p2Cap, p3Cap, p4Cap}
	var buckets [4][]core.Event

	for e := range stream {
		var idx int
		switch classifier.Classify(e) {
		case core.PriP1:
			idx = 0
		case core.PriP2:
			idx = 1
		case core.PriP3:
			idx = 2
		case core.PriP4:
			idx = 3
		default:
			idx = 3 // unknown priority → P4 bucket so events are still surfaced
		}
		if len(buckets[idx]) >= caps[idx] {
			// In-place FIFO shift. `s = s[1:]` would also drop the
			// front element but slowly grows the backing array on
			// repeated append, and would retain a reference to the
			// dropped Event in the unreachable head slot. copy +
			// overwrite keeps the cap bounded and lets GC collect
			// dropped event payloads.
			copy(buckets[idx], buckets[idx][1:])
			buckets[idx][len(buckets[idx])-1] = e
		} else {
			buckets[idx] = append(buckets[idx], e)
		}
	}

	// Concatenate tiers in priority order. Within each tier the
	// journal streamed events TS-ascending, so reverse-iterate to
	// surface newest-first — matching the previous sort.Slice
	// "TS.After" tiebreak.
	sorted := make([]core.Event, 0, len(buckets[0])+len(buckets[1])+len(buckets[2])+len(buckets[3]))
	for tier := 0; tier < 4; tier++ {
		bucket := buckets[tier]
		for i := len(bucket) - 1; i >= 0; i-- {
			sorted = append(sorted, bucket[i])
		}
	}

	// Greedy fill with budget. Render each candidate line first so we know
	// its exact byte cost, then stop as soon as the budget can't hold the
	// current event — the list is priority-sorted, so a smaller later event
	// sneaking in would violate the tier ordering.
	var used int
	var lines []string
	lines = append(lines, "# Session Snapshot\n")

	for _, e := range sorted {
		var dataStr string
		if e.Data != nil {
			dataStr = formatEventData(e.Data)
		}
		// Compact "MM-DD HH:MM:SS" — full RFC3339 doubled the per-line cost
		// for no benefit since recall is project-scoped, but pure HH:MM:SS
		// (review finding #24) collapsed multi-day sessions ambiguously.
		// Year is implied by the project's lifetime; month-day disambiguates.
		ts := e.TS.Format("01-02 15:04:05")
		actor := ""
		if e.Actor != "" {
			actor = fmt.Sprintf(" @%s", e.Actor)
		}
		tags := ""
		if len(e.Tags) > 0 {
			tags = fmt.Sprintf(" #%s", strings.Join(e.Tags, " #"))
		}
		line := fmt.Sprintf("- [%s] %s%s%s%s", e.Priority, ts, actor, tags, dataStr)
		// +1 for the newline strings.Join will insert between this line and
		// the next; slightly over-counts on the last line but never under.
		lineSize := len(line) + 1

		if used+lineSize > budget {
			break
		}

		lines = append(lines, line)
		used += lineSize
	}

	if len(lines) == 1 {
		lines = append(lines, "_No events in session_")
	}

	return &RecallResponse{
		Snapshot: strings.Join(lines, "\n"),
		Format:   format,
	}, nil
}

// StatsParams are the parameters for the Stats method.
type StatsParams struct {
	// NoCache bypasses the TTL-based memoisation. Human-driven CLI
	// callers (`dfmt stats`) set this so successive runs reflect the
	// current journal instead of a 5-second-stale snapshot. Dashboard
	// HTTP polling leaves this false to keep its high-frequency loop
	// from re-streaming the journal on every poll.
	NoCache bool `json:"no_cache,omitempty"`
}

// StatsResponse is the response for the Stats method.
//
// Most numeric fields use `omitempty` so a fresh project (zero events,
// zero token reports, zero MCP traffic) returns a small payload. The
// dashboard reads each field with a `|| 0` fallback (see
// internal/transport/dashboard.go) so an absent field reads as zero
// — same as the explicit zero would. EventsTotal stays without
// `omitempty` because consumers use its presence as a "stats are
// populated" sentinel.
type StatsResponse struct {
	EventsTotal      int            `json:"events_total"`
	EventsByType     map[string]int `json:"events_by_type,omitempty"`
	EventsByPriority map[string]int `json:"events_by_priority,omitempty"`
	SessionStart     string         `json:"session_start,omitempty"`
	SessionEnd       string         `json:"session_end,omitempty"`
	// LLM token metrics — populated only when callers pass input_tokens /
	// output_tokens / cached_tokens to dfmt_remember (the MCP layer cannot
	// observe API usage on its own).
	TotalInputTokens  int     `json:"total_input_tokens,omitempty"`
	TotalOutputTokens int     `json:"total_output_tokens,omitempty"`
	TotalCachedTokens int     `json:"total_cached_tokens,omitempty"`
	TokenSavings      int     `json:"token_savings,omitempty"` // alias of TotalCachedTokens
	CacheHitRate      float64 `json:"cache_hit_rate,omitempty"`
	// MCP byte savings — populated automatically by sandbox tool calls.
	// raw = pre-filter (post-redact) bytes; returned = bytes actually sent back.
	TotalRawBytes      int     `json:"total_raw_bytes,omitempty"`
	TotalReturnedBytes int     `json:"total_returned_bytes,omitempty"`
	BytesSaved         int     `json:"bytes_saved,omitempty"`       // raw - returned
	CompressionRatio   float64 `json:"compression_ratio,omitempty"` // saved / raw, 0..1
	// DedupHits is the number of times stashContent collapsed an
	// identical (kind, source, body) tuple to an existing chunk-set ID
	// instead of writing a new one. Process-lifetime counter — survives
	// idle restarts via re-warming, not via persistence.
	DedupHits int `json:"dedup_hits,omitempty"`
	// Native tool awareness: how many tool calls bypassed dfmt MCP
	NativeToolCalls      map[string]int `json:"native_tool_calls,omitempty"`       // count by tool name (Bash, Read, Glob, etc.)
	MCPToolCalls         map[string]int `json:"mcp_tool_calls,omitempty"`          // count by MCP tool name (dfmt.exec, dfmt.read, etc.)
	NativeToolBypassRate float64        `json:"native_tool_bypass_rate,omitempty"` // % of tool calls that used native tools
}

// knownNativeTools is the set of Claude Code built-in tool names captured
// by the PreToolUse hook and logged as note events with the tool name as tag.
var knownNativeTools = map[string]struct{}{
	"Bash": {}, "Read": {}, "Edit": {}, "Write": {}, "Glob": {},
	"Grep": {}, "WebFetch": {}, "WebSearch": {}, "TaskCreate": {},
	"TaskUpdate": {}, "TaskDone": {}, "Agent": {},
}

// Stats returns aggregated statistics from the journal.
// Aggregates as events stream in — O(|event-types| + |priorities|) memory,
// not O(|journal|). The previous implementation buffered every event into a
// slice, which grew unbounded on long-running projects.
func (h *Handlers) Stats(ctx context.Context, params StatsParams) (*StatsResponse, error) {
	h.touch()
	if h.journal == nil {
		return nil, errNoProject
	}

	// TTL cache: the dashboard polls /api/stats and re-streaming the full
	// journal every poll burned hundreds of ms on busy projects. Cache hits
	// return a defensive copy so callers can't mutate shared maps. Cache
	// misses (re)compute and store the fresh result — invalidation is
	// purely TTL-based; a freshly-appended event becomes visible at the
	// next refresh, which is well within human-readable polling rates.
	//
	// Bypassed entirely when params.NoCache is set so that `dfmt stats`
	// from the CLI shows the current journal state — humans interpret
	// "the number didn't change" as "DFMT is broken", and the 5-second
	// staleness window makes that interpretation easy to fall into.
	if !params.NoCache {
		h.statsCacheMu.Lock()
		if h.statsCache != nil && time.Since(h.statsCachedAt) < statsTTL {
			cached := h.statsCache
			h.statsCacheMu.Unlock()
			return cloneStatsResponse(cached), nil
		}
		h.statsCacheMu.Unlock()
	}

	stream, err := h.journal.Stream(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("stream journal: %w", err)
	}

	resp := &StatsResponse{
		EventsByType:     make(map[string]int),
		EventsByPriority: make(map[string]int),
		NativeToolCalls:  make(map[string]int),
		MCPToolCalls:     make(map[string]int),
	}

	classifier := core.NewClassifier()
	var earliest, latest time.Time
	var totalInput, totalOutput, totalCached int
	var totalRawBytes, totalReturnedBytes int
	total := 0

	for e := range stream {
		total++
		resp.EventsByType[string(e.Type)]++
		resp.EventsByPriority[string(classifier.Classify(e))]++

		if inputTokens, ok := getInt(e.Data, core.KeyInputTokens); ok {
			totalInput += inputTokens
		}
		if outputTokens, ok := getInt(e.Data, core.KeyOutputTokens); ok {
			totalOutput += outputTokens
		}
		if cachedTokens, ok := getInt(e.Data, core.KeyCachedTokens); ok {
			totalCached += cachedTokens
		}
		if rawBytes, ok := getInt(e.Data, core.KeyRawBytes); ok {
			totalRawBytes += rawBytes
		}
		if returnedBytes, ok := getInt(e.Data, core.KeyReturnedBytes); ok {
			totalReturnedBytes += returnedBytes
		}

		// Track native tool calls via PreToolUse hook (note events with tool tag)
		if e.Type == core.EvtNote && len(e.Tags) > 0 {
			if _, ok := knownNativeTools[e.Tags[0]]; ok {
				resp.NativeToolCalls[e.Tags[0]]++
			}
		}
		// Track dfmt MCP tool calls (tool.exec, tool.read, tool.fetch, tool.glob, tool.grep, tool.edit, tool.write)
		if e.Type == "tool.exec" || e.Type == "tool.read" || e.Type == "tool.fetch" ||
			e.Type == "tool.glob" || e.Type == "tool.grep" || e.Type == "tool.edit" || e.Type == "tool.write" {
			resp.MCPToolCalls[string(e.Type)]++
		}

		if earliest.IsZero() || e.TS.Before(earliest) {
			earliest = e.TS
		}
		if latest.IsZero() || e.TS.After(latest) {
			latest = e.TS
		}
	}

	resp.EventsTotal = total
	resp.TotalInputTokens = totalInput
	resp.TotalOutputTokens = totalOutput
	resp.TotalCachedTokens = totalCached
	resp.TokenSavings = totalCached
	if totalInput > 0 {
		resp.CacheHitRate = float64(totalCached) / float64(totalInput) * 100
	}

	resp.TotalRawBytes = totalRawBytes
	resp.TotalReturnedBytes = totalReturnedBytes
	if totalRawBytes > totalReturnedBytes {
		resp.BytesSaved = totalRawBytes - totalReturnedBytes
	}
	if totalRawBytes > 0 {
		resp.CompressionRatio = float64(resp.BytesSaved) / float64(totalRawBytes)
	}
	resp.DedupHits = int(h.dedupHits.Load())

	// Compute native tool bypass rate
	var nativeTotal, mcpTotal int
	for _, n := range resp.NativeToolCalls {
		nativeTotal += n
	}
	for _, n := range resp.MCPToolCalls {
		mcpTotal += n
	}
	totalToolCalls := nativeTotal + mcpTotal
	if totalToolCalls > 0 {
		resp.NativeToolBypassRate = float64(nativeTotal) / float64(totalToolCalls) * 100
	}

	if !earliest.IsZero() {
		resp.SessionStart = earliest.Format(time.RFC3339)
	}
	if !latest.IsZero() {
		resp.SessionEnd = latest.Format(time.RFC3339)
	}

	h.statsCacheMu.Lock()
	h.statsCache = resp
	h.statsCachedAt = time.Now()
	h.statsCacheMu.Unlock()

	return cloneStatsResponse(resp), nil
}

// cloneStatsResponse produces a defensive copy. The cache is shared across
// concurrent callers; without copying, the dashboard's mutating its own map
// (e.g., adding derived keys) would corrupt the cached value for every
// future caller. Maps are duplicated; primitive fields are value-copied.
func cloneStatsResponse(src *StatsResponse) *StatsResponse {
	if src == nil {
		return nil
	}
	out := *src
	out.EventsByType = cloneIntMap(src.EventsByType)
	out.EventsByPriority = cloneIntMap(src.EventsByPriority)
	out.NativeToolCalls = cloneIntMap(src.NativeToolCalls)
	out.MCPToolCalls = cloneIntMap(src.MCPToolCalls)
	return &out
}

func cloneIntMap(src map[string]int) map[string]int {
	if src == nil {
		return nil
	}
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

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
	From string `json:"from,omitempty"`
}

// Stream streams events from the journal.
func (h *Handlers) Stream(ctx context.Context, params StreamParams) (<-chan core.Event, error) {
	h.touch()
	if h.journal == nil {
		return nil, errNoProject
	}
	return h.journal.Stream(ctx, params.From)
}

// ── Sandbox wrappers ───────────────────────────────────────────────

// ExecParams are the parameters for sandbox execution.
type ExecParams struct {
	Code    string            `json:"code"`
	Lang    string            `json:"lang,omitempty"`
	Intent  string            `json:"intent,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds
	Return  string            `json:"return,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// ExecResponse is the response from sandbox execution.
type ExecResponse struct {
	Exit       int                    `json:"exit"`
	Stdout     string                 `json:"stdout,omitempty"`
	Stderr     string                 `json:"stderr,omitempty"`
	Summary    string                 `json:"summary,omitempty"`
	Matches    []sandbox.ContentMatch `json:"matches,omitempty"`
	Vocabulary []string               `json:"vocabulary,omitempty"`
	DurationMs int                    `json:"duration_ms"`
	TimedOut   bool                   `json:"timed_out"`
	ContentID  string                 `json:"content_id,omitempty"`
}

// Exec executes code via the sandbox.
func (h *Handlers) Exec(ctx context.Context, params ExecParams) (_ *ExecResponse, err error) {
	defer recordToolCall("exec", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.execSem)
	if err != nil {
		return nil, err
	}
	defer release()

	var timeout time.Duration
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	} else {
		timeout = sandbox.DefaultExecTimeout
	}

	req := sandbox.ExecReq{
		Code:    params.Code,
		Lang:    params.Lang,
		Intent:  params.Intent,
		Timeout: timeout,
		Return:  params.Return,
		Env:     params.Env,
	}

	resp, err := h.sandbox.Exec(ctx, req)
	if err != nil {
		return nil, err
	}

	// Stash the full pre-filter output so the agent can fetch raw bytes via
	// the chunk-set ID later. resp.Stdout may have been dropped by the
	// return-policy filter when the output was large with no intent — using
	// it for stashing would leave the content store with empty bytes and
	// the chunk-set ID a dead pointer. RawStdout always carries the full
	// (capped at MaxRawBytes) output.
	stderr := h.redactString(resp.Stderr)
	rawStash := h.redactString(resp.RawStdout) + stderr
	contentID := h.stashContent("exec-stdout", "sandbox.exec", params.Intent, rawStash)

	// Wire dedup (ADR-0009): if the same content_id was emitted earlier in
	// this daemon's lifetime, the agent already has these bytes. Strip the
	// payload to a thin acknowledgement and let the agent opt back in via
	// Return:"raw" when it actually needs them again. We log the invocation
	// at full byte size before short-circuiting so dashboard stats reflect
	// the work that ran.
	if params.Return != "raw" && h.seenBefore(ctx, contentID) {
		h.logEvent(ctx, "tool.exec", params.Intent, map[string]any{
			"code":                params.Code,
			"lang":                params.Lang,
			"exit":                resp.Exit,
			"duration":            resp.DurationMs,
			core.KeyRawBytes:      len(rawStash),
			core.KeyReturnedBytes: len(sentUnchangedSummary),
		})
		return &ExecResponse{
			Exit:       resp.Exit,
			Summary:    sentUnchangedSummary,
			DurationMs: resp.DurationMs,
			TimedOut:   resp.TimedOut,
			ContentID:  contentID,
		}, nil
	}

	stdout := h.redactString(resp.Stdout)
	summary := h.redactString(resp.Summary)
	matches := h.redactMatches(resp.Matches)

	rawBytes := len(rawStash)
	returnedBytes := len(stdout) + len(stderr) + len(summary)
	for _, m := range matches {
		returnedBytes += len(m.Text)
	}

	// Log the invocation. Code goes in redacted because secrets leak through
	// command lines far more often than through stdout.
	h.logEvent(ctx, "tool.exec", params.Intent, map[string]any{
		"code":                params.Code,
		"lang":                params.Lang,
		"exit":                resp.Exit,
		"duration":            resp.DurationMs,
		core.KeyRawBytes:      rawBytes,
		core.KeyReturnedBytes: returnedBytes,
	})

	h.markSent(ctx, contentID)
	return &ExecResponse{
		Exit:       resp.Exit,
		Stdout:     stdout,
		Stderr:     stderr,
		Summary:    summary,
		Matches:    matches,
		Vocabulary: resp.Vocabulary,
		DurationMs: resp.DurationMs,
		TimedOut:   resp.TimedOut,
		ContentID:  contentID,
	}, nil
}

// redactMatches returns a new slice with Text fields redacted.
func (h *Handlers) redactMatches(in []sandbox.ContentMatch) []sandbox.ContentMatch {
	if h.getRedactor() == nil || len(in) == 0 {
		return in
	}
	out := make([]sandbox.ContentMatch, len(in))
	for i, m := range in {
		m.Text = h.redactString(m.Text)
		out[i] = m
	}
	return out
}

// ReadParams are the parameters for sandbox file reading.
type ReadParams struct {
	Path   string `json:"path"`
	Intent string `json:"intent,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
	Return string `json:"return,omitempty"`
}

// ReadResponse is the response from sandbox file reading.
type ReadResponse struct {
	Content   string                 `json:"content,omitempty"`
	Summary   string                 `json:"summary,omitempty"`
	Matches   []sandbox.ContentMatch `json:"matches,omitempty"`
	Size      int64                  `json:"size"`
	ReadBytes int64                  `json:"read_bytes"`
	ContentID string                 `json:"content_id,omitempty"`
}

// Read reads a file via the sandbox.
func (h *Handlers) Read(ctx context.Context, params ReadParams) (_ *ReadResponse, err error) {
	defer recordToolCall("read", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.readSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.ReadReq{
		Path:   params.Path,
		Intent: params.Intent,
		Offset: params.Offset,
		Limit:  params.Limit,
		Return: params.Return,
	}

	resp, err := h.sandbox.Read(ctx, req)
	if err != nil {
		return nil, err
	}

	// Stash the full pre-filter content (RawContent) so the chunk-set ID is
	// a real pointer to the bytes. resp.Content carries the filtered view
	// for the client and may be empty when the policy excluded inline body.
	rawStash := h.redactString(resp.RawContent)
	contentID := h.stashContent("file-read", params.Path, params.Intent, rawStash)

	// Wire dedup short-circuit. See ADR-0009 and the matching block in Exec
	// for the full rationale.
	if params.Return != "raw" && h.seenBefore(ctx, contentID) {
		h.logEvent(ctx, "tool.read", params.Intent, map[string]any{
			"path":                params.Path,
			"read_bytes":          resp.ReadBytes,
			"size":                resp.Size,
			core.KeyRawBytes:      len(rawStash),
			core.KeyReturnedBytes: len(sentUnchangedSummary),
		})
		return &ReadResponse{
			Summary:   sentUnchangedSummary,
			Size:      resp.Size,
			ReadBytes: resp.ReadBytes,
			ContentID: contentID,
		}, nil
	}

	redContent := h.redactString(resp.Content)
	summary := h.redactString(resp.Summary)
	matches := h.redactMatches(resp.Matches)

	rawBytes := len(rawStash)
	returnedBytes := len(redContent) + len(summary)
	for _, m := range matches {
		returnedBytes += len(m.Text)
	}

	h.logEvent(ctx, "tool.read", params.Intent, map[string]any{
		"path":                params.Path,
		"read_bytes":          resp.ReadBytes,
		"size":                resp.Size,
		core.KeyRawBytes:      rawBytes,
		core.KeyReturnedBytes: returnedBytes,
	})

	h.markSent(ctx, contentID)
	return &ReadResponse{
		Content:   redContent,
		Summary:   summary,
		Matches:   matches,
		Size:      resp.Size,
		ReadBytes: resp.ReadBytes,
		ContentID: contentID,
	}, nil
}

// FetchParams are the parameters for sandbox HTTP fetching.
type FetchParams struct {
	URL     string            `json:"url"`
	Intent  string            `json:"intent,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Return  string            `json:"return,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds
}

// FetchResponse is the response from sandbox HTTP fetching.
type FetchResponse struct {
	Status     int                    `json:"status"`
	Headers    map[string]string      `json:"headers,omitempty"`
	Body       string                 `json:"body,omitempty"`
	Summary    string                 `json:"summary,omitempty"`
	Matches    []sandbox.ContentMatch `json:"matches,omitempty"`
	Vocabulary []string               `json:"vocabulary,omitempty"`
	TimedOut   bool                   `json:"timed_out"`
	ContentID  string                 `json:"content_id,omitempty"`
}

// GlobParams are the parameters for the Glob method.
type GlobParams struct {
	Pattern string `json:"pattern"`
	Intent  string `json:"intent,omitempty"`
}

// GlobResponse is the response from a glob operation.
type GlobResponse struct {
	Files   []string               `json:"files,omitempty"`
	Matches []sandbox.ContentMatch `json:"matches,omitempty"`
}

// GrepParams are the parameters for the Grep method.
type GrepParams struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Files           string `json:"files,omitempty"`
	Intent          string `json:"intent,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	Context         int    `json:"context,omitempty"`
}

// GrepResponse is the response from a grep operation.
type GrepResponse struct {
	Matches []sandbox.GrepMatch `json:"matches,omitempty"`
	Summary string              `json:"summary,omitempty"`
}

// Fetch fetches a URL via the sandbox.
func (h *Handlers) Fetch(ctx context.Context, params FetchParams) (_ *FetchResponse, err error) {
	defer recordToolCall("fetch", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.fetchSem)
	if err != nil {
		return nil, err
	}
	defer release()

	var timeout time.Duration
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	} else {
		timeout = 30 * time.Second
	}

	req := sandbox.FetchReq{
		URL:     params.URL,
		Intent:  params.Intent,
		Method:  params.Method,
		Headers: params.Headers,
		Body:    params.Body,
		Return:  params.Return,
		Timeout: timeout,
	}

	resp, err := h.sandbox.Fetch(ctx, req)
	if err != nil {
		return nil, err
	}

	// Stash full pre-filter body (RawBody); see Exec/Read rationale.
	rawStash := h.redactString(resp.RawBody)
	contentID := h.stashContent("fetch", params.URL, params.Intent, rawStash)

	// Wire dedup short-circuit. See ADR-0009. Status + Headers are kept
	// because they carry HTTP-level metadata (e.g. caching headers, redirect
	// chains) the agent may still want to reason about even when the body
	// hasn't changed.
	if params.Return != "raw" && h.seenBefore(ctx, contentID) {
		h.logEvent(ctx, "tool.fetch", params.Intent, map[string]any{
			"url":                 params.URL,
			"status":              resp.Status,
			core.KeyRawBytes:      len(rawStash),
			core.KeyReturnedBytes: len(sentUnchangedSummary),
		})
		return &FetchResponse{
			Status:    resp.Status,
			Headers:   resp.Headers,
			Summary:   sentUnchangedSummary,
			TimedOut:  resp.TimedOut,
			ContentID: contentID,
		}, nil
	}

	redBody := h.redactString(resp.Body)
	summary := h.redactString(resp.Summary)
	matches := h.redactMatches(resp.Matches)

	rawBytes := len(rawStash)
	returnedBytes := len(redBody) + len(summary)
	for _, m := range matches {
		returnedBytes += len(m.Text)
	}

	h.logEvent(ctx, "tool.fetch", params.Intent, map[string]any{
		"url":                 params.URL,
		"status":              resp.Status,
		core.KeyRawBytes:      rawBytes,
		core.KeyReturnedBytes: returnedBytes,
	})

	h.markSent(ctx, contentID)
	return &FetchResponse{
		Status:     resp.Status,
		Headers:    resp.Headers,
		Body:       redBody,
		Summary:    summary,
		Matches:    matches,
		Vocabulary: resp.Vocabulary,
		TimedOut:   resp.TimedOut,
		ContentID:  contentID,
	}, nil
}

// Glob performs glob pattern matching via the sandbox.
func (h *Handlers) Glob(ctx context.Context, params GlobParams) (_ *GlobResponse, err error) {
	defer recordToolCall("glob", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.readSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.GlobReq{
		Pattern: params.Pattern,
		Intent:  params.Intent,
	}

	resp, err := h.sandbox.Glob(ctx, req)
	if err != nil {
		return nil, err
	}

	h.logEvent(ctx, "tool.glob", params.Intent, map[string]any{
		"pattern": params.Pattern,
		"files":   len(resp.Files),
	})

	return &GlobResponse{
		Files:   resp.Files,
		Matches: h.redactMatches(resp.Matches),
	}, nil
}

// Grep performs text search via the sandbox.
func (h *Handlers) Grep(ctx context.Context, params GrepParams) (_ *GrepResponse, err error) {
	defer recordToolCall("grep", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.readSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.GrepReq{
		Pattern:         params.Pattern,
		Path:            params.Path,
		Files:           params.Files,
		Intent:          params.Intent,
		CaseInsensitive: params.CaseInsensitive,
		Context:         params.Context,
	}

	resp, err := h.sandbox.Grep(ctx, req)
	if err != nil {
		return nil, err
	}

	h.logEvent(ctx, "tool.grep", params.Intent, map[string]any{
		"pattern": params.Pattern,
		"files":   params.Files,
		"matches": len(resp.Matches),
	})

	// Redact match content
	matches := make([]sandbox.GrepMatch, len(resp.Matches))
	for i, m := range resp.Matches {
		matches[i] = sandbox.GrepMatch{
			File:    m.File,
			Line:    m.Line,
			Content: h.redactString(m.Content),
		}
	}

	return &GrepResponse{
		Matches: matches,
		Summary: h.redactString(resp.Summary),
	}, nil
}

// EditParams are the parameters for the Edit method.
type EditParams struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// EditResponse is the response from an edit operation.
type EditResponse struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// WriteParams are the parameters for the Write method.
type WriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteResponse is the response from a write operation.
type WriteResponse struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// Edit performs an edit on a file via the sandbox.
func (h *Handlers) Edit(ctx context.Context, params EditParams) (_ *EditResponse, err error) {
	defer recordToolCall("edit", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.writeSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.EditReq{
		Path:      params.Path,
		OldString: params.OldString,
		NewString: params.NewString,
	}

	resp, err := h.sandbox.Edit(ctx, req)
	if err != nil {
		return nil, err
	}

	h.logEvent(ctx, "tool.edit", params.Path, map[string]any{
		"path": params.Path,
	})

	return &EditResponse{
		Success: resp.Success,
		Summary: resp.Summary,
	}, nil
}

// Write writes content to a file via the sandbox.
func (h *Handlers) Write(ctx context.Context, params WriteParams) (_ *WriteResponse, err error) {
	defer recordToolCall("write", ctx, &err, time.Now())
	h.touch()
	release, err := acquireLimiter(ctx, h.writeSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.WriteReq{
		Path:    params.Path,
		Content: params.Content,
	}

	resp, err := h.sandbox.Write(ctx, req)
	if err != nil {
		return nil, err
	}

	// F-11: do NOT journal raw `params.Content`. Every dfmt_write of a
	// secrets-laden file (env, config, key) would otherwise land verbatim in
	// the journal — only pattern-redacted, not sanitized. A truncated SHA-256
	// plus byte count keeps the audit trail (same write detectable across
	// time) without exposing the payload.
	sum := sha256.Sum256([]byte(params.Content))
	h.logEvent(ctx, "tool.write", params.Path, map[string]any{
		"path":          params.Path,
		"bytes":         len(params.Content),
		"content_sha16": hex.EncodeToString(sum[:8]),
	})

	return &WriteResponse{
		Success: resp.Success,
		Summary: resp.Summary,
	}, nil
}
