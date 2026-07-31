package transport

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/sandbox"
)

// Async exec exists for work that outlives any synchronous ceiling. These
// tests pin the contract an agent depends on: submission answers instantly,
// the handle stays pollable, a finished job reports the real result, and a
// cancel actually stops the work rather than just detaching from it.

// scriptedSandbox is a sandbox stub whose Exec blocks until released, so a
// test can observe the "running" state deterministically instead of racing a
// real subprocess.
type scriptedSandbox struct {
	sandbox.Sandbox
	started chan struct{}
	release chan struct{}
	// ctxDone is written by the worker goroutine and read by the test, so
	// it has to be atomic — a plain bool here fails under -race, which is
	// exactly the kind of thing a test for concurrent code should not have.
	ctxDone atomic.Bool
}

func newScriptedSandbox() *scriptedSandbox {
	return &scriptedSandbox{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (s *scriptedSandbox) Exec(ctx context.Context, req sandbox.ExecReq) (sandbox.ExecResp, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return sandbox.ExecResp{Exit: 0, Stdout: "finished: " + req.Code, RawStdout: "finished: " + req.Code}, nil
	case <-ctx.Done():
		// What a canceled real command produces: partial output and the
		// context's error, not a silent success.
		s.ctxDone.Store(true)
		return sandbox.ExecResp{Exit: -1, TimedOut: true, RawStdout: "partial"}, ctx.Err()
	}
}

func asyncFixture(t *testing.T) (*Handlers, *scriptedSandbox) {
	t.Helper()
	sb := newScriptedSandbox()
	h := NewHandlers(core.NewIndex(), &mockJournal{}, sb)
	return h, sb
}

func TestAsyncExecReturnsJobHandleImmediately(t *testing.T) {
	h, sb := asyncFixture(t)
	defer close(sb.release)

	start := time.Now()
	resp, err := h.Exec(context.Background(), ExecParams{Code: "long migration", Async: true})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("submission took %s — async must not wait for the command", elapsed)
	}
	if resp.JobID == "" {
		t.Fatal("no job_id returned; the caller has no way to ask again")
	}
	if resp.Status != jobStatusRunning {
		t.Errorf("Status = %q, want %q", resp.Status, jobStatusRunning)
	}
}

func TestAsyncExecPollReportsRunningThenResult(t *testing.T) {
	h, sb := asyncFixture(t)

	submitted, err := h.Exec(context.Background(), ExecParams{Code: "build", Async: true})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-sb.started // the worker is inside Exec

	running, err := h.Exec(context.Background(), ExecParams{JobID: submitted.JobID})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if running.Status != jobStatusRunning {
		t.Fatalf("Status = %q, want running", running.Status)
	}

	close(sb.release)
	final := waitForJob(t, h, submitted.JobID)
	if final.Status != jobStatusDone {
		t.Fatalf("Status = %q, want done (error=%q)", final.Status, final.Error)
	}
	if !strings.Contains(final.Stdout, "finished: build") {
		t.Errorf("Stdout = %q, want the command's output", final.Stdout)
	}
}

func TestAsyncExecCancelStopsTheJob(t *testing.T) {
	h, sb := asyncFixture(t)
	defer close(sb.release)

	submitted, err := h.Exec(context.Background(), ExecParams{Code: "soak test", Async: true})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-sb.started

	canceled, err := h.Exec(context.Background(), ExecParams{JobID: submitted.JobID, Cancel: true})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != jobStatusCanceled {
		t.Errorf("Status = %q, want %q", canceled.Status, jobStatusCanceled)
	}
	if !sb.ctxDone.Load() {
		t.Error("the sandbox never saw its context canceled: the command was detached, not stopped")
	}
}

// An unknown handle must say so. Returning an empty "running" would leave a
// caller polling forever for a job that does not exist.
func TestAsyncExecUnknownJobIsAnError(t *testing.T) {
	h, sb := asyncFixture(t)
	defer close(sb.release)

	_, err := h.Exec(context.Background(), ExecParams{JobID: "01JQNOTAREALJOBID0000000"})
	if err == nil {
		t.Fatal("polling an unknown job_id succeeded")
	}
	if !strings.Contains(err.Error(), "unknown job_id") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

// A job_id call is about an existing job, so `code` must not be required —
// otherwise every poll would have to carry a dummy command.
func TestAsyncExecPollNeedsNoCode(t *testing.T) {
	if err := (ExecParams{JobID: "abc"}).Validate(); err != nil {
		t.Errorf("Validate rejected a poll with no code: %v", err)
	}
	if err := (ExecParams{}).Validate(); err == nil {
		t.Error("Validate accepted an exec with neither code nor job_id")
	}
}

// The job table is bounded: a submission past the cap is refused rather than
// evicting a job that may still be running.
func TestAsyncExecJobTableIsBounded(t *testing.T) {
	h, sb := asyncFixture(t)
	defer close(sb.release)

	for i := range maxAsyncJobs {
		if _, err := h.Exec(context.Background(), ExecParams{Code: "job", Async: true}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	_, err := h.Exec(context.Background(), ExecParams{Code: "one too many", Async: true})
	if err == nil {
		t.Fatal("submission past the cap succeeded")
	}
	if !strings.Contains(err.Error(), "too many async jobs") {
		t.Errorf("error = %v, want it to name the cap", err)
	}
}

// Shutdown must not leave a subprocess nobody owns behind.
func TestStopJobsCancelsRunningWork(t *testing.T) {
	h, sb := asyncFixture(t)
	defer close(sb.release)

	submitted, err := h.Exec(context.Background(), ExecParams{Code: "watch build", Async: true})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-sb.started

	h.StopJobs()

	final := waitForJob(t, h, submitted.JobID)
	if final.Status == jobStatusRunning {
		t.Error("job still running after StopJobs")
	}
	if !sb.ctxDone.Load() {
		t.Error("the sandbox never saw its context canceled")
	}
}

// Synchronous exec must be unaffected: no job_id, no status.
func TestSyncExecCarriesNoAsyncFields(t *testing.T) {
	h, sb := asyncFixture(t)
	close(sb.release) // finish immediately

	resp, err := h.Exec(context.Background(), ExecParams{Code: "quick"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.JobID != "" || resp.Status != "" {
		t.Errorf("sync response carries async fields: job_id=%q status=%q", resp.JobID, resp.Status)
	}
}

func TestAsyncTimeoutResolution(t *testing.T) {
	if got := asyncTimeout(0); got != DefaultAsyncExecTimeout {
		t.Errorf("asyncTimeout(0) = %v, want the async default %v", got, DefaultAsyncExecTimeout)
	}
	if got := asyncTimeout(120); got != 2*time.Minute {
		t.Errorf("asyncTimeout(120) = %v, want 2m", got)
	}
	if got := asyncTimeout(1 << 30); got != MaxAsyncExecTimeout {
		t.Errorf("asyncTimeout(huge) = %v, want the clamp %v", got, MaxAsyncExecTimeout)
	}
}

// waitForJob polls until the job leaves the running state.
func waitForJob(t *testing.T, h *Handlers, id string) *ExecResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := h.Exec(context.Background(), ExecParams{JobID: id})
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if resp.Status != jobStatusRunning {
			return resp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return nil
}
