package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// Async exec jobs.
//
// The synchronous ceiling (sandbox.MaxExecTimeout, 900s) is a compromise:
// long enough for a build or a test suite, short enough that a runaway
// cannot hold an exec slot forever. Some legitimate work does not fit under
// any such number — a data migration, a soak test, a watch build — and the
// answer to that is not a larger constant. It is a job handle: submit,
// answer immediately, let the caller ask again later.
//
// The design decisions worth stating, because none of them are visible from
// the struct alone:
//
//   - Jobs are process-local, not persisted. A daemon restart loses them.
//     Persisting would mean reattaching to a subprocess that no longer has a
//     parent, which is not something an in-process job store can honestly
//     promise; a lost job surfaces as "unknown job", which is at least true.
//   - Async work gets its OWN semaphore. Sharing execSem would let two
//     hour-long jobs sit in half the interactive capacity, so a quick
//     `git status` would queue behind a migration.
//   - The worker runs under a detached context, not the request's. The RPC
//     that submitted the job returns in milliseconds; if the job inherited
//     that context it would be canceled the moment its own submission
//     succeeded.
//   - Cancellation reuses the deadline machinery: canceling the job's
//     context is exactly what a timeout does, so it kills the whole process
//     tree (ADR-0023) rather than orphaning it.

// Job status values as they appear on the wire.
const (
	jobStatusRunning  = "running"
	jobStatusDone     = "done"
	jobStatusCanceled = "canceled"
	jobStatusFailed   = "failed"
)

const (
	// maxAsyncJobs bounds the job table. A submission past the cap is
	// refused rather than evicting something: an agent that has forgotten
	// 32 jobs has a bug, and silently dropping a running one would hide it.
	maxAsyncJobs = 32

	// asyncJobRetention is how long a finished job stays pollable. Long
	// enough that an agent doing other work in between still finds its
	// result; short enough that a session's worth of output does not
	// accumulate in the daemon.
	asyncJobRetention = 30 * time.Minute

	// asyncExecConcurrency is how many jobs may run at once, separate from
	// the interactive execSem.
	asyncExecConcurrency = 2

	// cancelSettleTimeout is how long a cancel call waits for the worker to
	// record its terminal state. It has to cover the sandbox's own kill
	// grace (execWaitDelay) plus the normalize/journal tail of runExec.
	cancelSettleTimeout = 6 * time.Second

	// DefaultAsyncExecTimeout applies when an async submission names no
	// timeout. It is the synchronous maximum: a caller who went async
	// without saying how long clearly expects more than the interactive
	// default, but should still say so to get more than this.
	DefaultAsyncExecTimeout = sandbox.MaxExecTimeout

	// MaxAsyncExecTimeout is the ceiling for a job that names its own
	// timeout. Two hours covers the work this feature exists for while
	// still bounding a job that hangs and is never polled again.
	MaxAsyncExecTimeout = 2 * time.Hour
)

var (
	errJobUnknown = errors.New("unknown job_id: it expired, was never submitted, or belonged to a previous daemon")
	errJobsFull   = fmt.Errorf("too many async jobs (max %d): poll or cancel the outstanding ones first", maxAsyncJobs)
)

// execJob is one submitted command. resp/err are written once by the worker
// and read by any number of pollers, all under mu.
type execJob struct {
	id       string
	started  time.Time
	cancelFn context.CancelFunc

	mu       sync.Mutex
	status   string
	resp     *ExecResponse
	err      error
	finished time.Time
}

// snapshot renders the job's current state as a tool response.
func (j *execJob) snapshot() *ExecResponse {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.status == jobStatusRunning {
		return &ExecResponse{
			JobID:      j.id,
			Status:     jobStatusRunning,
			DurationMs: int(time.Since(j.started).Milliseconds()),
		}
	}
	// Copy the completed response so a poller cannot mutate the stored one.
	out := ExecResponse{JobID: j.id, Status: j.status}
	if j.resp != nil {
		out = *j.resp
		out.JobID = j.id
		out.Status = j.status
	}
	if j.err != nil {
		out.Error = j.err.Error()
	}
	return &out
}

// submitJob starts a command in the background and answers with its handle.
func (h *Handlers) submitJob(ctx context.Context, bundle Bundle, params ExecParams) (*ExecResponse, error) {
	if err := h.pruneJobs(); err != nil {
		return nil, err
	}

	// The worker outlives this RPC, so it needs a context of its own — but
	// one that still carries the project and session identity, or the
	// redactor lookup and the wire-dedup bucket would resolve differently
	// for the job than they did for its submitter.
	jobCtx := WithProjectID(context.Background(), ProjectIDFrom(ctx))
	jobCtx = WithSessionID(jobCtx, SessionIDFrom(ctx))
	jobCtx, cancel := context.WithTimeout(jobCtx, asyncTimeout(params.Timeout))

	// The command itself is not copied onto the job: runExec journals it as
	// a tool.exec event with the intent, which is the record that survives
	// the process. Keeping a second copy here would just be a second thing
	// to redact.
	job := &execJob{
		id:       string(core.NewULID(time.Now())),
		started:  time.Now(),
		status:   jobStatusRunning,
		cancelFn: cancel,
	}

	h.jobsMu.Lock()
	if h.jobs == nil {
		h.jobs = make(map[string]*execJob, maxAsyncJobs)
	}
	h.jobs[job.id] = job
	h.jobsMu.Unlock()

	go h.runJob(jobCtx, job, bundle, params)

	return &ExecResponse{JobID: job.id, Status: jobStatusRunning}, nil
}

// runJob executes the command and records the outcome on the job.
func (h *Handlers) runJob(ctx context.Context, job *execJob, bundle Bundle, params ExecParams) {
	defer job.cancelFn()
	defer func() {
		if r := recover(); r != nil {
			// A panic in a background worker has no caller to surface it to;
			// without this the job would sit in "running" forever and the
			// daemon would lose a goroutine silently.
			logging.Errorf("async exec job %s panicked: %v", job.id, r)
			job.finish(jobStatusFailed, nil, fmt.Errorf("internal error: %v", r))
		}
	}()

	release, err := acquireLimiter(ctx, h.asyncSem)
	if err != nil {
		job.finish(jobStatusCanceled, nil, err)
		return
	}
	defer release()

	// Async submissions carry their own, longer ceiling. Both the handler
	// clamp (here) and the job context (set at submit) enforce it; the
	// sandbox's HardMaxExecTimeout is the backstop under both.
	inner := params
	inner.Async = false
	if inner.Timeout <= 0 {
		inner.Timeout = int(DefaultAsyncExecTimeout / time.Second)
	}
	resp, execErr := h.runExec(ctx, bundle, inner, MaxAsyncExecTimeout)

	switch {
	case execErr != nil && ctx.Err() != nil:
		// The command failed because we stopped it. "context canceled" as an
		// error string tells the caller nothing the status does not already
		// say, and reads like a defect in DFMT rather than the cancel they
		// asked for.
		job.finish(jobStatusCanceled, nil, nil)
	case execErr != nil:
		job.finish(jobStatusFailed, nil, execErr)
	default:
		status := jobStatusDone
		if ctx.Err() != nil {
			// The command came back, but our deadline or a cancel fired
			// while it did. runExec already reports timed_out and partial
			// output; the job status says which of the two happened.
			status = jobStatusCanceled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = jobStatusDone
			}
		}
		job.finish(status, resp, nil)
	}
}

// finish records a terminal state exactly once.
func (j *execJob) finish(status string, resp *ExecResponse, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != jobStatusRunning {
		return
	}
	j.status = status
	j.resp = resp
	j.err = err
	j.finished = time.Now()
}

// pollJob returns the current state of a submitted job.
func (h *Handlers) pollJob(id string) (*ExecResponse, error) {
	h.jobsMu.Lock()
	job, ok := h.jobs[id]
	h.jobsMu.Unlock()
	if !ok {
		return nil, errJobUnknown
	}
	return job.snapshot(), nil
}

// cancelJob stops a running job. Canceling a finished one is not an error —
// the caller's intent (this job should not be running) already holds.
func (h *Handlers) cancelJob(id string) (*ExecResponse, error) {
	h.jobsMu.Lock()
	job, ok := h.jobs[id]
	h.jobsMu.Unlock()
	if !ok {
		return nil, errJobUnknown
	}
	job.cancelFn()
	// Wait for the worker to record the terminal state so a single cancel
	// call answers with it instead of "running".
	//
	// The bound is not arbitrary: cancellation unwinds through the sandbox's
	// deadline path, which kills the process tree and then allows
	// execWaitDelay (2s) for a descendant that survives, before runExec
	// normalizes and journals the partial output. Measured against a real
	// daemon, a 2s wait was consistently too short — the command was already
	// dead and the response still said "running", which reads as a cancel
	// that did nothing. If the worker is still unwinding past this, the
	// snapshot is honest about that and the caller can poll.
	deadline := time.Now().Add(cancelSettleTimeout)
	for time.Now().Before(deadline) {
		if snap := job.snapshot(); snap.Status != jobStatusRunning {
			return snap, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return job.snapshot(), nil
}

// pruneJobs drops finished jobs past their retention window and reports
// whether there is room for another submission.
func (h *Handlers) pruneJobs() error {
	h.jobsMu.Lock()
	defer h.jobsMu.Unlock()

	now := time.Now()
	for id, job := range h.jobs {
		job.mu.Lock()
		expired := job.status != jobStatusRunning && now.Sub(job.finished) > asyncJobRetention
		job.mu.Unlock()
		if expired {
			delete(h.jobs, id)
		}
	}
	if len(h.jobs) >= maxAsyncJobs {
		return errJobsFull
	}
	return nil
}

// StopJobs cancels every running job. Called on daemon shutdown so a
// long-running child does not outlive the process that owns it.
func (h *Handlers) StopJobs() {
	h.jobsMu.Lock()
	jobs := make([]*execJob, 0, len(h.jobs))
	for _, job := range h.jobs {
		jobs = append(jobs, job)
	}
	h.jobsMu.Unlock()
	for _, job := range jobs {
		job.cancelFn()
	}
}

// asyncTimeout resolves the effective ceiling for an async submission.
// Clamping happens on the int seconds before the multiplication for the same
// overflow reason the synchronous path documents (V-17 / G-02).
func asyncTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return DefaultAsyncExecTimeout
	}
	if maxSecs := int(MaxAsyncExecTimeout / time.Second); seconds > maxSecs {
		seconds = maxSecs
	}
	return time.Duration(seconds) * time.Second
}
