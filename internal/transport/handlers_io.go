package transport

import (
	"context"
	"errors"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

type ReadParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Path      string `json:"path"`
	Intent    string `json:"intent,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
	Return    string `json:"return,omitempty"`
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
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
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

	resp, err := bundle.Sandbox.Read(ctx, req)
	if err != nil {
		return nil, err
	}

	// Stash the full pre-filter content (RawContent) so the chunk-set ID is
	// a real pointer to the bytes. resp.Content carries the filtered view
	// for the client and may be empty when the policy excluded inline body.
	rawStash := h.redactString(ctx, resp.RawContent)
	contentID := h.stashContent(bundle.ContentStore, bundle.ProjectPath, "file-read", params.Path, params.Intent, rawStash)

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

	redContent := h.redactString(ctx, resp.Content)
	summary := h.redactString(ctx, resp.Summary)
	matches := h.redactMatches(ctx, resp.Matches)

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
	ProjectID string            `json:"project_id,omitempty"`
	URL       string            `json:"url"`
	Intent    string            `json:"intent,omitempty"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Return    string            `json:"return,omitempty"`
	Timeout   int               `json:"timeout,omitempty"` // seconds
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
	ProjectID string `json:"project_id,omitempty"`
	Pattern   string `json:"pattern"`
	Intent    string `json:"intent,omitempty"`
}

// GlobResponse is the response from a glob operation.
type GlobResponse struct {
	Files   []string               `json:"files,omitempty"`
	Matches []sandbox.ContentMatch `json:"matches,omitempty"`
}

// GrepParams are the parameters for the Grep method.
type GrepParams struct {
	ProjectID       string `json:"project_id,omitempty"`
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
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
	release, err := acquireLimiter(ctx, h.fetchSem)
	if err != nil {
		return nil, err
	}
	defer release()

	// V-17 / G-02: clamp before multiplication, same reasoning as Exec.
	// MaxFetchTimeout caps the agent-supplied positive value; the
	// `<= 0` else branch supplies the default.
	var timeout time.Duration
	if params.Timeout > 0 {
		secs := params.Timeout
		if maxSecs := int(sandbox.MaxFetchTimeout / time.Second); secs > maxSecs {
			secs = maxSecs
		}
		timeout = time.Duration(secs) * time.Second
	} else {
		timeout = sandbox.DefaultFetchTimeout
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

	resp, err := bundle.Sandbox.Fetch(ctx, req)
	if err != nil {
		// SSRF-006: log when fetch is blocked by SSRF policy so operators can
		// see when blocked attempts occur (no alerting UI yet, but the event
		// is in the journal for forensic use).
		if errors.Is(err, sandbox.ErrBlockedHost) {
			logging.Warnf("fetch blocked by SSRF policy: %s — %s", params.URL, err.Error())
		}
		return nil, err
	}

	// Stash full pre-filter body (RawBody); see Exec/Read rationale.
	rawStash := h.redactString(ctx, resp.RawBody)
	contentID := h.stashContent(bundle.ContentStore, bundle.ProjectPath, "fetch", params.URL, params.Intent, rawStash)

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

	redBody := h.redactString(ctx, resp.Body)
	summary := h.redactString(ctx, resp.Summary)
	matches := h.redactMatches(ctx, resp.Matches)

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
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
	release, err := acquireLimiter(ctx, h.readSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.GlobReq{
		Pattern: params.Pattern,
		Intent:  params.Intent,
	}

	resp, err := bundle.Sandbox.Glob(ctx, req)
	if err != nil {
		return nil, err
	}

	h.logEvent(ctx, "tool.glob", params.Intent, map[string]any{
		"pattern": params.Pattern,
		"files":   len(resp.Files),
	})

	return &GlobResponse{
		Files:   resp.Files,
		Matches: h.redactMatches(ctx, resp.Matches),
	}, nil
}

// Grep performs text search via the sandbox.
func (h *Handlers) Grep(ctx context.Context, params GrepParams) (_ *GrepResponse, err error) {
	defer recordToolCall("grep", ctx, &err, time.Now())
	h.touch()
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
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

	resp, err := bundle.Sandbox.Grep(ctx, req)
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
			Content: h.redactString(ctx, m.Content),
		}
	}

	return &GrepResponse{
		Matches: matches,
		Summary: h.redactString(ctx, resp.Summary),
	}, nil
}

// EditParams are the parameters for the Edit method.
