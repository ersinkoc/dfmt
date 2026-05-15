package transport

import (
	"context"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

type ExecParams struct {
	ProjectID string            `json:"project_id,omitempty"`
	Code      string            `json:"code"`
	Lang      string            `json:"lang,omitempty"`
	Intent    string            `json:"intent,omitempty"`
	Timeout   int               `json:"timeout,omitempty"` // seconds
	Return    string            `json:"return,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
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
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
	release, err := acquireLimiter(ctx, h.execSem)
	if err != nil {
		return nil, err
	}
	defer release()

	// V-17 / G-02: clamp the agent-supplied timeout BEFORE the
	// time.Duration multiplication. A sufficiently large params.Timeout
	// (e.g., int64 max) would overflow `time.Duration(params.Timeout) *
	// time.Second` to a negative duration and the post-clamp `<= 0`
	// reset would silently mask the wrap. Clamping the int seconds to
	// MaxExecTimeout/Second first makes both directions safe.
	var timeout time.Duration
	if params.Timeout > 0 {
		secs := params.Timeout
		if maxSecs := int(sandbox.MaxExecTimeout / time.Second); secs > maxSecs {
			secs = maxSecs
		}
		timeout = time.Duration(secs) * time.Second
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

	resp, err := bundle.Sandbox.Exec(ctx, req)
	if err != nil {
		return nil, err
	}

	// Stash the full pre-filter output so the agent can fetch raw bytes via
	// the chunk-set ID later. resp.Stdout may have been dropped by the
	// return-policy filter when the output was large with no intent — using
	// it for stashing would leave the content store with empty bytes and
	// the chunk-set ID a dead pointer. RawStdout always carries the full
	// (capped at MaxRawBytes) output.
	stderr := h.redactString(ctx, resp.Stderr)
	rawStash := h.redactString(ctx, resp.RawStdout) + stderr
	contentID := h.stashContent(bundle.ContentStore, bundle.ProjectPath, "exec-stdout", "sandbox.exec", params.Intent, rawStash)

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

	stdout := h.redactString(ctx, resp.Stdout)
	summary := h.redactString(ctx, resp.Summary)
	matches := h.redactMatches(ctx, resp.Matches)

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

// redactMatches returns a new slice with Text fields redacted using the
// per-call (project-scoped) redactor when one is available.
