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

	// Async submits the command and returns a job_id immediately instead of
	// holding the call open. It exists for work that outlives any sensible
	// synchronous ceiling — a migration, a soak test, a full build — where
	// the alternative was raising MaxExecTimeout until it stopped bounding
	// anything.
	Async bool `json:"async,omitempty"`
	// JobID polls (or, with Cancel, stops) a job submitted with Async. When
	// set, every other field is ignored: this call is about an existing job,
	// not a new command.
	JobID string `json:"job_id,omitempty"`
	// Cancel stops the job named by JobID. The sandbox's deadline machinery
	// does the work, so a cancel kills the whole process tree exactly the
	// way a timeout does.
	Cancel bool `json:"cancel,omitempty"`
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

	// JobID and Status are the async surface. Status is one of "running",
	// "done", "canceled", or "failed"; it is empty on a synchronous call, so
	// an agent that never asks for async never sees the vocabulary.
	JobID  string `json:"job_id,omitempty"`
	Status string `json:"status,omitempty"`
	// Error carries the failure text for a job that could not run at all
	// (as opposed to a command that ran and exited non-zero, which is a
	// normal result with an exit code).
	Error string `json:"error,omitempty"`
}

// Exec executes code via the sandbox.
//
// Three shapes share this entry point:
//
//	{code}                   run it, answer when it finishes
//	{code, async:true}       submit it, answer with a job_id now
//	{job_id} / {job_id,cancel} ask about (or stop) a submitted job
//
// They are parameters on one tool rather than three tools because the
// tools/list payload is paid on every session start, and because "run a
// command" is one capability whose result the caller may want now or later.
func (h *Handlers) Exec(ctx context.Context, params ExecParams) (_ *ExecResponse, err error) {
	defer recordToolCall("exec", ctx, &err, time.Now())
	h.touch()

	// A job_id call is about an existing job: no sandbox, no semaphore, no
	// project resources needed beyond what the job already captured.
	if params.JobID != "" {
		if params.Cancel {
			return h.cancelJob(params.JobID)
		}
		return h.pollJob(params.JobID)
	}

	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}

	if params.Async {
		return h.submitJob(ctx, bundle, params)
	}

	release, err := acquireLimiter(ctx, h.execSem)
	if err != nil {
		return nil, err
	}
	defer release()
	return h.runExec(ctx, bundle, params, sandbox.MaxExecTimeout)
}

// runExec is the body both the synchronous call and the async worker use.
// The caller holds whichever semaphore applies and supplies the ceiling for
// its own call shape: 900s for a synchronous call (something is waiting on
// it), MaxAsyncExecTimeout for a job (nothing is).
func (h *Handlers) runExec(ctx context.Context, bundle Bundle, params ExecParams, maxTimeout time.Duration) (*ExecResponse, error) {

	// V-17 / G-02: clamp the agent-supplied timeout BEFORE the
	// time.Duration multiplication. A sufficiently large params.Timeout
	// (e.g., int64 max) would overflow `time.Duration(params.Timeout) *
	// time.Second` to a negative duration and the post-clamp `<= 0`
	// reset would silently mask the wrap. Clamping the int seconds to
	// MaxExecTimeout/Second first makes both directions safe.
	var timeout time.Duration
	if params.Timeout > 0 {
		secs := params.Timeout
		if maxSecs := int(maxTimeout / time.Second); secs > maxSecs {
			secs = maxSecs
		}
		timeout = time.Duration(secs) * time.Second
	} else {
		timeout = sandbox.DefaultExecTimeout
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
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
