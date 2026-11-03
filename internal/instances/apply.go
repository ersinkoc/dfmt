package instances

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Actions are the side effects Apply performs; tests replace them.
type Actions struct {
	Terminate func(pid int) error
	Remove    func(path string) error
}

// DefaultActions end processes politely first and delete files, falling back
// to a rename when the file is locked (a running Windows binary can be moved
// aside but not deleted).
func DefaultActions() Actions {
	return Actions{Terminate: terminate, Remove: removeFile}
}

// Result records what Apply did, one line per action.
type Result struct {
	Done   []string
	Failed []string
}

// Apply carries out the plan. A failed action is recorded and the rest still
// run: one stuck process must not stop the stray binaries being removed.
func (p Plan) Apply(a Actions) Result {
	var r Result
	for _, v := range p.Kill {
		desc := fmt.Sprintf("pid %d (%s, %s): %s", v.Proc.PID, v.Proc.Role(), v.Proc.Exe, v.Reason)
		if err := a.Terminate(v.Proc.PID); err != nil {
			r.Failed = append(r.Failed, fmt.Sprintf("stop %s: %v", desc, err))
			continue
		}
		r.Done = append(r.Done, "stopped "+desc)
	}
	for _, rm := range p.Remove {
		if err := a.Remove(rm.Path); err != nil {
			r.Failed = append(r.Failed, fmt.Sprintf("remove %s: %v", rm.Path, err))
			continue
		}
		r.Done = append(r.Done, fmt.Sprintf("removed %s (%s)", rm.Path, rm.Reason))
	}
	return r
}

// terminateGrace is how long a process gets to exit after SIGTERM, covering
// the daemon's own shutdown so it removes the state files that name it.
const terminateGrace = 12 * time.Second

func terminate(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// No console to deliver a control event to a detached daemon.
		return ignoreGone(proc.Kill())
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return ignoreGone(err)
	}
	deadline := time.Now().Add(terminateGrace)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return nil // gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ignoreGone(proc.Kill())
}

// ignoreGone treats "already finished" as success: the goal was that the
// process not be running.
func ignoreGone(err error) error {
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func removeFile(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	// Locked (Windows) or otherwise undeletable: move it out of the way so it
	// stops shadowing, and let the next run delete the .old.
	aside := path + ".old"
	_ = os.Remove(aside)
	if rerr := os.Rename(path, aside); rerr == nil {
		return nil
	}
	return err
}
