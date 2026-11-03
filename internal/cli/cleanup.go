package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ersinkoc/dfmt/internal/instances"
	"github.com/ersinkoc/dfmt/internal/osutil"
	"github.com/ersinkoc/dfmt/internal/selfupdate"
)

// runCleanup leaves exactly one installed dfmt and at most one daemon
// (ADR-0034). Without --yes it only prints the plan: it ends processes and
// deletes files, so it never does that by surprise.
func runCleanup(args []string) int {
	var yes bool
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.BoolVar(&yes, "yes", false, "Carry out the plan (default: print it only)")
	fs.BoolVar(&yes, "y", false, "Shorthand for --yes")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	keep, err := selfupdate.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: locate this binary: %v\n", err)
		return 1
	}
	plan, err := planCleanup(keep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return reportCleanup(os.Stdout, plan, yes)
}

// planCleanup observes the machine and builds the plan around keep.
func planCleanup(keep string) (instances.Plan, error) {
	snap, err := instances.List()
	if err != nil {
		return instances.Plan{}, fmt.Errorf("list processes: %w", err)
	}
	self := os.Getpid()
	return instances.BuildPlan(instances.Input{
		Keep:            keep,
		Procs:           snap.Procs,
		Copies:          instances.FindCopies(keep),
		Self:            self,
		Ancestors:       snap.AncestorsOf(self),
		DaemonPID:       readGlobalDaemonInfo().PID,
		CaseInsensitive: osutil.IsWindows(),
	}), nil
}

// reportCleanup prints the plan and, when apply is set, runs it. It returns
// 1 when an action failed so an installer can notice.
func reportCleanup(w io.Writer, plan instances.Plan, apply bool) int {
	cleanupf(w, "Keeping: %s\n", displayPath(plan.Keep))
	for _, s := range []string{"daemon", "mcp"} {
		if n := plan.Healthy[s]; n > 0 {
			cleanupf(w, "  running, current: %d %s\n", n, s)
		}
	}
	for _, msg := range plan.Warnings {
		cleanupf(w, "warning: %s\n", msg)
	}
	if plan.Empty() {
		cleanupln(w, "Nothing to clean: one binary, no stale or duplicate processes.")
		return 0
	}
	for _, v := range plan.Kill {
		cleanupf(w, "  stop    pid %d (%s) %s — %s\n", v.Proc.PID, v.Proc.Role(), displayPath(v.Proc.Exe), v.Reason)
	}
	for _, r := range plan.Remove {
		cleanupf(w, "  remove  %s — %s\n", displayPath(r.Path), r.Reason)
	}
	if !apply {
		cleanupln(w, "Dry run. Re-run with --yes to carry this out.")
		return 0
	}
	res := plan.Apply(instances.DefaultActions())
	for _, d := range res.Done {
		cleanupf(w, "  ok      %s\n", d)
	}
	for _, f := range res.Failed {
		cleanupf(w, "  FAILED  %s\n", f)
	}
	if len(res.Failed) > 0 {
		cleanupln(w, "Some actions failed; close what is holding those files (or use an elevated shell) and re-run.")
		return 1
	}
	cleanupln(w, "Done. Any dfmt command starts a fresh daemon on demand.")
	return 0
}

// cleanupf and cleanupln write a report line. A failing stdout (closed pipe)
// has no better handler than carrying on, so the error is dropped explicitly.
func cleanupf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func cleanupln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }
