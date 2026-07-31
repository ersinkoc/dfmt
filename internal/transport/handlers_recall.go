package transport

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/retrieve"
)

type RecallParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Budget    int    `json:"budget,omitempty"`
	Format    string `json:"format,omitempty"`
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
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Journal == nil {
		return nil, errNoProject
	}
	budget := h.recallBudget(params.Budget)
	format := h.recallFormat(params.Format)

	// Serve from cache when the journal has not moved. Checkpoint is the
	// ULID of the last appended event, so "same cursor" means "same input"
	// — the whole streaming, unmarshalling, signature-verifying, rendering
	// pass below would produce byte-identical output. A Checkpoint error is
	// not fatal: fall through and rebuild.
	cacheKey := bundle.ProjectPath + "|" + strconv.Itoa(budget) + "|" + format
	cursor, cursorErr := bundle.Journal.Checkpoint(ctx)
	// An empty cursor is not a cache key. It means the journal has no
	// appended events in this instance — true for a fresh journal, and true
	// again immediately after a rotation — so it cannot distinguish two
	// different journal states. Caching under it would make the cache
	// permanently valid for any implementation whose checkpoint does not
	// advance.
	cacheable := cursorErr == nil && cursor != ""
	if cacheable {
		if cached, ok := h.recallCached(cacheKey, cursor); ok {
			return &RecallResponse{Snapshot: cached, Format: format}, nil
		}
	}

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
	stream, err := bundle.Journal.Stream(streamCtx, "")
	if err != nil {
		return nil, fmt.Errorf("stream journal: %w", err)
	}

	const (
		p1Cap = 5000 // decisions/task-done — rare; keep nearly all
		p2Cap = 1000 // commits/errors/elevated notes
		p3Cap = 500  // file edits / audit findings
		p4Cap = 500  // tool calls / unelevated notes
	)
	sorted := retrieve.CollectTiers(stream, [4]int{p1Cap, p2Cap, p3Cap, p4Cap})

	// V-01: re-redact each event before handing it to the renderer. The
	// journal-write redactor catches what its patterns matched at the time
	// of write; this second pass covers near-misses, retroactively-added
	// patterns, and operator-supplied custom patterns added after the event
	// was journaled.
	redacted := make([]core.Event, len(sorted))
	for i, e := range sorted {
		redacted[i] = h.redactEventForRender(ctx, e)
	}

	// One SnapshotBuilder pass for every format (TRN-6): the markdown path
	// used to be a second, inline implementation with a different budget
	// model (rendered-line bytes vs json.Marshal bytes), no path interning,
	// and no ref-token-forgery escaping. Now all formats share the builder,
	// so interning and escaping are active on the default format too.
	sb := retrieve.NewSnapshotBuilder(budget)
	snap, _ := sb.Build(redacted)
	var snapshot string
	switch format {
	case "json":
		snapshot = retrieve.NewJSONRenderer().Render(snap)
	case "xml":
		snapshot = retrieve.NewXMLRenderer().Render(snap)
	default:
		snapshot = retrieve.NewMarkdownRenderer().Render(snap)
	}

	if cacheable {
		h.recallStore(cacheKey, cursor, snapshot)
	}
	return &RecallResponse{
		Snapshot: snapshot,
		Format:   format,
	}, nil
}

// StatsParams are the parameters for the Stats method.
